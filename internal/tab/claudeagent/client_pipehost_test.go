package claudeagent

// S862203-3 AC-1 wiring tests: the Client (not the raw PipeClient covered by
// Wave 2's ptyclient_test.go) pumps stream-json over a pipe-mode ptyhost,
// feeding lines through the SAME dispatch (onMessage → in production,
// processStreamMessage) the pre-S862203-3 exec-direct implementation used,
// keeps stderr on a separate channel, and tears the ptyhost down on Close.
// Uses the fake_ndjson emitter (internal/ptyhost/testdata/fake_ndjson.go) as
// the ptyhost child so this stays hermetic — no real claude, no quota.

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
)

// captureHandler is a minimal slog.Handler that records every log line's
// message+attrs as a single string, used to assert stderr chunks reach the
// logger (handleStderr's "claude.stderr" log line) separately from the
// onMessage stream.
type captureHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *captureHandler) WithGroup(string) slog.Handler            { return h }
func (h *captureHandler) Handle(_ context.Context, rec slog.Record) error {
	var sb strings.Builder
	sb.WriteString(rec.Message)
	rec.Attrs(func(a slog.Attr) bool {
		sb.WriteString(" ")
		sb.WriteString(a.Key)
		sb.WriteString("=")
		sb.WriteString(a.Value.String())
		return true
	})
	h.mu.Lock()
	h.msgs = append(h.msgs, sb.String())
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.msgs))
	copy(out, h.msgs)
	return out
}

// waitFor polls fn until it returns true or the deadline passes, failing
// the test on timeout.
func waitFor(t *testing.T, timeout time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// TestClient_PipeHost_LinesReachOnMessage covers [AC-S862203-3-1]: NewClient
// spawns the fake NDJSON emitter over a pipe-mode ptyhost (in-process test
// fallback — PalmuxBin unset) and every stdout line it prints reaches the
// Client's onMessage callback (the production seam into the UNCHANGED
// processStreamMessage dispatch), through the exact same switch
// (control_response / control_request / default) the pre-S862203-3
// bufio.Scanner loop used.
func TestClient_PipeHost_LinesReachOnMessage(t *testing.T) {
	bin := fakeNDJSONBin(t)
	t.Setenv("FAKE_NDJSON_COUNT", "5")
	t.Setenv("FAKE_NDJSON_STDERR_COUNT", "3")
	t.Setenv("FAKE_NDJSON_DELAY_MS", "5")

	logCapture := &captureHandler{}
	logger := slog.New(logCapture)

	var mu sync.Mutex
	var seen []string
	onMessage := func(msg streamMsg) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, msg.Type)
	}

	cli, err := NewClient(context.Background(), ClientOptions{
		Binary:         bin,
		Cwd:            t.TempDir(),
		Logger:         logger,
		RepoID:         "repo",
		BranchID:       "branch",
		TabID:          "claude:claude",
		RunDirOverride: t.TempDir(),
	}, onMessage, nil, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(cli.Close)

	waitFor(t, 10*time.Second, "5 fake_event lines via onMessage", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) >= 5
	})

	mu.Lock()
	for _, ty := range seen {
		if ty != "fake_event" {
			t.Errorf("unexpected onMessage type %q, want fake_event", ty)
		}
	}
	mu.Unlock()

	// Stderr is diagnostic-only and reaches the logger, NOT onMessage — the
	// separate-channel assertion.
	waitFor(t, 10*time.Second, "stderr diag lines via logger", func() bool {
		for _, m := range logCapture.snapshot() {
			if strings.Contains(m, "claude.stderr") && strings.Contains(m, "diag:") {
				return true
			}
		}
		return false
	})

	mu.Lock()
	for _, ty := range seen {
		if strings.Contains(ty, "diag") {
			t.Fatalf("stderr content leaked into onMessage stream: %q", ty)
		}
	}
	mu.Unlock()
}

// TestClient_PipeHost_CloseSendsShutdown covers the second half of
// [AC-S862203-3-1]: Close tears the ptyhost (and its held child) all the way
// down — Done() closes and the ptyhost's on-disk status file records the
// child as no longer alive.
func TestClient_PipeHost_CloseSendsShutdown(t *testing.T) {
	bin := fakeNDJSONBin(t)
	t.Setenv("FAKE_NDJSON_COUNT", "1")

	var mu sync.Mutex
	var seen int
	onMessage := func(streamMsg) {
		mu.Lock()
		seen++
		mu.Unlock()
	}

	cli, err := NewClient(context.Background(), ClientOptions{
		Binary:         bin,
		Cwd:            t.TempDir(),
		RepoID:         "repo",
		BranchID:       "branch",
		TabID:          "claude:claude",
		RunDirOverride: t.TempDir(),
	}, onMessage, nil, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	waitFor(t, 10*time.Second, "at least one line", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seen >= 1
	})

	statusPath := cli.statusPath
	cli.Close()

	select {
	case <-cli.Done():
	default:
		t.Fatal("Close did not close Done()")
	}

	sf, err := ptyhost.ReadStatusFile(statusPath)
	if err != nil {
		t.Fatalf("ReadStatusFile after Close: %v", err)
	}
	if sf.Alive {
		t.Fatal("ptyhost status file still reports Alive=true after Close (SHUTDOWN did not reach the child)")
	}
}

// TestClient_PipeHost_DetachLeavesChildRunning proves Detach is genuinely
// distinct from Close: after Detach, a FRESH Client (mirroring a palmux2
// restart's freshly-reconstructed Agent) reconnects to the SAME surviving
// ptyhost instead of spawning a new one — the mechanism DiscoverAndRestore
// and Manager.DetachAll rely on for lossless restart survival.
func TestClient_PipeHost_DetachLeavesChildRunning(t *testing.T) {
	bin := fakeNDJSONBin(t)
	t.Setenv("FAKE_NDJSON_COUNT", "1")

	runDir := t.TempDir()
	var mu sync.Mutex
	var seen int
	onMessage := func(streamMsg) {
		mu.Lock()
		seen++
		mu.Unlock()
	}

	cli, err := NewClient(context.Background(), ClientOptions{
		Binary:         bin,
		Cwd:            t.TempDir(),
		RepoID:         "repo",
		BranchID:       "branch",
		TabID:          "claude:claude",
		RunDirOverride: runDir,
	}, onMessage, nil, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	waitFor(t, 10*time.Second, "at least one line", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seen >= 1
	})
	if cli.currentPipeClientForTest() == nil {
		t.Fatal("no active pipe client before Detach")
	}

	cli.Detach()
	select {
	case <-cli.Done():
		t.Fatal("Detach must NOT close Done() — the child is still running")
	default:
	}

	// A brand new Client, same identity + run dir (simulating a fresh
	// palmux2 process after a restart), must ATTACH to the survivor rather
	// than spawning a second one.
	cli2, err := NewClient(context.Background(), ClientOptions{
		Binary:         bin,
		Cwd:            t.TempDir(),
		RepoID:         "repo",
		BranchID:       "branch",
		TabID:          "claude:claude",
		RunDirOverride: runDir,
	}, func(streamMsg) {}, nil, nil)
	if err != nil {
		t.Fatalf("NewClient (reconnect): %v", err)
	}
	t.Cleanup(cli2.Close)
	if !cli2.Reconnected() {
		t.Fatal("second Client did not reconnect to the surviving ptyhost — Detach did not leave it running")
	}
}
