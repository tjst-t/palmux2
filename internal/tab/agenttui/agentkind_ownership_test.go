package agenttui

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/agent"
	"github.com/tjst-t/palmux2/internal/ptyhost"
)

// This file is the [AC-S0e8afb-3-4] regression test: it proves, with REAL
// ptyhost.Server processes and REAL agenttui.Manager instances of TWO
// DIFFERENT [agent.Kind]s (claude and generic) pointed at the SAME on-disk
// run directory (exactly the shared-directory shape production uses once a
// second kind's Manager is wired — see cmd/palmux/main.go's per-kind loop),
// that the AgentKind ownership filter added by this Story (discover.go's
// ScanRunDir) keeps each Manager's startup discovery from
// dialing/adopting the OTHER kind's ptyhost.
//
// Unlike cmd/palmux/ptyhost_ownership_test.go's TestPtyOwnership_ModeFilter
// (which proves the Sfeed64-3 Mode filter distinguishes claudetui from
// claudeagent — a cross-PACKAGE, cross-transport-mode scenario), this test
// is entirely WITHIN agenttui: both ptyhosts are [ptyhost.ModePTY] (the
// Mode filter alone would NOT distinguish them — they only differ by
// AgentKind), and both Managers are plain *agenttui.Manager values of
// different Kind(). This is deliberate: it isolates the NEW filter this
// Story adds from the PRE-EXISTING Mode filter, so a bug in one can't hide
// behind the other still working.
//
// Sfeed64-3 lesson applied here (see discover.go's doc comment and
// docs/agenttui-ptyhost-merge-design.md's P3 risk #2): the ownership check
// must happen BEFORE any dial, not after. A liveness-probe dial against a
// ptyhost this SAME process does not yet manage is itself destructive
// (ptyhost.Server.replaceConn evicts whatever connection was previously
// active the instant a new one arrives) — dialing the OTHER kind's ptyhost
// even just to check "is it mine?" would evict that kind's eventual live
// connection. This test's "no cross-adoption" assertions below would catch
// exactly that class of ordering bug (dial-then-check instead of
// check-then-dial), not just a wrong boolean in the filter predicate.

// startRawKindPtyHost is a variant of startRawPtyHost (discover_test.go)
// that ALSO sets AgentKind — needed because production's own spawn path
// (Daemon.launchAndAttach) always sets it from the owning Manager's
// Adapter.Kind(), and this test must reproduce that exactly to exercise the
// real ownership filter (an AgentKind-less status file would fall back to
// "claude" by design, which would defeat this test for the generic side).
func startRawKindPtyHost(t *testing.T, runDir, bin, kind, repoID, branchID, tabID string) (sockPath, statusPath string, done chan struct{}, pid int) {
	t.Helper()
	seed := repoID + "__" + branchID + "__" + tabID
	sockPath = ptyhost.SocketPath(runDir, seed)
	statusPath = ptyhost.StatusPath(runDir, seed)
	srv, err := ptyhost.NewServer(ptyhost.Config{
		Argv:       []string{bin},
		SocketPath: sockPath,
		StatusPath: statusPath,
		RingSize:   1 << 16,
		Seed:       seed,
		RepoID:     repoID,
		BranchID:   branchID,
		TabID:      tabID,
		AgentKind:  kind,
	})
	if err != nil {
		t.Fatalf("ptyhost.NewServer(%s, kind=%s): %v", seed, kind, err)
	}
	done = make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Run(context.Background())
	}()
	if err := WaitForSocket(context.Background(), sockPath, 5*time.Second, nil); err != nil {
		t.Fatalf("ptyhost %s (kind=%s) never started listening: %v", seed, kind, err)
	}
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial %s: %v", seed, err)
	}
	hello, herr := SendHello(conn)
	_ = conn.Close()
	if herr != nil {
		t.Fatalf("HELLO to %s (kind=%s): %v", seed, kind, herr)
	}
	return sockPath, statusPath, done, hello.Pid
}

// TestAgentKindOwnership_DiscoveryDoesNotCrossAdopt is [AC-S0e8afb-3-4]: two
// REAL ModePTY ptyhosts — one AgentKind="claude", one AgentKind="generic" —
// sit side by side in the SAME run dir. Running BOTH Managers'
// DiscoverAndRestore against that shared dir must result in EACH Manager
// adopting ONLY its own-kind entry — never the other's, and never both
// entries by both Managers.
func TestAgentKindOwnership_DiscoveryDoesNotCrossAdopt(t *testing.T) {
	fakeClaudeBin := fakeBin(t)

	runDir := t.TempDir()
	ctx := context.Background()

	const (
		claudeRepo, claudeBranch, claudeTab    = "owner-claude-repo", "owner-claude-branch", "claude:claude"
		genericRepo, genericBranch, genericTab = "owner-generic-repo", "owner-generic-branch", "generic:generic"
	)

	claudeSock, _, claudeDone, claudePid := startRawKindPtyHost(t, runDir, fakeClaudeBin, "claude", claudeRepo, claudeBranch, claudeTab)
	genericSock, _, genericDone, genericPid := startRawKindPtyHost(t, runDir, fakeClaudeBin, "generic", genericRepo, genericBranch, genericTab)
	t.Cleanup(func() {
		shutdownRawPtyHost(claudeSock)
		shutdownRawPtyHost(genericSock)
		for _, d := range []chan struct{}{claudeDone, genericDone} {
			select {
			case <-d:
			case <-time.After(5 * time.Second):
				t.Log("raw ownership ptyhost did not fully tear down within 5s during cleanup")
			}
		}
	})

	claudeAdapter := agent.NewClaudeAdapter(fakeClaudeBin, nil)
	claudeMgr := NewManager(ManagerConfig{
		Adapter:        claudeAdapter,
		RingSize:       1 << 16,
		RunDirOverride: runDir,
	})
	t.Cleanup(func() { _ = claudeMgr.ShutdownAll(ctx) })

	genericAdapter := agent.NewGenericAdapter("generic", agent.GenericConfig{Command: fakeClaudeBin})
	genericMgr := NewManager(ManagerConfig{
		Adapter:        genericAdapter,
		RingSize:       1 << 16,
		RunDirOverride: runDir,
	})
	t.Cleanup(func() { _ = genericMgr.ShutdownAll(ctx) })

	// Sanity: the two Managers really are different kinds — otherwise this
	// test would not exercise the AgentKind filter at all.
	if claudeMgr.Kind() == genericMgr.Kind() {
		t.Fatalf("test setup bug: both managers report the same Kind() = %q", claudeMgr.Kind())
	}

	// Run BOTH managers' startup discovery against the SAME shared run dir —
	// this is the exact seam cmd/palmux/main.go's per-kind loop + discovery
	// goroutine drives once more than one kind is live in production.
	claudeAdopted, claudeCleaned, err := DiscoverAndRestore(ctx, claudeMgr, nil, nil)
	if err != nil {
		t.Fatalf("claude DiscoverAndRestore: %v", err)
	}
	genericAdopted, genericCleaned, err := DiscoverAndRestore(ctx, genericMgr, nil, nil)
	if err != nil {
		t.Fatalf("generic DiscoverAndRestore: %v", err)
	}

	// The whole point of the AgentKind filter: each manager adopts EXACTLY
	// its own entry, not both. Pre-fix (no AgentKind check), both managers
	// would find BOTH live ModePTY entries (HELLO doesn't care which kind is
	// asking, and the Mode filter alone doesn't distinguish two ModePTY
	// entries) and adopt both — claudeAdopted/genericAdopted would each be
	// 2, not 1.
	if claudeAdopted != 1 {
		t.Fatalf("claude manager adopted = %d, want 1 (only its own AgentKind entry — a value of 2 means the AgentKind filter let it dial/adopt the generic entry too)", claudeAdopted)
	}
	if genericAdopted != 1 {
		t.Fatalf("generic manager adopted = %d, want 1 (only its own AgentKind entry — a value of 2 means the AgentKind filter let it dial/adopt the claude entry too)", genericAdopted)
	}
	// Wrong-kind entries must be left COMPLETELY untouched — not even
	// counted as "cleaned" (they are not debris).
	if claudeCleaned != 0 {
		t.Fatalf("claude manager cleaned = %d, want 0 (the generic entry is live and not this manager's to manage, must not be touched at all)", claudeCleaned)
	}
	if genericCleaned != 0 {
		t.Fatalf("generic manager cleaned = %d, want 0 (the claude entry is live and not this manager's to manage, must not be touched at all)", genericCleaned)
	}

	// claudeMgr must have adopted the claude entry specifically, and must
	// NOT have an entry for the generic identity.
	claudeDaemon := claudeMgr.Get(claudeRepo, claudeBranch, claudeTab)
	if claudeDaemon == nil {
		t.Fatal("claude manager did not adopt its own ptyhost")
	}
	if got := claudeMgr.Get(genericRepo, genericBranch, genericTab); got != nil {
		t.Fatal("claude manager wrongly adopted the generic-kind ptyhost (cross-kind adoption regression)")
	}

	// genericMgr must have adopted the generic entry specifically, and must
	// NOT have an entry for the claude identity.
	genericDaemon := genericMgr.Get(genericRepo, genericBranch, genericTab)
	if genericDaemon == nil {
		t.Fatal("generic manager did not adopt its own ptyhost")
	}
	if got := genericMgr.Get(claudeRepo, claudeBranch, claudeTab); got != nil {
		t.Fatal("generic manager wrongly adopted the claude-kind ptyhost (cross-kind adoption regression)")
	}

	// The adopted Daemons must be ATTACHED to the pre-existing ptyhosts (same
	// pid), not a fresh respawn — proof the OTHER kind's discovery pass
	// never disturbed this one via an eviction dial (see this file's
	// Sfeed64-3-lesson doc comment above).
	waitForOwnershipRunning(t, claudeDaemon, 5*time.Second)
	if got := claudeDaemon.CurrentStats().PID; got != claudePid {
		t.Fatalf("adopted claude daemon pid = %d, want %d (the pre-existing ptyhost's pid) — a mismatch means it was evicted/respawned", got, claudePid)
	}
	waitForOwnershipRunning(t, genericDaemon, 5*time.Second)
	if got := genericDaemon.CurrentStats().PID; got != genericPid {
		t.Fatalf("adopted generic daemon pid = %d, want %d (the pre-existing ptyhost's pid) — a mismatch means it was evicted/respawned", got, genericPid)
	}
}

// TestAgentKindOwnership_EmptyAgentKindTreatedAsClaude proves the
// back-compat rule discover.go documents: a status file written BEFORE
// AgentKind existed (empty field) is adopted by the CLAUDE manager, not the
// generic one — the in-place-upgrade case where pre-existing ptyhosts must
// still be re-adopted by the (also upgraded) claude Manager without every
// surviving ptyhost being respawned.
func TestAgentKindOwnership_EmptyAgentKindTreatedAsClaude(t *testing.T) {
	fakeClaudeBin := fakeBin(t)
	runDir := t.TempDir()
	ctx := context.Background()

	const repoID, branchID, tabID = "legacy-repo", "legacy-branch", "claude:claude"

	// No AgentKind set at all — simulates a ptyhost spawned by a
	// pre-S0e8afb-3 binary.
	sock, _, done, _ := startRawPtyHost(t, runDir, fakeClaudeBin, repoID, branchID, tabID)
	t.Cleanup(func() {
		shutdownRawPtyHost(sock)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("raw legacy ptyhost did not fully tear down within 5s during cleanup")
		}
	})

	claudeMgr := NewManager(ManagerConfig{
		Adapter:        agent.NewClaudeAdapter(fakeClaudeBin, nil),
		RingSize:       1 << 16,
		RunDirOverride: runDir,
	})
	t.Cleanup(func() { _ = claudeMgr.ShutdownAll(ctx) })
	genericMgr := NewManager(ManagerConfig{
		Adapter:        agent.NewGenericAdapter("generic", agent.GenericConfig{Command: fakeClaudeBin}),
		RingSize:       1 << 16,
		RunDirOverride: runDir,
	})
	t.Cleanup(func() { _ = genericMgr.ShutdownAll(ctx) })

	claudeAdopted, _, err := DiscoverAndRestore(ctx, claudeMgr, nil, nil)
	if err != nil {
		t.Fatalf("claude DiscoverAndRestore: %v", err)
	}
	if claudeAdopted != 1 {
		t.Fatalf("claude manager adopted = %d, want 1 (empty AgentKind must back-compat to claude)", claudeAdopted)
	}
	genericAdopted, _, err := DiscoverAndRestore(ctx, genericMgr, nil, nil)
	if err != nil {
		t.Fatalf("generic DiscoverAndRestore: %v", err)
	}
	if genericAdopted != 0 {
		t.Fatalf("generic manager adopted = %d, want 0 (an empty-AgentKind legacy entry must NOT be claimed by a non-claude kind)", genericAdopted)
	}
}

// waitForOwnershipRunning polls a Daemon until CurrentStats reports Alive
// (State == StateRunning) or the timeout elapses.
func waitForOwnershipRunning(t *testing.T, d *Daemon, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last Stats
	for time.Now().Before(deadline) {
		last = d.CurrentStats()
		if last.Alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon did not reach StateRunning within %s (last stats: %+v)", timeout, last)
}
