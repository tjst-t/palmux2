package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tmux"
	"github.com/tjst-t/palmux2/internal/worktree"
)

// OpenBranch creates the worktree (via gwq if necessary), the tmux session,
// runs every Provider's OnBranchOpen, and registers the branch in the Store.
func (s *Store) OpenBranch(ctx context.Context, repoID, branchName string) (*domain.Branch, error) {
	branchName = strings.TrimSpace(branchName)
	if branchName == "" {
		return nil, fmt.Errorf("%w: branchName empty", ErrInvalidArg)
	}
	repoSnapshot, err := s.Repo(repoID)
	if err != nil {
		return nil, err
	}
	repoFullPath := repoSnapshot.FullPath

	// 1. Ensure a worktree exists for this branch.
	wt, err := s.ensureWorktree(ctx, repoFullPath, branchName)
	if err != nil {
		return nil, err
	}

	// 2. Build the Branch entity (does not yet touch tmux).
	branchID := domain.BranchSlugID(repoFullPath, wt.Branch)
	sessionName := domain.SessionName(repoID, branchID)

	branch := &domain.Branch{
		ID:           branchID,
		Name:         wt.Branch,
		WorktreePath: wt.Path,
		RepoID:       repoID,
		IsPrimary:    wt.IsPrimary,
		LastActivity: time.Now(),
		TabSet:       domain.TabSet{TmuxSession: sessionName},
	}

	// 3. Run each Provider's OnBranchOpen to gather windows + tab metadata.
	specs, err := s.collectOpenSpecs(ctx, branch, false)
	if err != nil {
		return nil, err
	}

	// 4. Bring up the tmux session with these windows. Idempotent: if the
	//    session already exists (Palmux restart, manual `tmux attach`),
	//    leave it alone.
	if err := s.ensureSession(ctx, branch, specs); err != nil {
		return nil, fmt.Errorf("ensureSession: %w", err)
	}

	// 5. Register in Store + publish event.
	s.mu.Lock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.Unlock()
		return nil, ErrRepoNotFound
	}
	for _, existing := range repo.OpenBranches {
		if existing.ID == branchID {
			s.recomputeTabs(ctx, existing)
			snap := cloneBranch(existing)
			s.mu.Unlock()
			return snap, nil
		}
	}
	repo.OpenBranches = append(repo.OpenBranches, branch)
	sortBranches(repo.OpenBranches)
	s.recomputeTabs(ctx, branch)
	snap := cloneBranch(branch)
	s.mu.Unlock()

	s.hub.Publish(Event{Type: EventBranchOpened, RepoID: repoID, BranchID: branchID, Payload: snap})
	return snap, nil
}

// CloseBranch tears down a branch: kill tmux, gwq remove (unless primary),
// drop from Store.
func (s *Store) CloseBranch(ctx context.Context, repoID, branchID string) error {
	s.mu.Lock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.Unlock()
		return ErrRepoNotFound
	}
	idx := -1
	for i, b := range repo.OpenBranches {
		if b.ID == branchID {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return ErrBranchNotFound
	}
	branch := repo.OpenBranches[idx]
	repo.OpenBranches = append(repo.OpenBranches[:idx], repo.OpenBranches[idx+1:]...)
	repoFullPath := repo.FullPath
	s.mu.Unlock()

	// Kill tmux first so client connections error out cleanly.
	if err := s.deps.Tmux.KillSession(ctx, branch.TabSet.TmuxSession); err != nil {
		s.logger.Warn("CloseBranch: tmux kill", "session", branch.TabSet.TmuxSession, "err", err)
	}
	if !branch.IsPrimary && s.deps.Gwq != nil {
		if err := s.deps.Gwq.Remove(ctx, repoFullPath, branch.Name); err != nil {
			s.logger.Warn("CloseBranch: gwq remove", "branch", branch.Name, "err", err)
		}
	}
	// Run OnBranchClose hooks.
	for _, p := range s.registry.Providers() {
		if err := p.OnBranchClose(ctx, tab.CloseParams{Branch: branch}); err != nil {
			s.logger.Warn("OnBranchClose error", "provider", p.Type(), "err", err)
		}
	}
	s.hub.Publish(Event{Type: EventBranchClosed, RepoID: repoID, BranchID: branchID})
	return nil
}

// ensureWorktree returns the worktree for branchName, creating one via gwq if
// necessary. The Worktree's Branch field reflects what git records (which may
// differ slightly from the requested branchName e.g. for renames).
func (s *Store) ensureWorktree(ctx context.Context, repoFullPath, branchName string) (worktree.Worktree, error) {
	wts, err := worktree.List(ctx, repoFullPath)
	if err != nil {
		return worktree.Worktree{}, fmt.Errorf("worktree.List: %w", err)
	}
	for _, wt := range wts {
		if wt.Branch == branchName {
			return wt, nil
		}
	}
	// Need to create one via gwq. Determine whether the branch already
	// exists (use -b only for new branches).
	if s.deps.Gwq == nil {
		return worktree.Worktree{}, errors.New("gwq client not configured")
	}
	branches, err := worktree.ListAllBranches(ctx, repoFullPath)
	if err != nil {
		return worktree.Worktree{}, fmt.Errorf("ListAllBranches: %w", err)
	}
	exists := false
	for _, b := range branches {
		if b.Name == branchName {
			exists = true
			break
		}
	}
	newBranch := !exists
	if err := s.deps.Gwq.Add(ctx, repoFullPath, branchName, newBranch); err != nil {
		return worktree.Worktree{}, err
	}
	wts, err = worktree.List(ctx, repoFullPath)
	if err != nil {
		return worktree.Worktree{}, err
	}
	for _, wt := range wts {
		if wt.Branch == branchName {
			return wt, nil
		}
	}
	return worktree.Worktree{}, fmt.Errorf("worktree for %q not found after gwq add", branchName)
}

// collectOpenSpecs queries every Provider and merges their declared windows
// + verifies they didn't try to register duplicate tabs.
func (s *Store) collectOpenSpecs(ctx context.Context, branch *domain.Branch, resume bool) ([]tab.WindowSpec, error) {
	var windows []tab.WindowSpec
	for _, p := range s.registry.Providers() {
		res, err := p.OnBranchOpen(ctx, tab.OpenParams{Branch: branch, Resume: resume})
		if err != nil {
			return nil, fmt.Errorf("OnBranchOpen %s: %w", p.Type(), err)
		}
		windows = append(windows, res.Windows...)
	}
	return windows, nil
}

// ensureSession creates the tmux session (with the first window inline) and
// then adds the rest. Idempotent — if the session already exists, only
// missing windows are created.
func (s *Store) ensureSession(ctx context.Context, branch *domain.Branch, windows []tab.WindowSpec) error {
	cwd := branch.WorktreePath
	exists, err := s.deps.Tmux.HasSession(ctx, branch.TabSet.TmuxSession)
	if err != nil {
		return err
	}
	if !exists {
		if len(windows) == 0 {
			// No terminal-backed providers (e.g. only Files/Git). Create a
			// minimal placeholder window so the session is valid.
			err = s.deps.Tmux.NewSession(ctx, tmux.NewSessionOpts{
				Name:       branch.TabSet.TmuxSession,
				WindowName: domain.WindowName("placeholder", "placeholder"),
				Cwd:        cwd,
			})
			if err != nil {
				return err
			}
			return nil
		}
		first := windows[0]
		err = s.deps.Tmux.NewSession(ctx, tmux.NewSessionOpts{
			Name:       branch.TabSet.TmuxSession,
			WindowName: first.Name,
			Cwd:        firstNonEmpty(first.Cwd, cwd),
			Command:    first.Command,
			Env:        first.Env,
		})
		if err != nil {
			return err
		}
		windows = windows[1:]
	}
	// Get current windows once so we don't add duplicates.
	have, err := s.deps.Tmux.ListWindows(ctx, branch.TabSet.TmuxSession)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, w := range have {
		existing[w.Name] = true
	}
	for _, w := range windows {
		if existing[w.Name] {
			continue
		}
		err := s.deps.Tmux.NewWindow(ctx, branch.TabSet.TmuxSession, tmux.NewWindowOpts{
			Name:    w.Name,
			Cwd:     firstNonEmpty(w.Cwd, cwd),
			Command: w.Command,
			Env:     w.Env,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
