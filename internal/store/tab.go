package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// AddTab creates a new tab of the given provider type. Only providers with
// Multiple()==true accept this — singletons return an error if a tab already
// exists.
//
// The optional `name` is the user-friendly suffix; if empty the Store
// auto-picks the next available `palmux:bash:bash[-N]`.
func (s *Store) AddTab(ctx context.Context, repoID, branchID, providerType, name string) (domain.Tab, error) {
	provider := s.registry.Get(providerType)
	if provider == nil {
		return domain.Tab{}, fmt.Errorf("%w: unknown provider type %q", ErrInvalidArg, providerType)
	}
	if !provider.NeedsTmuxWindow() {
		return domain.Tab{}, fmt.Errorf("%w: %q is not terminal-backed", ErrInvalidArg, providerType)
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
	sessionName := branch.TabSet.TmuxSession
	cwd := branch.WorktreePath
	s.mu.RUnlock()

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

// RemoveTab kills the underlying tmux window if any. Protected tabs (Claude /
// Files / Git) cannot be removed — that's the user's compass for "this branch
// always has these".
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
	sessionName := branch.TabSet.TmuxSession
	s.mu.RUnlock()

	if target.Protected {
		return ErrTabProtected
	}
	if target.WindowName != "" {
		if err := s.deps.Tmux.KillWindowByName(ctx, sessionName, target.WindowName); err != nil {
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
