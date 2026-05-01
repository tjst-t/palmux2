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
	// S009-fix-2: snapshot the set of conn IDs THIS process has ever
	// issued. The zombie-kill pass below uses it to leave group sessions
	// owned by other palmux instances alone (e.g. host + dev side-by-
	// side, both pointed at the same tmux server). Pre-fix, instance B
	// happily killed instance A's `__grp_xxx` because A's conn IDs were
	// never in B's `connsAlive` map — exactly the user-reported 3-second
	// WS reconnect loop on Bash tabs.
	knownConns := make(map[string]bool, len(s.knownConnIDs))
	for id := range s.knownConnIDs {
		knownConns[id] = true
	}
	// S009-fix-4: same idea, applied to BASE sessions. Pre-fix, a stale
	// or empty-repos.json palmux instance C with the default _palmux_
	// prefix would walk every `_palmux_*` session, find none in its
	// (empty) tracked set, and kill them all on every 5 s sync cycle —
	// even sessions belonging to a healthy peer instance D. With the
	// filter, C only kills sessions it created/recovered itself, so D's
	// sessions survive even if D and C share the prefix. Symmetric to
	// the knownConns check on group sessions above.
	knownBases := make(map[string]bool, len(s.knownBaseSessions))
	for n := range s.knownBaseSessions {
		knownBases[n] = true
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
			// S009-fix-2: only kill groups whose conn ID THIS process
			// has previously issued. An unknown conn ID means the
			// group belongs to another palmux instance sharing the
			// same tmux server (host + dev co-located) — leave it
			// alone or we'll trample its WS clients.
			if !knownConns[connID] {
				continue
			}
			if !connsAlive[connID] {
				_ = s.deps.Tmux.KillSession(ctx, sess.Name)
			}
			continue
		}
		if _, _, ok := domain.ParseSessionName(sess.Name); !ok {
			// Doesn't match the format Palmux generates — leave alone.
			continue
		}
		// S009-fix-4: only kill sessions THIS process previously
		// created/recovered. Foreign `_palmux_*` sessions (a peer
		// palmux instance with a stale repos.json or that simply runs
		// without the matching repo) are left alone — otherwise C's
		// sync_tmux would erase D's sessions every 5 s and the user
		// observes Bash WS oscillating into "Reconnecting…".
		if !knownBases[sess.Name] {
			continue
		}
		if !tracked[sess.Name] {
			s.logger.Info("sync_tmux: killing zombie session", "session", sess.Name)
			_ = s.deps.Tmux.KillSession(ctx, sess.Name)
			s.mu.Lock()
			delete(s.knownBaseSessions, sess.Name)
			s.mu.Unlock()
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
		// S009-fix-2: enrich the spec list with every additional
		// multi-instance window (e.g. `palmux:bash:bash-2`,
		// `palmux:bash:bash-3`) that the in-memory tab list already
		// knows about. Without this, `ensureSession` recreating a
		// killed-and-recovered session would only reinstate the
		// canonical `palmux:bash:bash` and silently lose every other
		// Bash tab — exactly what the user reported as "the new Bash
		// tab is gone after a few seconds / Reconnecting forever". The
		// canonical Bash window is already in `windows` from the
		// provider, so we de-duplicate by name.
		windows = s.enrichRecoverySpecs(r.branch, windows)
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

// enrichRecoverySpecs adds back any extra multi-instance, tmux-backed
// windows that the in-memory tab list knows about but the registered
// Provider's OnBranchOpen wouldn't include. The Bash provider, for
// example, only ever returns `palmux:bash:bash`; a recovery without this
// step would forget every `bash-2`, `bash-3` the user added since the
// session came up.
//
// IMPORTANT: this re-reads `s.repos` under the read lock instead of
// relying on the snapshot the recovery loop captured earlier. Without
// that, a RemoveTab that lands between toRecover construction and
// ensureSession execution would have its deletion silently undone — we
// would faithfully recreate a window the user just removed. (The pre-
// fix S009-fix-2 bug: deleting `bash:bash-2`, then sync_tmux's mid-
// flight cycle would resurrect it.)
//
// Singleton (Files/Git etc.) and non-tmux multi providers (Claude post-
// S009) are skipped — they own their own state via OnBranchOpen / the
// MultiTabHook.
func (s *Store) enrichRecoverySpecs(branch *domain.Branch, base []tab.WindowSpec) []tab.WindowSpec {
	if branch == nil {
		return base
	}
	// Re-read the live branch state. If the branch was closed between
	// the recovery snapshot and now, fall back to whatever specs the
	// providers gave us — recreating the canonical Bash is harmless and
	// the next recompute will bring everything in line.
	s.mu.RLock()
	var live *domain.Branch
	if repo, ok := s.repos[branch.RepoID]; ok {
		for _, b := range repo.OpenBranches {
			if b.ID == branch.ID {
				live = cloneBranch(b)
				break
			}
		}
	}
	s.mu.RUnlock()
	if live == nil || len(live.TabSet.Tabs) == 0 {
		return base
	}
	have := map[string]bool{}
	for _, w := range base {
		have[w.Name] = true
	}
	cwd := live.WorktreePath
	out := append([]tab.WindowSpec(nil), base...)
	for _, t := range live.TabSet.Tabs {
		if t.WindowName == "" || !t.Multiple {
			continue
		}
		provider := s.registry.Get(t.Type)
		if provider == nil || !provider.NeedsTmuxWindow() {
			continue
		}
		if have[t.WindowName] {
			continue
		}
		out = append(out, tab.WindowSpec{Name: t.WindowName, Cwd: cwd})
		have[t.WindowName] = true
	}
	return out
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
