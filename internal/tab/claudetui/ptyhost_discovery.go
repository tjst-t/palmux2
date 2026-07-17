package claudetui

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/tjst-t/palmux2/internal/tab/agenttui"
)

// This file is the claudetui-side residual of S0e8afb-1's ptyclient.go /
// discover.go move to internal/tab/agenttui (see
// docs/agenttui-ptyhost-merge-design.md, P1 — mechanical adopt). The
// Manager-agnostic scanning primitives (agenttui.ScanRunDir,
// agenttui.DiscoveredHost, agenttui.PidAlive, agenttui.SendOrphanShutdown,
// ...) moved to agenttui verbatim (package rename only) since they have zero
// coupling to claudetui's Manager/Daemon types. DiscoverAndRestore and
// (*Manager).GCOrphans stayed HERE, unmoved, because they are irreducibly
// coupled to *Manager (EnsureDaemon/EnsureStarted, and GCOrphans' own
// m.cfg.Logger/m.cfg.RuntimeResolver + reapContainerClaude) — Manager itself
// is not moving in this phase (that is a later Story's daemon.go/manager.go
// graft per the merge design doc), and Go does not allow defining new
// methods on a type from a different package. This split is expected to
// collapse back into a single file once Manager/Daemon themselves move to
// agenttui.
//
// External call sites are UNCHANGED by this split: DiscoverAndRestore is
// still called as claudetui.DiscoverAndRestore(...) (cmd/palmux/main.go),
// and GCOrphans is still called as mgr.GCOrphans(...) (internal/store's
// TuiOrphanGC interface, satisfied structurally by *Manager) — this phase
// changes no runtime behavior.

// DiscoverAndRestore implements the startup half of S3f2658-3: it scans
// mgr's run directory ([Manager.RunDir]) and, for every LIVE ptyhost found,
// re-adopts it into mgr in "attach existing" mode — EnsureDaemon followed by
// EnsureStarted, EXACTLY the lazy-attach path a first WebSocket attach would
// have taken (Story 2's launchAndAttach probes the socket and finds a
// survivor, so it attaches rather than spawning — see
// docs/no-halt-agent-design.md §5 for the accompanying screen-restore
// jiggle). Dead/unreachable entries are cleaned up (see
// [agenttui.ScanRunDir]).
//
// worktreeFn resolves (repoId, branchId) -> worktree path for the
// SessionWatcher wiring, exactly like Provider.resolveWorktree — nil or a
// resolver returning "" is tolerated (the restored Daemon simply skips
// session-ID auto-detection, same as any other worktree-less Daemon).
//
// Call this ONCE, early at boot, before the store's background loops start
// (so GCOrphans never races a startup discovery pass still adopting the
// same ptyhosts it would otherwise wrongly consider unreferenced before any
// tab list has been populated).
func DiscoverAndRestore(ctx context.Context, mgr *Manager, worktreeFn func(repoID, branchID string) string, logger *slog.Logger) (adopted, cleaned int, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	runDir := mgr.RunDir()
	live, cleaned, serr := agenttui.ScanRunDir(runDir, logger, nil)
	if serr != nil {
		return 0, cleaned, fmt.Errorf("claudetui: startup discovery: %w", serr)
	}
	for _, h := range live {
		worktree := ""
		if worktreeFn != nil {
			worktree = worktreeFn(h.RepoID, h.BranchID)
		}
		d, derr := mgr.EnsureDaemon(ctx, h.RepoID, h.BranchID, h.TabID, worktree)
		if derr != nil {
			logger.Warn("claudetui: discovery: EnsureDaemon failed",
				"repo", h.RepoID, "branch", h.BranchID, "tab", h.TabID, "err", derr)
			continue
		}
		if serr := d.EnsureStarted(ctx); serr != nil {
			logger.Warn("claudetui: discovery: attach to surviving ptyhost failed",
				"repo", h.RepoID, "branch", h.BranchID, "tab", h.TabID, "err", serr)
			continue
		}
		adopted++
		logger.Info("claudetui: discovery: re-adopted surviving ptyhost",
			"repo", h.RepoID, "branch", h.BranchID, "tab", h.TabID, "pid", h.Pid)
	}
	return adopted, cleaned, nil
}

// GCOrphans implements the orphan-GC half of S3f2658-3: piggybacked on the
// store's existing 10s scanPorts loop (internal/store/sync_worktree.go), it
// scans this Manager's run directory and SHUTDOWNs every LIVE ptyhost whose
// (repoId, branchId, tabId) isLive reports false for — tab delete / worktree
// removal / branch close, tmux-zombie-kill parity (see
// docs/no-halt-agent-design.md §3). Referenced ptyhosts (isLive == true) are
// never dialed at all ([agenttui.ScanRunDir]'s skipLive parameter, threaded
// through as isLive here), so a live Daemon's own active connection is never
// disturbed by this pass — a liveness-probe dial against an already-attached
// ptyhost would otherwise evict that connection (see ptyhost.Server.replaceConn:
// only one active connection is tolerated at a time).
//
// Socket/status file cleanup for a just-SHUTDOWN orphan is deliberately NOT
// synchronous with this call: the child has not necessarily exited yet (the
// ptyhost owns a SIGTERM→(grace)→SIGKILL escalation — see
// [Daemon.gracefulShutdownTimeout]), and the ptyhost's own teardown
// concurrently (re)writes the final status file the moment the child
// actually exits (see ptyhost.Server.waitChild) — proactively deleting here
// would race that write and could resurrect a stale file. Instead this
// self-heals on a LATER tick: once the child has genuinely exited, the
// ptyhost's own teardown removes the .sock (leaving a now-orphaned .json),
// which [agenttui.ScanRunDir] then recognizes as "no socket + status exists"
// and prunes — the exact same path a hard-killed ptyhost's leftover files
// take. Dead/unreachable entries encountered on THIS tick (from a previous
// SHUTDOWN, or any other cause) are cleaned up the same way, opportunistically.
//
// isLive MUST be a cheap, non-blocking lookup (called once per discovered
// ptyhost, every tick) — callers pass a Store method like
// `func(repoID, branchID, tabID string) bool { _, err := store.Tab(...); return err == nil }`.
// A nil isLive makes GCOrphans a no-op (defensive — callers should not wire
// GCOrphans at all in that case).
func (m *Manager) GCOrphans(ctx context.Context, isLive func(repoID, branchID, tabID string) bool) (shutdown, cleaned int, err error) {
	if isLive == nil {
		return 0, 0, nil
	}
	runDir := m.RunDir()
	// skipLive == isLive: referenced entries are excluded INSIDE ScanRunDir,
	// before any dial — see the doc comment above.
	live, cleaned, serr := agenttui.ScanRunDir(runDir, m.cfg.Logger, isLive)
	if serr != nil {
		return 0, cleaned, fmt.Errorf("claudetui: orphan gc: %w", serr)
	}
	for _, h := range live {
		// Everything in `live` here is, by construction, unreferenced
		// (isLive already filtered referenced entries out inside
		// ScanRunDir) — every entry gets shut down.
		if serr := agenttui.SendOrphanShutdown(h.SockPath, gracefulShutdownTimeout); serr != nil {
			m.cfg.Logger.Warn("claudetui: orphan gc: shutdown failed",
				"repo", h.RepoID, "branch", h.BranchID, "tab", h.TabID, "err", serr)
			continue
		}
		// S3f2658-4: unlike Daemon.teardown (tab close / branch close), this
		// SHUTDOWN never goes through a Daemon — GCOrphans dials the orphaned
		// ptyhost's socket directly (no live Daemon exists for it; see the
		// [agenttui.SendOrphanShutdown] doc comment) — so the S52fc2c-4
		// in-container reap that teardown performs must be triggered
		// explicitly here too. Without this, a tab/branch deleted while
		// palmux2 was down (the ptyhost — and any incus-wrapped
		// in-container claude it holds — outlives palmux2 per ADR-0001)
		// would leave the in-container claude running forever: nothing else
		// ever reaps it. [AC-S3f2658-4-2]
		reapContainerClaude(m.cfg.RuntimeResolver, h.RepoID, h.BranchID, gracefulShutdownTimeout, m.cfg.Logger)
		shutdown++
		m.cfg.Logger.Info("claudetui: orphan gc: shut down unreferenced ptyhost",
			"repo", h.RepoID, "branch", h.BranchID, "tab", h.TabID, "pid", h.Pid)
	}
	return shutdown, cleaned, nil
}
