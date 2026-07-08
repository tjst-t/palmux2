package store

// Regression for the runtime-switch tab-visibility bug: a conditional tab (e.g.
// Browser/Ports on incus-container) must appear/disappear via RecomputeBranchTabs,
// which diffs prev→next and emits tab.added / tab.removed. RestartBranchRuntime no
// longer pre-recomputes the TabSet (which would have swallowed that diff), so the
// sole recompute in the switch handler produces the events the FE needs.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// condProvider is a non-tmux conditional singleton whose tab is visible iff
// *visible is true (mirrors the Browser/Ports runtime gate).
type condProvider struct{ visible *bool }

func (condProvider) Type() string          { return "cond" }
func (condProvider) DisplayName() string   { return "Cond" }
func (condProvider) Protected() bool       { return false }
func (condProvider) Multiple() bool        { return false }
func (condProvider) NeedsTmuxWindow() bool { return false }
func (condProvider) Conditional() bool     { return true }
func (condProvider) Limits(_ tab.SettingsView) tab.InstanceLimits {
	return tab.InstanceLimits{Min: 0, Max: 1}
}
func (p condProvider) OnBranchOpen(_ context.Context, _ tab.OpenParams) (tab.ProviderResult, error) {
	if p.visible == nil || !*p.visible {
		return tab.ProviderResult{}, nil
	}
	return tab.ProviderResult{Tabs: []domain.Tab{{ID: "cond", Type: "cond", Name: "Cond"}}}, nil
}
func (condProvider) OnBranchClose(_ context.Context, _ tab.CloseParams) error { return nil }
func (condProvider) RegisterRoutes(_ *http.ServeMux, _ string)                {}

func TestRecomputeBranchTabs_ConditionalTabAddRemoveEmitsEvents(t *testing.T) {
	dir := t.TempDir()
	repoStore, err := config.NewRepoStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	visible := false
	registry := tab.NewRegistry()
	registry.Register(condProvider{visible: &visible})
	s, err := New(Deps{Tmux: tmux.NewMockClient(), RepoStore: repoStore, Settings: settings, Registry: registry, GHQRoot: dir})
	if err != nil {
		t.Fatal(err)
	}
	repoID := "r--0001"
	branchID := injectBranch(t, s, repoID, dir, "main", true)

	// Seed the TabSet with the tab hidden.
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatalf("seed recompute: %v", err)
	}

	ch, unsub := s.hub.Subscribe()
	defer unsub()
	drain := func() []Event {
		var evs []Event
		deadline := time.After(500 * time.Millisecond)
		for {
			select {
			case e := <-ch:
				evs = append(evs, e)
			case <-deadline:
				return evs
			}
		}
	}
	hasTab := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, tb := range s.repos[repoID].OpenBranches[0].TabSet.Tabs {
			if tb.Type == "cond" {
				return true
			}
		}
		return false
	}
	has := func(evs []Event, typ EventType) bool {
		for _, e := range evs {
			if e.Type == typ {
				return true
			}
		}
		return false
	}

	// Flip visible → recompute must ADD the tab + emit tab.added.
	visible = true
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	if evs := drain(); !has(evs, EventTabAdded) {
		t.Errorf("expected tab.added when conditional tab becomes visible; got %+v", evs)
	}
	if !hasTab() {
		t.Error("conditional tab not present after becoming visible")
	}

	// Flip hidden → recompute must REMOVE the tab + emit tab.removed.
	visible = false
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	if evs := drain(); !has(evs, EventTabRemoved) {
		t.Errorf("expected tab.removed when conditional tab hides; got %+v", evs)
	}
	if hasTab() {
		t.Error("conditional tab still present after hiding")
	}
}
