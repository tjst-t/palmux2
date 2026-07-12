package store

import (
	"context"
	"sync"
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

// TuiOrphanGC (S3f2658-3) is implemented by claudetui.Manager and wired via
// [Store.SetTuiOrphanGC] so the store's existing 10s scan loop can reap
// `palmux ptyhost` processes whose tab no longer exists — tab delete /
// worktree removal / branch close, the same tmux-zombie-kill parity the
// sync_tmux loop already provides for tmux sessions (see
// docs/no-halt-agent-design.md §3). A narrow interface (not a direct
// *claudetui.Manager field) so store does not import internal/tab/claudetui
// — main.go, the wiring layer, adapts the two.
type TuiOrphanGC interface {
	// GCOrphans scans for live ptyhosts and SHUTDOWNs every one whose
	// (repoId, branchId, tabId) isLive reports false for. Returns counts for
	// logging; a non-nil error means the scan itself failed (e.g. an
	// unreadable run directory), not that some entries were left alone.
	GCOrphans(ctx context.Context, isLive func(repoID, branchID, tabID string) bool) (shutdown, cleaned int, err error)
}

// SetTuiOrphanGC registers the orphan reaper used by [Store.gcTuiOrphans].
// Wired from main.go after the claudetui.Manager is built, mirroring
// [Store.SetMultiTabHook]. Nil (the default, and every existing test that
// doesn't call this) makes gcTuiOrphans a no-op.
func (s *Store) SetTuiOrphanGC(gc TuiOrphanGC) {
	s.tuiGC = gc
}

// ArmDiscoveryBarrier installs the ptyhost orphan-GC discovery barrier and
// returns a func that releases it (idempotent). It MUST be called BEFORE
// [Store.Run] starts the scan loop. Until the returned func is called,
// [Store.gcTuiOrphans] and [Store.gcAgentOrphans] skip their orphan-SHUTDOWN
// passes entirely.
//
// Sfeed64-1: startup DiscoverAndRestore now runs in the BACKGROUND so a slow
// or wedged reattach can never delay ListenAndServe (see cmd/palmux/main.go's
// runDiscoveryAsync). That removed the old guarantee — from the previous
// SYNCHRONOUS discovery calls — that discovery fully completed before Run
// started the 10s scan loop that drives the orphan-GC passes. Both
// DiscoverAndRestore and the two GC passes dial the SAME on-disk ptyhosts
// (claude-tui and claude-agent share one run dir / seed space — see
// [Store.gcAgentOrphans]'s GUARD-RAIL, and both managers resolve to
// ptyhost.RunDir(prefix) in production). A GC pass that dials/SHUTDOWNs an
// orphan socket while discovery is still mid-adoption of the SAME socket
// races: the ptyhost tolerates only one connection at a time
// (ptyhost.Server.replaceConn), and restored Daemons have ResumeOnDeath=true,
// so a clean orphan SHUTDOWN can flap into a respawn/re-kill. This barrier
// restores the "discovery before GC" ordering WITHOUT re-blocking serve — GC
// is merely deferred by at most the first (background) discovery pass. A
// discovery that never completes keeps GC deferred, which is the safe
// direction: orphan GC is a cleanup nicety, not correctness-critical, and a
// wedged discovery must never be raced (the lazy first-WS-attach path still
// re-adopts surviving ptyhosts on demand regardless).
//
// Both GC passes gate on this SINGLE signal (not one-per-manager): because
// the two managers share the run dir/seed space, each GC pass can touch a
// socket the OTHER manager's discovery is still adopting, so neither may run
// until ALL discovery is done. main.go correspondingly runs claude-tui then
// claude-agent discovery SEQUENTIALLY in one goroutine and releases this
// barrier only once BOTH have finished.
func (s *Store) ArmDiscoveryBarrier() (release func()) {
	ch := make(chan struct{})
	s.discoveryDone = ch
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// discoveryGateOpen reports whether the orphan-GC discovery barrier (if any)
// has been released. A nil channel means no barrier was armed (the default) —
// treated as open so every existing caller/test GCs immediately. See
// [Store.ArmDiscoveryBarrier].
func (s *Store) discoveryGateOpen() bool {
	ch := s.discoveryDone
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// gcTuiOrphans piggybacks the SAME 10s tick as scanPorts (see runPortScan) —
// deliberately not a new ticker/loop (the design doc explicitly calls for
// riding the existing sync loop). No-op when SetTuiOrphanGC was never called.
func (s *Store) gcTuiOrphans(ctx context.Context) {
	if s.tuiGC == nil {
		return
	}
	// Sfeed64-1: defer the whole orphan-GC pass (which dials/SHUTDOWNs orphan
	// sockets) until startup discovery completes — see ArmDiscoveryBarrier.
	// Deferring the incidental dead-socket pruning too is harmless (a later
	// tick cleans it).
	if !s.discoveryGateOpen() {
		return
	}
	shutdown, cleaned, err := s.tuiGC.GCOrphans(ctx, s.isTuiTabLive)
	if err != nil {
		s.logger.Warn("store.gcTuiOrphans: reconcile failed", "err", err)
		return
	}
	if shutdown > 0 || cleaned > 0 {
		s.logger.Info("store.gcTuiOrphans: reconciled", "shutdown", shutdown, "cleanedStale", cleaned)
	}
}

// isTuiTabLive reports whether (repoID, branchID, tabID) is a currently
// known tab — the "still referenced" test [Store.gcTuiOrphans] passes to the
// claudetui.Manager's GCOrphans.
func (s *Store) isTuiTabLive(repoID, branchID, tabID string) bool {
	_, err := s.Tab(repoID, branchID, tabID)
	return err == nil
}

// AgentOrphanGC (S64c835-3) is implemented by claudeagent.Manager and wired
// via [Store.SetAgentOrphanGC] so the store's existing 10s scan loop can
// reap the Claude AGENT tab's pipe-mode ptyhosts whose tab no longer
// exists — the same tmux-zombie-kill parity [TuiOrphanGC] already provides
// for claude-tui ptyhosts (S3f2658-3). A narrow interface (not a direct
// *claudeagent.Manager field) so store does not import
// internal/tab/claudeagent — main.go, the wiring layer, adapts the two.
type AgentOrphanGC interface {
	// GCOrphans scans for live pipe-mode ptyhosts and SHUTDOWNs every one
	// whose (repoId, branchId, tabId) isLive reports false for. Returns
	// counts for logging; a non-nil error means the scan itself failed
	// (e.g. an unreadable run directory), not that some entries were left
	// alone.
	GCOrphans(ctx context.Context, isLive func(repoID, branchID, tabID string) bool) (shutdown, cleaned int, err error)
}

// SetAgentOrphanGC registers the orphan reaper used by
// [Store.gcAgentOrphans]. Wired from main.go after the claudeagent.Manager
// is built, mirroring [Store.SetTuiOrphanGC]. Nil (the default, and every
// existing test that doesn't call this) makes gcAgentOrphans a no-op.
func (s *Store) SetAgentOrphanGC(gc AgentOrphanGC) {
	s.agentGC = gc
}

// gcAgentOrphans piggybacks the SAME 10s tick as scanPorts/gcTuiOrphans (see
// runPortScan) — deliberately not a new ticker/loop. No-op when
// SetAgentOrphanGC was never called. isTuiTabLive is reused as the isLive
// check: it is not actually claude-tui-specific — it is a plain
// store.Tab(repoID, branchID, tabID) existence lookup, and a Claude AGENT
// tab's id ("claude:claude", "claude:claude-2", ...) lives in the exact
// same per-branch TabSet as every other tab type, so the same lookup is
// correct here too.
//
// GUARD-RAIL (do not remove — S64c835-3 review): claude-tui and claude-agent
// share ONE ptyhost run dir / seed space by design (both compute it from
// ptyhost.RunDir(instancePrefix) with the same empty production prefix, and
// the single visible "claude" tab is one shared (repoID, branchID, tabID)
// seed regardless of mode). gcTuiOrphans and gcAgentOrphans therefore both
// scan the SAME on-disk ptyhosts. This is safe ONLY because both pass the
// SAME mode-agnostic isLive (isTuiTabLive = plain tab existence). A future
// mode-specific isLive (e.g. one that also asserts Tab.Type == "claude" vs
// "claude-tui") would make each reaper stop protecting the OTHER mode's
// referenced ptyhost — reintroducing the exact S3f2658-3-class false-positive
// SHUTDOWN that kills a live, referenced claude. If isLive is ever
// specialised, cross-mode protection MUST be preserved.
func (s *Store) gcAgentOrphans(ctx context.Context) {
	if s.agentGC == nil {
		return
	}
	// Sfeed64-1: gated on the SAME discovery barrier as gcTuiOrphans — the two
	// managers share one run dir/seed space, so this agent GC pass can dial a
	// socket claude-TUI discovery is still adopting (and vice-versa). Defer
	// until ALL startup discovery is done. See ArmDiscoveryBarrier.
	if !s.discoveryGateOpen() {
		return
	}
	shutdown, cleaned, err := s.agentGC.GCOrphans(ctx, s.isTuiTabLive)
	if err != nil {
		s.logger.Warn("store.gcAgentOrphans: reconcile failed", "err", err)
		return
	}
	if shutdown > 0 || cleaned > 0 {
		s.logger.Info("store.gcAgentOrphans: reconciled", "shutdown", shutdown, "cleanedStale", cleaned)
	}
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
			// S3f2658-3: piggyback the same 10s tick for claude-tui ptyhost
			// orphan GC — independent of RuntimeRegistry (unlike scanPorts,
			// which early-returns without one), since ptyhost orphan
			// reconciliation applies regardless of host vs incus runtime.
			s.gcTuiOrphans(ctx)
			// S64c835-3: same idea, for the Claude AGENT tab's pipe-mode
			// ptyhosts (claudetui/claudeagent parity).
			s.gcAgentOrphans(ctx)
		}
	}
}

// portScanner is the subset of the incus Runtime we need — injectable for tests.
type portScanner interface {
	ScanPortsOnce(ctx context.Context) ([]runtime.ListeningPort, error)
}

// portViewer is the subset of the incus Runtime that exposes the user-facing
// Ports view. It is PortViewProvider (see ports.go) — the scan-loop event MUST
// carry publicDomainConfigured / hostIP (built by PortsChangedPayload) so the
// FE, which replaces (not merges) the branchPorts slice on every event, does
// not flip out of host-port mode (and drop the unauth warning) every scan.
// (See8bd4-3 + S4c591a)
type portViewer = PortViewProvider

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

	// S52fc2c-7: per-cycle alias fingerprint cache. All workspaces using the
	// same image alias share a single `incus image list` resolution.
	// [AC-S52fc2c-7-1]
	fpCache := &runtime.AliasFingerprintCache{}

	// Sd44947: reconcile the host-wide palmux-shared profile once per cycle, but
	// only when at least one incus container is actually running — this avoids
	// spawning `incus` on pure-host deployments while still self-healing profile
	// drift (profile-as-mold). Guarded so it runs at most once per scan tick.
	sharedReconciled := false

	for _, ws := range workspaces {
		rt := s.deps.RuntimeRegistry.Get(ws.repoID, ws.branchID)
		if rt == nil || rt.Kind() != runtime.KindIncusContainer {
			continue
		}
		if rt.Status().State != runtime.StateReady {
			continue
		}
		// [AC-Sd44947-1-2] self-heal the shared profile on the same cadence.
		if !sharedReconciled {
			if rec, ok := s.deps.RuntimeRegistry.(runtime.SharedProfileReconciler); ok {
				if err := rec.ReconcileShared(ctx); err != nil {
					s.logger.Warn("store.scanPorts: reconcile palmux-shared profile failed", "err", err)
				}
			}
			sharedReconciled = true
		}
		// S7364e3: image-drift check on the same cadence. Compares the
		// container's base image against the current palmux-ws alias fingerprint
		// and publishes a drift event only on transitions. Independent of the
		// port scan below (a non-port-scanner runtime could still drift).
		if _, ok := rt.(runtime.ImageDriftChecker); ok {
			var stale bool
			var derr error
			// S52fc2c-7: use the per-cycle cache if available (incusRuntime always
			// implements CachedImageDriftChecker); fall back to the plain interface
			// for any future runtime that implements only ImageDriftChecker.
			if cdc, ok2 := rt.(runtime.CachedImageDriftChecker); ok2 {
				stale, derr = cdc.IsImageStaleWithCache(ctx, fpCache)
			} else {
				stale, derr = rt.(runtime.ImageDriftChecker).IsImageStale(ctx)
			}
			if derr != nil {
				s.logger.Warn("store.scanPorts: image drift check failed",
					"repoID", ws.repoID, "branchID", ws.branchID, "err", derr)
			} else if s.setDriftCached(ws.repoID, ws.branchID, stale) {
				s.hub.Publish(Event{
					Type:     EventBranchRuntimeDrift,
					RepoID:   ws.repoID,
					BranchID: ws.branchID,
					Payload:  map[string]any{"stale": stale},
				})
			}
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
		// See8bd4-3: also broadcast the enriched Ports view (with exposure
		// state) for the Ports tab.
		if pv, ok := rt.(portViewer); ok {
			s.hub.Publish(Event{
				Type:     EventBranchPortsChanged,
				RepoID:   ws.repoID,
				BranchID: ws.branchID,
				Payload:  PortsChangedPayload(string(rt.Kind()), pv),
			})
		}
	}
}
