// Package store is the in-memory state hub. It owns:
//   - the set of Open repositories and their open branches (TabSets)
//   - notifications, orphan sessions, and live connections
//   - the EventHub for broadcast
//
// Every mutation goes through Store so we can lock once, fan out events, and
// keep tmux in sync via the providers + sync loops.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/ghq"
	"github.com/tjst-t/palmux2/internal/gwq"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tmux"
	"github.com/tjst-t/palmux2/internal/worktree"
)

// Errors callers may want to inspect.
var (
	ErrRepoNotFound   = errors.New("repository not found")
	ErrBranchNotFound = errors.New("branch not found")
	ErrTabNotFound    = errors.New("tab not found")
	ErrTabProtected   = errors.New("tab is protected")
	// ErrTabLimit is returned when AddTab would exceed Provider.Limits().Max
	// or RemoveTab would drop below Provider.Limits().Min. Surfaced as 409
	// Conflict by the HTTP layer.
	ErrTabLimit   = errors.New("tab limit reached")
	ErrInvalidArg = errors.New("invalid argument")
)

// Deps bundles every external dependency Store needs.
type Deps struct {
	Tmux      tmux.Client
	GHQ       *ghq.Client
	Gwq       *gwq.Client
	RepoStore *config.RepoStore
	Settings  *config.SettingsStore
	Registry  *tab.Registry
	EventHub  *EventHub // optional; New creates one if nil
	Logger    *slog.Logger
	GHQRoot   string // optional override; if empty Store calls ghq.Root() lazily
	// MaxConnsPerBranch caps simultaneous WS attachments per branch. 0 means
	// unlimited. Wired from the --max-connections CLI flag.
	MaxConnsPerBranch int
}

// Store is concurrency-safe.
type Store struct {
	deps         Deps
	mu           sync.RWMutex
	repos        map[string]*domain.Repository // by RepoID
	conns        map[string]*domain.Connection
	notifs       map[string]domain.Notification
	logger       *slog.Logger
	ghqRoot      string
	hub          *EventHub
	registry     *tab.Registry
	multiTabHook MultiTabHook // S009: non-tmux multi providers (Claude)
}

// New creates a Store and hydrates it from disk. It does NOT start the sync
// loops — call Run separately so tests can drive Sync deterministically.
func New(deps Deps) (*Store, error) {
	if deps.Tmux == nil || deps.RepoStore == nil || deps.Settings == nil || deps.Registry == nil {
		return nil, fmt.Errorf("store.New: required dependency missing")
	}
	hub := deps.EventHub
	if hub == nil {
		hub = NewEventHub()
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{
		deps:     deps,
		repos:    map[string]*domain.Repository{},
		conns:    map[string]*domain.Connection{},
		notifs:   map[string]domain.Notification{},
		logger:   logger,
		ghqRoot:  deps.GHQRoot,
		hub:      hub,
		registry: deps.Registry,
	}
	if err := s.hydrate(context.Background()); err != nil {
		return nil, fmt.Errorf("store: hydrate: %w", err)
	}
	return s, nil
}

// PopulateTabs walks every Open branch and runs recomputeTabs against it.
// This must be called AFTER every Provider has been registered (otherwise
// REST-only tabs like Files / Git would be missing) and BEFORE the sync
// loops start (so the first GET /api/repos sees a populated tab list).
//
// The fallback was previously: SyncTmux's recovery path called recomputeTabs
// for branches whose tmux session was missing, but if the previous palmux
// died and left its sessions alive, recovery is a no-op and Tabs stays
// empty. main.go calls this before Run() to close that gap.
func (s *Store) PopulateTabs(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, repo := range s.repos {
		for _, b := range repo.OpenBranches {
			s.recomputeTabs(ctx, b)
		}
	}
}

// Hub returns the broadcaster.
func (s *Store) Hub() *EventHub { return s.hub }

// Tmux returns the tmux client wired into the Store. Tab providers use it
// to perform live tmux operations from their HTTP handlers.
func (s *Store) Tmux() tmux.Client { return s.deps.Tmux }

// Settings returns the live SettingsStore.
func (s *Store) Settings() *config.SettingsStore { return s.deps.Settings }

// Registry returns the TabProvider registry.
func (s *Store) Registry() *tab.Registry { return s.registry }

// Repos returns a snapshot of every Open repository, sorted by GHQPath.
func (s *Store) Repos() []*domain.Repository {
	s.mu.Lock()
	// Categorisation depends on persisted state (RepoStore + Settings) and
	// must run before we hand out copies. We do it under the write lock so
	// the in-memory branch records mutate consistently. The cost is a
	// brief contention spike on hot list endpoints, which is acceptable
	// given how rarely list calls fire (Drawer only re-fetches on events).
	s.applyCategoriesAllUnlocked()
	out := make([]*domain.Repository, 0, len(s.repos))
	for _, r := range s.repos {
		out = append(out, cloneRepo(r))
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].GHQPath < out[j].GHQPath })
	return out
}

// Repo returns a snapshot of one repository.
func (s *Store) Repo(id string) (*domain.Repository, error) {
	s.mu.Lock()
	r, ok := s.repos[id]
	if !ok {
		s.mu.Unlock()
		return nil, ErrRepoNotFound
	}
	s.applyCategoriesUnlocked(r)
	cp := cloneRepo(r)
	s.mu.Unlock()
	return cp, nil
}

// Branch returns a snapshot of one branch.
func (s *Store) Branch(repoID, branchID string) (*domain.Branch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[repoID]
	if !ok {
		return nil, ErrRepoNotFound
	}
	s.applyCategoriesUnlocked(r)
	for _, b := range r.OpenBranches {
		if b.ID == branchID {
			return cloneBranch(b), nil
		}
	}
	return nil, ErrBranchNotFound
}

// hydrate loads repos.json and seeds the in-memory state. It does NOT call
// out to tmux for resurrection — the sync loops handle that within 5s.
func (s *Store) hydrate(ctx context.Context) error {
	entries := s.deps.RepoStore.All()
	if len(entries) == 0 {
		return nil
	}
	root, err := s.resolveGHQRoot(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := filepath.Join(root, e.GHQPath)
		repo := &domain.Repository{
			ID:       e.ID,
			GHQPath:  e.GHQPath,
			FullPath: full,
			Starred:  e.Starred,
		}
		// Best-effort: enumerate worktrees. Failures here just mean the repo
		// shows up empty and the sync loop will retry.
		if wts, err := worktree.List(ctx, full); err == nil {
			for _, wt := range wts {
				if wt.Branch == "" || wt.IsDetached {
					continue // skip detached HEADs
				}
				branch := s.buildBranchFromWorktree(repo, wt)
				repo.OpenBranches = append(repo.OpenBranches, branch)
			}
			sortBranches(repo.OpenBranches)
		} else {
			s.logger.Warn("hydrate: worktree.List failed", "repo", e.GHQPath, "err", err)
		}
		s.repos[e.ID] = repo
	}
	return nil
}

func (s *Store) resolveGHQRoot(ctx context.Context) (string, error) {
	if s.ghqRoot != "" {
		return s.ghqRoot, nil
	}
	if s.deps.GHQ == nil {
		return "", fmt.Errorf("ghq client not configured and no override")
	}
	root, err := s.deps.GHQ.Root(ctx)
	if err != nil {
		return "", err
	}
	s.ghqRoot = root
	return root, nil
}

// buildBranchFromWorktree builds a Branch entity from a worktree record.
// The tabs list is computed from the current tmux session state (see
// computeTabs).
func (s *Store) buildBranchFromWorktree(repo *domain.Repository, wt worktree.Worktree) *domain.Branch {
	branchID := domain.BranchSlugID(repo.FullPath, wt.Branch)
	sessionName := domain.SessionName(repo.ID, branchID)
	branch := &domain.Branch{
		ID:           branchID,
		Name:         wt.Branch,
		WorktreePath: wt.Path,
		RepoID:       repo.ID,
		IsPrimary:    wt.IsPrimary,
		LastActivity: time.Now(),
		// Empty (non-nil) slice so JSON serialises as `[]` rather than `null`
		// even before recomputeTabs has run. The frontend's `tabs.find(...)`
		// blows up on null, and on a server restart with already-live tmux
		// sessions the SyncTmux recovery path skips recomputeTabs entirely.
		TabSet: domain.TabSet{TmuxSession: sessionName, Tabs: []domain.Tab{}},
	}
	return branch
}

// recomputeTabs rebuilds branch.TabSet.Tabs from current tmux window state +
// the registered providers. Called whenever the underlying state may have
// changed. Caller MUST hold the write lock.
func (s *Store) recomputeTabs(ctx context.Context, branch *domain.Branch) {
	windows, err := s.deps.Tmux.ListWindows(ctx, branch.TabSet.TmuxSession)
	if err != nil {
		// Session may not exist yet; sync_tmux will recreate it within 5s.
		windows = nil
	}
	// Index windows by tab type, preserving tmux index order so user-added
	// bash tabs stay in stable positions.
	byType := map[string][]string{} // type -> []name (window suffix)
	for _, w := range windows {
		typ, name, ok := domain.ParseWindowName(w.Name)
		if !ok {
			continue
		}
		byType[typ] = append(byType[typ], name)
	}

	var tabs []domain.Tab
	for _, p := range s.registry.Providers() {
		if !p.NeedsTmuxWindow() {
			// Non-tmux singletons (Files, Git): one fixed tab.
			if !p.Multiple() {
				tabs = append(tabs, domain.Tab{
					ID:        p.Type(),
					Type:      p.Type(),
					Name:      p.DisplayName(),
					Protected: p.Protected(),
					Multiple:  p.Multiple(),
				})
				continue
			}
			// Non-tmux multi (Claude post-S009): re-derive from the
			// provider via OnBranchOpen so the persisted tab list is the
			// source of truth. recomputeTabs is read-only; it doesn't
			// re-trigger any side effects.
			res, err := p.OnBranchOpen(ctx, tab.OpenParams{Branch: branch, Resume: false})
			if err != nil {
				s.logger.Warn("recomputeTabs: OnBranchOpen for non-tmux multi", "type", p.Type(), "err", err)
				continue
			}
			tabs = append(tabs, res.Tabs...)
			continue
		}
		names := byType[p.Type()]
		// Singleton terminal-backed (Claude): exactly one tab.
		if !p.Multiple() {
			tabs = append(tabs, domain.Tab{
				ID:         p.Type(),
				Type:       p.Type(),
				Name:       p.DisplayName(),
				Protected:  p.Protected(),
				Multiple:   false,
				WindowName: domain.WindowName(p.Type(), p.Type()),
			})
			continue
		}
		// Multi-instance (Bash): one tab per window.
		for _, n := range names {
			tabs = append(tabs, domain.Tab{
				ID:         domain.TabID(p.Type(), n),
				Type:       p.Type(),
				Name:       displayNameFor(p, n),
				Protected:  false,
				Multiple:   true,
				WindowName: domain.WindowName(p.Type(), n),
			})
		}
	}
	branch.TabSet.Tabs = tabs
}

func displayNameFor(p tab.Provider, windowName string) string {
	// Default display: capitalised window suffix unless it's the canonical name.
	if windowName == p.Type() {
		return p.DisplayName()
	}
	if strings.HasPrefix(windowName, p.Type()+"-") {
		// e.g. "bash-2" → "Bash 2"
		return p.DisplayName() + " " + strings.TrimPrefix(windowName, p.Type()+"-")
	}
	return windowName
}

// AvailableRepos returns ghq's list of all known repositories so the UI can
// show them as Open candidates.
func (s *Store) AvailableRepos(ctx context.Context) ([]ghq.Repository, error) {
	if s.deps.GHQ == nil {
		return nil, fmt.Errorf("ghq client not configured")
	}
	return s.deps.GHQ.List(ctx)
}

// OpenRepo records a repository in repos.json + the in-memory store and
// hydrates its open branches.
func (s *Store) OpenRepo(ctx context.Context, ghqPath string) (*domain.Repository, error) {
	ghqPath = strings.Trim(ghqPath, "/")
	if ghqPath == "" {
		return nil, fmt.Errorf("%w: ghqPath empty", ErrInvalidArg)
	}
	root, err := s.resolveGHQRoot(ctx)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(root, ghqPath)
	repoID := domain.RepoSlugID(ghqPath)

	// Persist in repos.json first so a crash leaves a recoverable state.
	if _, err := s.deps.RepoStore.Add(config.RepoEntry{ID: repoID, GHQPath: ghqPath}); err != nil {
		return nil, err
	}

	repo := &domain.Repository{
		ID:       repoID,
		GHQPath:  ghqPath,
		FullPath: full,
	}
	if wts, err := worktree.List(ctx, full); err == nil {
		for _, wt := range wts {
			if wt.Branch == "" || wt.IsDetached {
				continue
			}
			branch := s.buildBranchFromWorktree(repo, wt)
			repo.OpenBranches = append(repo.OpenBranches, branch)
		}
		sortBranches(repo.OpenBranches)
	}

	s.mu.Lock()
	if existing, ok := s.repos[repoID]; ok {
		s.mu.Unlock()
		return cloneRepo(existing), nil
	}
	s.repos[repoID] = repo
	// Compute initial tab sets for any pre-existing branches.
	for _, b := range repo.OpenBranches {
		s.recomputeTabs(ctx, b)
	}
	snap := cloneRepo(repo)
	s.mu.Unlock()

	s.hub.Publish(Event{Type: EventRepoOpened, RepoID: repoID, Payload: snap})
	return snap, nil
}

// CloseRepo removes a repository from repos.json and kills every Palmux tmux
// session it owns (worktrees are left alone — only the primary branch's
// worktree is also left, all linked worktrees are destroyed via gwq remove).
func (s *Store) CloseRepo(ctx context.Context, repoID string) error {
	s.mu.Lock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.Unlock()
		return ErrRepoNotFound
	}
	branches := append([]*domain.Branch(nil), repo.OpenBranches...)
	delete(s.repos, repoID)
	s.mu.Unlock()

	if _, err := s.deps.RepoStore.Remove(repoID); err != nil {
		return err
	}
	for _, b := range branches {
		_ = s.deps.Tmux.KillSession(ctx, b.TabSet.TmuxSession)
		// linked worktrees get cleaned up below
		if !b.IsPrimary && s.deps.Gwq != nil {
			if err := s.deps.Gwq.Remove(ctx, repo.FullPath, b.Name); err != nil {
				s.logger.Warn("CloseRepo: gwq remove failed", "branch", b.Name, "err", err)
			}
		}
	}
	s.hub.Publish(Event{Type: EventRepoClosed, RepoID: repoID})
	return nil
}

// SetStarred toggles the starred flag on a repo.
func (s *Store) SetStarred(repoID string, starred bool) error {
	s.mu.Lock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.Unlock()
		return ErrRepoNotFound
	}
	repo.Starred = starred
	s.mu.Unlock()
	if _, err := s.deps.RepoStore.SetStarred(repoID, starred); err != nil {
		return err
	}
	evt := EventRepoUnstarred
	if starred {
		evt = EventRepoStarred
	}
	s.hub.Publish(Event{Type: evt, RepoID: repoID})
	return nil
}

func cloneRepo(r *domain.Repository) *domain.Repository {
	if r == nil {
		return nil
	}
	cp := *r
	cp.OpenBranches = make([]*domain.Branch, len(r.OpenBranches))
	for i, b := range r.OpenBranches {
		cp.OpenBranches[i] = cloneBranch(b)
	}
	return &cp
}

func cloneBranch(b *domain.Branch) *domain.Branch {
	if b == nil {
		return nil
	}
	cp := *b
	cp.TabSet = domain.TabSet{TmuxSession: b.TabSet.TmuxSession}
	cp.TabSet.Tabs = append([]domain.Tab(nil), b.TabSet.Tabs...)
	return &cp
}

func sortBranches(branches []*domain.Branch) {
	sort.SliceStable(branches, func(i, j int) bool {
		// Primary always first, then alphabetical by branch name.
		if branches[i].IsPrimary != branches[j].IsPrimary {
			return branches[i].IsPrimary
		}
		return branches[i].Name < branches[j].Name
	})
}
