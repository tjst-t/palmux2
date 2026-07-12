package ptyhost

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeChildBin compiles testdata/fake_child.go and returns its path, cached
// per test TempDir (mirrors internal/tab/claudetui/daemon_test.go's fakeBin
// helper).
func fakeChildBin(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	src := filepath.Join(wd, "testdata", "fake_child.go")
	bin := filepath.Join(t.TempDir(), "fake_child")
	cmd := exec.Command("go", "build", "-o", bin, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("compiling fake_child: %v\n%s", err, out)
	}
	return bin
}

// newTestServer starts a Server holding the compiled fake_child binary under
// a real PTY, serving the socket protocol on a temp-dir socket. It returns
// the Server plus a context+cancel the test can use to stop it, and arranges
// cleanup.
func newTestServer(t *testing.T, extraArgv ...string) (*Server, context.Context, string) {
	t.Helper()
	bin := fakeChildBin(t)
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "ptyhost.sock")
	statusPath := filepath.Join(dir, "ptyhost.json")

	argv := append([]string{bin}, extraArgv...)
	srv, err := NewServer(Config{
		Argv:           argv,
		SocketPath:     sockPath,
		StatusPath:     statusPath,
		RingSize:       1 << 16,
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

	// Wait for the socket to appear before returning.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return srv, ctx, sockPath
}

// dialAndFrame is a tiny test client: dial the socket, write one frame, and
// optionally read frames via a helper method.
type testClient struct {
	t    *testing.T
	conn net.Conn
	mu   sync.Mutex
}

func dial(t *testing.T, sockPath string) *testClient {
	t.Helper()
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial %s: %v", sockPath, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &testClient{t: t, conn: conn}
}

func (c *testClient) send(msgType MsgType, payload []byte) {
	c.t.Helper()
	if err := WriteFrame(c.conn, msgType, payload); err != nil {
		c.t.Fatalf("send %v: %v", msgType, err)
	}
}

func (c *testClient) recv() (MsgType, []byte) {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, payload, err := ReadFrame(c.conn)
	if err != nil {
		c.t.Fatalf("recv: %v", err)
	}
	return mt, payload
}

// recvUntil reads frames until one of the given types is seen (skipping
// others, e.g. DATA chunks that arrive interleaved with a STATUS reply), and
// returns it. Fails the test if none arrives before the deadline.
func (c *testClient) recvUntil(want MsgType) []byte {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		mt, payload, err := ReadFrame(c.conn)
		if err != nil {
			c.t.Fatalf("recvUntil(%v): %v", want, err)
		}
		if mt == want {
			return payload
		}
	}
	c.t.Fatalf("recvUntil(%v): deadline exceeded", want)
	return nil
}

// TestServer_HelloCarriesVersionModePidArgvHash covers the HELLO handshake
// (AC-S3f2658-1-1).
func TestServer_HelloCarriesVersionModePidArgvHash(t *testing.T) {
	srv, _, sockPath := newTestServer(t)
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
	if hello.ProtocolVersion != ProtocolVersion {
		t.Fatalf("ProtocolVersion = %d, want %d", hello.ProtocolVersion, ProtocolVersion)
	}
	if hello.Mode != "pty" {
		t.Fatalf("Mode = %q, want %q", hello.Mode, "pty")
	}
	if hello.Pid <= 0 {
		t.Fatalf("Pid = %d, want > 0", hello.Pid)
	}
	if hello.ArgvHash == "" {
		t.Fatal("ArgvHash is empty")
	}
	wantHash := ArgvHash(srv.cfg.Argv)
	if hello.ArgvHash != wantHash {
		t.Fatalf("ArgvHash = %q, want %q", hello.ArgvHash, wantHash)
	}
}

// TestServer_AttachReplaysStartupOutput covers ATTACH{offset:-1} replaying
// the child's startup banner via DATA frames with monotone absolute offsets
// (AC-S3f2658-1-1).
func TestServer_AttachReplaysStartupOutput(t *testing.T) {
	_, _, sockPath := newTestServer(t)

	// Give the child a moment to emit its startup line before attaching, so
	// this exercises REPLAY (not just live delivery).
	time.Sleep(200 * time.Millisecond)

	c := dial(t, sockPath)
	c.send(MsgAttach, EncodeAttach(-1))
	payload := c.recvUntil(MsgData)
	offset, data, err := DecodeData(payload)
	if err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if offset != 0 {
		t.Fatalf("replay start offset = %d, want 0", offset)
	}
	if !strings.Contains(string(data), "fake_child started") {
		t.Fatalf("replay data = %q, want it to contain startup banner", data)
	}
}

// TestServer_InputEchoesOverData covers INPUT -> child -> DATA round trip
// with monotonically increasing absolute offsets (AC-S3f2658-1-1).
func TestServer_InputEchoesOverData(t *testing.T) {
	_, _, sockPath := newTestServer(t)
	c := dial(t, sockPath)

	c.send(MsgAttach, EncodeAttach(-1))
	_ = c.recvUntil(MsgData) // initial replay (startup banner)

	c.send(MsgInput, EncodeInput([]byte("hello-ptyhost\n")))

	var lastOffset int64 = -1
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
		offset, data, err := DecodeData(payload)
		if err != nil {
			t.Fatalf("DecodeData: %v", err)
		}
		if offset < lastOffset {
			t.Fatalf("DATA offset went backwards: %d after %d", offset, lastOffset)
		}
		lastOffset = offset
		if bytes.Contains(data, []byte("echo: hello-ptyhost")) {
			sawEcho = true
		}
	}
	if !sawEcho {
		t.Fatal("did not observe echoed input over DATA frames")
	}
}

// TestServer_ResizeChangesChildWinsize covers RESIZE{cols,rows} -> the child
// observing the new PTY winsize via TIOCGWINSZ (AC-S3f2658-1-1).
func TestServer_ResizeChangesChildWinsize(t *testing.T) {
	_, _, sockPath := newTestServer(t)
	c := dial(t, sockPath)

	c.send(MsgAttach, EncodeAttach(-1))
	_ = c.recvUntil(MsgData)

	c.send(MsgResize, EncodeResize(133, 55))

	deadline := time.Now().Add(5 * time.Second)
	sawSize := false
	for time.Now().Before(deadline) && !sawSize {
		_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		mt, payload, err := ReadFrame(c.conn)
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if mt != MsgData {
			continue
		}
		_, data, err := DecodeData(payload)
		if err != nil {
			t.Fatalf("DecodeData: %v", err)
		}
		if bytes.Contains(data, []byte("winsize: 133x55")) {
			sawSize = true
		}
	}
	if !sawSize {
		t.Fatal("did not observe the child reporting the new winsize after RESIZE")
	}
}

// TestServer_StatusReportsAliveRingUsage covers a STATUS request/response
// including pid, alive, and ring usage/offset fields (AC-S3f2658-1-1).
func TestServer_StatusReportsAliveRingUsage(t *testing.T) {
	_, _, sockPath := newTestServer(t)
	time.Sleep(200 * time.Millisecond) // let some ring data accumulate
	c := dial(t, sockPath)

	c.send(MsgStatus, EncodeStatusRequest())
	payload := c.recvUntil(MsgStatus)
	st, err := DecodeStatusResponse(payload)
	if err != nil {
		t.Fatalf("DecodeStatusResponse: %v", err)
	}
	if !st.Alive {
		t.Fatal("Alive = false, want true (child still running)")
	}
	if st.Pid <= 0 {
		t.Fatalf("Pid = %d, want > 0", st.Pid)
	}
	if st.RingBytes <= 0 {
		t.Fatalf("RingBytes = %d, want > 0 (startup banner should have been written)", st.RingBytes)
	}
	if st.RingTotalOffset < int64(st.RingBytes) {
		t.Fatalf("RingTotalOffset = %d, should be >= RingBytes = %d", st.RingTotalOffset, st.RingBytes)
	}
}

// TestServer_ExitStatusRecordedInStatusFrameAndFile covers AC-S3f2658-1-4:
// after the child exits, STATUS reports alive=false + the exact exit code,
// the on-disk status file agrees, and the server itself then returns from
// Run without respawning.
func TestServer_ExitStatusRecordedInStatusFrameAndFile(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "ptyhost.sock")
	statusPath := filepath.Join(dir, "ptyhost.json")

	srv, err := NewServer(Config{
		Argv:           []string{"/bin/sh", "-c", "exit 7"},
		SocketPath:     sockPath,
		StatusPath:     statusPath,
		RingSize:       1 << 16,
		GracePeriod:    2 * time.Second,
		PostExitLinger: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx := context.Background()
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- srv.Run(ctx) }()

	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Server.Run did not self-terminate after child exit within 10s")
	}

	// The exit status must be recorded in the on-disk status file.
	sf, err := ReadStatusFile(statusPath)
	if err != nil {
		t.Fatalf("ReadStatusFile: %v", err)
	}
	if sf.Alive {
		t.Fatal("status file Alive = true, want false after child exit")
	}
	if !sf.ExitCodeValid {
		t.Fatal("status file ExitCodeValid = false, want true")
	}
	if sf.ExitCode != 7 {
		t.Fatalf("status file ExitCode = %d, want 7", sf.ExitCode)
	}
	if sf.ExitedAt == nil {
		t.Fatal("status file ExitedAt is nil, want set")
	}

	// AC-S3f2658-1-4: no orphaned PTY fd / goroutine leak after clean
	// shutdown. Run() has already returned by this point, meaning
	// server.wg.Wait() completed inside teardown() — every readLoop,
	// waitChild, accept-loop and per-connection goroutine this Server
	// started must have exited. Directly assert the wg is drained (a
	// leaked goroutine still holding it would make a fresh Wait() block).
	waitDone := make(chan struct{})
	go func() {
		srv.wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("server.wg is not drained after Run returned — goroutine leak")
	}

	// The PTY master fd must be closed (waitChild calls ptmx.Close() before
	// recording the exit) — a further Read must fail, not block or succeed.
	buf := make([]byte, 1)
	if _, err := srv.ptmx.Read(buf); err == nil {
		t.Fatal("expected the PTY master fd to be closed after child exit, but Read succeeded")
	}
}

// TestServer_ExitStatusViaStatusRequest_BeforeTeardown starts a longer-lived
// child, kills it via SHUTDOWN, and asserts a STATUS request made just
// before the server tears down would see the exit recorded — this exercises
// the same "record in both channels" contract as
// TestServer_ExitStatusRecordedInStatusFrameAndFile but through the socket
// STATUS response instead of only the file, for a child that dies with a
// known custom exit code communicated in-band (not via shell -c).
func TestServer_ExitStatusViaStatusRequest_BeforeTeardown(t *testing.T) {
	_, _, sockPath := newTestServer(t)
	c := dial(t, sockPath)
	c.send(MsgAttach, EncodeAttach(-1))
	_ = c.recvUntil(MsgData)

	// Ask fake_child to exit with code 3.
	c.send(MsgInput, EncodeInput([]byte("PTYHOST_TEST_EXIT:3\n")))

	// Poll STATUS until alive=false (the exit is asynchronous).
	deadline := time.Now().Add(5 * time.Second)
	var st StatusPayload
	for time.Now().Before(deadline) {
		c.send(MsgStatus, EncodeStatusRequest())
		payload := c.recvUntil(MsgStatus)
		var derr error
		st, derr = DecodeStatusResponse(payload)
		if derr != nil {
			t.Fatalf("DecodeStatusResponse: %v", derr)
		}
		if !st.Alive {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st.Alive {
		t.Fatal("expected STATUS to eventually report alive=false")
	}
	if !st.ExitCodeValid || st.ExitCode != 3 {
		t.Fatalf("ExitCode = %d (valid=%v), want 3 (valid=true)", st.ExitCode, st.ExitCodeValid)
	}
}

// TestServer_ShutdownTerminatesChildAndServerExits covers the SHUTDOWN
// message: SIGTERM the child, then the server tears down (thin holder — no
// respawn).
func TestServer_ShutdownTerminatesChildAndServerExits(t *testing.T) {
	bin := fakeChildBin(t)
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "ptyhost.sock")
	statusPath := filepath.Join(dir, "ptyhost.json")

	srv, err := NewServer(Config{
		Argv:           []string{bin},
		SocketPath:     sockPath,
		StatusPath:     statusPath,
		RingSize:       1 << 16,
		GracePeriod:    2 * time.Second,
		PostExitLinger: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx := context.Background()
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- srv.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	c := dial(t, sockPath)
	c.send(MsgShutdown, EncodeShutdown(ShutdownPayload{GraceMillis: 500}))

	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("Run returned error after SHUTDOWN: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Server.Run did not return within 10s of SHUTDOWN")
	}

	sf, err := ReadStatusFile(statusPath)
	if err != nil {
		t.Fatalf("ReadStatusFile: %v", err)
	}
	if sf.Alive {
		t.Fatal("status file Alive = true after SHUTDOWN, want false")
	}
}

// TestServer_Reconnect_ClosesPreviousConnAndReplaysFromNewOffset simulates
// palmux2 restarting and reconnecting: a second client ATTACHing supersedes
// the first (closed), and can resume from an arbitrary offset it remembers.
func TestServer_Reconnect_ClosesPreviousConnAndReplaysFromNewOffset(t *testing.T) {
	_, _, sockPath := newTestServer(t)

	c1 := dial(t, sockPath)
	c1.send(MsgAttach, EncodeAttach(-1))
	payload := c1.recvUntil(MsgData)
	_, data1, err := DecodeData(payload)
	if err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	firstLen := int64(len(data1))

	// New "palmux2 instance" reconnects, remembers the offset it last saw,
	// and asks to resume from there.
	c2 := dial(t, sockPath)
	c2.send(MsgAttach, EncodeAttach(firstLen))
	payload2 := c2.recvUntil(MsgData)
	offset2, _, err := DecodeData(payload2)
	if err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if offset2 != firstLen {
		t.Fatalf("reconnect replay start offset = %d, want %d (no gap/overlap)", offset2, firstLen)
	}

	// c1 should observe its connection being closed by the server (since c2
	// is now the active connection) — a read should return an error/EOF.
	_ = c1.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = ReadFrame(c1.conn)
	if err == nil {
		t.Fatal("expected the superseded connection to be closed by the server")
	}
}
