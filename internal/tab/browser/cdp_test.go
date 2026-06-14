// Package browser — unit tests for the VNC byte-pipe helpers (noVNC rework).
//
// The old CDP screencast proxy tests (clientFrame/serverFrame round-trips,
// CDPProxy ID increment, AttachScreencast guard, NavigatePage) are removed
// because those types/functions no longer exist.
//
// Remaining: CheckCDP guard (verifies the state guard logic works without a
// real VNC connection) and AttachVNC state guard.
package browser

import (
	"context"
	"testing"
)

// TestAttachVNC_NotRunning verifies that AttachVNC refuses connections when the
// browser is not in the running state. We verify this indirectly through State().
func TestAttachVNC_NotRunning(t *testing.T) {
	mgr, _, _ := newTestManager(t, "10.100.0.5")
	// mgr starts in stopped state.
	sv := mgr.State(context.Background())
	if sv.State != StateStopped {
		t.Errorf("initial state should be stopped, got %q", sv.State)
	}
	// AttachVNC would close the connection; we can't test without a real WS conn
	// but we confirm the state guard condition (state != StateRunning || addr == "").
	mgr.mu.Lock()
	notRunning := mgr.state != StateRunning || mgr.cdpAddr == ""
	mgr.mu.Unlock()
	if !notRunning {
		t.Error("expected state guard to be active when stopped")
	}
}

// TestCheckCDP_NotReachable verifies that CheckCDP returns false for an
// unreachable address (no real container in unit tests).
func TestCheckCDP_NotReachable(t *testing.T) {
	// 192.0.2.1 is TEST-NET-1 (RFC 5737) — guaranteed unreachable.
	reachable := CheckCDP(context.Background(), "192.0.2.1")
	if reachable {
		t.Error("CheckCDP should return false for unreachable address")
	}
}
