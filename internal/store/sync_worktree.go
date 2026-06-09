package store

import (
	"context"
	"time"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/worktree"
)

// SyncWorktreeInterval is the cadence of worktree reconciliation. 30s is the
// spec recommendation — worktree changes from outside Palmux are rare so the
// cost-benefit favors a long interval.
const SyncWorktreeInterval = 30 * time.Second

// SyncWorktree reconciles git worktree state with the Store:
//   - new worktrees on disk are auto-registered as Open branches (via
//     OpenBranchAuto)
//   - vanished worktrees trigger a Close (tmux kill + Store removal)
//   - **S1e8d02**: an existing worktree path whose checked-out branch
//     changed in-place (= the user ran `git checkout other-branch`) is
//     observed as a *rename*, not a close+open, and only the display
//     name is updated. Tmux session, Claude agent, tab list, Drawer
//     position, and the BranchID URL all stay alive.
//
// The reconciliation key is the **worktree path**, not the branch name.
// This is the whole point of the S1e8d02 refactor: Branch name is a
// dynamic attribute of the workspace (= worktree path), not its identity.
func (s *Store) SyncWorktree(ctx context.Context) error {
	s.mu.RLock()
	repos := make([]*domain.Repository, 0, len(s.repos))
	for _, r := range s.repos {
		repos = append(repos, cloneRepo(r))
	}
	s.mu.RUnlock()

	for _, repo := range repos {
		if IsHostRepoID(repo.ID) {
			// S0c6a1b: the reserved host scope has no git worktree to
			// reconcile — skip it so we never run `git worktree list` on
			// $HOME nor close/rename the synthetic branch.
			continue
		}
		wts, err := worktree.List(ctx, repo.FullPath)
		if err != nil {
			s.logger.Warn("sync_worktree: List", "repo", repo.GHQPath, "err", err)
			continue
		}
		live := map[string]worktree.Worktree{} // worktree path -> worktree
		for _, wt := range wts {
			if wt.Branch == "" || wt.IsDetached {
				continue
			}
			live[wt.Path] = wt
		}

		// Snapshot existing branches indexed by their workspace path so we
		// can compare path-by-path. Names are recorded so we can detect
		// in-place rename without falling back to ID re-derivation.
		s.mu.RLock()
		current, ok := s.repos[repo.ID]
		type existingEntry struct {
			id   string
			name string
		}
		var existing map[string]existingEntry // worktree path -> {id, name}
		if ok {
			existing = map[string]existingEntry{}
			for _, b := range current.OpenBranches {
				existing[b.WorktreePath] = existingEntry{id: b.ID, name: b.Name}
			}
		}
		s.mu.RUnlock()
		if !ok {
			continue
		}

		// 1. Detect new worktrees (path appeared on disk that we don't track yet).
		for path, wt := range live {
			if _, found := existing[path]; found {
				continue
			}
			s.logger.Info("sync_worktree: detected new worktree",
				"branch", wt.Branch, "path", path, "workspace", path)
			// S015: auto-detected worktrees should NOT land in
			// userOpenedBranches — they came from outside Palmux.
			// Use OpenBranchAuto to keep them in `unmanaged` /
			// `subagent` until the user promotes them.
			if _, err := s.OpenBranchAuto(ctx, repo.ID, wt.Branch); err != nil {
				s.logger.Warn("sync_worktree: OpenBranchAuto",
					"branch", wt.Branch, "path", path, "err", err)
			}
		}

		// 2. Detect rename / removal of existing worktrees.
		for path, entry := range existing {
			wt, stillThere := live[path]
			if !stillThere {
				s.logger.Info("sync_worktree: detected removed worktree",
					"branch", entry.name, "path", path, "workspace", path)
				if err := s.CloseBranch(ctx, repo.ID, entry.id); err != nil {
					s.logger.Warn("sync_worktree: CloseBranch",
						"branch", entry.name, "err", err)
				}
				continue
			}
			// Path still alive. If the branch *name* changed under the
			// same path, that's an in-place `git checkout`. Rename in
			// place (no close+open).
			if wt.Branch != entry.name {
				s.logger.Info("sync_worktree: detected in-place head change",
					"workspace", path, "old", entry.name, "new", wt.Branch,
					"branchId", entry.id)
				if err := s.RenameBranch(ctx, repo.ID, entry.id, wt.Branch); err != nil {
					s.logger.Warn("sync_worktree: RenameBranch",
						"workspace", path, "old", entry.name, "new", wt.Branch, "err", err)
				}
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
	go s.runPortScan(ctx)
}

// runPortScan is the background loop that drives port detection for every
// incus-container Workspace.  It polls the RuntimeRegistry every
// portScanLoopInterval for currently-ready incus containers and, for each one,
// calls ScanPortsOnce.  The result is broadcast as a branch.portsDetected WS
// event so connected browsers can surface per-workspace service URLs in real
// time without polling.
//
// [AC-S8478ca-4-1]
const portScanLoopInterval = 10 * time.Second

func (s *Store) runPortScan(ctx context.Context) {
	ticker := time.NewTicker(portScanLoopInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanPorts(ctx)
		}
	}
}

// portScanner is the subset of the incus Runtime we need — injectable for tests.
type portScanner interface {
	ScanPortsOnce(ctx context.Context) ([]runtime.ListeningPort, error)
}

func (s *Store) scanPorts(ctx context.Context) {
	if s.deps.RuntimeRegistry == nil {
		return
	}

	// Snapshot all open (repoID, branchID) pairs under the read lock.
	type wsKey struct{ repoID, branchID string }
	var workspaces []wsKey
	s.mu.RLock()
	for _, repo := range s.repos {
		if IsHostRepoID(repo.ID) {
			continue
		}
		for _, b := range repo.OpenBranches {
			workspaces = append(workspaces, wsKey{repo.ID, b.ID})
		}
	}
	s.mu.RUnlock()

	for _, ws := range workspaces {
		rt := s.deps.RuntimeRegistry.Get(ws.repoID, ws.branchID)
		if rt == nil || rt.Kind() != runtime.KindIncusContainer {
			continue
		}
		if rt.Status().State != runtime.StateReady {
			continue
		}
		scanner, ok := rt.(portScanner)
		if !ok {
			continue
		}
		ports, err := scanner.ScanPortsOnce(ctx)
		if err != nil {
			s.logger.Warn("store.scanPorts: ScanPortsOnce failed",
				"repoID", ws.repoID, "branchID", ws.branchID, "err", err)
			continue
		}
		// Broadcast WS event — frontend can use this to show service URLs.
		s.hub.Publish(Event{
			Type:     EventBranchPortsDetected,
			RepoID:   ws.repoID,
			BranchID: ws.branchID,
			Payload: map[string]any{
				"ports": ports,
				"inst":  rt.Status().Address, // containerIP as hint
			},
		})
	}
}
