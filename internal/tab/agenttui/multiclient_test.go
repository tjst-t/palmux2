package agenttui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// wsAttach dials the given test-server URL and returns the connection.
// The caller is responsible for closing.
func wsAttach(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + ts.URL[len("http"):]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	return conn
}

// readRoleEvent reads frames from conn until a {type:"role"} text frame arrives
// or the timeout expires.  It returns the first role event it sees.
func readRoleEvent(t *testing.T, conn *websocket.Conn, timeout time.Duration) RoleEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for role event (timeout=%v)", timeout)
		}
		readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		mt, raw, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			// timeout or context done; keep looping until outer deadline
			continue
		}
		if mt != websocket.MessageText {
			continue // skip binary frames (PTY bytes)
		}
		var ev RoleEvent
		if jErr := json.Unmarshal(raw, &ev); jErr != nil {
			continue // skip non-JSON text frames
		}
		if ev.Type != "role" {
			continue // skip grid.init / grid.diff frames
		}
		return ev
	}
}

// drainUntilRole drains all frames until a role event is seen.
// It returns the role event; other frames are discarded.
func drainUntilRole(t *testing.T, conn *websocket.Conn, timeout time.Duration) RoleEvent {
	t.Helper()
	return readRoleEvent(t, conn, timeout)
}

// waitForRoleEvent reads until a role event with the expected role arrives or
// the timeout is exceeded.  It fails the test if the wrong role is seen first.
func waitForRoleEvent(t *testing.T, conn *websocket.Conn, wantRole string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for role=%q (timeout=%v)", wantRole, timeout)
		}
		readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		mt, raw, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			continue
		}
		if mt != websocket.MessageText {
			continue
		}
		var ev RoleEvent
		if jErr := json.Unmarshal(raw, &ev); jErr != nil {
			continue
		}
		if ev.Type != "role" {
			continue
		}
		if ev.Role != wantRole {
			t.Fatalf("expected role=%q but got role=%q", wantRole, ev.Role)
		}
		return
	}
}

// ─── newTestDaemonStarted creates a daemon backed by fake_claude, starts it,
// and waits for StateRunning.
func newTestDaemonStarted(t *testing.T) *Daemon {
	t.Helper()
	d := newTestDaemon(t)
	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)
	return d
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestSingleClientGetsActive verifies that the first subscriber to attach
// receives a role event with role=active.
// [AC-S0fd64b-3-1]
func TestSingleClientGetsActive(t *testing.T) {
	d := newTestDaemonStarted(t)
	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	conn := wsAttach(t, ts)
	defer conn.CloseNow()

	ev := drainUntilRole(t, conn, 5*time.Second)
	if ev.Role != RoleActive {
		t.Fatalf("single client: expected role=%q, got %q", RoleActive, ev.Role)
	}
	if ev.Since <= 0 {
		t.Fatalf("single client: role event has zero/negative since: %d", ev.Since)
	}
	t.Logf("single client role event: role=%q since=%d", ev.Role, ev.Since)
}

// TestSecondClientIsViewer verifies that a second client attaching while the
// first is still connected receives role=viewer.
// [AC-S0fd64b-3-2]
func TestSecondClientIsViewer(t *testing.T) {
	d := newTestDaemonStarted(t)
	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	// Client A connects first and should be active.
	connA := wsAttach(t, ts)
	defer connA.CloseNow()

	evA := drainUntilRole(t, connA, 5*time.Second)
	if evA.Role != RoleActive {
		t.Fatalf("client A: expected active, got %q", evA.Role)
	}

	// Client B connects second and should be a viewer.
	connB := wsAttach(t, ts)
	defer connB.CloseNow()

	evB := drainUntilRole(t, connB, 5*time.Second)
	if evB.Role != RoleViewer {
		t.Fatalf("client B: expected viewer, got %q", evB.Role)
	}
	t.Logf("client A: role=%q  client B: role=%q", evA.Role, evB.Role)
}

// TestLastTypedWinsRoleTransition verifies that when a viewer client sends
// input, both clients receive a role event that swaps their roles.
// [AC-S0fd64b-3-3]
func TestLastTypedWinsRoleTransition(t *testing.T) {
	d := newTestDaemonStarted(t)
	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	// Client A → active.
	connA := wsAttach(t, ts)
	defer connA.CloseNow()
	drainUntilRole(t, connA, 5*time.Second) // consume initial active event

	// Client B → viewer.
	connB := wsAttach(t, ts)
	defer connB.CloseNow()
	drainUntilRole(t, connB, 5*time.Second) // consume initial viewer event

	// Client B sends input → should trigger role transfer.
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer sendCancel()
	if err := connB.Write(sendCtx, websocket.MessageBinary, []byte("hello\n")); err != nil {
		t.Fatalf("client B send input: %v", err)
	}

	// Both clients should receive a new role event within 1 s.
	// Use goroutines so we wait on both concurrently.
	type result struct {
		ev  RoleEvent
		err string
	}
	chA := make(chan result, 1)
	chB := make(chan result, 1)

	go func() {
		deadline := time.Now().Add(2 * time.Second)
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		for time.Now().Before(deadline) {
			readCtx, readCancel := context.WithTimeout(ctx, 300*time.Millisecond)
			mt, raw, err := connA.Read(readCtx)
			readCancel()
			if err != nil {
				continue
			}
			if mt != websocket.MessageText {
				continue
			}
			var ev RoleEvent
			if jErr := json.Unmarshal(raw, &ev); jErr != nil || ev.Type != "role" {
				continue
			}
			chA <- result{ev: ev}
			return
		}
		chA <- result{err: "timed out"}
	}()

	go func() {
		deadline := time.Now().Add(2 * time.Second)
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		for time.Now().Before(deadline) {
			readCtx, readCancel := context.WithTimeout(ctx, 300*time.Millisecond)
			mt, raw, err := connB.Read(readCtx)
			readCancel()
			if err != nil {
				continue
			}
			if mt != websocket.MessageText {
				continue
			}
			var ev RoleEvent
			if jErr := json.Unmarshal(raw, &ev); jErr != nil || ev.Type != "role" {
				continue
			}
			chB <- result{ev: ev}
			return
		}
		chB <- result{err: "timed out"}
	}()

	resA := <-chA
	resB := <-chB

	if resA.err != "" {
		t.Fatalf("client A: %s", resA.err)
	}
	if resB.err != "" {
		t.Fatalf("client B: %s", resB.err)
	}

	// After B sends input: B → active, A → viewer.
	if resA.ev.Role != RoleViewer {
		t.Errorf("client A expected viewer after B sends input, got %q", resA.ev.Role)
	}
	if resB.ev.Role != RoleActive {
		t.Errorf("client B expected active after sending input, got %q", resB.ev.Role)
	}
	t.Logf("role transition: A=%q B=%q (last-typed-wins)", resA.ev.Role, resB.ev.Role)
}

// TestActiveTransferOnDisconnect verifies that when the active client
// disconnects, the remaining viewer receives a role=active event within 1 s.
// [AC-S0fd64b-3-4]
func TestActiveTransferOnDisconnect(t *testing.T) {
	d := newTestDaemonStarted(t)
	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	// Client A → active.
	connA := wsAttach(t, ts)
	drainUntilRole(t, connA, 5*time.Second) // consume initial active event

	// Client B → viewer.
	connB := wsAttach(t, ts)
	defer connB.CloseNow()
	drainUntilRole(t, connB, 5*time.Second) // consume initial viewer event

	// Disconnect client A (the active one).
	connA.CloseNow()

	// Client B should now receive role=active within 1 s.
	waitForRoleEvent(t, connB, RoleActive, 2*time.Second)
	t.Log("active transfer on disconnect: viewer promoted to active")
}

// TestAllDisconnectThenReconnect verifies that after all clients disconnect the
// coordinator resets to idle and the next client gets role=active.
// [AC-S0fd64b-3-1] (regression)
func TestAllDisconnectThenReconnect(t *testing.T) {
	d := newTestDaemonStarted(t)
	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	// First client connects and disconnects.
	conn1 := wsAttach(t, ts)
	drainUntilRole(t, conn1, 5*time.Second) // should be active
	conn1.CloseNow()

	// Give the server a moment to process the disconnect.
	time.Sleep(50 * time.Millisecond)

	// Second client: should also get active (no previous subscriber).
	conn2 := wsAttach(t, ts)
	defer conn2.CloseNow()

	ev := drainUntilRole(t, conn2, 5*time.Second)
	if ev.Role != RoleActive {
		t.Fatalf("reconnect: expected active after all disconnects, got %q", ev.Role)
	}
	t.Log("all-disconnect-then-reconnect: new client gets active")
}

// TestRoleEventSchema verifies the wire shape of a role event:
// {"type":"role","role":"active"|"viewer","since":<positive-int>}.
func TestRoleEventSchema(t *testing.T) {
	d := newTestDaemonStarted(t)
	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	conn := wsAttach(t, ts)
	defer conn.CloseNow()

	ev := drainUntilRole(t, conn, 5*time.Second)

	if ev.Type != "role" {
		t.Errorf("type = %q, want %q", ev.Type, "role")
	}
	if ev.Role != RoleActive && ev.Role != RoleViewer {
		t.Errorf("role = %q, want %q or %q", ev.Role, RoleActive, RoleViewer)
	}
	if ev.Since <= 0 {
		t.Errorf("since = %d, want positive unix milliseconds", ev.Since)
	}
	t.Logf("role event schema OK: %s", fmt.Sprintf(`{"type":%q,"role":%q,"since":%d}`,
		ev.Type, ev.Role, ev.Since))
}
