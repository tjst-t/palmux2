package agenttab

import (
	"testing"

	"github.com/tjst-t/palmux2/internal/agent"
	"github.com/tjst-t/palmux2/internal/tab/agenttui"
)

// TestDeleteLastAgentTabStaysGone is the regression test for the "delete
// freely" change: with multi-agent support there is no minimum-count floor, so
// the last agent tab of a kind can be closed and must STAY closed (the user
// re-adds one from the `+` menu on demand). Previously tabsForBranch re-seeded
// the canonical whenever the persisted list was empty, so the last tab could
// never be deleted.
func TestDeleteLastAgentTabStaysGone(t *testing.T) {
	store, err := agenttui.NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	adapter := agent.NewGenericAdapter("dummy", agent.GenericConfig{Command: "bash"})
	mgr := agenttui.NewManager(agenttui.ManagerConfig{Adapter: adapter, Store: store})
	p := New("dummy", adapter, mgr, store)

	// Fresh branch: the canonical tab is seeded for display (not yet touched).
	if got := p.tabsForBranch("r1", "b1"); len(got) != 1 || got[0] != p.CanonicalTabID() {
		t.Fatalf("fresh branch tabs = %v, want [%s]", got, p.CanonicalTabID())
	}
	if store.HasBranchTabs("dummy", "r1", "b1") {
		t.Fatal("fresh branch should not be marked touched yet")
	}

	// User closes the last (canonical) tab → persisted empty, and it stays
	// empty (no re-seed).
	if err := store.SetBranchTabs("dummy", "r1", "b1", nil); err != nil {
		t.Fatalf("SetBranchTabs: %v", err)
	}
	if !store.HasBranchTabs("dummy", "r1", "b1") {
		t.Fatal("emptied branch must be marked touched (HasBranchTabs)")
	}
	if got := p.tabsForBranch("r1", "b1"); len(got) != 0 {
		t.Fatalf("emptied branch re-seeded tabs %v, want none", got)
	}

	// Re-adding recreates the canonical id (named "Dummy", not "Dummy 2").
	newID, err := p.AddTabForBranch("r1", "b1")
	if err != nil {
		t.Fatalf("AddTabForBranch: %v", err)
	}
	if newID != p.CanonicalTabID() {
		t.Fatalf("re-add produced %q, want canonical %q", newID, p.CanonicalTabID())
	}
	if got := p.tabsForBranch("r1", "b1"); len(got) != 1 || got[0] != p.CanonicalTabID() {
		t.Fatalf("after re-add tabs = %v, want [%s]", got, p.CanonicalTabID())
	}
}
