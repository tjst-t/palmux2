package store

import (
	"context"
	"strings"
	"time"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
)

// SyncTmuxInterval is the cadence of session-level reconciliation. The 5s
// figure is the spec recommendation: short enough to feel real-time when a
// user kills a session externally, long enough to be cheap.
const SyncTmuxInterval = 5 * time.Second

// SyncTmux reconciles the tmux state with the Store:
//   - missing sessions for Open branches are recreated (claude --resume)
//   - Palmux-prefixed sessions not tracked by the Store are killed (zombies)
//   - per-connection group sessions whose conn has gone away are killed
//
// Safe to call concurrently with API mutations; uses the same mutex.
func (s *Store) SyncTmux(ctx context.Context) error {
	sessions, err := s.deps.Tmux.ListSessions(ctx)
	if err != nil {
		return err
	}
	live := make(map[string]bool, len(sessions))
	for _, sess := range sessions {
		live[sess.Name] = true
	}

	// 1. Recreate missing sessions and prune zombies.
	type recovery struct {
		repoID, branchID string
		branch           *domain.Branch
	}
	var toRecover []recovery

	s.mu.RLock()
	tracked := map[string]bool{} // session names we expect
	for _, repo := range s.repos {
		for _, b := range repo.OpenBranches {
			tracked[b.TabSet.TmuxSession] = true
			if !live[b.TabSet.TmuxSession] {
				toRecover = append(toRecover, recovery{repoID: repo.ID, branchID: b.ID, branch: cloneBranch(b)})
			}
		}
	}
	connsAlive := make(map[string]bool, len(s.conns))
	for _, c := range s.conns {
		connsAlive[c.ID] = true
	}
	s.mu.RUnlock()

	// 2. Kill zombie Palmux sessions (and group sessions with missing conn).
	//
	// Only kill sessions that match Palmux's strict naming format
	// (_palmux_{repoId}_{branchId}). Other `_palmux_*` sessions that don't
	// parse — for example created by unrelated tools — are left alone so
	// Palmux never breaks software it doesn't own.
	for _, sess := range sessions {
		if !domain.IsPalmuxSession(sess.Name) {
			continue
		}
		// Group session: `_palmux_..._branch__grp_{connId}`. Only manage
		// groups whose base session parses as one of ours.
		if idx := strings.Index(sess.Name, domain.SessionGroupSeparator); idx > 0 {
			base := sess.Name[:idx]
			if _, _, ok := domain.ParseSessionName(base); !ok {
				continue
			}
			connID := sess.Name[idx+len(domain.SessionGroupSeparator):]
			if !connsAlive[connID] {
				_ = s.deps.Tmux.KillSession(ctx, sess.Name)
			}
			continue
		}
		if _, _, ok := domain.ParseSessionName(sess.Name); !ok {
			// Doesn't match the format Palmux generates — leave alone.
			continue
		}
		if !tracked[sess.Name] {
			s.logger.Info("sync_tmux: killing zombie session", "session", sess.Name)
			_ = s.deps.Tmux.KillSession(ctx, sess.Name)
		}
	}

	// 3. Recreate missing sessions for tracked branches.
	for _, r := range toRecover {
		s.logger.Info("sync_tmux: recovering session", "branch", r.branch.Name)
		windows, err := s.collectOpenSpecs(ctx, r.branch, true)
		if err != nil {
			s.logger.Warn("sync_tmux: collect specs", "branch", r.branch.Name, "err", err)
			continue
		}
		if err := s.ensureSession(ctx, r.branch, windows); err != nil {
			s.logger.Warn("sync_tmux: ensureSession", "branch", r.branch.Name, "err", err)
			continue
		}
		// Fold tabset back into the live branch.
		s.mu.Lock()
		if repo, ok := s.repos[r.repoID]; ok {
			for _, b := range repo.OpenBranches {
				if b.ID == r.branchID {
					s.recomputeTabs(ctx, b)
					break
				}
			}
		}
		s.mu.Unlock()
	}
	return nil
}

// runSyncTmux is the goroutine driving SyncTmux on an interval.
func (s *Store) runSyncTmux(ctx context.Context) {
	ticker := time.NewTicker(SyncTmuxInterval)
	defer ticker.Stop()
	// Run once immediately.
	if err := s.SyncTmux(ctx); err != nil {
		s.logger.Warn("SyncTmux initial run", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.SyncTmux(ctx); err != nil {
				s.logger.Warn("SyncTmux", "err", err)
			}
		}
	}
}

// avoid unused import in some build configurations
var _ = tab.Provider(nil)
