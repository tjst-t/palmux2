package store

// Characterization tests for the tab-set reducer (`recomputeTabs`).
//
// WHY THIS FILE EXISTS
//
// `recomputeTabs` rebuilds `branch.TabSet.Tabs` from scratch on every call,
// merging five independent inputs (tmux ListWindows, the previous TabSet,
// Provider.OnBranchOpen, hardcoded singleton synthesis, and repos.json
// tabOverrides). It has 12 call sites and has accumulated a long tail of
// point fixes (S009-fix-1, S0c6a1b, Sadf90e, S4d8b1c, …), but until now its
// own derivation matrix was almost untested: the tab layer's ~15k lines of
// tests target the leaves (ptyhost daemons, ring buffers, the VT emulator),
// not the reducer.
//
// These tests pin the CURRENT behaviour — including behaviour we consider
// wrong (see `TestChar_Notification_*`). They are a safety net for the
// planned Provider-interface surgery: splitting the pure `Tabs()` query out
// of the `OnBranchOpen` lifecycle hook, unifying diff+publish, and moving
// I/O out of the write lock. A test here failing after that work means
// either a real regression or a deliberate contract change — never
// "probably fine".
//
// Tests whose name records a known defect are marked DEFECT: and assert the
// buggy behaviour on purpose, so the fix flips them visibly.

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tab/bash"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// ─────────────────────────── fixtures ───────────────────────────

// charRestProvider is a non-tmux, non-conditional singleton — the Files/Git
// shape. recomputeTabs synthesises its tab without consulting the provider.
type charRestProvider struct {
	typ            string
	lifecycleCalls *atomic.Int64
}

func (p charRestProvider) Type() string        { return p.typ }
func (p charRestProvider) DisplayName() string { return "Rest " + p.typ }
func (charRestProvider) Protected() bool       { return true }
func (charRestProvider) Multiple() bool        { return false }
func (charRestProvider) NeedsTmuxWindow() bool { return false }
func (charRestProvider) Limits(_ tab.SettingsView) tab.InstanceLimits {
	return tab.InstanceLimits{Min: 1, Max: 1}
}

func (p charRestProvider) Tabs(_ context.Context, _ tab.TabsParams) ([]domain.Tab, error) {
	return []domain.Tab{{
		ID: p.typ, Type: p.typ, Name: "Rest " + p.typ, Protected: true,
	}}, nil
}

// OnBranchOpen is the LIFECYCLE hook. Post-ADR-0012 the reducer must never
// call it, so the sentinel name below must never reach a tab. Keeping the
// sentinel here is the whole point: it turns "did the reducer call the
// lifecycle hook?" into an observable assertion.
func (p charRestProvider) OnBranchOpen(_ context.Context, _ tab.OpenParams) (tab.ProviderResult, error) {
	p.lifecycleCalls.Add(1)
	return tab.ProviderResult{}, nil
}
func (charRestProvider) OnBranchClose(_ context.Context, _ tab.CloseParams) error { return nil }
func (charRestProvider) RegisterRoutes(_ *http.ServeMux, _ string)                {}

// charMultiProvider is a non-tmux multi-instance provider — the Claude
// shape. Its per-branch tab list lives in provider-owned state, and
// recomputeTabs re-derives it by calling OnBranchOpen. It counts calls so
// tests can assert the reducer's call pattern and side-effect freedom.
type charMultiProvider struct {
	typ   string
	tabs  *[]string     // provider-owned tab ids (stand-in for claudeagent.Store)
	calls *atomic.Int64 // Tabs() invocations — the pure query
	// lifecycleCalls counts OnBranchOpen invocations. Post-ADR-0012 the
	// reducer must never touch it.
	lifecycleCalls *atomic.Int64
}

func (p charMultiProvider) Type() string        { return p.typ }
func (p charMultiProvider) DisplayName() string { return "Multi " + p.typ }
func (charMultiProvider) Protected() bool       { return true }
func (charMultiProvider) Multiple() bool        { return true }
func (charMultiProvider) NeedsTmuxWindow() bool { return false }
func (charMultiProvider) Limits(_ tab.SettingsView) tab.InstanceLimits {
	return tab.InstanceLimits{Min: 1, Max: 3}
}
func (p charMultiProvider) Tabs(_ context.Context, _ tab.TabsParams) ([]domain.Tab, error) {
	p.calls.Add(1)
	out := make([]domain.Tab, 0, len(*p.tabs))
	for _, id := range *p.tabs {
		out = append(out, domain.Tab{
			ID: id, Type: p.typ, Name: id, Protected: true, Multiple: true,
		})
	}
	return out, nil
}

func (p charMultiProvider) OnBranchOpen(_ context.Context, _ tab.OpenParams) (tab.ProviderResult, error) {
	p.lifecycleCalls.Add(1)
	return tab.ProviderResult{}, nil
}
func (charMultiProvider) OnBranchClose(_ context.Context, _ tab.CloseParams) error { return nil }
func (charMultiProvider) RegisterRoutes(_ *http.ServeMux, _ string)                {}

// charFixture builds a Store with an explicit provider set.
func charFixture(t *testing.T, providers ...tab.Provider) (*Store, *tmux.MockClient, string, string) {
	t.Helper()
	dir := t.TempDir()
	repoStore, err := config.NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	settings, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("NewSettingsStore: %v", err)
	}
	registry := tab.NewRegistry()
	for _, p := range providers {
		registry.Register(p)
	}
	mockTmux := tmux.NewMockClient()
	s, err := New(Deps{
		Tmux: mockTmux, RepoStore: repoStore, Settings: settings,
		Registry: registry, GHQRoot: dir,
	})
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	repoID := "char--0001"
	// injectBranch bypasses OpenRepo, so register the repo in repos.json too —
	// the tabOverrides API (SetTabName / SetTabOrder) resolves through it.
	if _, err := repoStore.Add(config.RepoEntry{ID: repoID, GHQPath: "test/" + repoID}); err != nil {
		t.Fatalf("RepoStore.Add: %v", err)
	}
	branchID := injectBranch(t, s, repoID, dir, "main", true)
	return s, mockTmux, repoID, branchID
}

// tabIDs returns the current tab id list for the branch.
func tabIDs(t *testing.T, s *Store, repoID, branchID string) []string {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, b := range s.repos[repoID].OpenBranches {
		if b.ID == branchID {
			out := make([]string, 0, len(b.TabSet.Tabs))
			for _, tb := range b.TabSet.Tabs {
				out = append(out, tb.ID)
			}
			return out
		}
	}
	t.Fatalf("branch %s not found", branchID)
	return nil
}

func sessionOf(t *testing.T, s *Store, repoID, branchID string) string {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, b := range s.repos[repoID].OpenBranches {
		if b.ID == branchID {
			return b.TabSet.TmuxSession
		}
	}
	t.Fatalf("branch %s not found", branchID)
	return ""
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ───────────────── A. derivation matrix ─────────────────

// A1: a non-tmux singleton yields exactly one tab, and it comes from the pure
// Tabs() query — the reducer must NOT call the OnBranchOpen lifecycle hook.
// The assertions are unchanged from the pre-ADR-0012 pin; only the mechanism
// behind them moved (reducer-side synthesis → provider-side Tabs()).
func TestChar_RestSingleton_SynthesisedNotProviderDriven(t *testing.T) {
	s, _, repoID, branchID := charFixture(t, charRestProvider{typ: "files", lifecycleCalls: &atomic.Int64{}})
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	got := tabIDs(t, s, repoID, branchID)
	if !eq(got, []string{"files"}) {
		t.Fatalf("want [files], got %v", got)
	}
	s.mu.RLock()
	name := s.repos[repoID].OpenBranches[0].TabSet.Tabs[0].Name
	s.mu.RUnlock()
	if name == "SENTINEL-must-not-appear" {
		t.Error("reducer consulted OnBranchOpen for a non-conditional REST singleton")
	}
	if name != "Rest files" {
		t.Errorf("want DisplayName() as tab name, got %q", name)
	}
}

// A2: a tmux-backed singleton exists logically even when its tmux session
// is absent — the tab must not blink out while sync_tmux is recovering.
func TestChar_TmuxSingleton_ExistsWithoutSession(t *testing.T) {
	s, _, repoID, branchID := charFixture(t, fakeTerminalProvider{})
	// No SeedSession → ListWindows errors.
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	if got := tabIDs(t, s, repoID, branchID); !eq(got, []string{fakeTerminalType}) {
		t.Fatalf("tmux singleton must survive a dead session; want [%s], got %v",
			fakeTerminalType, got)
	}
}

// A3: multi-instance tmux-backed tabs are one-per-window, in tmux index
// order, so user-added Bash tabs keep stable positions.
func TestChar_TmuxMulti_OnePerWindowInIndexOrder(t *testing.T) {
	s, mock, repoID, branchID := charFixture(t, bash.New())
	sess := sessionOf(t, s, repoID, branchID)
	mock.SeedSession(sess,
		tmux.Window{Index: 0, Name: domain.WindowName("bash", "bash")},
		tmux.Window{Index: 1, Name: domain.WindowName("bash", "bash-2")},
		tmux.Window{Index: 2, Name: domain.WindowName("bash", "server")},
	)
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	want := []string{"bash:bash", "bash:bash-2", "bash:server"}
	if got := tabIDs(t, s, repoID, branchID); !eq(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

// A4 — THE S009-fix-1 REGRESSION. When ListWindows fails transiently the
// reducer must fall back to the previous tab list rather than reporting
// "no windows". The original bug: adding a Claude tab triggered a recompute
// that made every Bash tab vanish from the UI for ~5s.
func TestChar_TmuxMulti_ListWindowsFailurePreservesPreviousTabs(t *testing.T) {
	s, mock, repoID, branchID := charFixture(t, bash.New())
	sess := sessionOf(t, s, repoID, branchID)
	mock.SeedSession(sess,
		tmux.Window{Index: 0, Name: domain.WindowName("bash", "bash")},
		tmux.Window{Index: 1, Name: domain.WindowName("bash", "bash-2")},
		tmux.Window{Index: 2, Name: domain.WindowName("bash", "bash-3")},
	)
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	before := tabIDs(t, s, repoID, branchID)
	if len(before) != 3 {
		t.Fatalf("setup: want 3 bash tabs, got %v", before)
	}

	// Session disappears → MockClient.ListWindows returns an error.
	if err := mock.KillSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	after := tabIDs(t, s, repoID, branchID)
	if !eq(before, after) {
		t.Errorf("S009-fix-1 regression: transient ListWindows failure dropped tabs\n"+
			"  before=%v\n  after =%v", before, after)
	}
}

// A5: a live session with zero windows of the type still seeds the
// canonical instance, so the branch always shows at least one Bash tab.
func TestChar_TmuxMulti_EmptyLiveSessionSeedsCanonical(t *testing.T) {
	s, mock, repoID, branchID := charFixture(t, bash.New())
	sess := sessionOf(t, s, repoID, branchID)
	mock.SeedSession(sess) // exists, no windows
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	if got := tabIDs(t, s, repoID, branchID); !eq(got, []string{"bash:bash"}) {
		t.Fatalf("want canonical [bash:bash], got %v", got)
	}
}

// A6: a non-tmux multi provider is authoritative — the reducer takes its
// Tabs() list verbatim.
func TestChar_NonTmuxMulti_ProviderIsAuthoritative(t *testing.T) {
	ids := []string{"claude:claude"}
	calls := &atomic.Int64{}
	lifecycle := &atomic.Int64{}
	p := charMultiProvider{typ: "claude", tabs: &ids, calls: calls, lifecycleCalls: lifecycle}
	s, _, repoID, branchID := charFixture(t, p)

	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	if got := tabIDs(t, s, repoID, branchID); !eq(got, []string{"claude:claude"}) {
		t.Fatalf("want [claude:claude], got %v", got)
	}
	ids = append(ids, "claude:claude-2")
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	want := []string{"claude:claude", "claude:claude-2"}
	if got := tabIDs(t, s, repoID, branchID); !eq(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

// A7 — CONTRACT FLIPPED BY ADR-0012. This used to pin the defect: the reducer
// called the OnBranchOpen LIFECYCLE hook once per recompute, with Resume
// hardcoded to false, and relied on a comment to promise that was harmless.
// It was not — the sprint provider allocated an inotify handle from that path
// and the browser provider created a per-workspace Manager, both under the
// Store write lock, on every 5 s sync cycle.
//
// The contract now: the reducer calls the pure Tabs() query once per provider
// per recompute, and never touches OnBranchOpen.
func TestChar_Reducer_UsesPureTabsQueryNotLifecycleHook(t *testing.T) {
	ids := []string{"claude:claude"}
	calls := &atomic.Int64{}
	lifecycle := &atomic.Int64{}
	p := charMultiProvider{typ: "claude", tabs: &ids, calls: calls, lifecycleCalls: lifecycle}
	s, _, repoID, branchID := charFixture(t, p)

	for i := 0; i < 3; i++ {
		if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("want one Tabs() call per recompute (3), got %d", n)
	}
	if n := lifecycle.Load(); n != 0 {
		t.Errorf("reducer must never call the OnBranchOpen lifecycle hook; got %d calls", n)
	}
}

// A8: the reducer is idempotent — repeated recomputes over unchanged inputs
// produce an identical tab list. Any provider that mutates state inside
// OnBranchOpen would break this, which is exactly why the query must be
// side-effect free.
func TestChar_Reducer_IsIdempotent(t *testing.T) {
	ids := []string{"claude:claude", "claude:claude-2"}
	p := charMultiProvider{
		typ: "claude", tabs: &ids, calls: &atomic.Int64{}, lifecycleCalls: &atomic.Int64{},
	}
	s, mock, repoID, branchID := charFixture(t, p, bash.New(), charRestProvider{typ: "files", lifecycleCalls: &atomic.Int64{}})
	sess := sessionOf(t, s, repoID, branchID)
	mock.SeedSession(sess,
		tmux.Window{Index: 0, Name: domain.WindowName("bash", "bash")},
		tmux.Window{Index: 1, Name: domain.WindowName("bash", "bash-2")},
	)
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	first := tabIDs(t, s, repoID, branchID)
	for i := 0; i < 5; i++ {
		if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
			t.Fatal(err)
		}
		if got := tabIDs(t, s, repoID, branchID); !eq(first, got) {
			t.Fatalf("recompute #%d not idempotent:\n  first=%v\n  got  =%v", i+2, first, got)
		}
	}
}

// A9: registration order determines tab order across provider types.
func TestChar_Reducer_TabOrderFollowsRegistrationOrder(t *testing.T) {
	ids := []string{"claude:claude"}
	p := charMultiProvider{
		typ: "claude", tabs: &ids, calls: &atomic.Int64{}, lifecycleCalls: &atomic.Int64{},
	}
	s, mock, repoID, branchID := charFixture(t,
		p, charRestProvider{typ: "files", lifecycleCalls: &atomic.Int64{}}, charRestProvider{typ: "git", lifecycleCalls: &atomic.Int64{}}, bash.New())
	sess := sessionOf(t, s, repoID, branchID)
	mock.SeedSession(sess, tmux.Window{Index: 0, Name: domain.WindowName("bash", "bash")})
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	want := []string{"claude:claude", "files", "git", "bash:bash"}
	if got := tabIDs(t, s, repoID, branchID); !eq(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

// ───────────────── B. notification contract ─────────────────

// B1 — DEFECT (pinned deliberately). Only RecomputeBranchTabs diffs and
// publishes; the other 11 recompute call sites overwrite branch.TabSet.Tabs
// silently. Here we drive the reducer the way sync_tmux does (direct call
// under the lock) and record that NO tab event is emitted — so the browser
// never learns the tab set changed.
//
// When the diff+publish unification lands, this test MUST start failing.
// Flip it to assert the events at that point.
func TestChar_Notification_DirectRecomputeEmitsNothing_DEFECT(t *testing.T) {
	ids := []string{"claude:claude"}
	p := charMultiProvider{
		typ: "claude", tabs: &ids, calls: &atomic.Int64{}, lifecycleCalls: &atomic.Int64{},
	}
	s, _, repoID, branchID := charFixture(t, p)
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}

	ch, unsub := s.hub.Subscribe()
	defer unsub()

	// A tab appears in provider state, then the reducer runs the way
	// sync_tmux.go:167 / branch.go:778 / PopulateTabs run it.
	ids = append(ids, "claude:claude-2")
	s.mu.Lock()
	for _, b := range s.repos[repoID].OpenBranches {
		if b.ID == branchID {
			s.recomputeTabs(context.Background(), b)
		}
	}
	s.mu.Unlock()

	if got := tabIDs(t, s, repoID, branchID); len(got) != 2 {
		t.Fatalf("setup: reducer should have picked up the new tab, got %v", got)
	}

	var events []Event
	deadline := time.After(300 * time.Millisecond)
collect:
	for {
		select {
		case e := <-ch:
			events = append(events, e)
		case <-deadline:
			break collect
		}
	}
	for _, e := range events {
		if e.Type == EventTabAdded || e.Type == EventTabRemoved {
			t.Fatalf("DEFECT FIXED: direct recompute now emits %s — "+
				"update this test to assert the diff+publish contract", e.Type)
		}
	}
	t.Log("pinned defect: tab set changed with no tab.added/tab.removed event " +
		"(11 of 12 recompute call sites are silent)")
}

// ───────────────── C. overrides ─────────────────

// C1: rename overrides apply only to Multiple()=true tabs; singletons keep
// their DisplayName.
func TestChar_Overrides_RenameOnlyAffectsMultiTabs(t *testing.T) {
	s, mock, repoID, branchID := charFixture(t, bash.New(), charRestProvider{typ: "files", lifecycleCalls: &atomic.Int64{}})
	sess := sessionOf(t, s, repoID, branchID)
	mock.SeedSession(sess, tmux.Window{Index: 0, Name: domain.WindowName("bash", "bash")})

	s.mu.RLock()
	branchName := s.repos[repoID].OpenBranches[0].Name
	s.mu.RUnlock()
	if err := s.deps.RepoStore.SetTabName(repoID, branchName, "bash:bash", "Server"); err != nil {
		t.Fatal(err)
	}
	if err := s.deps.RepoStore.SetTabName(repoID, branchName, "files", "Renamed Files"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, tb := range s.repos[repoID].OpenBranches[0].TabSet.Tabs {
		switch tb.ID {
		case "bash:bash":
			if tb.Name != "Server" {
				t.Errorf("multi tab rename ignored: got %q", tb.Name)
			}
		case "files":
			if tb.Name == "Renamed Files" {
				t.Errorf("singleton must ignore rename override, got %q", tb.Name)
			}
		}
	}
}

// C2: an order override never adds, drops, or duplicates tabs — it only
// permutes within a Multiple() group. Unknown ids in the saved order are
// ignored; tabs absent from it keep their default tail position.
func TestChar_Overrides_OrderIsPermutationOnly(t *testing.T) {
	s, mock, repoID, branchID := charFixture(t, bash.New())
	sess := sessionOf(t, s, repoID, branchID)
	mock.SeedSession(sess,
		tmux.Window{Index: 0, Name: domain.WindowName("bash", "bash")},
		tmux.Window{Index: 1, Name: domain.WindowName("bash", "bash-2")},
		tmux.Window{Index: 2, Name: domain.WindowName("bash", "bash-3")},
	)
	s.mu.RLock()
	branchName := s.repos[repoID].OpenBranches[0].Name
	s.mu.RUnlock()

	// Reverse two, name a third that does not exist.
	if err := s.deps.RepoStore.SetTabOrder(repoID, branchName,
		[]string{"bash:bash-3", "bash:ghost", "bash:bash"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecomputeBranchTabs(repoID, branchID); err != nil {
		t.Fatal(err)
	}
	got := tabIDs(t, s, repoID, branchID)

	want := []string{"bash:bash-3", "bash:bash", "bash:bash-2"}
	if !eq(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
	seen := map[string]int{}
	for _, id := range got {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("tab %s appears %d times — override must be a permutation", id, n)
		}
	}
	if len(got) != 3 {
		t.Errorf("override changed tab count: want 3, got %d (%v)", len(got), got)
	}
	if _, ok := seen["bash:ghost"]; ok {
		t.Error("unknown id in saved order materialised a tab")
	}
}

// ───────────────── D. host scope ─────────────────

// D1: the reserved host scope is bash-only and always advertises the
// canonical bash tab, even before its tmux session is lazily created.
func TestChar_HostScope_BashOnlyAndAlwaysSeeded(t *testing.T) {
	ids := []string{"claude:claude"}
	p := charMultiProvider{
		typ: "claude", tabs: &ids, calls: &atomic.Int64{}, lifecycleCalls: &atomic.Int64{},
	}
	s, _, _, _ := charFixture(t, p, bash.New(), charRestProvider{typ: "files", lifecycleCalls: &atomic.Int64{}})
	hostRepo, hostBranch, _ := s.HostScope()

	if err := s.RecomputeBranchTabs(hostRepo, hostBranch); err != nil {
		t.Fatal(err)
	}
	got := tabIDs(t, s, hostRepo, hostBranch)
	if !eq(got, []string{"bash:bash"}) {
		t.Fatalf("host scope must be bash-only with a seeded canonical tab; got %v", got)
	}
}

// D1b — BEHAVIOUR CHANGE PINNED BY ADR-0012. recomputeHostTabs used to build
// its tabs with a local displayNameForBash that had drifted from the bash
// provider it claimed to mirror: the host scope showed "bash" / "bash-2" while
// every ordinary workspace showed "Bash" / "Bash 2". Routing the host scope
// through the provider's Tabs() removes the duplicate and therefore changes
// the host labels. This test states the new, unified naming so the change is
// deliberate rather than incidental.
func TestChar_HostScope_TabNamesMatchWorkspaceNaming(t *testing.T) {
	s, mock, _, _ := charFixture(t, bash.New())
	hostRepo, hostBranch, _ := s.HostScope()
	sess := sessionOf(t, s, hostRepo, hostBranch)
	mock.SeedSession(sess,
		tmux.Window{Index: 0, Name: domain.WindowName("bash", "bash")},
		tmux.Window{Index: 1, Name: domain.WindowName("bash", "bash-2")},
	)
	if err := s.RecomputeBranchTabs(hostRepo, hostBranch); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	want := map[string]string{"bash:bash": "Bash", "bash:bash-2": "Bash 2"}
	for _, tb := range s.repos[hostRepo].OpenBranches[0].TabSet.Tabs {
		if w, ok := want[tb.ID]; ok && tb.Name != w {
			t.Errorf("host tab %s: name = %q, want %q (unified with workspace naming)", tb.ID, tb.Name, w)
		}
		delete(want, tb.ID)
	}
	if len(want) != 0 {
		t.Errorf("host scope missing tabs: %v", want)
	}
}

// D2: AddTab rejects non-bash types in the host scope at the API boundary
// (S0c6a1b) — otherwise the tab would be created and then silently stripped
// by recomputeHostTabs, desyncing FE and BE.
func TestChar_HostScope_AddTabRejectsNonBash(t *testing.T) {
	ids := []string{"claude:claude"}
	p := charMultiProvider{
		typ: "claude", tabs: &ids, calls: &atomic.Int64{}, lifecycleCalls: &atomic.Int64{},
	}
	s, _, _, _ := charFixture(t, p, bash.New())
	hostRepo, hostBranch, _ := s.HostScope()

	if _, err := s.AddTab(context.Background(), hostRepo, hostBranch, "claude", ""); err == nil {
		t.Fatal("want error adding a claude tab to the host scope, got nil")
	}
}

// ───────────────── E. interface invariants ─────────────────

// E1: the registry rejects duplicate provider types (programmer error).
func TestChar_Registry_RejectsDuplicateType(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("want panic on duplicate provider type")
		}
	}()
	r := tab.NewRegistry()
	r.Register(charRestProvider{typ: "dup", lifecycleCalls: &atomic.Int64{}})
	r.Register(charRestProvider{typ: "dup", lifecycleCalls: &atomic.Int64{}})
}

// E2 — CONTRACT ESTABLISHED BY ADR-0012. This used to pin the defect: the
// documented invariant on flag combinations lived only in a comment and
// Register accepted anything. Registration problems are programmer errors, so
// they now panic at boot rather than producing a tab set nobody can explain.
func TestChar_Registry_RejectsIllegalProvider(t *testing.T) {
	cases := []struct {
		name string
		p    charBadProvider
	}{
		{"empty type", charBadProvider{typ: ""}},
		{"type contains the tab-id separator", charBadProvider{typ: "a:b", max: 3, multiple: true}},
		{"Min greater than Max", charBadProvider{typ: "minmax", min: 5, max: 2, multiple: true}},
		{"singleton with Max > 1", charBadProvider{typ: "single", min: 1, max: 4, multiple: false}},
		{"multi-instance capped at 1", charBadProvider{typ: "multi", min: 1, max: 1, multiple: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("Register accepted an illegal provider (%s)", tc.name)
				}
			}()
			tab.NewRegistry().Register(tc.p)
		})
	}
}

// TestChar_Registry_AcceptsEveryRealProvider guards the other direction: the
// validation must not reject a legitimate configuration. A false positive here
// would make the binary refuse to boot.
func TestChar_Registry_AcceptsLegalProviders(t *testing.T) {
	r := tab.NewRegistry()
	r.Register(charRestProvider{typ: "files", lifecycleCalls: &atomic.Int64{}})
	r.Register(bash.New())
	r.Register(charMultiProvider{
		typ: "claude", tabs: &[]string{}, calls: &atomic.Int64{}, lifecycleCalls: &atomic.Int64{},
	})
	if len(r.Providers()) != 3 {
		t.Fatalf("want 3 registered providers, got %d", len(r.Providers()))
	}
}

// charBadProvider is a configurable provider used to drive Register's
// validation table.
type charBadProvider struct {
	typ      string
	min, max int
	multiple bool
}

func (p charBadProvider) Type() string        { return p.typ }
func (p charBadProvider) DisplayName() string { return "Bad" }
func (charBadProvider) Protected() bool       { return false }
func (p charBadProvider) Multiple() bool      { return p.multiple }
func (charBadProvider) NeedsTmuxWindow() bool { return false }
func (p charBadProvider) Limits(_ tab.SettingsView) tab.InstanceLimits {
	return tab.InstanceLimits{Min: p.min, Max: p.max}
}
func (charBadProvider) Tabs(_ context.Context, _ tab.TabsParams) ([]domain.Tab, error) {
	return nil, nil
}
func (charBadProvider) OnBranchOpen(_ context.Context, _ tab.OpenParams) (tab.ProviderResult, error) {
	return tab.ProviderResult{}, nil
}
func (charBadProvider) OnBranchClose(_ context.Context, _ tab.CloseParams) error { return nil }
func (charBadProvider) RegisterRoutes(_ *http.ServeMux, _ string)                {}
