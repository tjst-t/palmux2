package ptyhost

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// fakeNDJSONBin compiles testdata/fake_ndjson.go and returns its path
// (mirrors fakeChildBin, S862203-2's fake stream-json emitter — see the
// testdata file's doc comment for the FAKE_NDJSON_* env knobs).
func fakeNDJSONBin(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	src := filepath.Join(wd, "testdata", "fake_ndjson.go")
	bin := filepath.Join(t.TempDir(), "fake_ndjson")
	cmd := exec.Command("go", "build", "-o", bin, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("compiling fake_ndjson: %v\n%s", err, out)
	}
	return bin
}

// newPipeTestServer starts a ModePipe Server holding the compiled
// fake_ndjson binary, serving the socket protocol on a temp-dir socket.
// env configures the child (FAKE_NDJSON_* — see testdata/fake_ndjson.go).
func newPipeTestServer(t *testing.T, ringSize int, env ...string) (*Server, string) {
	t.Helper()
	bin := fakeNDJSONBin(t)
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "ptyhost.sock")
	statusPath := filepath.Join(dir, "ptyhost.json")

	fullEnv := append([]string{}, os.Environ()...)
	fullEnv = append(fullEnv, env...)

	if ringSize <= 0 {
		ringSize = 1 << 16
	}
	srv, err := NewServer(Config{
		Argv:           []string{bin},
		Env:            fullEnv,
		Mode:           ModePipe,
		SocketPath:     sockPath,
		StatusPath:     statusPath,
		RingSize:       ringSize,
		GracePeriod:    2 * time.Second,
		PostExitLinger: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
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
			t.Error("Server.Run did not return within 5s of cancellation")
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return srv, sockPath
}

// TestServer_PipeMode_HelloReportsPipeMode covers the HELLO.Mode field
// reflecting the actual spawn mode (AC-S862203-2-1 step 1).
func TestServer_PipeMode_HelloReportsPipeMode(t *testing.T) {
	_, sockPath := newPipeTestServer(t, 0, "FAKE_NDJSON_COUNT=1", "FAKE_NDJSON_STDERR_COUNT=0")
	c := dial(t, sockPath)
	c.send(MsgHello, nil)
	mt, payload := c.recv()
	if mt != MsgHello {
		t.Fatalf("reply type = %v, want HELLO", mt)
	}
	hello, err := DecodeHello(payload)
	if err != nil {
		t.Fatalf("DecodeHello: %v", err)
	}
	if hello.Mode != ModePipe {
		t.Fatalf("Mode = %q, want %q", hello.Mode, ModePipe)
	}
	if hello.Pid <= 0 {
		t.Fatalf("Pid = %d, want > 0", hello.Pid)
	}
}

// TestServer_PipeMode_StdoutStderrSeparation covers AC-S862203-2-1: stdout
// NDJSON lines land in the stdout ring (via MsgData), stderr diagnostic
// lines land ONLY on the separate MsgStderrData channel, and the two never
// cross-contaminate.
func TestServer_PipeMode_StdoutStderrSeparation(t *testing.T) {
	_, sockPath := newPipeTestServer(t, 0,
		"FAKE_NDJSON_COUNT=5", "FAKE_NDJSON_STDERR_COUNT=3", "FAKE_NDJSON_DELAY_MS=0")

	// Let the child finish its burst before attaching, so this exercises
	// REPLAY (both rings already hold the full burst) rather than racing
	// live delivery.
	time.Sleep(300 * time.Millisecond)

	c := dial(t, sockPath)
	c.send(MsgAttach, EncodeAttach(-1))

	stdoutPayload := c.recvUntil(MsgData)
	stdoutOffset, stdoutData, err := DecodeData(stdoutPayload)
	if err != nil {
		t.Fatalf("DecodeData(stdout): %v", err)
	}
	if stdoutOffset != 0 {
		t.Fatalf("stdout replay start offset = %d, want 0", stdoutOffset)
	}

	stderrPayload := c.recvUntil(MsgStderrData)
	stderrOffset, stderrData, err := DecodeData(stderrPayload)
	if err != nil {
		t.Fatalf("DecodeData(stderr): %v", err)
	}
	if stderrOffset != 0 {
		t.Fatalf("stderr replay start offset = %d, want 0", stderrOffset)
	}

	for i := 0; i < 5; i++ {
		want := []byte(fmt.Sprintf(`"seq":%d`, i))
		if !bytes.Contains(stdoutData, want) {
			t.Errorf("stdout ring missing line seq=%d: %q", i, stdoutData)
		}
	}
	for i := 0; i < 3; i++ {
		want := []byte(fmt.Sprintf("diag: %d", i))
		if !bytes.Contains(stderrData, want) {
			t.Errorf("stderr channel missing diag line %d: %q", i, stderrData)
		}
	}

	// The core invariant: no cross-contamination in either direction.
	if bytes.Contains(stdoutData, []byte("diag:")) {
		t.Fatalf("stdout ring contains a stderr line — cross-contamination: %q", stdoutData)
	}
	if bytes.Contains(stderrData, []byte("fake_event")) {
		t.Fatalf("stderr channel contains a stdout NDJSON line — cross-contamination: %q", stderrData)
	}
}

// TestServer_PipeMode_InputWritesToChildStdin covers MsgInput being written
// to the child's stdin PIPE (not a PTY) in pipe mode, round-tripping through
// the child's echo behaviour back over the stdout ring.
func TestServer_PipeMode_InputWritesToChildStdin(t *testing.T) {
	_, sockPath := newPipeTestServer(t, 0, "FAKE_NDJSON_COUNT=1", "FAKE_NDJSON_STDERR_COUNT=0")
	c := dial(t, sockPath)
	c.send(MsgAttach, EncodeAttach(-1))
	_ = c.recvUntil(MsgData) // initial burst (1 stdout line)

	c.send(MsgInput, EncodeInput([]byte("hello-pipe-mode\n")))

	deadline := time.Now().Add(5 * time.Second)
	sawEcho := false
	for time.Now().Before(deadline) && !sawEcho {
		_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		mt, payload, err := ReadFrame(c.conn)
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if mt != MsgData {
			continue
		}
		_, data, derr := DecodeData(payload)
		if derr != nil {
			t.Fatalf("DecodeData: %v", derr)
		}
		if bytes.Contains(data, []byte("hello-pipe-mode")) {
			sawEcho = true
		}
	}
	if !sawEcho {
		t.Fatal("did not observe echoed stdin input over the stdout ring")
	}
}

// TestServer_PipeMode_ResizeIsNoOp covers RESIZE being a documented no-op in
// pipe mode (§2 of docs/no-halt-agent-design.md): sending it must not error,
// hang, or crash the server, and normal traffic continues afterward.
func TestServer_PipeMode_ResizeIsNoOp(t *testing.T) {
	_, sockPath := newPipeTestServer(t, 0, "FAKE_NDJSON_COUNT=1", "FAKE_NDJSON_STDERR_COUNT=0")
	c := dial(t, sockPath)
	c.send(MsgAttach, EncodeAttach(-1))
	_ = c.recvUntil(MsgData)

	c.send(MsgResize, EncodeResize(80, 24)) // must be silently accepted (no-op)

	// The connection must still be usable afterward.
	c.send(MsgStatus, EncodeStatusRequest())
	payload := c.recvUntil(MsgStatus)
	st, err := DecodeStatusResponse(payload)
	if err != nil {
		t.Fatalf("DecodeStatusResponse: %v", err)
	}
	if !st.Alive {
		t.Fatal("Alive = false after RESIZE no-op, want true")
	}
}

// TestServer_PipeMode_AckIsNoOp covers ADR-0004/PD-4: MsgAck is honored as a
// no-op/informational message on the ptyhost side (persistence is
// palmux2-side, in claudeagent's OffsetStore — ptyhost never persists).
// Sending it must not error, hang, or disrupt subsequent traffic.
func TestServer_PipeMode_AckIsNoOp(t *testing.T) {
	_, sockPath := newPipeTestServer(t, 0, "FAKE_NDJSON_COUNT=1", "FAKE_NDJSON_STDERR_COUNT=0")
	c := dial(t, sockPath)
	c.send(MsgAttach, EncodeAttach(-1))
	_ = c.recvUntil(MsgData)

	c.send(MsgAck, EncodeAck(12345))

	c.send(MsgStatus, EncodeStatusRequest())
	payload := c.recvUntil(MsgStatus)
	if _, err := DecodeStatusResponse(payload); err != nil {
		t.Fatalf("DecodeStatusResponse: %v", err)
	}
}

// TestServer_PipeMode_Kill9Reconnect_NoDupNoLoss simulates a palmux2 kill -9
// (the client connection just drops, WITHOUT a SHUTDOWN frame — the ptyhost
// and its held child are unaffected and keep running) followed by a
// reconnect that resumes replay exactly where the disconnected client left
// off: no duplicate bytes, no gap (AC-S862203-2-2).
func TestServer_PipeMode_Kill9Reconnect_NoDupNoLoss(t *testing.T) {
	_, sockPath := newPipeTestServer(t, 0,
		"FAKE_NDJSON_COUNT=10", "FAKE_NDJSON_STDERR_COUNT=0", "FAKE_NDJSON_DELAY_MS=20")

	// Attach early (live-streaming, not full replay) and read a few live
	// chunks to establish "last acked" progress, then simulate kill -9: just
	// close the connection (no SHUTDOWN) — the child and ptyhost survive.
	c1 := dial(t, sockPath)
	c1.send(MsgAttach, EncodeAttach(-1))

	var observedThroughOffset int64 = -1
	var allDataSoFar []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = c1.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		mt, payload, err := ReadFrame(c1.conn)
		if err != nil {
			break
		}
		if mt != MsgData {
			continue
		}
		offset, data, derr := DecodeData(payload)
		if derr != nil {
			t.Fatalf("DecodeData: %v", derr)
		}
		allDataSoFar = append(allDataSoFar, data...)
		observedThroughOffset = offset + int64(len(data))
		if bytes.Count(allDataSoFar, []byte("\n")) >= 3 {
			break // stop once we've seen at least 3 full lines
		}
	}
	if observedThroughOffset <= 0 {
		t.Fatal("did not observe any live data before simulated kill -9")
	}

	// "kill -9": drop the connection without SHUTDOWN. The ptyhost (and its
	// held fake_ndjson child) must keep running.
	_ = c1.conn.Close()
	time.Sleep(100 * time.Millisecond) // let the child keep producing output

	// Reconnect and resume exactly from the last offset this client saw. The
	// load-bearing assertion is the offset EQUALITY below: ATTACH returns
	// bytes starting exactly at the requested offset unless the ring evicted
	// it (covered separately by the overflow tests) — an exact match proves
	// no byte in [0, observedThroughOffset) is redelivered (no dup) and no
	// byte at/after observedThroughOffset is skipped (no loss).
	c2 := dial(t, sockPath)
	c2.send(MsgAttach, EncodeAttach(observedThroughOffset))
	payload := c2.recvUntil(MsgData)
	resumeOffset, _, err := DecodeData(payload)
	if err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if resumeOffset != observedThroughOffset {
		t.Fatalf("resume offset = %d, want exactly %d (no gap, no dup)", resumeOffset, observedThroughOffset)
	}
}

// TestServer_PipeMode_Overflow_ClampedAttachStartExceedsRequested covers
// AC-S862203-2-3: when the requested replay offset has been evicted by ring
// wrap-around, ATTACH's returned start offset is GREATER than what was
// requested — the primitive palmux2 uses to detect "lossless restore
// impossible" and treat the reconnect as a new session instead of silently
// emitting a truncated transcript.
func TestServer_PipeMode_Overflow_ClampedAttachStartExceedsRequested(t *testing.T) {
	// A tiny ring (well under the NDJSON burst size) guarantees wrap-around.
	// The child is left alive (no FAKE_NDJSON_EXIT_AFTER) so the socket
	// doesn't tear down (PostExitLinger) before this test dials it.
	_, sockPath := newPipeTestServer(t, 128,
		"FAKE_NDJSON_COUNT=200", "FAKE_NDJSON_STDERR_COUNT=0", "FAKE_NDJSON_DELAY_MS=0")

	// Let the (small, fast) burst complete and evict the ring well past
	// offset 0.
	time.Sleep(400 * time.Millisecond)

	c := dial(t, sockPath)
	requested := int64(0) // "resume from the very start" — long evicted by now.
	c.send(MsgAttach, EncodeAttach(requested))
	payload := c.recvUntil(MsgData)
	start, _, err := DecodeData(payload)
	if err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if start <= requested {
		t.Fatalf("ATTACH start offset = %d, want > requested %d (ring should have evicted offset 0 by now)", start, requested)
	}

	// AC-S862203-2-3 step 4: the STATUS payload's RingHeadOffset/
	// RingTotalOffset (already exposed by S3f2658) support the SAME
	// decision independently of the ATTACH reply — RingHeadOffset (oldest
	// retained byte) must agree with the clamped ATTACH start, and both
	// must be well past the requested (evicted) offset.
	c.send(MsgStatus, EncodeStatusRequest())
	stPayload := c.recvUntil(MsgStatus)
	st, err := DecodeStatusResponse(stPayload)
	if err != nil {
		t.Fatalf("DecodeStatusResponse: %v", err)
	}
	if st.RingHeadOffset <= requested {
		t.Fatalf("STATUS RingHeadOffset = %d, want > requested %d", st.RingHeadOffset, requested)
	}
	if st.RingHeadOffset != start {
		t.Fatalf("STATUS RingHeadOffset = %d, want == ATTACH clamped start %d", st.RingHeadOffset, start)
	}
	if st.RingTotalOffset < st.RingHeadOffset {
		t.Fatalf("STATUS RingTotalOffset = %d, want >= RingHeadOffset = %d", st.RingTotalOffset, st.RingHeadOffset)
	}
}

// TestServer_PipeMode_NoOverflow_ShortDisconnectReplaysCleanly is the
// negative control for AC-S862203-2-3: a disconnect SHORT enough that the
// requested offset is still within the retained window replays cleanly
// (start == requested, no false-positive overflow).
func TestServer_PipeMode_NoOverflow_ShortDisconnectReplaysCleanly(t *testing.T) {
	_, sockPath := newPipeTestServer(t, 0, // default (large) ring — no eviction
		"FAKE_NDJSON_COUNT=5", "FAKE_NDJSON_STDERR_COUNT=0")
	time.Sleep(200 * time.Millisecond)

	c := dial(t, sockPath)
	requested := int64(0)
	c.send(MsgAttach, EncodeAttach(requested))
	payload := c.recvUntil(MsgData)
	start, _, err := DecodeData(payload)
	if err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if start != requested {
		t.Fatalf("ATTACH start offset = %d, want exactly %d (false-positive overflow)", start, requested)
	}
}
