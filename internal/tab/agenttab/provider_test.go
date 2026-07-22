package agenttab

import (
	"context"
	"testing"

	"github.com/tjst-t/palmux2/internal/agent"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tab/agenttui"
)

func newTestProvider(t *testing.T) (*Provider, *agenttui.SessionStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := agenttui.NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	adapter := agent.NewGenericAdapter("dummy", agent.GenericConfig{Command: "bash"})
	mgr := agenttui.NewManager(agenttui.ManagerConfig{Adapter: adapter, Store: store})
	p := New("dummy", adapter, mgr, store)
	return p, store
}

func TestProvider_TypeAndShape(t *testing.T) {
	p, _ := newTestProvider(t)
	if p.Type() != "dummy" {
		t.Errorf("Type() = %q, want dummy", p.Type())
	}
	if p.DisplayName() != "dummy" {
		t.Errorf("DisplayName() = %q, want dummy", p.DisplayName())
	}
	if p.Protected() {
		t.Error("Protected() = true, want false (D5)")
	}
	if !p.Multiple() {
		t.Error("Multiple() = false, want true")
	}
	if p.NeedsTmuxWindow() {
		t.Error("NeedsTmuxWindow() = true, want false")
	}
	if p.Conditional() {
		t.Error("Conditional() = true, want false")
	}
}

func TestProvider_LimitsDefaultsAndSettingsView(t *testing.T) {
	p, _ := newTestProvider(t)
	// nil SettingsView → default.
	limits := p.Limits(nil)
	if limits.Min != 1 || limits.Max != defaultMaxTabsPerBranch {
		t.Errorf("Limits(nil) = %+v, want Min=1 Max=%d", limits, defaultMaxTabsPerBranch)
	}
	// SettingsView-driven override.
	limits2 := p.Limits(fakeSettingsView{max: 7})
	if limits2.Max != 7 {
		t.Errorf("Limits(view) Max = %d, want 7", limits2.Max)
	}
}

type fakeSettingsView struct{ max int }

func (f fakeSettingsView) MaxClaudeTabsPerBranch() int      { return 3 }
func (f fakeSettingsView) MaxBashTabsPerBranch() int        { return 5 }
func (f fakeSettingsView) MaxTabsPerBranch(kind string) int { return f.max }

func TestProvider_OnBranchOpenSeedsCanonicalTab(t *testing.T) {
	p, _ := newTestProvider(t)
	branch := &domain.Branch{ID: "b1", RepoID: "r1"}
	result, err := p.Tabs(context.Background(), tab.TabsParams{Branch: branch})
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("Tabs = %v, want exactly 1 (auto-seeded canonical)", result)
	}
	got := result[0]
	if got.ID != "dummy:dummy" {
		t.Errorf("Tabs[0].ID = %q, want dummy:dummy", got.ID)
	}
	if got.Type != "dummy" {
		t.Errorf("Tabs[0].Type = %q, want dummy", got.Type)
	}
	if got.Protected {
		t.Error("Tabs[0].Protected = true, want false")
	}
	if got.Name != "dummy" {
		t.Errorf("Tabs[0].Name = %q, want dummy (adapter DisplayName fallback)", got.Name)
	}
}

func TestProvider_AddTabForBranchAndOnBranchOpenReflectsIt(t *testing.T) {
	p, _ := newTestProvider(t)
	id2, err := p.AddTabForBranch("r1", "b1")
	if err != nil {
		t.Fatalf("AddTabForBranch: %v", err)
	}
	if id2 != "dummy:dummy-2" {
		t.Errorf("second tab id = %q, want dummy:dummy-2", id2)
	}

	branch := &domain.Branch{ID: "b1", RepoID: "r1"}
	result, err := p.Tabs(context.Background(), tab.TabsParams{Branch: branch})
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Tabs = %v, want 2 (canonical + added)", result)
	}
	if result[1].Name != "dummy 2" {
		t.Errorf("Tabs[1].Name = %q, want %q", result[1].Name, "dummy 2")
	}
}

func TestProvider_RemoveTabForBranch(t *testing.T) {
	p, store := newTestProvider(t)
	id2, err := p.AddTabForBranch("r1", "b1")
	if err != nil {
		t.Fatalf("AddTabForBranch: %v", err)
	}
	if err := p.RemoveTabForBranch(context.Background(), "r1", "b1", id2); err != nil {
		t.Fatalf("RemoveTabForBranch: %v", err)
	}
	tabs := store.BranchTabs("dummy", "r1", "b1")
	for _, tid := range tabs {
		if tid == id2 {
			t.Errorf("removed tab %q still present in %v", id2, tabs)
		}
	}
}

// TestProvider_KindNamespacedPersistenceSurvivesRestart verifies D7: a
// "dummy:dummy-2" tab persists across a fresh Provider/Manager built from
// the same on-disk SessionStore (simulated restart), and does not collide
// with a different kind's tab list on the same branch.
func TestProvider_KindNamespacedPersistenceSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store1, err := agenttui.NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	dummyAdapter := agent.NewGenericAdapter("dummy", agent.GenericConfig{Command: "bash"})
	dummyMgr := agenttui.NewManager(agenttui.ManagerConfig{Adapter: dummyAdapter, Store: store1})
	dummyProvider := New("dummy", dummyAdapter, dummyMgr, store1)

	otherAdapter := agent.NewGenericAdapter("other", agent.GenericConfig{Command: "cat"})
	otherMgr := agenttui.NewManager(agenttui.ManagerConfig{Adapter: otherAdapter, Store: store1})
	otherProvider := New("other", otherAdapter, otherMgr, store1)

	if _, err := dummyProvider.AddTabForBranch("r1", "b1"); err != nil {
		t.Fatalf("AddTabForBranch(dummy): %v", err)
	}
	if _, err := otherProvider.AddTabForBranch("r1", "b1"); err != nil {
		t.Fatalf("AddTabForBranch(other): %v", err)
	}

	// Simulate restart: fresh SessionStore instance from the same dir, fresh
	// Provider.
	store2, err := agenttui.NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore (restart): %v", err)
	}
	dummyProvider2 := New("dummy", dummyAdapter, dummyMgr, store2)
	branch := &domain.Branch{ID: "b1", RepoID: "r1"}
	result, err := dummyProvider2.Tabs(context.Background(), tab.TabsParams{Branch: branch})
	if err != nil {
		t.Fatalf("Tabs (restart): %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("after restart: dummy Tabs = %v, want 2 (own kind only, not polluted by 'other')", result)
	}
	for _, tb := range result {
		if tb.Type != "dummy" {
			t.Errorf("restart leaked a tab of the wrong kind: %+v", tb)
		}
	}
}

func TestProvider_OnBranchCloseClearsTabsAndCloseDaemons(t *testing.T) {
	p, store := newTestProvider(t)
	if _, err := p.AddTabForBranch("r1", "b1"); err != nil {
		t.Fatalf("AddTabForBranch: %v", err)
	}
	branch := &domain.Branch{ID: "b1", RepoID: "r1"}
	if err := p.OnBranchClose(context.Background(), tab.CloseParams{Branch: branch}); err != nil {
		t.Fatalf("OnBranchClose: %v", err)
	}
	if got := store.BranchTabs("dummy", "r1", "b1"); len(got) != 0 {
		t.Errorf("BranchTabs after close = %v, want empty", got)
	}
}

func TestProvider_DisplayNameForTab(t *testing.T) {
	p, _ := newTestProvider(t)
	cases := map[string]string{
		"dummy:dummy":   "dummy",
		"":              "dummy",
		"dummy:dummy-2": "dummy 2",
		"dummy:dummy-9": "dummy 9",
	}
	for in, want := range cases {
		if got := p.DisplayNameForTab(in); got != want {
			t.Errorf("DisplayNameForTab(%q) = %q, want %q", in, got, want)
		}
	}
}
