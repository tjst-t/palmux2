package claudetui

import (
	"context"
	"testing"
	"time"
)

// defaultTabID is the canonical tabID used across single-tab tests. Sadf90e
// switched the Manager / SessionStore key from (repo, branch) to
// (repo, branch, tab); we keep one constant here so the tests stay grep-able.
const defaultTabID = "claude:claude"

// TestManagerEnsureDaemon verifies that EnsureDaemon returns the same Daemon
// for the same (repoID, branchID, tabID) key on repeated calls.
func TestManagerEnsureDaemon(t *testing.T) {
	m := NewManager(ManagerConfig{ClaudeBin: "claude"})

	ctx := context.Background()
	d1, err := m.EnsureDaemon(ctx, "repo-1", "branch-1", defaultTabID, "")
	if err != nil {
		t.Fatalf("EnsureDaemon 1: %v", err)
	}
	d2, err := m.EnsureDaemon(ctx, "repo-1", "branch-1", defaultTabID, "")
	if err != nil {
		t.Fatalf("EnsureDaemon 2: %v", err)
	}
	if d1 != d2 {
		t.Fatal("EnsureDaemon returned different Daemon instances for same key")
	}

	d3, err := m.EnsureDaemon(ctx, "repo-1", "branch-2", defaultTabID, "")
	if err != nil {
		t.Fatalf("EnsureDaemon 3: %v", err)
	}
	if d3 == d1 {
		t.Fatal("EnsureDaemon returned same Daemon for different branch")
	}

	if m.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", m.Len())
	}
}

// TestManagerEnsureDaemonPerTabIsolation verifies that two tabIDs on the same
// (repo, branch) yield distinct Daemons — the per-tab isolation that Sadf90e
// introduced.
func TestManagerEnsureDaemonPerTabIsolation(t *testing.T) {
	m := NewManager(ManagerConfig{ClaudeBin: "claude"})
	ctx := context.Background()

	dA, err := m.EnsureDaemon(ctx, "repo-1", "branch-1", "tab-A", "")
	if err != nil {
		t.Fatalf("EnsureDaemon tab-A: %v", err)
	}
	dB, err := m.EnsureDaemon(ctx, "repo-1", "branch-1", "tab-B", "")
	if err != nil {
		t.Fatalf("EnsureDaemon tab-B: %v", err)
	}
	if dA == dB {
		t.Fatal("EnsureDaemon returned same Daemon for distinct tabIDs on same branch")
	}
	if m.Len() != 2 {
		t.Fatalf("Len() = %d, want 2 (one per tab)", m.Len())
	}
}

// TestManagerGetBeforeEnsure verifies that Get returns nil before EnsureDaemon.
func TestManagerGetBeforeEnsure(t *testing.T) {
	m := NewManager(ManagerConfig{ClaudeBin: "claude"})
	if got := m.Get("r", "b", defaultTabID); got != nil {
		t.Fatal("Get should return nil before EnsureDaemon")
	}
}

// TestManagerCloseDaemon verifies that CloseDaemon shuts down and removes the
// daemon, and that a new EnsureDaemon call returns a fresh instance.
func TestManagerCloseDaemon(t *testing.T) {
	bin := fakeBin(t)
	m := NewManager(ManagerConfig{ClaudeBin: bin})
	ctx := context.Background()

	d1, err := m.EnsureDaemon(ctx, "r", "b", defaultTabID, "")
	if err != nil {
		t.Fatalf("EnsureDaemon: %v", err)
	}

	if err := m.CloseDaemon(ctx, "r", "b", defaultTabID); err != nil {
		t.Fatalf("CloseDaemon: %v", err)
	}
	if m.Len() != 0 {
		t.Fatalf("Len() = %d after close, want 0", m.Len())
	}

	// A new EnsureDaemon should give a fresh Daemon.
	d2, err := m.EnsureDaemon(ctx, "r", "b", defaultTabID, "")
	if err != nil {
		t.Fatalf("EnsureDaemon after close: %v", err)
	}
	if d2 == d1 {
		t.Fatal("EnsureDaemon after close returned the same (closed) Daemon")
	}
	d2.Shutdown()
}

// TestManagerCloseDaemonNoop verifies that CloseDaemon on a non-existent entry
// is a no-op.
func TestManagerCloseDaemonNoop(t *testing.T) {
	m := NewManager(ManagerConfig{ClaudeBin: "claude"})
	if err := m.CloseDaemon(context.Background(), "ghost-repo", "ghost-branch", defaultTabID); err != nil {
		t.Fatalf("CloseDaemon on non-existent: %v", err)
	}
}

// TestManagerCloseBranchDaemons verifies that CloseBranchDaemons (Sadf90e)
// reaps every daemon belonging to the given (repo, branch), regardless of
// tabID. This is the hook Provider.OnBranchClose now uses.
func TestManagerCloseBranchDaemons(t *testing.T) {
	bin := fakeBin(t)
	m := NewManager(ManagerConfig{ClaudeBin: bin})
	ctx := context.Background()

	// Two tabs on the same (repo, branch).
	if _, err := m.EnsureDaemon(ctx, "r", "b", "tab-A", ""); err != nil {
		t.Fatalf("EnsureDaemon tab-A: %v", err)
	}
	if _, err := m.EnsureDaemon(ctx, "r", "b", "tab-B", ""); err != nil {
		t.Fatalf("EnsureDaemon tab-B: %v", err)
	}
	// And one tab on a different branch — should NOT be touched.
	if _, err := m.EnsureDaemon(ctx, "r", "other", defaultTabID, ""); err != nil {
		t.Fatalf("EnsureDaemon other: %v", err)
	}
	if m.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", m.Len())
	}

	m.CloseBranchDaemons(ctx, "r", "b")

	if m.Len() != 1 {
		t.Fatalf("Len() = %d after CloseBranchDaemons, want 1", m.Len())
	}
	if got := m.Get("r", "other", defaultTabID); got == nil {
		t.Fatal("CloseBranchDaemons should not have touched the other branch's daemon")
	}
}

// TestManagerShutdownAll verifies that ShutdownAll removes and stops all daemons.
func TestManagerShutdownAll(t *testing.T) {
	bin := fakeBin(t)
	m := NewManager(ManagerConfig{ClaudeBin: bin})
	ctx := context.Background()

	for _, br := range []string{"b1", "b2", "b3"} {
		if _, err := m.EnsureDaemon(ctx, "r", br, defaultTabID, ""); err != nil {
			t.Fatalf("EnsureDaemon r/%s: %v", br, err)
		}
	}
	if m.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", m.Len())
	}

	done := make(chan struct{})
	go func() {
		m.ShutdownAll(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("ShutdownAll timed out")
	}

	if m.Len() != 0 {
		t.Fatalf("Len() = %d after ShutdownAll, want 0", m.Len())
	}
}

// TestManagerEnsureStarted tests the convenience helper.
func TestManagerEnsureStarted(t *testing.T) {
	bin := fakeBin(t)
	m := NewManager(ManagerConfig{ClaudeBin: bin, RingSize: 1 << 16})

	ctx := context.Background()
	d, err := m.EnsureStarted(ctx, "repo", "branch", defaultTabID)
	if err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	t.Cleanup(func() { m.CloseDaemon(ctx, "repo", "branch", defaultTabID) })

	waitForState(t, d, StateRunning, 5*time.Second)
	if !d.CurrentStats().Alive {
		t.Fatal("daemon should be alive after EnsureStarted")
	}
}
