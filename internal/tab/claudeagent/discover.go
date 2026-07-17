package claudeagent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
	"github.com/tjst-t/palmux2/internal/runtime"
)

// This file implements S862203-3's startup discovery (DiscoverAndRestore)
// and S64c835-3's orphan-GC (GCOrphans): both are near-verbatim mirrors of
// internal/tab/agenttui/discover.go — same on-disk scan, same "explicit
// RepoID/BranchID/TabID status-file fields, never a parsed Seed" identity
// discipline (a repoId/branchId containing the literal substring "__"
// would otherwise mis-attribute a discovered ptyhost — a data-loss bug,
// not just a cosmetic one).
//
// DiscoverAndRestore re-adopts pipe-mode ptyhosts that survived a PRIOR
// palmux2 lifetime, so a restart's very first REST/WS response already
// shows a live, resumable Agent instead of waiting for a lazy first user
// message. GCOrphans piggybacks the store's existing 10s scanPorts loop
// and SHUTDOWNs every LIVE pipe-mode ptyhost whose (repoId, branchId,
// tabId) the caller's isLive callback does not recognize — the same
// tmux-zombie-kill parity claudetui's GCOrphans provides. Without this, a
// branch/repo closed while palmux2 is DOWN would leave its agent
// pipe-mode ptyhost un-reaped until the branch happened to be reopened
// (S64c835-3; see decisions.json — this closes the backlog gap noted at
// S862203-3's done time).
//
// Sfeed64-3: this Manager's run directory is SHARED on disk with
// agenttui-scanned, claudetui-owned Manager — both walk the
// SAME (*.sock, *.json) seed space (a ptyhost's socket path is derived
// purely from (repoId, branchId, tabId), independent of which package
// spawned it). Before this fix, scanAgentRunDir treated every
// identity-bearing entry as "mine", so a real dogfood restart showed both
// managers dialing/adopting each OTHER's ptyhosts — and since a ptyhost
// socket tolerates only ONE live connection at a time
// (ptyhost.Server.replaceConn evicts the previously-active connection the
// instant a new one dials in), the two managers evicted each other in a
// loop. The fix: [ptyhost.StatusFile.Mode] is the explicit, authoritative
// ownership marker (written directly from the spawning Config) —
// scanAgentRunDir checks it immediately after reading the status file's
// identity, BEFORE any dial, and skips (leaves COMPLETELY untouched) any
// entry that is not [ptyhost.ModePipe]. claude-tui's scanRunDir applies the
// symmetric filter for [ptyhost.ModePTY]/empty.

// discoveredAgentHost is one LIVE pipe-mode ptyhost found by
// scanAgentRunDir: its identity (read from the status file's explicit
// RepoID/BranchID/TabID fields) plus the paths a caller needs to re-adopt
// it.
type discoveredAgentHost struct {
	RepoID, BranchID, TabID string
	SockPath, StatusPath    string
	Pid                     int
}

// agentPidAlive reports whether pid refers to a currently-running process,
// via the standard Unix "signal 0" existence probe.
func agentPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// removeAgentStaleFiles best-effort removes a (possibly partial)
// socket/status pair. Errors are intentionally swallowed.
func removeAgentStaleFiles(sockPath, statusPath string) {
	if sockPath != "" {
		_ = os.Remove(sockPath)
	}
	if statusPath != "" {
		_ = os.Remove(statusPath)
	}
}

// agentScanDialTimeout bounds the liveness-probe dial+HELLO used by
// scanAgentRunDir.
const agentScanDialTimeout = 2 * time.Second

// skipLive, when non-nil, is consulted with the (repoId, branchId, tabId)
// parsed from EACH entry's status file BEFORE any liveness probe (pid
// check, dial, HELLO) is performed. An entry it reports true for is left
// COMPLETELY untouched — not dialed, not counted as live, not counted as
// cleaned. This mirrors agenttui's ScanRunDir skipLive parameter exactly
// and for the same reason: a pipe-mode ptyhost socket only tolerates ONE
// active connection at a time (ptyhost.Server.replaceConn closes whatever
// connection was previously active the instant a new one arrives), so a
// liveness-probe dial against an entry this SAME process already has a
// live Agent/Client attached to would silently evict that connection out
// from under it. GCOrphans passes its isLive callback here for exactly
// this reason; DiscoverAndRestore passes nil (at startup nothing has been
// adopted into an Agent yet).
//
// scanAgentRunDir enumerates runDir and classifies every (*.sock, *.json)
// pair found, exactly like agenttui's ScanRunDir: unidentifiable/stale
// debris is removed, everything else LIVE is returned.
func scanAgentRunDir(runDir string, logger *slog.Logger, skipLive func(repoID, branchID, tabID string) bool) (live []discoveredAgentHost, cleaned int, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	entries, rerr := os.ReadDir(runDir)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("claudeagent: scan ptyhost run dir %s: %w", runDir, rerr)
	}

	type pair struct{ sock, status bool }
	bases := map[string]pair{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".sock"):
			b := strings.TrimSuffix(name, ".sock")
			p := bases[b]
			p.sock = true
			bases[b] = p
		case strings.HasSuffix(name, ".json"):
			b := strings.TrimSuffix(name, ".json")
			p := bases[b]
			p.status = true
			bases[b] = p
		}
	}

	for base, p := range bases {
		sockPath := filepath.Join(runDir, base+".sock")
		statusPath := filepath.Join(runDir, base+".json")

		if !p.sock {
			removeAgentStaleFiles("", statusPath)
			cleaned++
			continue
		}
		if !p.status {
			logger.Warn("claudeagent: discovery: socket with no status file, cleaning up", "socket", sockPath)
			removeAgentStaleFiles(sockPath, "")
			cleaned++
			continue
		}

		sf, serr := ptyhost.ReadStatusFile(statusPath)
		if serr != nil {
			logger.Warn("claudeagent: discovery: unreadable status file, cleaning up", "status", statusPath, "err", serr)
			removeAgentStaleFiles(sockPath, statusPath)
			cleaned++
			continue
		}
		repoID, branchID, tabID := sf.RepoID, sf.BranchID, sf.TabID
		if repoID == "" || branchID == "" || tabID == "" {
			logger.Warn("claudeagent: discovery: status file missing explicit identity fields, cleaning up",
				"status", statusPath, "seed", sf.Seed)
			removeAgentStaleFiles(sockPath, statusPath)
			cleaned++
			continue
		}
		// Sfeed64-3: ownership filter, BEFORE any dial. claude-agent only
		// ever spawns [ptyhost.ModePipe]. A "pty" (or empty, pre-Mode-field
		// back-compat) entry belongs to claudetui's Manager and MUST be left
		// completely alone: dialing it here (even just for the HELLO
		// liveness probe below) would evict claudetui's own live connection
		// (ptyhost.Server.replaceConn tolerates only one active connection)
		// — the exact dual-manager eviction loop this story fixes. Not
		// counted as cleaned either: it is not debris, it is a live ptyhost
		// that simply isn't this Manager's to manage.
		if sf.Mode != ptyhost.ModePipe {
			continue
		}
		if skipLive != nil && skipLive(repoID, branchID, tabID) {
			// Known-referenced — see the skipLive parameter doc comment.
			// Left completely alone: no dial, no counting.
			continue
		}
		if !agentPidAlive(sf.Pid) {
			logger.Info("claudeagent: discovery: dead pid, cleaning up stale ptyhost files",
				"repo", repoID, "branch", branchID, "tab", tabID, "pid", sf.Pid)
			removeAgentStaleFiles(sockPath, statusPath)
			cleaned++
			continue
		}

		conn, derr := net.DialTimeout("unix", sockPath, agentScanDialTimeout)
		if derr != nil {
			logger.Info("claudeagent: discovery: socket unreachable, cleaning up stale ptyhost files",
				"repo", repoID, "branch", branchID, "tab", tabID, "err", derr)
			removeAgentStaleFiles(sockPath, statusPath)
			cleaned++
			continue
		}
		pc := &PipeClient{conn: conn}
		hello, herr := pc.Hello()
		_ = conn.Close()
		if herr != nil {
			logger.Info("claudeagent: discovery: HELLO failed, cleaning up stale ptyhost files",
				"repo", repoID, "branch", branchID, "tab", tabID, "err", herr)
			removeAgentStaleFiles(sockPath, statusPath)
			cleaned++
			continue
		}

		live = append(live, discoveredAgentHost{
			RepoID:     repoID,
			BranchID:   branchID,
			TabID:      tabID,
			SockPath:   sockPath,
			StatusPath: statusPath,
			Pid:        hello.Pid,
		})
	}
	return live, cleaned, nil
}

// DiscoverAndRestore scans mgr's ptyhost run directory ([Manager.RunDir])
// and, for every LIVE pipe-mode ptyhost found, re-adopts it: EnsureAgent
// followed by EnsureClient — the SAME lazy-attach path a first user message
// would have taken (Client.launchAndAttachPipe probes the socket and finds
// a survivor, so it attaches rather than spawning). Dead/unreachable
// entries are cleaned up (see scanAgentRunDir).
//
// A worktree that no longer resolves (branch closed / worktree removed
// while palmux2 was down) is logged and skipped — its ptyhost is left
// running un-adopted here. It self-heals one of two ways: the branch is
// reopened (the socket path is deterministic from (repoID, branchID,
// tabID) alone, so a fresh EnsureAgent finds it), or [Manager.GCOrphans]
// reaps it on the next 10s store scan tick once the store confirms the
// (repoID, branchID, tabID) really has no matching tab (S64c835-3).
//
// Call this ONCE, early at boot, before the store's background loops
// start.
func DiscoverAndRestore(ctx context.Context, mgr *Manager, logger *slog.Logger) (adopted, cleaned int, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	runDir := mgr.RunDir()
	live, cleaned, serr := scanAgentRunDir(runDir, logger, nil)
	if serr != nil {
		return 0, cleaned, fmt.Errorf("claudeagent: startup discovery: %w", serr)
	}
	for _, h := range live {
		a, aerr := mgr.EnsureAgent(h.RepoID, h.BranchID, h.TabID)
		if aerr != nil {
			logger.Warn("claudeagent: discovery: EnsureAgent failed (worktree gone? ptyhost left running for later reopen)",
				"repo", h.RepoID, "branch", h.BranchID, "tab", h.TabID, "err", aerr)
			continue
		}
		if cerr := a.EnsureClient(ctx); cerr != nil {
			logger.Warn("claudeagent: discovery: attach to surviving ptyhost failed",
				"repo", h.RepoID, "branch", h.BranchID, "tab", h.TabID, "err", cerr)
			continue
		}
		adopted++
		logger.Info("claudeagent: discovery: re-adopted surviving ptyhost",
			"repo", h.RepoID, "branch", h.BranchID, "tab", h.TabID, "pid", h.Pid)
	}
	return adopted, cleaned, nil
}

// GCOrphans implements S64c835-3's orphan-GC half — claudetui parity
// (mirrors [claudetui.Manager.GCOrphans] near-verbatim). Piggybacked on the
// store's existing 10s scanPorts loop (internal/store/sync_worktree.go), it
// scans this Manager's run directory and SHUTDOWNs every LIVE pipe-mode
// ptyhost whose (repoId, branchId, tabId) isLive reports false for — tab
// delete / worktree removal / branch close, tmux-zombie-kill parity. This
// closes the gap left open at S862203-3's done time (see decisions.json):
// a branch/repo closed while palmux2 was DOWN previously left its agent
// ptyhost running forever until the branch happened to be reopened.
//
// Referenced ptyhosts (isLive == true) are never dialed at all
// ([scanAgentRunDir]'s skipLive parameter, threaded through as isLive
// here), so a live Agent's own active connection is never disturbed by
// this pass — a liveness-probe dial against an already-attached ptyhost
// would otherwise evict that connection (ptyhost.Server.replaceConn only
// tolerates one active connection at a time).
//
// Socket/status file cleanup for a just-SHUTDOWN orphan is deliberately
// NOT synchronous with this call — same rationale as claudetui's
// GCOrphans: the child has not necessarily exited yet (SIGTERM→(grace)→
// SIGKILL escalation lives inside ptyhost), and ptyhost's own teardown
// concurrently rewrites the final status file the moment the child
// actually exits. This self-heals on a LATER tick once the ptyhost's own
// teardown removes the .sock, which [scanAgentRunDir] then recognizes as
// "no socket + status exists" and prunes.
//
// isLive MUST be a cheap, non-blocking lookup (called once per discovered
// ptyhost, every tick) — callers pass a Store method like
// `func(repoID, branchID, tabID string) bool { _, err := store.Tab(...); return err == nil }`.
// A nil isLive makes GCOrphans a no-op (defensive — callers should not
// wire GCOrphans at all in that case).
//
// GUARD-RAIL (do not remove — S64c835-3 review, corrected by Sfeed64-3): this
// Manager's run dir ([Manager.RunDir], ptyhost.RunDir(instancePrefix)) is the
// SAME directory / seed space claude-tui's Manager scans — in production
// both use the same empty instancePrefix, and the single visible "claude"
// tab is ONE shared (repoID, branchID, tabID) seed regardless of mode. So
// claude-tui's and claude-agent's GCOrphans (and DiscoverAndRestore) both
// walk the SAME on-disk ptyhosts every tick.
//
// This was ORIGINALLY believed safe purely because both are driven by the
// same mode-agnostic isLive (plain store.Tab existence). That was
// insufficient on its own: isLive is only ever consulted for entries the
// scan already dialed to confirm liveness, and dialing is itself
// destructive (ptyhost.Server.replaceConn evicts whatever connection was
// previously active) — a real dogfood restart proved both managers were
// dialing/adopting each OTHER's ptyhosts and evicting each other in a loop
// BEFORE isLive ever entered the picture (Sfeed64-3). The actual fix is in
// [scanAgentRunDir]: it now checks [ptyhost.StatusFile.Mode] — the explicit
// ownership marker written by whichever package spawned the ptyhost — and
// skips any entry that is not [ptyhost.ModePipe] BEFORE any dial, so a
// claude-tui-owned ([ptyhost.ModePTY]) entry never reaches isLive at all.
//
// isLive itself MUST STILL stay mode-agnostic (plain store.Tab existence —
// see store.gcAgentOrphans): a variant that also asserted the tab's MODE
// (agent vs tui) would make this reaper stop recognising a referenced
// ptyhost of the OTHER mode as live and SHUTDOWN a running claude — the
// exact S3f2658-3-class false-positive this story exists to prevent. The
// two defenses are complementary and both required: the Mode filter keeps
// the WRONG-mode entries from ever being dialed/considered; mode-agnostic
// isLive keeps the RIGHT-mode entries from being wrongly reaped.
func (m *Manager) GCOrphans(ctx context.Context, isLive func(repoID, branchID, tabID string) bool) (shutdown, cleaned int, err error) {
	if isLive == nil {
		return 0, 0, nil
	}
	runDir := m.RunDir()
	// skipLive == isLive: referenced entries are excluded INSIDE
	// scanAgentRunDir, before any dial — see the doc comment above.
	live, cleaned, serr := scanAgentRunDir(runDir, m.logger, isLive)
	if serr != nil {
		return 0, cleaned, fmt.Errorf("claudeagent: orphan gc: %w", serr)
	}
	for _, h := range live {
		// Everything in `live` here is, by construction, unreferenced
		// (isLive already filtered referenced entries out inside
		// scanAgentRunDir) — every entry gets shut down.
		if serr := sendAgentOrphanShutdown(h.SockPath); serr != nil {
			m.logger.Warn("claudeagent: orphan gc: shutdown failed",
				"repo", h.RepoID, "branch", h.BranchID, "tab", h.TabID, "err", serr)
			continue
		}
		// S52fc2c-4/S3f2658-4 parity: unlike Agent.Shutdown/Client.Close
		// (tab close / branch close), this SHUTDOWN never goes through a
		// live Client — GCOrphans dials the orphaned ptyhost's socket
		// directly (no live Agent exists for it; see
		// [sendAgentOrphanShutdown]) — so the in-container reap that
		// Client.teardown(kill=true) performs must be triggered explicitly
		// here too. Without this, a tab/branch deleted while palmux2 was
		// down (the ptyhost — and any incus-wrapped in-container claude it
		// holds — outlives palmux2 per ADR-0001) would leave the
		// in-container claude running forever: nothing else ever reaps it.
		reapAgentContainerClaude(m.cfg.RuntimeResolver, h.RepoID, h.BranchID, gracefulShutdownTimeout, m.logger)
		shutdown++
		m.logger.Info("claudeagent: orphan gc: shut down unreferenced ptyhost",
			"repo", h.RepoID, "branch", h.BranchID, "tab", h.TabID, "pid", h.Pid)
	}
	return shutdown, cleaned, nil
}

// sendAgentOrphanShutdown dials sockPath directly (bypassing any Agent/
// Client — GCOrphans only ever reaches an UNREFERENCED ptyhost, so there
// is no live connection to disturb) and sends a SHUTDOWN frame (the
// ptyhost owns SIGTERM→SIGKILL escalation of the child it holds, same as
// [Client.teardown]). It does NOT touch the on-disk files — see the
// [Manager.GCOrphans] doc comment for why cleanup is deliberately
// deferred to a later scan tick instead of being synchronous with this
// call.
func sendAgentOrphanShutdown(sockPath string) error {
	conn, err := net.DialTimeout("unix", sockPath, agentScanDialTimeout)
	if err != nil {
		return fmt.Errorf("dial for orphan shutdown: %w", err)
	}
	pc := &PipeClient{conn: conn}
	if err := pc.Shutdown(ptyhost.ShutdownPayload{
		GraceMillis: int(gracefulShutdownTimeout / time.Millisecond),
	}); err != nil {
		return fmt.Errorf("send SHUTDOWN: %w", err)
	}
	return nil
}

// reapAgentContainerClaude best-effort TERMs any in-container claude
// process for the workspace identified by (repoID, branchID), via the
// workspace runtime's optional runtime.ContainerProcessKiller capability —
// the claudeagent-side counterpart of claudetui's reapContainerClaude
// (S52fc2c-4/S3f2658-4), adapted to claudeagent's RuntimeResolver, which
// returns a runtime.ExecCommander rather than a runtime.PTYCommander (both
// are implemented by the same underlying incus runtime and both may also
// implement runtime.ContainerProcessKiller).
//
// No-op when resolver is nil, resolves to nil (host runtime / no
// workspace runtime configured), or does not implement
// ContainerProcessKiller. Errors from the kill itself are logged at
// Debug, not returned — pkill exit 1 ("no matching process") is the
// common/expected case, and a genuinely unreachable/destroyed container
// is not a failure of the caller's own SHUTDOWN.
func reapAgentContainerClaude(resolver func(repoID, branchID string) runtime.ExecCommander, repoID, branchID string, timeout time.Duration, logger *slog.Logger) {
	if resolver == nil {
		return
	}
	ec := resolver(repoID, branchID)
	if ec == nil {
		return
	}
	kk, ok := ec.(runtime.ContainerProcessKiller)
	if !ok {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	kCtx, kCancel := context.WithTimeout(context.Background(), timeout)
	defer kCancel()
	if err := kk.KillContainerProcesses(kCtx, "TERM", containerClaudeBin); err != nil {
		logger.Debug("claudeagent: in-container claude reap (non-fatal)",
			"repo", repoID, "branch", branchID, "err", err)
	}
}
