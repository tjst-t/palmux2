package browser

import (
	"context"
	"testing"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
)

// TestTabs_DoesNotCreateManagers pins the ADR-0012 contract for the second
// provider that violated it.
//
// Before the split, the "should this tab be visible?" logic lived inside
// OnBranchOpen together with getOrCreateManager(), and the Store called
// OnBranchOpen as a query from every recompute — including the 5 s sync loop —
// under its write lock. So merely recomputing the tab set could allocate a
// per-workspace browser Manager.
//
// Visibility is now decided by the pure Tabs() query; Manager creation stays
// in the lifecycle hook.
func TestTabs_DoesNotCreateManagers(t *testing.T) {
	p := newProviderForTest()
	branch := &domain.Branch{ID: "b1", RepoID: "r1", Name: "main"}

	for i := 0; i < 100; i++ {
		if _, err := p.Tabs(context.Background(), tab.TabsParams{Branch: branch}); err != nil {
			t.Fatalf("Tabs #%d: %v", i, err)
		}
	}

	p.manMu.Lock()
	n := len(p.managers)
	p.manMu.Unlock()
	if n != 0 {
		t.Errorf("Tabs() created %d Manager(s) — that is a side effect and belongs in OnBranchOpen (ADR-0012)", n)
	}
}

// TestTabs_NilBranchIsSafe — the Store calls Tabs for every provider on every
// recompute, so nil-safety is part of the contract.
func TestTabs_NilBranchIsSafe(t *testing.T) {
	p := newProviderForTest()
	got, err := p.Tabs(context.Background(), tab.TabsParams{Branch: nil})
	if err != nil {
		t.Fatalf("Tabs(nil branch): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want 0 tabs for a nil branch, got %d", len(got))
	}
}

// TestTabs_HiddenWithoutRuntimeRegistry — with no resolvable runtime the tab
// must hide rather than panic. This is the path a host-runtime workspace takes.
func TestTabs_HiddenWithoutRuntimeRegistry(t *testing.T) {
	p := newProviderForTest()
	branch := &domain.Branch{ID: "b1", RepoID: "r1", Name: "main"}
	got, err := p.Tabs(context.Background(), tab.TabsParams{Branch: branch})
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want the Browser tab hidden when no incus runtime resolves, got %d tab(s)", len(got))
	}
}
