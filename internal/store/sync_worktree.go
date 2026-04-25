package store

import (
	"context"
	"time"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/worktree"
)

// SyncWorktreeInterval is the cadence of worktree reconciliation. 30s is the
// spec recommendation — worktree changes from outside Palmux are rare so the
// cost-benefit favors a long interval.
const SyncWorktreeInterval = 30 * time.Second

// SyncWorktree reconciles git worktree state with the Store:
//   - new worktrees on disk are auto-registered as Open branches
//   - vanished worktrees trigger a Close (tmux kill + Store removal)
func (s *Store) SyncWorktree(ctx context.Context) error {
	s.mu.RLock()
	repos := make([]*domain.Repository, 0, len(s.repos))
	for _, r := range s.repos {
		repos = append(repos, cloneRepo(r))
	}
	s.mu.RUnlock()

	for _, repo := range repos {
		wts, err := worktree.List(ctx, repo.FullPath)
		if err != nil {
			s.logger.Warn("sync_worktree: List", "repo", repo.GHQPath, "err", err)
			continue
		}
		live := map[string]worktree.Worktree{} // branch -> worktree
		for _, wt := range wts {
			if wt.Branch == "" || wt.IsDetached {
				continue
			}
			live[wt.Branch] = wt
		}

		// Detect new worktrees.
		s.mu.RLock()
		current, ok := s.repos[repo.ID]
		var existing map[string]string // branch name -> branchID
		if ok {
			existing = map[string]string{}
			for _, b := range current.OpenBranches {
				existing[b.Name] = b.ID
			}
		}
		s.mu.RUnlock()
		if !ok {
			continue
		}

		for branchName, wt := range live {
			if _, found := existing[branchName]; found {
				continue
			}
			s.logger.Info("sync_worktree: detected new worktree", "branch", branchName, "path", wt.Path)
			if _, err := s.OpenBranch(ctx, repo.ID, branchName); err != nil {
				s.logger.Warn("sync_worktree: OpenBranch", "branch", branchName, "err", err)
			}
		}

		// Detect removed worktrees.
		for branchName, branchID := range existing {
			if _, stillThere := live[branchName]; stillThere {
				continue
			}
			s.logger.Info("sync_worktree: detected removed worktree", "branch", branchName)
			if err := s.CloseBranch(ctx, repo.ID, branchID); err != nil {
				s.logger.Warn("sync_worktree: CloseBranch", "branch", branchName, "err", err)
			}
		}
	}
	return nil
}

func (s *Store) runSyncWorktree(ctx context.Context) {
	ticker := time.NewTicker(SyncWorktreeInterval)
	defer ticker.Stop()
	if err := s.SyncWorktree(ctx); err != nil {
		s.logger.Warn("SyncWorktree initial run", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.SyncWorktree(ctx); err != nil {
				s.logger.Warn("SyncWorktree", "err", err)
			}
		}
	}
}

// Run starts the background sync loops. They run until ctx is done.
func (s *Store) Run(ctx context.Context) {
	go s.runSyncTmux(ctx)
	go s.runSyncWorktree(ctx)
}
