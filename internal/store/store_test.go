package store

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tab/bash"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// fakeTerminalProvider is a stand-in for the legacy tmux-backed Claude tab
// that store_test relies on for "create the per-branch tmux window" coverage.
// The production Claude tab is now WebSocket-only (no tmux window) so we
// can't drive these scenarios off it; a minimal terminal-backed provider
// keeps the existing tests meaningful.
type fakeTerminalProvider struct{}

const fakeTerminalType = "fake-term"
const fakeTerminalWindow = "palmux:fake-term:fake-term"

func (fakeTerminalProvider) Type() string          { return fakeTerminalType }
func (fakeTerminalProvider) DisplayName() string   { return "Fake Term" }
func (fakeTerminalProvider) Protected() bool       { return true }
func (fakeTerminalProvider) Multiple() bool        { return false }
func (fakeTerminalProvider) NeedsTmuxWindow() bool { return true }
func (fakeTerminalProvider) Limits(_ tab.SettingsView) tab.InstanceLimits {
	return tab.InstanceLimits{Min: 1, Max: 1}
}
func (fakeTerminalProvider) OnBranchOpen(_ context.Context, _ tab.OpenParams) (tab.ProviderResult, error) {
	return tab.ProviderResult{
		Tabs: []domain.Tab{{
			ID:         fakeTerminalType,
			Type:       fakeTerminalType,
			Name:       "Fake Term",
			Protected:  true,
			WindowName: fakeTerminalWindow,
		}},
		Windows: []tab.WindowSpec{{Name: fakeTerminalWindow, Command: "sh"}},
	}, nil
}
func (fakeTerminalProvider) OnBranchClose(_ context.Context, _ tab.CloseParams) error { return nil }
func (fakeTerminalProvider) RegisterRoutes(_ *http.ServeMux, _ string)                {}

func newStoreFixture(t *testing.T) (*Store, *tmux.MockClient) {
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
	registry.Register(fakeTerminalProvider{})
	registry.Register(bash.New())

	mockTmux := tmux.NewMockClient()
	s, err := New(Deps{
		Tmux:      mockTmux,
		RepoStore: repoStore,
		Settings:  settings,
		Registry:  registry,
		GHQRoot:   dir, // bypass ghq.Root()
	})
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	return s, mockTmux
}

// injectBranch wires a fake branch directly into the store map, bypassing
// OpenBranch (which would touch real gwq/git). This is what unit tests use.
func injectBranch(t *testing.T, s *Store, repoID, repoFullPath, branchName string, isPrimary bool) string {
	t.Helper()
	branchID := domain.BranchSlugID(repoFullPath, branchName)
	sessionName := domain.SessionName(repoID, branchID)
	branch := &domain.Branch{
		ID:           branchID,
		Name:         branchName,
		WorktreePath: t.TempDir(),
		RepoID:       repoID,
		IsPrimary:    isPrimary,
		LastActivity: time.Now(),
		TabSet:       domain.TabSet{TmuxSession: sessionName},
	}
	s.mu.Lock()
	repo, ok := s.repos[repoID]
	if !ok {
		repo = &domain.Repository{ID: repoID, GHQPath: "test/" + repoID, FullPath: repoFullPath}
		s.repos[repoID] = repo
	}
	repo.OpenBranches = append(repo.OpenBranches, branch)
	s.mu.Unlock()
	return branchID
}

func TestSyncTmux_RecreatesMissingSession(t *testing.T) {
	s, mockTmux := newStoreFixture(t)
	repoID := "tjst-t--demo--abcd"
	branchID := injectBranch(t, s, repoID, "/tmp/repo-demo", "main", true)
	sessionName := domain.SessionName(repoID, branchID)

	// At first the mock has no sessions. Sync must create one.
	if err := s.SyncTmux(context.Background()); err != nil {
		t.Fatalf("SyncTmux: %v", err)
	}
	has, _ := mockTmux.HasSession(context.Background(), sessionName)
	if !has {
		t.Fatalf("expected session %s to be recreated", sessionName)
	}
	// Should have created at least the claude window.
	windows, _ := mockTmux.ListWindows(context.Background(), sessionName)
	wantWindow := fakeTerminalWindow
	found := false
	for _, w := range windows {
		if w.Name == wantWindow {
			found = true
		}
	}
	if !found {
		t.Errorf("expected window %q after sync; got %+v", wantWindow, windows)
	}
}

func TestSyncTmux_KillsZombieSessions(t *testing.T) {
	s, mockTmux := newStoreFixture(t)

	// A palmux-prefixed session that THIS process previously created but
	// is no longer tracked (branch closed, but the kill SIGNAL got lost
	// somewhere) is a zombie.
	// Use a slug+hash repoID so the strict ParseSessionName (S009-
	// fix-3) recognises it as ours.
	zombie := "_palmux_orphan-repo--dead_main--1234"
	mockTmux.SeedSession(zombie)
	// S009-fix-4: only sessions THIS process previously owned are
	// killed as zombies. Mark `zombie` as ours.
	s.mu.Lock()
	s.knownBaseSessions[zombie] = struct{}{}
	s.mu.Unlock()
	// And one Palmux-managed session that the Store knows about.
	repoID := "tjst-t--demo--abcd"
	branchID := injectBranch(t, s, repoID, "/tmp/repo-demo", "main", true)
	sessionName := domain.SessionName(repoID, branchID)
	mockTmux.SeedSession(sessionName, tmux.Window{Index: 0, Name: fakeTerminalWindow})

	if err := s.SyncTmux(context.Background()); err != nil {
		t.Fatalf("SyncTmux: %v", err)
	}

	if has, _ := mockTmux.HasSession(context.Background(), zombie); has {
		t.Error("expected zombie session to be killed")
	}
	if has, _ := mockTmux.HasSession(context.Background(), sessionName); !has {
		t.Error("tracked session should be left alone")
	}
}

// TestSyncTmux_LeavesForeignBaseSessionAlone — S009-fix-4: if a
// `_palmux_*`-shaped session exists that THIS process never created or
// recovered, it must be left alone (it's owned by a peer palmux process
// or a stale prior instance with empty repos.json). Without this filter
// the peer's sessions get killed every 5 s and the user observes their
// Bash WS oscillating into "Reconnecting…" forever.
func TestSyncTmux_LeavesForeignBaseSessionAlone(t *testing.T) {
	s, mockTmux := newStoreFixture(t)
	foreign := "_palmux_some-peer-repo--dead_main--1234"
	mockTmux.SeedSession(foreign)
	// Note: NOT registered in s.knownBaseSessions — that's the point.
	if err := s.SyncTmux(context.Background()); err != nil {
		t.Fatalf("SyncTmux: %v", err)
	}
	if has, _ := mockTmux.HasSession(context.Background(), foreign); !has {
		t.Error("foreign-instance base session must not be killed")
	}
}

// TestSyncTmux_LeavesPeerInstanceSessionsAlone verifies the S009-fix-3
// invariant: a `_palmux_<instance>_<repo>_<branch>` session belongs to
// another palmux process running with `--tmux-prefix=_palmux_<instance>_`
// and the default-prefix host MUST NOT touch it. Without this rule the
// host's sync_tmux loop would treat every peer session as a zombie and
// kill it on each 5s tick — exactly the periodic reconnect pathology
// the user reported.
func TestSyncTmux_LeavesPeerInstanceSessionsAlone(t *testing.T) {
	s, mockTmux := newStoreFixture(t)
	peer := "_palmux_dev_tjst-t--demo--abcd_main--1234"
	mockTmux.SeedSession(peer)

	if err := s.SyncTmux(context.Background()); err != nil {
		t.Fatalf("SyncTmux: %v", err)
	}
	if has, _ := mockTmux.HasSession(context.Background(), peer); !has {
		t.Error("peer-instance session must not be killed by default-prefix host")
	}
}

func TestSyncTmux_LeavesUnrecognizedPalmuxSessionsAlone(t *testing.T) {
	// A `_palmux_<hash>` session that doesn't follow the
	// _palmux_{repo}_{branch} format must not be killed — it likely belongs
	// to another tool / a different Palmux installation.
	s, mockTmux := newStoreFixture(t)
	mockTmux.SeedSession("_palmux_a5e0b6e193deafbc") // single segment, no inner _

	if err := s.SyncTmux(context.Background()); err != nil {
		t.Fatalf("SyncTmux: %v", err)
	}
	if has, _ := mockTmux.HasSession(context.Background(), "_palmux_a5e0b6e193deafbc"); !has {
		t.Error("non-palmux-formatted _palmux_ session should not be killed")
	}
}

func TestSyncTmux_KillsOrphanGroupSessions(t *testing.T) {
	s, mockTmux := newStoreFixture(t)
	repoID := "tjst-t--demo--abcd"
	branchID := injectBranch(t, s, repoID, "/tmp/repo-demo", "main", true)
	sessionName := domain.SessionName(repoID, branchID)
	mockTmux.SeedSession(sessionName, tmux.Window{Index: 0, Name: fakeTerminalWindow})

	// A group session whose connection has gone away. S009-fix-2:
	// the conn ID must have been issued by THIS Store at some point;
	// otherwise sync_tmux assumes it belongs to another palmux
	// instance sharing the tmux server and leaves it alone.
	s.mu.Lock()
	s.knownConnIDs["stale"] = struct{}{}
	s.mu.Unlock()
	groupName := domain.GroupSessionName(sessionName, "stale")
	mockTmux.SeedSession(groupName, tmux.Window{Index: 0, Name: fakeTerminalWindow})

	if err := s.SyncTmux(context.Background()); err != nil {
		t.Fatalf("SyncTmux: %v", err)
	}
	if has, _ := mockTmux.HasSession(context.Background(), groupName); has {
		t.Error("orphan group session should be killed")
	}

	// Conversely: a group session whose conn ID we have never seen
	// must NOT be killed (cross-instance safety).
	foreignGroup := domain.GroupSessionName(sessionName, "from-other-instance")
	mockTmux.SeedSession(foreignGroup, tmux.Window{Index: 0, Name: fakeTerminalWindow})
	if err := s.SyncTmux(context.Background()); err != nil {
		t.Fatalf("SyncTmux: %v", err)
	}
	if has, _ := mockTmux.HasSession(context.Background(), foreignGroup); !has {
		t.Error("group session belonging to another palmux instance must be left alone")
	}
}

func TestEventHub_FanOut(t *testing.T) {
	hub := NewEventHub()
	ch1, unsub1 := hub.Subscribe()
	ch2, unsub2 := hub.Subscribe()
	defer unsub1()
	defer unsub2()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ev := <-ch1
		if ev.Type != EventBranchOpened {
			t.Errorf("ch1 got %v", ev.Type)
		}
	}()
	go func() {
		defer wg.Done()
		ev := <-ch2
		if ev.Type != EventBranchOpened {
			t.Errorf("ch2 got %v", ev.Type)
		}
	}()

	hub.Publish(Event{Type: EventBranchOpened})
	wg.Wait()

	if hub.SubscriberCount() != 2 {
		t.Errorf("subscribers = %d", hub.SubscriberCount())
	}
}

func TestEventHub_DropsOnSlowSubscriber(t *testing.T) {
	hub := NewEventHub()
	_, _ = hub.Subscribe()
	// Don't read; channel buffer (64) will fill, then Publish should drop.
	for i := 0; i < 200; i++ {
		hub.Publish(Event{Type: EventNotification})
	}
	// If we got here without blocking, the drop logic worked.
}
