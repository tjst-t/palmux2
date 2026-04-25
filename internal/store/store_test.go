package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tab/bash"
	"github.com/tjst-t/palmux2/internal/tab/claude"
	"github.com/tjst-t/palmux2/internal/tmux"
)

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
	registry.Register(claude.New(claude.Options{}))
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
	wantWindow := claude.WindowName
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

	// A palmux-prefixed session not tracked by the Store is a zombie.
	mockTmux.SeedSession("_palmux_orphan_repo_orphan_branch")
	// And one Palmux-managed session that the Store knows about.
	repoID := "tjst-t--demo--abcd"
	branchID := injectBranch(t, s, repoID, "/tmp/repo-demo", "main", true)
	sessionName := domain.SessionName(repoID, branchID)
	mockTmux.SeedSession(sessionName, tmux.Window{Index: 0, Name: claude.WindowName})

	if err := s.SyncTmux(context.Background()); err != nil {
		t.Fatalf("SyncTmux: %v", err)
	}

	if has, _ := mockTmux.HasSession(context.Background(), "_palmux_orphan_repo_orphan_branch"); has {
		t.Error("expected zombie session to be killed")
	}
	if has, _ := mockTmux.HasSession(context.Background(), sessionName); !has {
		t.Error("tracked session should be left alone")
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
	mockTmux.SeedSession(sessionName, tmux.Window{Index: 0, Name: claude.WindowName})

	// A group session whose connection has gone away.
	groupName := domain.GroupSessionName(sessionName, "stale")
	mockTmux.SeedSession(groupName, tmux.Window{Index: 0, Name: claude.WindowName})

	if err := s.SyncTmux(context.Background()); err != nil {
		t.Fatalf("SyncTmux: %v", err)
	}
	if has, _ := mockTmux.HasSession(context.Background(), groupName); has {
		t.Error("orphan group session should be killed")
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
