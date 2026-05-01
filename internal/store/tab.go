package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// MultiTabHook is implemented by providers that store the per-branch tab
// list outside the tmux window registry (Claude, post-S009). The Store
// delegates AddTab / RemoveTab to this hook so it doesn't need to know
// about each non-tmux provider's persistence layer. Providers that only
// produce tmux-backed multi tabs leave this nil.
type MultiTabHook interface {
	// CreateTab persists a fresh tab of the given provider for this
	// branch and returns it. The hook is responsible for assigning a
	// unique tab ID, deciding the user-visible name, and serialising
	// state to disk.
	CreateTab(ctx context.Context, repoID, branchID, providerType string) (domain.Tab, error)
	// DeleteTab removes a previously-created tab. No-op when the tab is
	// not owned by this hook.
	DeleteTab(ctx context.Context, repoID, branchID, tabID string) error
}

// SetMultiTabHook registers the hook used for non-tmux multi-instance
// providers. Wired from main.go after the claudeagent.Manager is built.
func (s *Store) SetMultiTabHook(h MultiTabHook) {
	s.multiTabHook = h
}

// AddTab creates a new tab of the given provider type. Only providers with
// Multiple()==true accept this — singletons return an error if a tab already
// exists.
//
// For tmux-backed providers (Bash) we ask tmux for a fresh window name.
// For non-tmux multi providers (Claude, S009) we route through the
// MultiTabHook which owns the per-branch tab id list. Both paths
// enforce Provider.Limits() Max so the user can't blow past the cap.
//
// The optional `name` is the user-friendly suffix; for now only the bash
// path honours it (the Claude hook auto-picks).
func (s *Store) AddTab(ctx context.Context, repoID, branchID, providerType, name string) (domain.Tab, error) {
	provider := s.registry.Get(providerType)
	if provider == nil {
		return domain.Tab{}, fmt.Errorf("%w: unknown provider type %q", ErrInvalidArg, providerType)
	}
	if !provider.Multiple() {
		return domain.Tab{}, fmt.Errorf("%w: %q is a singleton", ErrInvalidArg, providerType)
	}

	s.mu.RLock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.RUnlock()
		return domain.Tab{}, ErrRepoNotFound
	}
	var branch *domain.Branch
	for _, b := range repo.OpenBranches {
		if b.ID == branchID {
			branch = b
			break
		}
	}
	if branch == nil {
		s.mu.RUnlock()
		return domain.Tab{}, ErrBranchNotFound
	}
	// Enforce max instances before mutating anything.
	limits := provider.Limits(s.deps.Settings)
	if limits.Max > 0 {
		count := 0
		for _, t := range branch.TabSet.Tabs {
			if t.Type == providerType {
				count++
			}
		}
		if count >= limits.Max {
			s.mu.RUnlock()
			return domain.Tab{}, fmt.Errorf("%w: %q tabs are at the cap of %d for this branch", ErrTabLimit, providerType, limits.Max)
		}
	}
	sessionName := branch.TabSet.TmuxSession
	cwd := branch.WorktreePath
	s.mu.RUnlock()

	// Branch on provider kind. Non-tmux multi providers delegate to the
	// MultiTabHook (Claude); tmux-backed providers create a window.
	if !provider.NeedsTmuxWindow() {
		if s.multiTabHook == nil {
			return domain.Tab{}, fmt.Errorf("%w: no multi-tab hook registered for %q", ErrInvalidArg, providerType)
		}
		added, err := s.multiTabHook.CreateTab(ctx, repoID, branchID, providerType)
		if err != nil {
			return domain.Tab{}, err
		}
		s.mu.Lock()
		s.recomputeTabs(ctx, branch)
		s.mu.Unlock()
		s.hub.Publish(Event{Type: EventTabAdded, RepoID: repoID, BranchID: branchID, TabID: added.ID, Payload: added})
		return added, nil
	}

	// S009-fix-1: ensure the tmux session is alive before we ask it for
	// a fresh window name. Without this, AddTab races sync_tmux: if a
	// recovery cycle is mid-flight (or the user just closed the only
	// attached client and tmux GC'd the session) `pickNextWindowName`
	// fails with "can't find session" and the user's `+` click looks
	// like a no-op. Calling ensureSession is idempotent — if the session
	// already exists this is a single `tmux has-session` round-trip.
	if err := s.ensureBranchSession(ctx, repoID, branchID); err != nil {
		return domain.Tab{}, fmt.Errorf("ensure branch session: %w", err)
	}

	// Decide window name.
	windowName, err := s.pickNextWindowName(ctx, sessionName, providerType, name)
	if err != nil {
		return domain.Tab{}, err
	}
	if err := s.deps.Tmux.NewWindow(ctx, sessionName, tmux.NewWindowOpts{Name: windowName, Cwd: cwd}); err != nil {
		return domain.Tab{}, err
	}

	// Recompute and return the new tab.
	s.mu.Lock()
	s.recomputeTabs(ctx, branch)
	var added domain.Tab
	for _, t := range branch.TabSet.Tabs {
		if t.WindowName == windowName {
			added = t
			break
		}
	}
	s.mu.Unlock()

	s.hub.Publish(Event{Type: EventTabAdded, RepoID: repoID, BranchID: branchID, TabID: added.ID, Payload: added})
	return added, nil
}

// RemoveTab kills the underlying tmux window if any. Protected tabs (Files /
// Git, plus the lone Claude tab) are guarded by their Provider's Limits Min;
// the floor protection blocks removal of the last instance of any
// Multiple()=true type so a branch always has at least one of each.
//
// S009: post-Claude-multi the protected flag is no longer the right signal
// (Claude tabs are protected to lock the type but multiple instances are
// removable). Removal eligibility is now: tab must belong to a Multiple()
// type AND removing it must not put the count below Limits.Min.
func (s *Store) RemoveTab(ctx context.Context, repoID, branchID, tabID string) error {
	s.mu.RLock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.RUnlock()
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
		s.mu.RUnlock()
		return ErrBranchNotFound
	}
	var target domain.Tab
	for _, t := range branch.TabSet.Tabs {
		if t.ID == tabID {
			target = t
			break
		}
	}
	if target.ID == "" {
		s.mu.RUnlock()
		return ErrTabNotFound
	}
	provider := s.registry.Get(target.Type)
	if provider == nil {
		s.mu.RUnlock()
		return fmt.Errorf("%w: provider %q not registered", ErrInvalidArg, target.Type)
	}
	// Singleton: refuse outright (Files, Git).
	if !provider.Multiple() {
		s.mu.RUnlock()
		return ErrTabProtected
	}
	// Floor protection: would removing this drop the count below Min?
	limits := provider.Limits(s.deps.Settings)
	count := 0
	for _, t := range branch.TabSet.Tabs {
		if t.Type == target.Type {
			count++
		}
	}
	if limits.Min > 0 && count <= limits.Min {
		s.mu.RUnlock()
		return fmt.Errorf("%w: at least %d %q tab(s) must remain", ErrTabLimit, limits.Min, target.Type)
	}
	sessionName := branch.TabSet.TmuxSession
	s.mu.RUnlock()

	if target.WindowName != "" {
		if err := s.deps.Tmux.KillWindowByName(ctx, sessionName, target.WindowName); err != nil {
			return err
		}
	} else if s.multiTabHook != nil {
		// Non-tmux multi tab (Claude): hand off to the hook so per-tab
		// state (agent, sessions.json entries) is torn down too.
		if err := s.multiTabHook.DeleteTab(ctx, repoID, branchID, tabID); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.recomputeTabs(ctx, branch)
	s.mu.Unlock()
	s.hub.Publish(Event{Type: EventTabRemoved, RepoID: repoID, BranchID: branchID, TabID: tabID})
	return nil
}

// RenameTab renames a multi-instance tab's window. The tab ID changes
// because IDs are derived from window names — callers should re-read state.
func (s *Store) RenameTab(ctx context.Context, repoID, branchID, tabID, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("%w: empty name", ErrInvalidArg)
	}
	s.mu.RLock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.RUnlock()
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
		s.mu.RUnlock()
		return ErrBranchNotFound
	}
	var target domain.Tab
	for _, t := range branch.TabSet.Tabs {
		if t.ID == tabID {
			target = t
			break
		}
	}
	if target.ID == "" || target.WindowName == "" {
		s.mu.RUnlock()
		return ErrTabNotFound
	}
	if !target.Multiple {
		s.mu.RUnlock()
		return fmt.Errorf("%w: only multi-instance tabs can be renamed", ErrInvalidArg)
	}
	sessionName := branch.TabSet.TmuxSession
	s.mu.RUnlock()

	newWindowName := domain.WindowName(target.Type, newName)
	if err := s.deps.Tmux.RenameWindow(ctx, sessionName, target.WindowName, newWindowName); err != nil {
		return err
	}
	s.mu.Lock()
	s.recomputeTabs(ctx, branch)
	s.mu.Unlock()
	newTabID := domain.TabID(target.Type, newName)
	s.hub.Publish(Event{Type: EventTabRenamed, RepoID: repoID, BranchID: branchID, TabID: newTabID, Payload: newName})
	return nil
}

// ensureBranchSession is a thin wrapper around ensureSession that resolves
// the branch by id and re-collects window specs from every Provider before
// delegating. Returns nil if the branch is gone (the caller path will fail
// downstream and surface a 404).
//
// Used by AddTab so a freshly-created Bash window doesn't fail because the
// underlying tmux session is between sync_tmux cycles.
func (s *Store) ensureBranchSession(ctx context.Context, repoID, branchID string) error {
	s.mu.RLock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.RUnlock()
		return ErrRepoNotFound
	}
	var b *domain.Branch
	for _, br := range repo.OpenBranches {
		if br.ID == branchID {
			b = cloneBranch(br)
			break
		}
	}
	s.mu.RUnlock()
	if b == nil {
		return ErrBranchNotFound
	}
	specs, err := s.collectOpenSpecs(ctx, b, true)
	if err != nil {
		return err
	}
	return s.ensureSession(ctx, b, specs)
}

// pickNextWindowName chooses an available `palmux:{type}:{name}` for the
// given session.
func (s *Store) pickNextWindowName(ctx context.Context, session, providerType, requested string) (string, error) {
	have, err := s.deps.Tmux.ListWindows(ctx, session)
	if err != nil {
		return "", err
	}
	existing := map[string]bool{}
	for _, w := range have {
		existing[w.Name] = true
	}
	if requested != "" {
		candidate := domain.WindowName(providerType, requested)
		if existing[candidate] {
			return "", fmt.Errorf("%w: window %q already exists", ErrInvalidArg, candidate)
		}
		return candidate, nil
	}
	if providerType == "bash" {
		return domain.NextBashWindowName(existing), nil
	}
	// Generic fallback: try {type}, {type}-2, {type}-3, ...
	if !existing[domain.WindowName(providerType, providerType)] {
		return domain.WindowName(providerType, providerType), nil
	}
	for i := 2; i < 10000; i++ {
		w := domain.WindowName(providerType, fmt.Sprintf("%s-%d", providerType, i))
		if !existing[w] {
			return w, nil
		}
	}
	return "", fmt.Errorf("could not pick free window name for %s", providerType)
}
