package claudetui

import (
	"context"
	"testing"
	"time"
)

// TestManagerEnsureDaemon verifies that EnsureDaemon returns the same Daemon
// for the same (repoID, branchID) key on repeated calls.
func TestManagerEnsureDaemon(t *testing.T) {
	m := NewManager(ManagerConfig{ClaudeBin: "claude"})

	ctx := context.Background()
	d1, err := m.EnsureDaemon(ctx, "repo-1", "branch-1", "")
	if err != nil {
		t.Fatalf("EnsureDaemon 1: %v", err)
	}
	d2, err := m.EnsureDaemon(ctx, "repo-1", "branch-1", "")
	if err != nil {
		t.Fatalf("EnsureDaemon 2: %v", err)
	}
	if d1 != d2 {
		t.Fatal("EnsureDaemon returned different Daemon instances for same key")
	}

	d3, err := m.EnsureDaemon(ctx, "repo-1", "branch-2", "")
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

// TestManagerGetBeforeEnsure verifies that Get returns nil before EnsureDaemon.
func TestManagerGetBeforeEnsure(t *testing.T) {
	m := NewManager(ManagerConfig{ClaudeBin: "claude"})
	if got := m.Get("r", "b"); got != nil {
		t.Fatal("Get should return nil before EnsureDaemon")
	}
}

// TestManagerCloseDaemon verifies that CloseDaemon shuts down and removes the
// daemon, and that a new EnsureDaemon call returns a fresh instance.
func TestManagerCloseDaemon(t *testing.T) {
	bin := fakeBin(t)
	m := NewManager(ManagerConfig{ClaudeBin: bin})
	ctx := context.Background()

	d1, err := m.EnsureDaemon(ctx, "r", "b", "")
	if err != nil {
		t.Fatalf("EnsureDaemon: %v", err)
	}

	if err := m.CloseDaemon(ctx, "r", "b"); err != nil {
		t.Fatalf("CloseDaemon: %v", err)
	}
	if m.Len() != 0 {
		t.Fatalf("Len() = %d after close, want 0", m.Len())
	}

	// A new EnsureDaemon should give a fresh Daemon.
	d2, err := m.EnsureDaemon(ctx, "r", "b", "")
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
	if err := m.CloseDaemon(context.Background(), "ghost-repo", "ghost-branch"); err != nil {
		t.Fatalf("CloseDaemon on non-existent: %v", err)
	}
}

// TestManagerShutdownAll verifies that ShutdownAll removes and stops all daemons.
func TestManagerShutdownAll(t *testing.T) {
	bin := fakeBin(t)
	m := NewManager(ManagerConfig{ClaudeBin: bin})
	ctx := context.Background()

	for _, br := range []string{"b1", "b2", "b3"} {
		if _, err := m.EnsureDaemon(ctx, "r", br, ""); err != nil {
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
	d, err := m.EnsureStarted(ctx, "repo", "branch")
	if err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	t.Cleanup(func() { m.CloseDaemon(ctx, "repo", "branch") })

	waitForState(t, d, StateRunning, 5*time.Second)
	if !d.CurrentStats().Alive {
		t.Fatal("daemon should be alive after EnsureStarted")
	}
}
