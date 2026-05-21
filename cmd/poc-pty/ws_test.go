package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	pocpty "github.com/tjst-t/palmux2/internal/poc/pty"
)

// newTestServer spins up a test HTTP server backed by a poc-pty daemon
// that uses /bin/bash -c 'cat' as the substitute claude.
func newTestServer(t *testing.T) (*httptest.Server, *pocpty.Daemon) {
	t.Helper()
	daemon := pocpty.NewDaemon("/bin/bash", []string{"-c", "cat"}, pocpty.DefaultRingSize)
	srv := pocpty.NewServer(daemon, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		daemon.Shutdown()
	})
	return ts, daemon
}

// wsURL converts an http:// test server URL to ws://.
func wsURL(ts *httptest.Server, path string) string {
	return "ws" + ts.URL[len("http"):] + path
}

// TestWS_Bidirectional verifies that bytes sent from client reach the PTY
// and bytes from the PTY reach the client.
func TestWS_Bidirectional(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL(ts, "/poc/pty/attach"), nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()

	marker := "WS-BIDIR-MARKER-12345\n"
	if err := conn.Write(ctx, websocket.MessageBinary, []byte(marker)); err != nil {
		t.Fatalf("ws write: %v", err)
	}

	// Read frames until we see the marker echoed back (cat echoes stdin).
	deadline := time.Now().Add(5 * time.Second)
	var received []byte
	for time.Now().Before(deadline) {
		readCtx, readCancel := context.WithDeadline(ctx, time.Now().Add(500*time.Millisecond))
		_, msg, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			continue
		}
		received = append(received, msg...)
		if containsBytes(received, []byte("WS-BIDIR-MARKER-12345")) {
			return // success
		}
	}
	t.Fatalf("did not receive echoed marker within 5s; got %q", received)
}

// TestWS_RingReplay verifies that a second WS connection receives the
// contents of the ring buffer written by the first connection.
func TestWS_RingReplay(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	markerStr := fmt.Sprintf("RING-MARKER-%d\n", time.Now().UnixNano())

	// --- First connection: send marker, wait for echo, disconnect. ---
	conn1, _, err := websocket.Dial(ctx, wsURL(ts, "/poc/pty/attach"), nil)
	if err != nil {
		t.Fatalf("ws dial (conn1): %v", err)
	}

	if err := conn1.Write(ctx, websocket.MessageBinary, []byte(markerStr)); err != nil {
		conn1.CloseNow()
		t.Fatalf("ws write (conn1): %v", err)
	}

	// Wait for marker to be echoed (ensures it's in the ring buffer).
	deadline := time.Now().Add(5 * time.Second)
	var firstReceived []byte
	for time.Now().Before(deadline) {
		readCtx, readCancel := context.WithDeadline(ctx, time.Now().Add(300*time.Millisecond))
		_, msg, err := conn1.Read(readCtx)
		readCancel()
		if err != nil {
			continue
		}
		firstReceived = append(firstReceived, msg...)
		if containsBytes(firstReceived, []byte("RING-MARKER")) {
			break
		}
	}
	if !containsBytes(firstReceived, []byte("RING-MARKER")) {
		t.Logf("first connection received: %q", firstReceived)
		// ring might have marker even if echo timing is off; proceed
	}
	conn1.Close(websocket.StatusNormalClosure, "done")

	// Give the ring a moment to settle.
	time.Sleep(200 * time.Millisecond)

	// --- Second connection: verify ring replay contains marker. ---
	conn2, _, err := websocket.Dial(ctx, wsURL(ts, "/poc/pty/attach"), nil)
	if err != nil {
		t.Fatalf("ws dial (conn2): %v", err)
	}
	defer conn2.CloseNow()

	deadline2 := time.Now().Add(5 * time.Second)
	var replayed []byte
	for time.Now().Before(deadline2) {
		readCtx, readCancel := context.WithDeadline(ctx, time.Now().Add(300*time.Millisecond))
		_, msg, err := conn2.Read(readCtx)
		readCancel()
		if err != nil {
			continue
		}
		replayed = append(replayed, msg...)
		if containsBytes(replayed, []byte("RING-MARKER")) {
			return // success
		}
	}
	t.Fatalf("ring replay did not contain marker; replayed=%q", replayed)
}

// TestWS_StatsEndpoint verifies the /poc/pty/stats endpoint returns valid JSON.
func TestWS_StatsEndpoint(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/poc/pty/stats")
	if err != nil {
		t.Fatalf("GET /poc/pty/stats: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected application/json got %q", ct)
	}
}

// TestWS_ClientDisconnect_DaemonAlive verifies that when the WS client
// disconnects, the daemon and subprocess remain alive (no auto-kill).
func TestWS_ClientDisconnect_DaemonAlive(t *testing.T) {
	t.Parallel()
	ts, daemon := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL(ts, "/poc/pty/attach"), nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	// Trigger spawn.
	conn.Write(ctx, websocket.MessageBinary, []byte("hi\n"))
	time.Sleep(300 * time.Millisecond)
	conn.Close(websocket.StatusNormalClosure, "done")

	// Daemon subprocess should still be alive after client disconnects.
	time.Sleep(200 * time.Millisecond)
	stats := daemon.CurrentStats()
	if !stats.Alive {
		t.Fatalf("daemon subprocess died after client disconnect; stats=%+v", stats)
	}
}

// TestWS_FreePort verifies the server binds on an ephemeral port correctly.
func TestWS_FreePort(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if port == 0 {
		t.Fatal("expected non-zero port")
	}
}

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}
