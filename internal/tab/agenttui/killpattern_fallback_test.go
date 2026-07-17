package agenttui

import (
	"context"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/agent"
	"github.com/tjst-t/palmux2/internal/runtime"
)

// This file is the regression test for a post-S0e8afb-3-review finding:
// killPatternOrFallback (ptyhost_discovery.go) originally fell back to the
// hardcoded containerClaudeBin constant whenever a StatusFile's KillPattern
// was empty, REGARDLESS of which kind the orphan belonged to. That is
// correct back-compat for a genuinely legacy claude-only StatusFile
// (written before AgentKind/KillPattern existed), but [agent.GenericAdapter.
// SpawnSpec] (internal/agent/generic.go) never populates SpawnSpec.
// KillPattern at all — so once a live generic-kind ptyhost genuinely exists
// (as soon as a future Story wires one up), an orphan of that kind would
// ALWAYS have an empty on-disk KillPattern. Falling back to
// containerClaudeBin unconditionally would have GCOrphans pkill-TERM the
// claude binary path inside that SAME (repoId, branchId) workspace
// container — which could be a completely unrelated, LIVE, in-use claude
// session running alongside the orphaned generic process, not the process
// GC was meant to reap. The fix gates the claude-specific fallback on KIND,
// not just on pattern emptiness — see killPatternOrFallback's own doc
// comment in ptyhost_discovery.go for the full reasoning.

// TestKillPatternOrFallback is a focused table-driven unit test of the pure
// decision function itself.
func TestKillPatternOrFallback(t *testing.T) {
	cases := []struct {
		name    string
		kind    agent.Kind
		pattern string
		want    string
	}{
		{"claude with explicit pattern uses it", agent.KindClaude, "/custom/claude", "/custom/claude"},
		{"claude with empty pattern falls back to containerClaudeBin", agent.KindClaude, "", containerClaudeBin},
		{"empty (legacy pre-AgentKind) kind with empty pattern back-compats to containerClaudeBin", "", "", containerClaudeBin},
		{"generic with explicit pattern uses it", "generic", "/custom/generic", "/custom/generic"},
		{"generic with empty pattern returns empty — MUST NOT guess containerClaudeBin", "generic", "", ""},
		{"arbitrary future kind with empty pattern also returns empty", "codex", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := killPatternOrFallback(tc.kind, tc.pattern); got != tc.want {
				t.Errorf("killPatternOrFallback(%q, %q) = %q, want %q", tc.kind, tc.pattern, got, tc.want)
			}
		})
	}
}

// TestGCOrphans_GenericKindEmptyKillPatternSkipsInContainerReap is the
// integration-level proof: a REAL orphaned generic-kind ptyhost with NO
// KillPattern (exactly what GenericAdapter.SpawnSpec produces today) must
// still be SHUTDOWN (the ptyhost itself is reaped — that part is
// unconditional and unaffected by this fix) but must NOT trigger ANY
// in-container KillContainerProcesses call — not with containerClaudeBin,
// not with anything else. Calling KillContainerProcesses with a guessed
// claude pattern against a generic-kind orphan's container would risk
// TERMing an unrelated, live claude session sharing that same workspace
// container.
func TestGCOrphans_GenericKindEmptyKillPatternSkipsInContainerReap(t *testing.T) {
	fakeClaudeBin := fakeBin(t)
	runDir := t.TempDir()
	ctx := context.Background()

	const repoID, branchID, tabID = "orphan-generic-repo", "orphan-generic-branch", "generic:generic"

	// Real raw ptyhost, AgentKind="generic", NO KillPattern set — mirrors
	// exactly what a real GenericAdapter-backed spawn produces on disk
	// today (GenericAdapter.SpawnSpec never populates KillPattern).
	sock, _, done, _ := startRawKindPtyHost(t, runDir, fakeClaudeBin, "generic", repoID, branchID, tabID)
	t.Cleanup(func() {
		shutdownRawPtyHost(sock)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("raw ptyhost did not fully tear down within 5s during cleanup")
		}
	})

	killer := &fakeContainerKiller{}
	genericMgr := NewManager(ManagerConfig{
		Adapter:         agent.NewGenericAdapter("generic", agent.GenericConfig{Command: fakeClaudeBin}),
		RingSize:        1 << 16,
		RunDirOverride:  runDir,
		RuntimeResolver: func(_, _ string) runtime.PTYCommander { return killer },
	})
	t.Cleanup(func() { _ = genericMgr.ShutdownAll(ctx) })

	// isLive reports false for everything below — this ptyhost is,
	// deliberately, an orphan (no matching tab).
	isLive := func(string, string, string) bool { return false }

	shutdown, cleaned, err := genericMgr.GCOrphans(ctx, isLive)
	if err != nil {
		t.Fatalf("GCOrphans: %v", err)
	}
	if shutdown != 1 {
		t.Fatalf("shutdown = %d, want 1 (the orphan ptyhost itself must still be SHUTDOWN — only the extra in-container reap is skipped)", shutdown)
	}
	if cleaned != 0 {
		t.Fatalf("cleaned = %d, want 0", cleaned)
	}
	// The critical assertion.
	if calls := killer.Calls(); len(calls) != 0 {
		t.Fatalf("KillContainerProcesses calls = %+v, want NONE — a generic-kind orphan with no declared KillPattern must skip the in-container reap, not guess claude's kill pattern (which could TERM an unrelated live claude session in the same container)", calls)
	}
}

// TestGCOrphans_ClaudeKindEmptyKillPatternStillReapsWithFallback is the
// positive control for the fix above: claude's own back-compat fallback
// (a legacy StatusFile written before KillPattern existed) must be
// UNCHANGED — this is the one case where guessing containerClaudeBin is
// correct (it's a stable, literal claude-specific constant, not a guess
// about which pattern applies to which kind).
func TestGCOrphans_ClaudeKindEmptyKillPatternStillReapsWithFallback(t *testing.T) {
	fakeClaudeBin := fakeBin(t)
	runDir := t.TempDir()
	ctx := context.Background()

	const repoID, branchID, tabID = "orphan-claude-repo", "orphan-claude-branch", "claude:claude"

	// startRawPtyHost (discover_test.go) never sets AgentKind/KillPattern —
	// simulates a pre-S0e8afb-3 legacy claude ptyhost exactly.
	sock, _, done, _ := startRawPtyHost(t, runDir, fakeClaudeBin, repoID, branchID, tabID)
	t.Cleanup(func() {
		shutdownRawPtyHost(sock)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("raw ptyhost did not fully tear down within 5s during cleanup")
		}
	})

	killer := &fakeContainerKiller{}
	claudeMgr := NewManager(ManagerConfig{
		Adapter:         agent.NewClaudeAdapter(fakeClaudeBin, nil),
		RingSize:        1 << 16,
		RunDirOverride:  runDir,
		RuntimeResolver: func(_, _ string) runtime.PTYCommander { return killer },
	})
	t.Cleanup(func() { _ = claudeMgr.ShutdownAll(ctx) })

	isLive := func(string, string, string) bool { return false }

	shutdown, _, err := claudeMgr.GCOrphans(ctx, isLive)
	if err != nil {
		t.Fatalf("GCOrphans: %v", err)
	}
	if shutdown != 1 {
		t.Fatalf("shutdown = %d, want 1", shutdown)
	}
	assertSingleTermCall(t, "post-S0e8afb-3-review-fix-claude-still-works", killer.Calls())
}
