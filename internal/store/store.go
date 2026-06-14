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
	"github.com/tjst-t/palmux2/internal/runtime"
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
	// RuntimeRegistry maps (repoID, branchID) to the live Runtime for that
	// workspace.  Optional — when nil the store behaves exactly as before
	// S8478ca (all tmux calls go directly through Deps.Tmux).  Story S8478ca-3
	// will make resolution non-trivial; for now the default registry always
	// returns a host Runtime.
	RuntimeRegistry runtime.Registry
}

// Store is concurrency-safe.
type Store struct {
	deps              Deps
	mu                sync.RWMutex
	repos             map[string]*domain.Repository // by RepoID
	conns             map[string]*domain.Connection
	knownConnIDs      map[string]struct{} // S009-fix-2: ever-seen conn IDs (for cross-instance group session safety)
	knownBaseSessions map[string]struct{} // S009-fix-4: base session names this process has created/recovered
	notifs            map[string]domain.Notification
	logger            *slog.Logger
	ghqRoot           string
	hub               *EventHub
	registry          *tab.Registry
	multiTabHook      MultiTabHook // S009: non-tmux multi providers (Claude)

	// driftMu guards the per-workspace image-drift cache. The 10s scanPorts loop
	// runs the (incus-CLI) staleness check and updates this map; RuntimeViewFor
	// reads it cheaply (no CLI) so hot list endpoints stay fast. Key:
	// "repoID/branchID". (S7364e3)
	driftMu sync.RWMutex
	drift   map[string]bool
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
		deps:              deps,
		repos:             map[string]*domain.Repository{},
		conns:             map[string]*domain.Connection{},
		knownConnIDs:      map[string]struct{}{},
		knownBaseSessions: map[string]struct{}{},
		notifs:            map[string]domain.Notification{},
		logger:            logger,
		ghqRoot:           deps.GHQRoot,
		hub:               hub,
		registry:          deps.Registry,
		drift:             map[string]bool{},
	}
	if err := s.hydrate(context.Background()); err != nil {
		return nil, fmt.Errorf("store: hydrate: %w", err)
	}
	// S0c6a1b: inject the reserved, repository-independent host scope so the
	// user can open a terminal before opening any ghq repo. Skeleton only —
	// PopulateTabs (called after providers register) computes its bash tab.
	s.seedHostScope()
	return s, nil
}

// RecomputeBranchTabs is the public entry point used by Conditional tab
// providers (S016 Sprint) when their visibility may have changed without
// any tmux activity — e.g. ROADMAP.md was created or deleted in the
// worktree. It re-runs `recomputeTabs` for the given branch and diffs
// the previous TabSet against the new one, emitting `tab.added` /
// `tab.removed` events for the differences so subscribed browsers can
// update their TabBar immediately.
//
// The diff is keyed on Tab.ID + Tab.Type so multiple Bash instances
// (which share Type but differ on ID) are handled correctly.
//
// Returns ErrRepoNotFound / ErrBranchNotFound when the IDs don't match.
func (s *Store) RecomputeBranchTabs(repoID, branchID string) error {
	ctx := context.Background()
	s.mu.Lock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.Unlock()
		return ErrRepoNotFound
	}
	var branch *domain.Branch
	for _, b := range repo.OpenBranches {
		if b.ID == branchID {
			branch = b
			break
		}
	}
	if branch == nil {
		s.mu.Unlock()
		return ErrBranchNotFound
	}
	prev := append([]domain.Tab(nil), branch.TabSet.Tabs...)
	s.recomputeTabs(ctx, branch)
	next := append([]domain.Tab(nil), branch.TabSet.Tabs...)
	s.mu.Unlock()

	prevSet := map[string]struct{}{}
	for _, t := range prev {
		prevSet[t.Type+"\x00"+t.ID] = struct{}{}
	}
	nextSet := map[string]struct{}{}
	for _, t := range next {
		nextSet[t.Type+"\x00"+t.ID] = struct{}{}
	}
	for _, t := range next {
		if _, was := prevSet[t.Type+"\x00"+t.ID]; !was {
			s.hub.Publish(Event{
				Type: EventTabAdded, RepoID: repoID, BranchID: branchID, TabID: t.ID,
				Payload: map[string]any{"tab": t},
			})
		}
	}
	for _, t := range prev {
		if _, still := nextSet[t.Type+"\x00"+t.ID]; !still {
			s.hub.Publish(Event{
				Type: EventTabRemoved, RepoID: repoID, BranchID: branchID, TabID: t.ID,
			})
		}
	}
	return nil
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

// TmuxFor returns the tmux.Client that should drive tmux operations for the
// given workspace.
//
//   - host workspace (or no RuntimeRegistry configured): returns s.deps.Tmux
//     unchanged — behaviour is byte-identical to pre-S8478ca.
//   - incus-container workspace: returns the incusTmuxClient for that
//     workspace's container.  If the container is not yet started, Start()
//     is called first (idempotent — if it was already started this is a
//     single status check).
//
// This is the single dispatch point that lets ensureSession, EnsureTabWindow,
// AddTab, RemoveTab and the WS attach path all route correctly without knowing
// which runtime a workspace uses.
func (s *Store) TmuxFor(ctx context.Context, repoID, branchID string) tmux.Client {
	return s.tmuxFor(ctx, repoID, branchID)
}

// tmuxFor is the private implementation; public TmuxFor is the store entry-point
// for handlers that hold a *Store but not a branch pointer.
func (s *Store) tmuxFor(ctx context.Context, repoID, branchID string) tmux.Client {
	if s.deps.RuntimeRegistry == nil {
		return s.deps.Tmux
	}
	rt := s.deps.RuntimeRegistry.Get(repoID, branchID)
	if rt == nil {
		return s.deps.Tmux
	}
	tc := rt.TmuxClient()
	// For host runtimes TmuxClient() returns the same client as s.deps.Tmux.
	// For incus-container runtimes we need to ensure the container is started
	// before the first tmux call.
	if rt.Kind() == runtime.KindIncusContainer {
		if st := rt.Status(); st.State != runtime.StateReady {
			s.logger.Info("store.tmuxFor: starting incus container", "repoID", repoID, "branchID", branchID)
			if err := rt.Start(ctx); err != nil {
				// Do NOT fall back to s.deps.Tmux: recreating the session on the
				// HOST tmux server silently breaks isolation and, during a
				// host→incus switch, races the sync_tmux recovery loop into
				// resurrecting the just-killed host session. Return the incus
				// client; tmux ops surface the error and the next reconcile
				// retries Start (now serialised + idempotent).
				s.logger.Error("store.tmuxFor: incus Start failed (returning incus client, not host)",
					"repoID", repoID, "branchID", branchID, "err", err)
			}
		}
	}
	return tc
}

// Hub returns the broadcaster.
func (s *Store) Hub() *EventHub { return s.hub }

// Tmux returns the tmux client wired into the Store. Tab providers use it
// to perform live tmux operations from their HTTP handlers.
func (s *Store) Tmux() tmux.Client { return s.deps.Tmux }

// Settings returns the live SettingsStore.
func (s *Store) Settings() *config.SettingsStore { return s.deps.Settings }

// RepoStore returns the live RepoStore. Used by server handlers that need
// to read or write per-branch settings (e.g. claude_mode, S1f75ec-2).
func (s *Store) RepoStore() *config.RepoStore { return s.deps.RepoStore }

// Registry returns the TabProvider registry.
func (s *Store) Registry() *tab.Registry { return s.registry }

// GHQClient returns the store's ghq.Client, or nil if not configured.
// Exposed for the clone handler which needs to run `ghq get`.
func (s *Store) GHQClient() *ghq.Client { return s.deps.GHQ }

// RuntimeRegistry returns the runtime.Registry used by the store.
// May be nil if the store was constructed without one.
func (s *Store) RuntimeRegistry() runtime.Registry { return s.deps.RuntimeRegistry }

// WorkspaceRuntime returns the live Runtime for the given workspace,
// or nil when no RuntimeRegistry is configured.
func (s *Store) WorkspaceRuntime(repoID, branchID string) runtime.Runtime {
	if s.deps.RuntimeRegistry == nil {
		return nil
	}
	return s.deps.RuntimeRegistry.Get(repoID, branchID)
}

// RuntimeViewFor returns the domain.RuntimeView for the given workspace.
// Falls back to a "host / ready / localhost" view when no registry is wired.
// CurrentRuntime returns the runtime the workspace currently resolves to,
// captured for callers that need the OLD runtime BEFORE persisting a change
// (the registry re-resolves config on every Get). Returns nil if no registry.
func (s *Store) CurrentRuntime(repoID, branchID string) runtime.Runtime {
	if s.deps.RuntimeRegistry == nil {
		return nil
	}
	return s.deps.RuntimeRegistry.Get(repoID, branchID)
}

func (s *Store) RuntimeViewFor(repoID, branchID string) *domain.RuntimeView {
	if s.deps.RuntimeRegistry == nil {
		return &domain.RuntimeView{Kind: "host", State: "ready", Address: "localhost"}
	}
	rt := s.deps.RuntimeRegistry.Get(repoID, branchID)
	if rt == nil {
		return &domain.RuntimeView{Kind: "host", State: "ready", Address: "localhost"}
	}
	st := rt.Status()
	return &domain.RuntimeView{
		Kind:    string(rt.Kind()),
		State:   string(st.State),
		Address: st.Address,
		Error:   st.Error,
		Stale:   s.driftCached(repoID, branchID),
	}
}

// driftKey is the map key for the per-workspace image-drift cache.
func driftKey(repoID, branchID string) string { return repoID + "/" + branchID }

// driftCached returns the last-computed image-drift result for a workspace
// (false if never computed). Cheap — no incus CLI. (S7364e3)
func (s *Store) driftCached(repoID, branchID string) bool {
	s.driftMu.RLock()
	defer s.driftMu.RUnlock()
	return s.drift[driftKey(repoID, branchID)]
}

// setDriftCached records a workspace's image-drift result and reports whether
// the value changed (so callers publish a drift event only on transitions).
// (S7364e3)
func (s *Store) setDriftCached(repoID, branchID string, stale bool) (changed bool) {
	key := driftKey(repoID, branchID)
	s.driftMu.Lock()
	defer s.driftMu.Unlock()
	prev, known := s.drift[key]
	s.drift[key] = stale
	return !known || prev != stale
}

// clearDriftCached drops a workspace's cached drift result (e.g. after the
// container is regenerated or closed). (S7364e3)
func (s *Store) clearDriftCached(repoID, branchID string) {
	s.driftMu.Lock()
	defer s.driftMu.Unlock()
	delete(s.drift, driftKey(repoID, branchID))
}

// OpenBranchInternal exposes the internal open-branch path to server handlers.
// markUserOpened controls whether the branch is recorded in repos.json#userOpenedBranches.
func (s *Store) OpenBranchInternal(ctx context.Context, repoID, branchName string, markUserOpened bool) (*domain.Branch, error) {
	return s.openBranchInternal(ctx, repoID, branchName, markUserOpened)
}

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
		if IsHostRepoID(r.ID) {
			// S0c6a1b: the reserved host scope is exposed via GET /api/host,
			// not the repo list — keep it out of /api/repos so it never
			// pollutes the Repositories section or repos.json.
			continue
		}
		out = append(out, cloneRepo(r))
	}
	s.mu.Unlock()
	// S8478ca-5: enrich each branch with a live RuntimeView (done outside
	// the lock because RuntimeRegistry.Get may call incus CLI in future).
	for _, r := range out {
		s.enrichRepoRuntimeViews(r)
	}
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
	// S8478ca-5: enrich branches with live RuntimeView.
	s.enrichRepoRuntimeViews(cp)
	return cp, nil
}

// enrichRepoRuntimeViews populates the Runtime field on every OpenBranch of r.
// For the host scope (repoId=host--0000) or when no RuntimeRegistry is wired,
// every branch gets a host/ready view. The Host scope branch (branchId=host)
// is intentionally given a nil Runtime so the FE renders the host-scope-label
// instead of a runtime chip.
func (s *Store) enrichRepoRuntimeViews(r *domain.Repository) {
	// S0c6a1b: Host login scope has no runtime concept — leave Runtime nil so
	// the FE renders host-scope-label (AC-5-3).
	if IsHostRepoID(r.ID) {
		return
	}
	for _, b := range r.OpenBranches {
		b.Runtime = s.RuntimeViewFor(r.ID, b.ID)
	}
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

// Tab (Sadf90e) returns the tab with the given ID on the given open branch.
// Returns ErrRepoNotFound / ErrBranchNotFound / ErrTabNotFound to match the
// other resource-lookup helpers in this package. Used by the tab-scope
// settings handler so a 404 can be issued before reaching RepoStore.
func (s *Store) Tab(repoID, branchID, tabID string) (*domain.Tab, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.repos[repoID]
	if !ok {
		return nil, ErrRepoNotFound
	}
	for _, b := range r.OpenBranches {
		if b.ID != branchID {
			continue
		}
		for _, t := range b.TabSet.Tabs {
			if t.ID == tabID {
				cp := t
				return &cp, nil
			}
		}
		return nil, ErrTabNotFound
	}
	return nil, ErrBranchNotFound
}

// ResolveLegacyBranchID (S1e8d02) tries to interpret `id` as a pre-S1e8d02
// branch-name-based ID and returns the current path-based ID for the
// matching live branch. Used by HTTP handlers that want to issue a 302
// redirect from old bookmarks/URLs to the new canonical location.
//
// Returns ok=false if:
//   - the repo is not Open
//   - no live branch's name re-derives to `id` via the legacy
//     [domain.BranchSlugID] formula
//
// Cheap — runs in O(open branches in this repo), which is small.
func (s *Store) ResolveLegacyBranchID(repoID, id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.repos[repoID]
	if !ok {
		return "", false
	}
	for _, b := range r.OpenBranches {
		if domain.BranchSlugID(r.FullPath, b.Name) == id {
			return b.ID, true
		}
	}
	return "", false
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
			ID:               e.ID,
			GHQPath:          e.GHQPath,
			FullPath:         full,
			Starred:          e.Starred,
			LastActiveBranch: e.LastActiveBranch,
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
//
// S1e8d02: BranchID is now path-derived (see [domain.WorkspaceSlugIDFromPath]).
// In-place `git checkout` keeps the path fixed, hence keeps the ID fixed,
// hence does NOT trigger close+open which used to destroy Claude/tmux/tabs.
func (s *Store) buildBranchFromWorktree(repo *domain.Repository, wt worktree.Worktree) *domain.Branch {
	branchID := domain.WorkspaceSlugIDFromPath(wt.Path, wt.IsPrimary, repo.FullPath)
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
//
// S009-fix-1: ListWindows can fail transiently when sync_tmux is mid-cycle
// — the old behaviour of treating that as "no windows" caused multi-instance
// tmux-backed tabs (Bash) to vanish from the snapshot whenever an unrelated
// non-tmux mutation (e.g. POST /tabs {type:claude}) triggered a recompute,
// which in turn broke the FE: the user added a Claude tab and watched their
// Bash tabs disappear (and re-appear after sync_tmux recreated the session
// 5s later). We now distinguish "ListWindows failed transiently" from
// "session legitimately has zero windows of this type" and, in the failure
// case, fall back to the previously-known tab list for tmux-backed multi
// providers. Singletons stay deterministic; non-tmux providers were
// already unaffected.
func (s *Store) recomputeTabs(ctx context.Context, branch *domain.Branch) {
	if IsHostRepoID(branch.RepoID) {
		// S0c6a1b: reserved host scope is bash-only (no Claude/Files/Git/Sprint)
		// and always advertises the canonical bash tab even before its tmux
		// session is lazily created.
		s.recomputeHostTabs(ctx, branch)
		return
	}
	// S8478ca-2: route through tmuxFor so incus-container workspaces query
	// the in-container tmux server.  Note: tmuxFor may Start the container
	// when kind=incus-container; we hold the write lock here so keep Start
	// fast (it short-circuits via status check when the container is already
	// running).  For host workspaces tmuxFor returns s.deps.Tmux unchanged.
	windows, err := s.tmuxFor(ctx, branch.RepoID, branch.ID).ListWindows(ctx, branch.TabSet.TmuxSession)
	listFailed := err != nil
	if listFailed {
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

	// Index existing tabs by type so we can preserve them across a failed
	// ListWindows (S009-fix-1).
	prevByType := map[string][]domain.Tab{}
	for _, t := range branch.TabSet.Tabs {
		prevByType[t.Type] = append(prevByType[t.Type], t)
	}

	var tabs []domain.Tab
	for _, p := range s.registry.Providers() {
		if !p.NeedsTmuxWindow() {
			// Non-tmux singletons (Files, Git): one fixed tab unless the
			// provider is Conditional (S016 Sprint), in which case its
			// OnBranchOpen result drives visibility per-branch.
			if !p.Multiple() {
				if p.Conditional() {
					res, err := p.OnBranchOpen(ctx, tab.OpenParams{Branch: branch, Resume: false})
					if err != nil {
						s.logger.Warn("recomputeTabs: OnBranchOpen for conditional singleton",
							"type", p.Type(), "err", err)
						continue
					}
					tabs = append(tabs, res.Tabs...)
					continue
				}
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
		// Singleton terminal-backed: exactly one tab. The Window may not be
		// up yet (sync_tmux pending) but the tab still exists logically.
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
		names := byType[p.Type()]
		if listFailed && len(names) == 0 {
			// Tmux query failed and we have no live data. Fall back to the
			// previous tab list for this type so we don't transiently drop
			// the user's Bash tabs while sync_tmux is mid-recovery.
			tabs = append(tabs, prevByType[p.Type()]...)
			continue
		}
		// If the live session is up but has no windows of this type yet
		// (e.g. fresh session pre-ensureSession), seed the canonical
		// instance so the user always sees at least one Bash tab.
		if !listFailed && len(names) == 0 {
			names = []string{p.Type()} // canonical "bash"
		}
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
	// S020: apply per-branch name overrides + user-set ordering recorded
	// in repos.json `tabOverrides`. Singleton (Files/Git) tabs ignore both.
	tabs = s.applyTabOverrides(branch, tabs)
	branch.TabSet.Tabs = tabs
}

// applyTabOverrides (S020) consults RepoStore for any user-set rename or
// reorder entries for the given branch and rewrites the slice accordingly.
// Order rule: tabs are first grouped by type (preserving group adjacency
// and singleton positions), then within each Multiple()=true group the
// user's saved order takes precedence; tabs whose IDs are not in the
// saved order keep their default position at the tail of their group.
func (s *Store) applyTabOverrides(branch *domain.Branch, in []domain.Tab) []domain.Tab {
	if branch == nil || s.deps.RepoStore == nil {
		return in
	}
	// 1. Apply name overrides (cheap — a single map lookup per tab).
	for i := range in {
		if !in[i].Multiple {
			continue
		}
		if name := s.deps.RepoStore.TabName(branch.RepoID, branch.Name, in[i].ID); name != "" {
			in[i].Name = name
		}
	}
	// 2. Apply order overrides.
	order := s.deps.RepoStore.TabOrder(branch.RepoID, branch.Name)
	if len(order) == 0 {
		return in
	}
	// Index input by ID.
	byID := map[string]domain.Tab{}
	for _, t := range in {
		byID[t.ID] = t
	}
	// Walk the original list; whenever we hit the start of a Multiple()=true
	// group, emit it in the order: (a) IDs from `order` matching this group,
	// then (b) any leftover IDs in their original sequence.
	out := make([]domain.Tab, 0, len(in))
	emittedIDs := map[string]struct{}{}
	i := 0
	for i < len(in) {
		t := in[i]
		if !t.Multiple {
			out = append(out, t)
			i++
			continue
		}
		// Collect the consecutive same-type group.
		groupType := t.Type
		groupTabs := []domain.Tab{}
		j := i
		for j < len(in) && in[j].Type == groupType && in[j].Multiple {
			groupTabs = append(groupTabs, in[j])
			j++
		}
		// Emit user-ordered IDs of this group first.
		groupIDs := map[string]struct{}{}
		for _, gt := range groupTabs {
			groupIDs[gt.ID] = struct{}{}
		}
		for _, id := range order {
			if _, ok := groupIDs[id]; !ok {
				continue
			}
			if _, done := emittedIDs[id]; done {
				continue
			}
			out = append(out, byID[id])
			emittedIDs[id] = struct{}{}
		}
		// Then any remaining tabs in their original order.
		for _, gt := range groupTabs {
			if _, done := emittedIDs[gt.ID]; done {
				continue
			}
			out = append(out, gt)
			emittedIDs[gt.ID] = struct{}{}
		}
		i = j
	}
	return out
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

	// Sadf90e (review fix): Branches discovered at OpenRepo time get their
	// tab lists computed via recomputeTabs above, which bypasses store.AddTab
	// and openBranchInternal. Seed TabClaudeModes for each Claude tab now so
	// the global settings.claude.default_mode applies to the canonical first
	// tab on a freshly-opened workspace. Idempotent — re-opening a repo with
	// existing entries leaves them untouched.
	s.initClaudeTabModes(repoID, snap.OpenBranches)

	s.hub.Publish(Event{Type: EventRepoOpened, RepoID: repoID, Payload: snap})
	return snap, nil
}

// initClaudeTabModes (Sadf90e) walks the given branches' tab lists and calls
// RepoStore.InitTabClaudeMode for every Claude tab using the global
// settings.claude.default_mode as the value. Called from both OpenRepo (for
// branches discovered at startup) and openBranchInternal (for branches opened
// later). Idempotent — existing entries are left unchanged. Errors are
// logged, not propagated, so a transient repos.json write failure does not
// block branch open.
func (s *Store) initClaudeTabModes(repoID string, branches []*domain.Branch) {
	if s.deps.RepoStore == nil || s.deps.Settings == nil {
		return
	}
	defaultMode := configClaudeMode(s.deps.Settings.ClaudeDefaultMode())
	for _, b := range branches {
		if b == nil {
			continue
		}
		for _, t := range b.TabSet.Tabs {
			if t.Type != "claude" {
				continue
			}
			if err := s.deps.RepoStore.InitTabClaudeMode(repoID, b.ID, t.ID, defaultMode); err != nil {
				s.logger.Warn("initClaudeTabModes: InitTabClaudeMode failed",
					"repo", repoID, "branch", b.Name, "tab", t.ID, "err", err)
			}
		}
	}
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

// DeleteRepo permanently removes a repository: it runs CloseRepo to kill tmux
// sessions and remove linked worktrees, then removes the ghq directory from
// disk.
//
// This is the "Delete repository" path. The caller is responsible for verifying
// any unpushed work and requiring a typed confirmation before calling this
// (see handler_repo.go cloneRepo / deleteRepo).
func (s *Store) DeleteRepo(ctx context.Context, repoID string) error {
	// hotfix: also delete repos that are NOT currently Open in Palmux —
	// the user can request deletion of any ghq-tracked repo from the
	// Open Repository modal's per-row 🗑.
	s.mu.RLock()
	repo, isOpen := s.repos[repoID]
	s.mu.RUnlock()

	var ghqPath, fullPath string
	if isOpen {
		ghqPath = repo.GHQPath
		fullPath = repo.FullPath
	} else {
		// Closed repo: discover its on-disk paths via ghq list.
		if s.deps.GHQ == nil {
			return ErrRepoNotFound
		}
		all, err := s.deps.GHQ.List(ctx)
		if err != nil {
			return fmt.Errorf("DeleteRepo ghq list: %w", err)
		}
		for _, r := range all {
			if domain.RepoSlugID(r.GHQPath) == repoID {
				ghqPath = r.GHQPath
				fullPath = r.FullPath
				break
			}
		}
		if fullPath == "" {
			return ErrRepoNotFound
		}
	}

	// Step 1: if currently Open, close the repo (kills tmux, removes linked
	// worktrees, removes from repos.json). Skip for already-closed repos.
	if isOpen {
		if err := s.CloseRepo(ctx, repoID); err != nil {
			return fmt.Errorf("DeleteRepo CloseRepo: %w", err)
		}
	}

	// Step 2: remove the ghq-managed directory.
	if s.deps.GHQ != nil {
		if err := s.deps.GHQ.Rm(ctx, ghqPath, fullPath); err != nil {
			s.logger.Warn("DeleteRepo: ghq rm failed", "ghqPath", ghqPath, "err", err)
			// Non-fatal warning: the repo is already removed from Palmux's
			// perspective. The leftover directory is an orphan the user can
			// clean up manually.
		}
	}
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
