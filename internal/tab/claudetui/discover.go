package claudetui

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
)

// This file implements S3f2658-3 (discovery / reconnect / orphan GC /
// INSTANCE separation) — see docs/no-halt-agent-design.md §3. It has two
// halves that share the same on-disk scan (scanRunDir):
//
//   - DiscoverAndRestore: run ONCE at palmux2 startup. Every LIVE ptyhost
//     found under this Manager's run directory is re-adopted into an
//     "attach existing" Daemon (reusing Story 2's launchAndAttach survivor
//     probe verbatim — no new spawn/attach code). Dead/unreachable entries
//     are cleaned up from disk.
//   - GCOrphans: piggybacks the store's existing 10s scanPorts loop. Every
//     LIVE ptyhost whose (repoId, branchId, tabId) the caller's isLive
//     callback does not recognize gets SHUTDOWN — tmux-zombie-kill parity.
//
// Both recover a socket's owning (repoId, branchId, tabId) from the EXPLICIT
// [ptyhost.StatusFile] RepoID/BranchID/TabID fields (S3f2658-3), which are
// written DIRECTLY from the ptyhost's Config (no join, no parse). This is
// required rather than parsing the Seed label: the socket/status FILENAME is
// a one-way hash of the seed (AF_UNIX sun_path length limit — see
// [ptyhost.FileKey]), and the Seed string itself ("repoId__branchId__tabId")
// is NOT reliably splittable back — an ID may contain the literal "__"
// (domain IDs permit "_", and two adjacent sanitized-out chars collapse to
// "__"), so a "__"-split would mis-attribute the ptyhost to the WRONG tuple.
// A misparse here is a data-loss bug: GC would fail to recognize a LIVE,
// referenced tab as live and SHUTDOWN the user's running claude (fixed by
// carrying identity in-band as explicit fields, never a delimited string).

// DiscoveredHost is one LIVE ptyhost found by [scanRunDir]: its identity
// (read from the status file's explicit RepoID/BranchID/TabID fields) plus
// the paths a caller needs to re-adopt or SHUTDOWN it.
type DiscoveredHost struct {
	RepoID, BranchID, TabID string
	SockPath, StatusPath    string
	Pid                     int
}

// pidAlive reports whether pid refers to a currently-running process, via
// the standard Unix "signal 0" existence probe (no signal is actually
// delivered). false for pid<=0.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// removeStaleFiles best-effort removes a (possibly partial) socket/status
// pair. Errors are intentionally swallowed — cleanup of an already-gone or
// permission-denied file is not itself a failure condition for a discovery
// pass that runs unattended.
func removeStaleFiles(sockPath, statusPath string) {
	if sockPath != "" {
		_ = os.Remove(sockPath)
	}
	if statusPath != "" {
		_ = os.Remove(statusPath)
	}
}

// scanRunDirDialTimeout bounds the liveness-probe dial+HELLO used by
// scanRunDir, and the SHUTDOWN dial in sendOrphanShutdown. Generous for a
// LOCAL unix socket (normally sub-millisecond) so a heavily loaded host
// running many concurrent process spawns (e.g. this Sprint's own test suite)
// does not misclassify a merely-slow-to-schedule ptyhost as dead.
const scanRunDirDialTimeout = 2 * time.Second

// scanRunDir enumerates runDir (see [ptyhost.RunDir]) and classifies every
// (*.sock, *.json) pair found:
//
//   - A .sock with no matching .json (or vice versa) is unidentifiable
//     debris — removed.
//   - A pair whose status file fails to parse, whose Seed does not split
//     into exactly (repoId, branchId, tabId), whose recorded pid is dead, or
//     whose socket does not answer HELLO within [scanRunDirDialTimeout] is
//     STALE — both files are removed.
//   - Everything else is LIVE and returned in the result slice.
//
// skipLive, when non-nil, is consulted with the (repoId, branchId, tabId)
// parsed from EACH entry's Seed BEFORE any liveness probe (pid check,
// dial, HELLO) is performed. An entry it reports true for is left
// COMPLETELY untouched — not dialed, not counted as live, not counted as
// cleaned. This matters beyond efficiency: a ptyhost socket only ever
// tolerates ONE active connection (see ptyhost.Server.replaceConn, which
// closes whatever connection was previously active the instant a new one
// arrives) — so a liveness-probe dial against an entry this SAME process
// already has a live Daemon attached to would silently evict that Daemon's
// real connection out from under it. GCOrphans passes its isLive callback
// here for exactly this reason (a referenced ptyhost must never be
// disturbed); DiscoverAndRestore passes nil (at startup nothing has been
// adopted into a Daemon yet, so there is nothing to protect, and probing
// IS the discovery mechanism).
//
// A missing runDir (nothing has ever been discovered/spawned under this
// instancePrefix yet) is NOT an error — it returns an empty result.
func scanRunDir(runDir string, logger *slog.Logger, skipLive func(repoID, branchID, tabID string) bool) (live []DiscoveredHost, cleaned int, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	entries, rerr := os.ReadDir(runDir)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("claudetui: scan ptyhost run dir %s: %w", runDir, rerr)
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
			// Leftover status record from a ptyhost that already tore its
			// socket down (normal exit path — see ptyhost.Server.Run's
			// teardown, which removes the .sock but intentionally leaves the
			// .json as a last-known-exit record). Nothing left to discover;
			// prune the record.
			removeStaleFiles("", statusPath)
			cleaned++
			continue
		}
		if !p.status {
			// A socket file with no identity record at all — cannot be
			// resolved to a (repoId, branchId, tabId), so it can never be
			// discovered/re-adopted or safely orphan-GC'd by identity. Treat
			// as debris.
			logger.Warn("claudetui: discovery: socket with no status file, cleaning up", "socket", sockPath)
			removeStaleFiles(sockPath, "")
			cleaned++
			continue
		}

		sf, serr := ptyhost.ReadStatusFile(statusPath)
		if serr != nil {
			logger.Warn("claudetui: discovery: unreadable status file, cleaning up", "status", statusPath, "err", serr)
			removeStaleFiles(sockPath, statusPath)
			cleaned++
			continue
		}
		// Identity comes from the EXPLICIT status-file fields (written
		// directly from Config, never parsed from a delimited string) so an
		// ID containing "__" round-trips exactly — see the file-level doc
		// comment for why parsing Seed would be a data-loss bug.
		repoID, branchID, tabID := sf.RepoID, sf.BranchID, sf.TabID
		if repoID == "" || branchID == "" || tabID == "" {
			// No usable identity (empty fields, or a pre-S3f2658-3-fix
			// ptyhost that only ever wrote Seed) — cannot be safely
			// re-adopted or orphan-GC'd by identity. Treat as debris.
			logger.Warn("claudetui: discovery: status file missing explicit identity fields, cleaning up",
				"status", statusPath, "seed", sf.Seed)
			removeStaleFiles(sockPath, statusPath)
			cleaned++
			continue
		}
		if skipLive != nil && skipLive(repoID, branchID, tabID) {
			// Known-referenced — see the skipLive parameter doc comment.
			// Left completely alone: no dial, no counting.
			continue
		}
		if !pidAlive(sf.Pid) {
			logger.Info("claudetui: discovery: dead pid, cleaning up stale ptyhost files",
				"repo", repoID, "branch", branchID, "tab", tabID, "pid", sf.Pid)
			removeStaleFiles(sockPath, statusPath)
			cleaned++
			continue
		}

		conn, derr := net.DialTimeout("unix", sockPath, scanRunDirDialTimeout)
		if derr != nil {
			logger.Info("claudetui: discovery: socket unreachable, cleaning up stale ptyhost files",
				"repo", repoID, "branch", branchID, "tab", tabID, "err", derr)
			removeStaleFiles(sockPath, statusPath)
			cleaned++
			continue
		}
		hello, herr := sendHello(conn)
		_ = conn.Close()
		if herr != nil {
			logger.Info("claudetui: discovery: HELLO failed, cleaning up stale ptyhost files",
				"repo", repoID, "branch", branchID, "tab", tabID, "err", herr)
			removeStaleFiles(sockPath, statusPath)
			cleaned++
			continue
		}

		live = append(live, DiscoveredHost{
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

// DiscoverAndRestore implements the startup half of S3f2658-3: it scans
// mgr's run directory ([Manager.RunDir]) and, for every LIVE ptyhost found,
// re-adopts it into mgr in "attach existing" mode — EnsureDaemon followed by
// EnsureStarted, EXACTLY the lazy-attach path a first WebSocket attach would
// have taken (Story 2's launchAndAttach probes the socket and finds a
// survivor, so it attaches rather than spawning — see
// docs/no-halt-agent-design.md §5 for the accompanying screen-restore
// jiggle). Dead/unreachable entries are cleaned up (see [scanRunDir]).
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
	live, cleaned, serr := scanRunDir(runDir, logger, nil)
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
// never dialed at all ([scanRunDir]'s skipLive parameter, threaded through
// as isLive here), so a live Daemon's own active connection is never
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
// which [scanRunDir] then recognizes as "no socket + status exists" and
// prunes — the exact same path a hard-killed ptyhost's leftover files take.
// Dead/unreachable entries encountered on THIS tick (from a previous
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
	// skipLive == isLive: referenced entries are excluded INSIDE scanRunDir,
	// before any dial — see the doc comment above.
	live, cleaned, serr := scanRunDir(runDir, m.cfg.Logger, isLive)
	if serr != nil {
		return 0, cleaned, fmt.Errorf("claudetui: orphan gc: %w", serr)
	}
	for _, h := range live {
		// Everything in `live` here is, by construction, unreferenced
		// (isLive already filtered referenced entries out inside
		// scanRunDir) — every entry gets shut down.
		if serr := sendOrphanShutdown(h.SockPath); serr != nil {
			m.cfg.Logger.Warn("claudetui: orphan gc: shutdown failed",
				"repo", h.RepoID, "branch", h.BranchID, "tab", h.TabID, "err", serr)
			continue
		}
		shutdown++
		m.cfg.Logger.Info("claudetui: orphan gc: shut down unreferenced ptyhost",
			"repo", h.RepoID, "branch", h.BranchID, "tab", h.TabID, "pid", h.Pid)
	}
	return shutdown, cleaned, nil
}

// sendOrphanShutdown dials sockPath directly (bypassing any Daemon —
// GCOrphans only ever reaches an UNREFERENCED ptyhost, so there is no live
// Daemon connection to disturb) and sends a SHUTDOWN frame (the ptyhost owns
// SIGTERM→SIGKILL escalation of the child it holds, same as
// [Daemon.teardown]). It does NOT touch the on-disk files — see the
// [Manager.GCOrphans] doc comment for why cleanup is deliberately deferred
// to a later scan tick instead of being synchronous with this call.
func sendOrphanShutdown(sockPath string) error {
	conn, err := net.DialTimeout("unix", sockPath, scanRunDirDialTimeout)
	if err != nil {
		return fmt.Errorf("dial for orphan shutdown: %w", err)
	}
	defer func() { _ = conn.Close() }()
	payload := ptyhost.EncodeShutdown(ptyhost.ShutdownPayload{
		GraceMillis: int(gracefulShutdownTimeout / time.Millisecond),
	})
	if err := ptyhost.WriteFrame(conn, ptyhost.MsgShutdown, payload); err != nil {
		return fmt.Errorf("send SHUTDOWN: %w", err)
	}
	return nil
}
