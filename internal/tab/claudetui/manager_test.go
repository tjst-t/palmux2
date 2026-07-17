package claudetui

import (
	"context"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
	"github.com/tjst-t/palmux2/internal/tab/agenttui"
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

// TestManagerDetachAll verifies the SURVIVAL contract of DetachAll (the call
// palmux2's own process-exit path uses): it removes every daemon from the
// Manager (Len()==0, so a freshly (re)started palmux2 rebuilds them) BUT
// leaves each underlying ptyhost process alive and its socket still
// listening — i.e. it detaches the client WITHOUT shutting the held claude
// process down. This is the whole point of ADR-0001/0002 restart survival;
// the slow real-process E2E already proves it end-to-end, this is the fast
// guard on the exact same path.
func TestManagerDetachAll(t *testing.T) {
	bin := fakeBin(t)
	m := NewManager(ManagerConfig{ClaudeBin: bin, RingSize: 1 << 16})
	ctx := context.Background()

	// Start 2+ daemons via the in-process ptyhost fallback (no PalmuxBin) so
	// each has a REAL, listening ptyhost holding a real fake_claude child.
	// Capture the daemon refs + their socket paths BEFORE DetachAll clears
	// the Manager's map (we need them to probe survival afterward).
	type held struct {
		d    *Daemon
		sock string
	}
	var helds []held
	for _, br := range []string{"b1", "b2", "b3"} {
		d, err := m.EnsureDaemon(ctx, "r", br, defaultTabID, "")
		if err != nil {
			t.Fatalf("EnsureDaemon r/%s: %v", br, err)
		}
		if err := d.EnsureStarted(ctx); err != nil {
			t.Fatalf("EnsureStarted r/%s: %v", br, err)
		}
		waitForState(t, d, StateRunning, 5*time.Second)
		sock, _ := d.ptyHostPaths()
		helds = append(helds, held{d: d, sock: sock})
	}
	if m.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", m.Len())
	}

	// Ensure the surviving ptyhosts are ALWAYS reaped, pass or fail, so this
	// test never leaks processes (DetachAll deliberately does NOT kill them).
	t.Cleanup(func() {
		for _, h := range helds {
			if conn, ok := agenttui.ProbeExisting(h.sock); ok {
				_ = ptyhost.WriteFrame(conn, ptyhost.MsgShutdown, ptyhost.EncodeShutdown(ptyhost.ShutdownPayload{GraceMillis: 200}))
				_ = conn.Close()
			}
		}
	})

	done := make(chan struct{})
	go func() {
		_ = m.DetachAll(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("DetachAll timed out")
	}

	// (1) The Manager forgot every daemon.
	if m.Len() != 0 {
		t.Fatalf("Len() = %d after DetachAll, want 0", m.Len())
	}

	// (2) SURVIVAL: every ptyhost is STILL listening (DetachAll must NOT have
	// sent SHUTDOWN). Probe each socket and confirm a HELLO round-trips —
	// proving the held claude process is alive and a future palmux2 could
	// reconnect to it.
	for _, h := range helds {
		conn, ok := agenttui.ProbeExisting(h.sock)
		if !ok {
			t.Fatalf("SURVIVAL FAIL: ptyhost socket %s no longer listening after DetachAll — the held process was killed, defeating restart survival", h.sock)
		}
		hello, herr := agenttui.SendHello(conn)
		_ = conn.Close()
		if herr != nil {
			t.Fatalf("SURVIVAL FAIL: HELLO to surviving ptyhost %s failed: %v", h.sock, herr)
		}
		if hello.Pid <= 0 {
			t.Fatalf("SURVIVAL FAIL: surviving ptyhost %s reported non-positive pid %d", h.sock, hello.Pid)
		}
	}
	t.Logf("DetachAll: Manager cleared (Len=0) and all 3 ptyhosts survived, still listening + HELLO-responsive")
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
