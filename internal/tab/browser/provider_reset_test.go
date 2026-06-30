// Package browser — unit tests for the container-regenerate reset hook
// (S52fc2c-6). When a workspace's container is recreated in place, the store
// invokes tab.RuntimeRestartHook.OnBranchRuntimeRestarted on every provider;
// the Browser provider implements it to forget the stale per-workspace Manager
// state so the next attach reconnects to the NEW container.
//
// [AC-S52fc2c-6-1] [AC-S52fc2c-6-2]
package browser

import (
	"context"
	"testing"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/tab"
)

// newProviderForTest builds a Provider with an empty managers map. The store
// reference is left nil — OnBranchRuntimeRestarted / managerFor never touch it.
func newProviderForTest() *Provider {
	return &Provider{
		managers: map[string]*Manager{},
	}
}

// seedRunningManager registers a Manager for the given workspace and puts it in
// a "running" state with stale PIDs + bridge IP, mimicking a live browser stack
// in the OLD container before a regenerate.
func seedRunningManager(p *Provider, repoID, branchID string) *Manager {
	mgr := NewManager(func() runtime.Runtime { return nil }, "ws-"+branchID, nil, nil)
	mgr.mu.Lock()
	mgr.state = StateRunning
	mgr.xvfbPID = "100"
	mgr.dbusPID = "101"
	mgr.pid = "102"
	mgr.fcitxPID = "103"
	mgr.vncPID = "104"
	mgr.relayPID = "105"
	mgr.cdpAddr = "10.100.0.5"
	mgr.mu.Unlock()

	p.manMu.Lock()
	p.managers[managerKey(repoID, branchID)] = mgr
	p.manMu.Unlock()
	return mgr
}

func branchFixture(repoID, branchID string) *domain.Branch {
	return &domain.Branch{ID: branchID, RepoID: repoID}
}

// TestOnBranchRuntimeRestarted_ResetsManager verifies that regenerating the
// container resets the matching workspace's Manager: state → stopped and all
// stale PIDs / bridge IP cleared, so the next Start() reconnects to the new
// container. [AC-S52fc2c-6-1] [AC-S52fc2c-6-2]
func TestOnBranchRuntimeRestarted_ResetsManager(t *testing.T) {
	p := newProviderForTest()
	repoID, branchID := "tjst-t--demo--a1b2", "main--e5f6"
	mgr := seedRunningManager(p, repoID, branchID)

	if err := p.OnBranchRuntimeRestarted(context.Background(), tab.CloseParams{
		Branch: branchFixture(repoID, branchID),
	}); err != nil {
		t.Fatalf("OnBranchRuntimeRestarted returned error: %v", err)
	}

	// The Manager must REMAIN in the map (the workspace is still open — only its
	// container changed), unlike OnBranchClose which removes it. Checked BEFORE
	// taking mgr.mu so the test never imposes a mgr.mu→manMu lock order.
	if p.managerFor(repoID, branchID) == nil {
		t.Error("manager should remain registered after runtime restart")
	}

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if mgr.state != StateStopped {
		t.Errorf("state = %q, want %q", mgr.state, StateStopped)
	}
	for name, got := range map[string]string{
		"xvfbPID":  mgr.xvfbPID,
		"dbusPID":  mgr.dbusPID,
		"pid":      mgr.pid,
		"fcitxPID": mgr.fcitxPID,
		"vncPID":   mgr.vncPID,
		"relayPID": mgr.relayPID,
		"cdpAddr":  mgr.cdpAddr,
	} {
		if got != "" {
			t.Errorf("after reset %s = %q, want empty", name, got)
		}
	}
}

// TestOnBranchRuntimeRestarted_OnlyAffectsTargetBranch verifies the reset is
// scoped to the branch whose container was regenerated; a sibling workspace's
// Manager is untouched. [AC-S52fc2c-6-2]
func TestOnBranchRuntimeRestarted_OnlyAffectsTargetBranch(t *testing.T) {
	p := newProviderForTest()
	repoID := "tjst-t--demo--a1b2"
	target := "main--e5f6"
	other := "feat--3c4d"
	_ = seedRunningManager(p, repoID, target)
	otherMgr := seedRunningManager(p, repoID, other)

	if err := p.OnBranchRuntimeRestarted(context.Background(), tab.CloseParams{
		Branch: branchFixture(repoID, target),
	}); err != nil {
		t.Fatalf("OnBranchRuntimeRestarted: %v", err)
	}

	otherMgr.mu.Lock()
	defer otherMgr.mu.Unlock()
	if otherMgr.state != StateRunning || otherMgr.pid != "102" {
		t.Errorf("sibling manager should be untouched, got state=%q pid=%q", otherMgr.state, otherMgr.pid)
	}
}

// TestOnBranchRuntimeRestarted_NoManager verifies the hook is a safe no-op when
// no Manager exists for the branch — the host-runtime / browser-never-opened
// case. [AC-S52fc2c-6-1]
func TestOnBranchRuntimeRestarted_NoManager(t *testing.T) {
	p := newProviderForTest()
	if err := p.OnBranchRuntimeRestarted(context.Background(), tab.CloseParams{
		Branch: branchFixture("tjst-t--demo--a1b2", "no-mgr--0000"),
	}); err != nil {
		t.Fatalf("expected no-op, got error: %v", err)
	}
}

// TestOnBranchRuntimeRestarted_NilBranch verifies a nil Branch is tolerated.
func TestOnBranchRuntimeRestarted_NilBranch(t *testing.T) {
	p := newProviderForTest()
	if err := p.OnBranchRuntimeRestarted(context.Background(), tab.CloseParams{Branch: nil}); err != nil {
		t.Fatalf("expected no-op for nil branch, got error: %v", err)
	}
}

// TestManagerReset_Idempotent verifies Reset on an already-stopped Manager is a
// no-op that leaves it stopped (defensive: hook may fire when browser was never
// started). [AC-S52fc2c-6-1]
func TestManagerReset_Idempotent(t *testing.T) {
	mgr := NewManager(func() runtime.Runtime { return nil }, "ws-idem", nil, nil)
	mgr.Reset()
	mgr.Reset()
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if mgr.state != StateStopped {
		t.Errorf("state = %q, want %q", mgr.state, StateStopped)
	}
}
