package claudeagent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
)

// fakeNDJSONBin compiles the SAME fake stream-json emitter internal/ptyhost
// uses for its own pipe-mode tests (internal/ptyhost/testdata/fake_ndjson.go
// — see its doc comment for the FAKE_NDJSON_* env knobs). Reused by relative
// path (a plain //go:build ignore program, not a Go package import) rather
// than duplicated, per S862203-2 scenario notes ("fake NDJSON emitter... a
// tiny helper program").
func fakeNDJSONBin(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	src := filepath.Join(wd, "..", "..", "ptyhost", "testdata", "fake_ndjson.go")
	bin := filepath.Join(t.TempDir(), "fake_ndjson")
	cmd := exec.Command("go", "build", "-o", bin, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("compiling fake_ndjson: %v\n%s", err, out)
	}
	return bin
}

// newAgentPipeHost starts a real ptyhost.Server (ModePipe) in-process,
// holding the fake_ndjson emitter, and returns its socket path. Mirrors
// internal/tab/claudetui's inProcessLaunchPtyHost pattern: the genuine
// ptyhost protocol/ring/spawn code runs, only the OS-process-detachment
// half of a production launch (internal/ptyhost/launch.go, covered by that
// package's own tests) is swapped for an in-process goroutine.
func newAgentPipeHost(t *testing.T, ringSize int, env ...string) (sockPath string) {
	t.Helper()
	bin := fakeNDJSONBin(t)
	dir := t.TempDir()
	sockPath = filepath.Join(dir, "ptyhost.sock")
	statusPath := filepath.Join(dir, "ptyhost.json")

	fullEnv := append([]string{}, os.Environ()...)
	fullEnv = append(fullEnv, env...)

	if ringSize <= 0 {
		ringSize = 1 << 16
	}
	srv, err := ptyhost.NewServer(ptyhost.Config{
		Argv:           []string{bin},
		Env:            fullEnv,
		Mode:           ptyhost.ModePipe,
		SocketPath:     sockPath,
		StatusPath:     statusPath,
		RingSize:       ringSize,
		GracePeriod:    2 * time.Second,
		PostExitLinger: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ptyhost.NewServer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("ptyhost.Server.Run did not return within 5s of cancellation")
		}
	})

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return sockPath
}

// fakeEvent mirrors the {"type":"fake_event","seq":N} shape
// testdata/fake_ndjson.go prints.
type fakeEvent struct {
	Type string `json:"type"`
	Seq  int    `json:"seq"`
}

// collector is a small concurrency-safe sink for lines/stderr chunks
// observed during a PipeClient.Run call.
type collector struct {
	mu      sync.Mutex
	seqs    []int
	stderr  [][]byte
	lastEnd int64
}

func (c *collector) onLine(line []byte, endOffset int64) error {
	var ev fakeEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return err
	}
	c.mu.Lock()
	c.seqs = append(c.seqs, ev.Seq)
	c.lastEnd = endOffset
	c.mu.Unlock()
	return nil
}

func (c *collector) onStderr(_ int64, data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	c.mu.Lock()
	c.stderr = append(c.stderr, cp)
	c.mu.Unlock()
}

func (c *collector) Seqs() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.seqs...)
}

func (c *collector) LastEnd() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastEnd
}

func (c *collector) StderrCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.stderr)
}

// TestPipeClient_LineReassembly_MonotoneOffsets covers AC-S862203-2-1 from
// the claudeagent (generic replay client) side: NDJSON lines are
// reassembled with correct, monotonically increasing absolute end offsets,
// and stderr is delivered separately (never mixed into onLine).
func TestPipeClient_LineReassembly_MonotoneOffsets(t *testing.T) {
	sock := newAgentPipeHost(t, 0, "FAKE_NDJSON_COUNT=5", "FAKE_NDJSON_STDERR_COUNT=2", "FAKE_NDJSON_DELAY_MS=10")

	col := &collector{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := DialPipeClient(ctx, sock, col.onLine, col.onStderr)
	if err != nil {
		t.Fatalf("DialPipeClient: %v", err)
	}
	defer client.Close()

	var attachResult AttachResult
	onAttach := func(r AttachResult) { attachResult = r }

	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, -1, onAttach) }()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && len(col.Seqs()) < 5 {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if attachResult.Overflowed {
		t.Fatal("Overflowed = true on a fresh ring, want false")
	}
	seqs := col.Seqs()
	if len(seqs) != 5 {
		t.Fatalf("observed %d lines, want 5 (seqs=%v)", len(seqs), seqs)
	}
	for i, seq := range seqs {
		if seq != i {
			t.Errorf("seqs[%d] = %d, want %d (out of order or dup/gap)", i, seq, i)
		}
	}
	if col.StderrCount() == 0 {
		t.Fatal("expected at least one stderr chunk to be delivered separately")
	}
}

// TestPipeClient_CleanReconnect_ResumesExactlyAfterLastPersistedLine covers
// AC-S862203-2-2's non-kill-9 half: a clean disconnect (Close, no
// SHUTDOWN — the ptyhost/child survive), persisted via [OffsetStore] after
// each processed line, followed by a fresh client reconnecting from the
// persisted offset — must resume at exactly the next line, no dup, no gap.
func TestPipeClient_CleanReconnect_ResumesExactlyAfterLastPersistedLine(t *testing.T) {
	sock := newAgentPipeHost(t, 0, "FAKE_NDJSON_COUNT=6", "FAKE_NDJSON_STDERR_COUNT=0", "FAKE_NDJSON_DELAY_MS=25")

	dir := t.TempDir()
	store, err := NewOffsetStore(dir)
	if err != nil {
		t.Fatalf("NewOffsetStore: %v", err)
	}
	const repoID, branchID, tabID = "repo", "branch", "claude:claude"

	// First client: process exactly 3 lines (seq 0,1,2), persisting after
	// each, then disconnect cleanly (simulating a routine palmux2 restart
	// mid-conversation, NOT a kill -9 — see the next test for that).
	ctx1, cancel1 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel1()

	var seen1 []int
	var mu1 sync.Mutex
	onLine1 := func(line []byte, endOffset int64) error {
		var ev fakeEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return err
		}
		if err := store.Save(repoID, branchID, tabID, endOffset, "", nil); err != nil {
			return err
		}
		mu1.Lock()
		seen1 = append(seen1, ev.Seq)
		mu1.Unlock()
		return nil
	}

	client1, err := DialPipeClient(ctx1, sock, onLine1, nil)
	if err != nil {
		t.Fatalf("DialPipeClient (client1): %v", err)
	}
	done1 := make(chan error, 1)
	go func() { done1 <- client1.Run(ctx1, -1, nil) }()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		mu1.Lock()
		n := len(seen1)
		mu1.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu1.Lock()
	got1 := append([]int(nil), seen1...)
	mu1.Unlock()
	if len(got1) < 3 {
		t.Fatalf("client1 only observed %d lines before deadline, want >= 3 (got %v)", len(got1), got1)
	}

	// Clean disconnect: Close (no SHUTDOWN) — ptyhost + child survive.
	_ = client1.Close()
	cancel1()
	<-done1

	rec, ok := store.Get(repoID, branchID, tabID)
	if !ok {
		t.Fatal("OffsetStore has no record after client1's Saves")
	}

	// Second client: a FRESH OffsetStore handle (as a real palmux2 restart
	// would construct) reading the SAME on-disk file, reconnecting from the
	// persisted offset.
	store2, err := NewOffsetStore(dir)
	if err != nil {
		t.Fatalf("NewOffsetStore (reload): %v", err)
	}
	rec2, ok := store2.Get(repoID, branchID, tabID)
	if !ok || rec2.LastAckOffset != rec.LastAckOffset {
		t.Fatalf("reloaded OffsetStore record = %+v (ok=%v), want LastAckOffset=%d", rec2, ok, rec.LastAckOffset)
	}

	col2 := &collector{}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	client2, err := DialPipeClient(ctx2, sock, col2.onLine, nil)
	if err != nil {
		t.Fatalf("DialPipeClient (client2): %v", err)
	}
	defer client2.Close()

	var attachResult2 AttachResult
	done2 := make(chan error, 1)
	go func() {
		done2 <- client2.Run(ctx2, rec2.LastAckOffset, func(r AttachResult) { attachResult2 = r })
	}()

	deadline2 := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline2) && len(col2.Seqs()) < (6-len(got1)) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel2()
	<-done2

	if attachResult2.Overflowed {
		t.Fatal("client2 Overflowed = true, want false (well within retained window)")
	}
	seqs2 := col2.Seqs()
	wantFirst := len(got1) // next unseen seq
	if len(seqs2) == 0 {
		t.Fatal("client2 observed no lines after reconnect")
	}
	if seqs2[0] != wantFirst {
		t.Fatalf("client2 first observed seq = %d, want %d (no dup, no gap)", seqs2[0], wantFirst)
	}
	for i, seq := range seqs2 {
		want := wantFirst + i
		if seq != want {
			t.Errorf("client2 seqs2[%d] = %d, want %d", i, seq, want)
		}
	}
}

// errSimulatedKill9 is returned by a LineHandler to simulate palmux2 dying
// (kill -9) the instant AFTER it has seen a line's bytes but BEFORE it
// managed to persist/ack it — matching [LineHandler]'s documented contract
// (a non-nil return means: do not treat this line as durably processed).
var errSimulatedKill9 = errors.New("simulated kill -9 mid-line")

// TestPipeClient_Kill9Reconnect_RedeliversUnackedLine_ExactlyOnce covers
// AC-S862203-2-2's kill-9 half explicitly: the connection is severed with
// NO clean ACK/persist of the most recently seen line (the persisted
// offset lags behind what was actually delivered) — on reconnect, that
// line must be redelivered exactly once (as part of a contiguous replay
// from the persisted offset) and nothing earlier is duplicated, nothing
// later is skipped.
func TestPipeClient_Kill9Reconnect_RedeliversUnackedLine_ExactlyOnce(t *testing.T) {
	sock := newAgentPipeHost(t, 0, "FAKE_NDJSON_COUNT=6", "FAKE_NDJSON_STDERR_COUNT=0", "FAKE_NDJSON_DELAY_MS=30")

	dir := t.TempDir()
	store, err := NewOffsetStore(dir)
	if err != nil {
		t.Fatalf("NewOffsetStore: %v", err)
	}
	const repoID, branchID, tabID = "repo", "branch", "claude:claude"

	// killAtSeq is the line palmux2 will have SEEN (in-memory) but crash
	// before persisting — client-side analogue of a kill -9 landing between
	// "line arrived" and "offset flushed to disk".
	const killAtSeq = 2

	ctx1, cancel1 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel1()
	onLine1 := func(line []byte, endOffset int64) error {
		var ev fakeEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return err
		}
		if ev.Seq == killAtSeq {
			// Deliberately do NOT call store.Save for this line — the
			// non-nil return is exactly "crashed before ack" per
			// LineHandler's documented contract.
			return errSimulatedKill9
		}
		return store.Save(repoID, branchID, tabID, endOffset, "", nil)
	}

	client1, err := DialPipeClient(ctx1, sock, onLine1, nil)
	if err != nil {
		t.Fatalf("DialPipeClient (client1): %v", err)
	}
	runErr := client1.Run(ctx1, -1, nil)
	_ = client1.Close() // idempotent; connection is likely already dead
	if !errors.Is(runErr, errSimulatedKill9) {
		t.Fatalf("client1.Run error = %v, want wrapping errSimulatedKill9", runErr)
	}

	rec, ok := store.Get(repoID, branchID, tabID)
	if !ok {
		t.Fatal("OffsetStore has no record — expected lines before killAtSeq to have been persisted")
	}

	// Reconnect from the persisted (LAGGING) offset.
	col2 := &collector{}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	client2, err := DialPipeClient(ctx2, sock, col2.onLine, nil)
	if err != nil {
		t.Fatalf("DialPipeClient (client2): %v", err)
	}
	defer client2.Close()

	done2 := make(chan error, 1)
	go func() { done2 <- client2.Run(ctx2, rec.LastAckOffset, nil) }()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		seqs := col2.Seqs()
		if len(seqs) > 0 && seqs[len(seqs)-1] == 5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel2()
	<-done2

	seqs2 := col2.Seqs()
	if len(seqs2) == 0 {
		t.Fatal("client2 observed no lines after reconnect")
	}
	// The un-acked line (killAtSeq) must be the FIRST thing redelivered —
	// proving no earlier (already-persisted) line is duplicated.
	if seqs2[0] != killAtSeq {
		t.Fatalf("client2 first observed seq = %d, want %d (the un-acked line, redelivered exactly once — earlier lines must not be duplicated)", seqs2[0], killAtSeq)
	}
	// And everything from killAtSeq through the end must be present with no
	// gap (nothing after the persisted offset skipped) and no internal dup.
	for i, seq := range seqs2 {
		want := killAtSeq + i
		if seq != want {
			t.Fatalf("client2 seqs2[%d] = %d, want %d (gap or dup in post-restart replay: %v)", i, seq, want, seqs2)
		}
	}
}

// TestPipeClient_Overflow_SurfacesNewSessionSignal covers AC-S862203-2-3:
// when the ring has evicted the requested offset (long disconnect + small
// ring), the client MUST surface Overflowed=true via AttachResult rather
// than silently replaying a gapped transcript.
func TestPipeClient_Overflow_SurfacesNewSessionSignal(t *testing.T) {
	// A tiny ring guarantees eviction well before the 200-line burst ends.
	sock := newAgentPipeHost(t, 128, "FAKE_NDJSON_COUNT=200", "FAKE_NDJSON_STDERR_COUNT=0", "FAKE_NDJSON_DELAY_MS=0")

	col := &collector{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := DialPipeClient(ctx, sock, col.onLine, nil)
	if err != nil {
		t.Fatalf("DialPipeClient: %v", err)
	}
	defer client.Close()

	// Poll STATUS until the ring has actually evicted past offset 0
	// (RingHeadOffset > 0) — deterministic instead of racing a fixed sleep
	// against the child's scheduling under CPU load. Status()/Run() share
	// the conn sequentially (STATUS before ATTACH).
	evDeadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(evDeadline) {
		st, serr := client.Status(5 * time.Second)
		if serr != nil {
			t.Fatalf("Status: %v", serr)
		}
		if st.RingHeadOffset > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	attachCh := make(chan AttachResult, 1)
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx, 0, func(r AttachResult) {
			select {
			case attachCh <- r:
			default:
			}
		})
	}()

	var result AttachResult
	select {
	case result = <-attachCh:
	case <-time.After(20 * time.Second):
		t.Fatal("did not observe an AttachResult before timeout")
	}
	cancel()
	<-done

	if !result.Overflowed {
		t.Fatalf("AttachResult.Overflowed = false, want true (requested=%d, start=%d)", result.Requested, result.StartOffset)
	}
	if result.StartOffset <= result.Requested {
		t.Fatalf("StartOffset = %d, want > Requested = %d", result.StartOffset, result.Requested)
	}
}

// TestPipeClient_NoOverflow_ShortDisconnectReplaysCleanly is the negative
// control for AC-S862203-2-3.
func TestPipeClient_NoOverflow_ShortDisconnectReplaysCleanly(t *testing.T) {
	sock := newAgentPipeHost(t, 0, "FAKE_NDJSON_COUNT=5", "FAKE_NDJSON_STDERR_COUNT=0") // large default ring
	time.Sleep(200 * time.Millisecond)

	col := &collector{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := DialPipeClient(ctx, sock, col.onLine, nil)
	if err != nil {
		t.Fatalf("DialPipeClient: %v", err)
	}
	defer client.Close()

	attachCh := make(chan AttachResult, 1)
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx, 0, func(r AttachResult) {
			select {
			case attachCh <- r:
			default:
			}
		})
	}()

	var result AttachResult
	select {
	case result = <-attachCh:
	case <-time.After(20 * time.Second):
		t.Fatal("did not observe an AttachResult before timeout")
	}
	cancel()
	<-done

	if result.Overflowed {
		t.Fatalf("AttachResult.Overflowed = true, want false (requested=%d, start=%d)", result.Requested, result.StartOffset)
	}
	if result.StartOffset != result.Requested {
		t.Fatalf("StartOffset = %d, want == Requested = %d (false-positive overflow)", result.StartOffset, result.Requested)
	}
}

// TestPipeClient_NonContiguousFrame_ErrorsNotSplice is the S862203-2 review
// HIGH-1 regression: a live DATA frame whose absolute offset is NOT
// contiguous with the bytes seen so far (the shape a ring subscriber-lag
// DROP produces — the ring's non-blocking broadcast discards a chunk when
// the 256-slot subscription channel fills during a stalled socket write)
// must make PipeClient.Run return ErrNonContiguous rather than SPLICE the
// gapped chunk onto the buffered partial line (which would corrupt NDJSON —
// sometimes into silently-valid-but-wrong JSON that gets persisted and never
// re-fetched: a silent transcript gap).
//
// Rather than race real socket/OS-buffer/Ch dynamics to provoke a drop
// (inherently timing-dependent and flaky — unix socket send buffers are
// large, and pumpToRing's 32KiB reads coalesce lines into few chunks, so
// deterministically filling the 256-slot Ch is impractical), this drives a
// scripted server that emits exactly the byte-offset gap a drop leaves. It
// exercises the identical production code path — Run's contiguity check —
// and the exact observable outcome the fix guarantees.
func TestPipeClient_NonContiguousFrame_ErrorsNotSplice(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "gap.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	line0 := []byte(`{"seq":0}` + "\n")

	serverErr := make(chan error, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			serverErr <- aerr
			return
		}
		defer func() { _ = conn.Close() }()
		// Consume the client's ATTACH request.
		if _, _, rerr := ptyhost.ReadFrame(conn); rerr != nil {
			serverErr <- rerr
			return
		}
		// First DATA = the ATTACH replay at offset 0 (one complete line).
		if werr := ptyhost.WriteFrame(conn, ptyhost.MsgData, ptyhost.EncodeData(0, line0)); werr != nil {
			serverErr <- werr
			return
		}
		// Second (live) DATA jumps to a NON-CONTIGUOUS offset — the client
		// expects len(line0); we jump +500 past it, exactly the gap a ring
		// subscriber-lag drop of the intervening chunks would leave.
		gapOffset := int64(len(line0)) + 500
		if werr := ptyhost.WriteFrame(conn, ptyhost.MsgData, ptyhost.EncodeData(gapOffset, []byte(`{"seq":1}`+"\n"))); werr != nil {
			serverErr <- werr
			return
		}
		// Drain/ignore whatever the client writes back (e.g. its ACK for the
		// first line) until it disconnects.
		for {
			if _, _, rerr := ptyhost.ReadFrame(conn); rerr != nil {
				break
			}
		}
		serverErr <- nil
	}()

	var mu sync.Mutex
	var lines []string
	onLine := func(line []byte, _ int64) error {
		mu.Lock()
		lines = append(lines, string(line))
		mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := DialPipeClient(ctx, sock, onLine, nil)
	if err != nil {
		t.Fatalf("DialPipeClient: %v", err)
	}
	defer client.Close()

	runErr := client.Run(ctx, -1, nil)
	if !errors.Is(runErr, ErrNonContiguous) {
		t.Fatalf("Run error = %v, want wrapping ErrNonContiguous (must NOT splice the gapped chunk)", runErr)
	}

	mu.Lock()
	got := append([]string(nil), lines...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("onLine called %d times (%v), want exactly 1 — the gapped/spliced line must NOT be delivered", len(got), got)
	}
	if got[0] != `{"seq":0}` {
		t.Fatalf("onLine delivered %q, want %q (only the contiguous pre-gap line)", got[0], `{"seq":0}`)
	}
}
