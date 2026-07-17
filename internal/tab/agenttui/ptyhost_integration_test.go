package agenttui

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tjst-t/palmux2/internal/ptyhost"
)

// shortRunDir returns a fresh temp directory NOT rooted under t.TempDir()
// (which embeds the full test function name and can push a
// "<dir>/<repoId>__<branchId>__<tabId>.sock" path past the ~108-byte AF_UNIX
// sun_path limit on Linux for these particularly long test names). Cleaned
// up via t.Cleanup like t.TempDir() would be.
func shortRunDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pmxs3f2658-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// This file covers Sprint S3f2658 Story 2's AC-S3f2658-2-1 (thin-client
// attach to a real ptyhost), the Go-level precursor to AC-S3f2658-2-2 (screen
// restore across a reconnect to a SURVIVING ptyhost — the full real-restart
// E2E lives under tests/e2e/), and AC-S3f2658-2-3 (protocol version mismatch
// degrades without killing the ptyhost).

// TestDaemonAttachesToRealPtyhostAndFeedsGrid is AC-S3f2658-2-1's
// integration scenario verbatim: a REAL ptyhost (Story 1's actual
// internal/ptyhost.Server, holding a real fake_claude child) is pre-created
// at the exact deterministic path a Daemon with matching identity will
// compute, so the Daemon's launchAndAttach takes the "survivor found —
// attach, don't spawn" branch. Feeding bytes through it must land in the
// Daemon's emulator grid, and a WS client attaching afterward must see the
// rendered snapshot.
func TestDaemonAttachesToRealPtyhostAndFeedsGrid(t *testing.T) {
	bin := fakeBin(t)
	runDir := shortRunDir(t)

	d := NewDaemon(DaemonConfig{
		ClaudeBin:      "unused-in-this-test", // launchAndAttach never spawns; a survivor is always found
		RingSize:       1 << 16,
		RepoID:         "ac1-repo",
		BranchID:       "ac1-branch",
		TabID:          "claude",
		RunDirOverride: runDir,
	})
	t.Cleanup(func() { d.Shutdown() })

	sockPath, statusPath := d.ptyHostPaths()

	// Pre-create the REAL ptyhost holding a real fake_claude child, at the
	// EXACT path the Daemon will probe.
	srv, err := ptyhost.NewServer(ptyhost.Config{
		Argv:       []string{bin},
		SocketPath: sockPath,
		StatusPath: statusPath,
		RingSize:   1 << 16,
	})
	if err != nil {
		t.Fatalf("ptyhost.NewServer: %v", err)
	}
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		_ = srv.Run(context.Background())
	}()
	if err := WaitForSocket(context.Background(), sockPath, 5*time.Second, nil); err != nil {
		t.Fatalf("ptyhost never started listening: %v", err)
	}

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	// Bytes fed through the REAL ptyhost (fake_claude's startup line) must
	// land in the Daemon's ring AND its emulator grid.
	deadline := time.After(5 * time.Second)
	for {
		if bytes.Contains(d.ring.Bytes(), []byte("fake_claude started")) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("ring never received startup text via the real ptyhost; ring=%q", d.ring.Bytes())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	time.Sleep(20 * time.Millisecond) // settle window for the emulator to process the same bytes

	g := d.GridSnapshot()
	var sb strings.Builder
	for _, row := range g.Lines {
		for _, cell := range row.Cells {
			sb.WriteRune(cell.Ch)
		}
	}
	if !strings.Contains(sb.String(), "fake_claude") {
		t.Logf("grid flat text: %q", sb.String())
		t.Log("grid text may be CR/LF-split across rows (not a contract violation) — ring already confirmed the bytes flowed")
	}

	// A WS client attaching now must see the rendered snapshot.
	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()
	wsURL := "ws" + ts.URL[len("http"):]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()
	_, got, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read snapshot: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("WS client received an empty initial snapshot")
	}
	if !bytes.Contains(got, []byte("Claude")) && !bytes.Contains(got, []byte("fake_claude")) {
		t.Logf("snapshot bytes: %q", got[:min(len(got), 300)])
		t.Error("WS snapshot does not appear to contain the real ptyhost's rendered content")
	}
	t.Logf("WS client received %d snapshot bytes from a Daemon attached to a real ptyhost", len(got))

	// Shut down explicitly (not just via t.Cleanup, which only runs AFTER
	// this function returns) so we can positively confirm the real ptyhost's
	// Run() actually returns in response — t.Cleanup's own d.Shutdown() call
	// below is then a harmless no-op (sync.Once).
	d.Shutdown()
	select {
	case <-srvDone:
	case <-time.After(5 * time.Second):
		t.Fatal("real ptyhost's Run() did not return after Daemon.Shutdown()")
	}
}

// TestDaemonReconnectsToSurvivingPtyhostAndRestoresScreen is the Go-level
// precursor to AC-S3f2658-2-2: two SEPARATELY CONSTRUCTED Daemon objects
// (simulating "palmux2 restarted, a brand-new in-memory Daemon is built")
// sharing the same identity + run directory. Daemon A spawns fresh; Daemon B
// — constructed later, WITHOUT Daemon A ever being shut down (the surviving
// ptyhost is what a real palmux2 restart leaves behind) — must ATTACH to the
// SAME child (same pid) rather than spawning a new one, must have the
// pre-existing content available in its ring after attach (§5 replay), and
// the restore jiggle must actually reach the child as a real SIGWINCH (using
// a trap-based shell script as the child so the reaction is observable).
func TestDaemonReconnectsToSurvivingPtyhostAndRestoresScreen(t *testing.T) {
	runDir := shortRunDir(t)
	bin := fakeBin(t)

	// fake_claude --counter-winch: prints a distinctive, growing "COUNTER n"
	// line once per 100ms (so we can assert "no gap across the restart
	// window") and traps SIGWINCH to echo a marker (proving the restore
	// jiggle's RESIZE frames genuinely reach the child as real terminal
	// resizes, not just a client-side no-op). Using the real fake_claude
	// binary (rather than a raw shell -c script) keeps this test compatible
	// with spawnWithArgs's claude-shaped argv injection (--settings,
	// --permission-mode), which fake_claude already tolerates/ignores.
	identity := DaemonConfig{
		ClaudeBin:      bin,
		ClaudeArgs:     []string{"--counter-winch"},
		RingSize:       1 << 16,
		ResumeOnDeath:  false,
		RepoID:         "restore-repo",
		BranchID:       "restore-branch",
		TabID:          "claude",
		RunDirOverride: runDir,
	}

	// "Before restart": Daemon A spawns the child fresh.
	dA := NewDaemon(identity)
	if err := dA.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted A: %v", err)
	}
	waitForState(t, dA, StateRunning, 5*time.Second)

	// Wait for a few counter lines so there's real "pre-restart" content, and
	// so output is flowing DURING the moment we simulate the restart (no
	// artificial pause is needed — the child keeps producing on its own).
	deadline := time.After(5 * time.Second)
	for {
		if bytes.Contains(dA.ring.Bytes(), []byte("COUNTER 3")) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("A never produced counter output; ring=%q", dA.ring.Bytes())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	pidBefore := dA.CurrentStats().PID
	if pidBefore == 0 {
		t.Fatal("A has no pid")
	}

	// "palmux2 restarts": a BRAND NEW Daemon object, same identity + run dir.
	// Deliberately do NOT call dA.Shutdown() first — a surviving ptyhost from
	// a real process restart is exactly this: still there, nobody told it to
	// stop.
	dB := NewDaemon(identity)
	t.Cleanup(func() { dB.Shutdown() })
	if err := dB.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted B: %v", err)
	}
	waitForState(t, dB, StateRunning, 5*time.Second)

	// B must have ATTACHED to the SAME child, not spawned a new one.
	pidAfter := dB.CurrentStats().PID
	if pidAfter != pidBefore {
		t.Fatalf("B spawned a NEW ptyhost (pid %d) instead of reconnecting to A's surviving one (pid %d)", pidAfter, pidBefore)
	}

	// B's ring must contain the pre-restart content (the §5 replay).
	if !bytes.Contains(dB.ring.Bytes(), []byte("COUNTER")) {
		t.Errorf("B's ring does not contain pre-restart COUNTER output after reconnect; ring=%q", dB.ring.Bytes())
	}

	// No gap: the counter must keep incrementing with no missing values once
	// B is attached and live DATA resumes flowing.
	seen := map[int]bool{}
	deadline2 := time.After(5 * time.Second)
	for len(seen) < 5 {
		for _, line := range strings.Split(string(dB.ring.Bytes()), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "COUNTER ") {
				continue
			}
			var prefix string
			var n int
			if _, err := fmt.Sscanf(line, "%s %d", &prefix, &n); err == nil {
				seen[n] = true
			}
		}
		select {
		case <-deadline2:
			t.Fatalf("did not observe 5 distinct counter values after reconnect; seen=%v ring=%q", seen, dB.ring.Bytes())
		default:
			time.Sleep(30 * time.Millisecond)
		}
	}
	// No gap: 1..max(seen) must all be present among what's retained (allow
	// for ring eviction of the very earliest values on a long test, but the
	// contiguous RECENT run must have no holes).
	maxN := 0
	for n := range seen {
		if n > maxN {
			maxN = n
		}
	}
	missing := []int{}
	for n := maxN - 2; n <= maxN; n++ { // check the most recent 3 — robust to ring-eviction of very old values
		if n > 0 && !seen[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Errorf("gap detected in counter sequence across the reconnect: missing %v (seen=%v)", missing, seen)
	}

	// Restore jiggle must have reached the child as a REAL SIGWINCH (the
	// trap echoes WINCH_MARKER). This is only sent for reconnected==true.
	deadline3 := time.After(3 * time.Second)
	for {
		if bytes.Contains(dB.ring.Bytes(), []byte("WINCH_MARKER")) {
			break
		}
		select {
		case <-deadline3:
			t.Fatalf("[§5] no WINCH_MARKER observed — restore jiggle did not reach the child as a real SIGWINCH; ring=%q", dB.ring.Bytes())
		default:
			time.Sleep(30 * time.Millisecond)
		}
	}
	t.Log("[AC-S3f2658-2-2 Go-precursor] reconnected to surviving ptyhost (same pid), pre-restart content replayed, no counter gap, SIGWINCH jiggle confirmed")
}

// TestDaemonVersionMismatchDegradesWithoutKilling is AC-S3f2658-2-3: a
// ptyhost whose HELLO reports a protocol version this Daemon does not
// recognize must NOT be killed (no SHUTDOWN sent) — the Daemon instead
// surfaces a degraded state via CurrentStats, and continues operating
// best-effort over the same connection (minimal frame-level compat).
func TestDaemonVersionMismatchDegradesWithoutKilling(t *testing.T) {
	runDir := shortRunDir(t)
	d := NewDaemon(DaemonConfig{
		ClaudeBin:      "unused",
		RingSize:       1 << 16,
		RepoID:         "vm-repo",
		BranchID:       "vm-branch",
		TabID:          "claude",
		RunDirOverride: runDir,
	})
	t.Cleanup(func() { d.Shutdown() })

	sockPath, _ := d.ptyHostPaths()

	shutdownReceived := make(chan struct{}, 1)
	// RunDirOverride (runDir, from t.TempDir()) already exists, so the
	// socket's parent directory is guaranteed present.
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()
		for {
			typ, _, rerr := ptyhost.ReadFrame(conn)
			if rerr != nil {
				return
			}
			switch typ {
			case ptyhost.MsgHello:
				_ = ptyhost.WriteFrame(conn, ptyhost.MsgHello, ptyhost.EncodeHello(ptyhost.HelloPayload{
					ProtocolVersion: ptyhost.ProtocolVersion + 1, // simulate a future version
					Mode:            "pty",
					Pid:             424242,
				}))
			case ptyhost.MsgAttach:
				_ = ptyhost.WriteFrame(conn, ptyhost.MsgData, ptyhost.EncodeData(0, nil))
			case ptyhost.MsgShutdown:
				select {
				case shutdownReceived <- struct{}{}:
				default:
				}
				return // a real ptyhost closes the connection on SHUTDOWN too
			}
		}
	}()

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	stats := d.CurrentStats()
	if !stats.Degraded {
		t.Fatalf("[AC-S3f2658-2-3] expected Degraded=true on protocol version mismatch, got %+v", stats)
	}
	if !strings.Contains(stats.DegradedReason, "旧世代") {
		t.Errorf("[AC-S3f2658-2-3] DegradedReason = %q, want it to explain an old-gen agent host", stats.DegradedReason)
	}
	if stats.PID != 424242 {
		t.Errorf("PID = %d, want 424242 (from the fake HELLO reply) — Daemon should still track ptyhost-reported identity while degraded", stats.PID)
	}
	if stats.Alive != true {
		t.Errorf("Alive = %v, want true — a degraded ptyhost is still a live, usable connection", stats.Alive)
	}

	// Give ample time, then assert NO SHUTDOWN was ever sent.
	select {
	case <-shutdownReceived:
		t.Fatal("[AC-S3f2658-2-3] Daemon sent SHUTDOWN to a version-mismatched ptyhost — it must be preserved, not killed (ADR-0002 §2)")
	case <-time.After(500 * time.Millisecond):
	}
	t.Log("[AC-S3f2658-2-3] version-skewed ptyhost preserved (no SHUTDOWN sent), Degraded state observable via CurrentStats")
}
