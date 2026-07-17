package agenttui

import (
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

// This file implements the Manager-agnostic half of S3f2658-3 (discovery /
// reconnect / orphan GC / INSTANCE separation) — see
// docs/no-halt-agent-design.md §3. [ScanRunDir] is the shared on-disk scan
// both of claudetui's Manager-coupled halves build on:
//
//   - claudetui.DiscoverAndRestore: run ONCE at palmux2 startup. Every LIVE
//     ptyhost found under a Manager's run directory is re-adopted into an
//     "attach existing" Daemon (reusing Story 2's launchAndAttach survivor
//     probe verbatim — no new spawn/attach code). Dead/unreachable entries
//     are cleaned up from disk.
//   - claudetui.Manager.GCOrphans: piggybacks the store's existing 10s
//     scanPorts loop. Every LIVE ptyhost whose (repoId, branchId, tabId) the
//     caller's isLive callback does not recognize gets SHUTDOWN —
//     tmux-zombie-kill parity.
//
// S0e8afb-1 (docs/agenttui-ptyhost-merge-design.md, P1): this file, plus
// ptyclient.go, moved here verbatim from claudetui as the mechanical first
// step of the agenttui/ptyhost merge — claudetui/daemon.go and manager.go
// (and therefore Manager/Daemon themselves) have NOT moved yet, so
// DiscoverAndRestore and GCOrphans — both irreducibly coupled to *Manager —
// stayed behind in internal/tab/claudetui/ptyhost_discovery.go, calling back
// into this package's exported [ScanRunDir]/[SendOrphanShutdown]/[PidAlive].
// This split is expected to collapse back into one file once Manager/Daemon
// themselves move to agenttui in a later Story.
//
// Both scanning halves recover a socket's owning (repoId, branchId, tabId)
// from the EXPLICIT [ptyhost.StatusFile] RepoID/BranchID/TabID fields
// (S3f2658-3), which are written DIRECTLY from the ptyhost's Config (no
// join, no parse). This is required rather than parsing the Seed label: the
// socket/status FILENAME is a one-way hash of the seed (AF_UNIX sun_path
// length limit — see [ptyhost.FileKey]), and the Seed string itself
// ("repoId__branchId__tabId") is NOT reliably splittable back — an ID may
// contain the literal "__" (domain IDs permit "_", and two adjacent
// sanitized-out chars collapse to "__"), so a "__"-split would mis-attribute
// the ptyhost to the WRONG tuple. A misparse here is a data-loss bug: GC
// would fail to recognize a LIVE, referenced tab as live and SHUTDOWN the
// user's running claude (fixed by carrying identity in-band as explicit
// fields, never a delimited string).
//
// Sfeed64-3: a claude-tui Manager's run directory is SHARED on disk with
// claudeagent's Manager (internal/tab/claudeagent/discover.go) — both walk
// the SAME (*.sock, *.json) seed space, because a ptyhost's socket path is
// derived purely from (repoId, branchId, tabId), independent of which
// package spawned it (claude-tui in [ptyhost.ModePTY], claude-agent in
// [ptyhost.ModePipe]). Before this fix, ScanRunDir treated every
// identity-bearing entry as "mine": a real dogfood restart showed BOTH
// managers dialing/adopting each OTHER's ptyhosts, and since a ptyhost
// socket tolerates only ONE live connection at a time
// (ptyhost.Server.replaceConn evicts whatever connection was previously
// active), the two managers evicted each other in a loop — broken pipe,
// interpreted as session death, SHUTDOWN + respawn storm, and the surviving
// claude session lost its screen/conversation continuity even though it
// self-healed (no 502). The fix: [ptyhost.StatusFile.Mode] is the explicit,
// authoritative ownership marker (written directly from the spawning
// Config, exactly like RepoID/BranchID/TabID above) — ScanRunDir checks it
// immediately after reading the status file's identity, BEFORE any dial,
// and skips (leaves COMPLETELY untouched — no dial, no adopt, no SHUTDOWN,
// no cleaned-count) any entry whose Mode is not one this Manager owns. This
// is the ownership marker the S3f2658-3 GUARD-RAIL comment in claudeagent's
// GCOrphans anticipated but did not yet apply to the on-disk scan.

// DiscoveredHost is one LIVE ptyhost found by [ScanRunDir]: its identity
// (read from the status file's explicit RepoID/BranchID/TabID fields) plus
// the paths a caller needs to re-adopt or SHUTDOWN it.
type DiscoveredHost struct {
	RepoID, BranchID, TabID string
	SockPath, StatusPath    string
	Pid                     int
}

// PidAlive reports whether pid refers to a currently-running process, via
// the standard Unix "signal 0" existence probe (no signal is actually
// delivered). false for pid<=0.
func PidAlive(pid int) bool {
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
// ScanRunDir, and the SHUTDOWN dial in SendOrphanShutdown. Generous for a
// LOCAL unix socket (normally sub-millisecond) so a heavily loaded host
// running many concurrent process spawns (e.g. this Sprint's own test suite)
// does not misclassify a merely-slow-to-schedule ptyhost as dead.
const scanRunDirDialTimeout = 2 * time.Second

// ScanRunDir enumerates runDir (see [ptyhost.RunDir]) and classifies every
// (*.sock, *.json) pair found:
//
//   - A .sock with no matching .json (or vice versa) is unidentifiable
//     debris — removed.
//   - A pair whose status file fails to parse, whose Seed does not split
//     into exactly (repoId, branchId, tabId), whose recorded pid is dead, or
//     whose socket does not answer HELLO within scanRunDirDialTimeout is
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
func ScanRunDir(runDir string, logger *slog.Logger, skipLive func(repoID, branchID, tabID string) bool) (live []DiscoveredHost, cleaned int, err error) {
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
		// Sfeed64-3: ownership filter, BEFORE any dial. claude-tui only ever
		// spawns [ptyhost.ModePTY] (sf.Mode == "" is a pre-Mode-field
		// back-compat record, also pty — see ptyhost.Config's own
		// defaulting). A "pipe" entry belongs to claudeagent's Manager and
		// MUST be left completely alone: dialing it here (even just for the
		// HELLO liveness probe below) would evict claudeagent's own live
		// connection (ptyhost.Server.replaceConn tolerates only one active
		// connection) — the exact dual-manager eviction loop this story
		// fixes. Not counted as cleaned either: it is not debris, it is a
		// live ptyhost that simply isn't this Manager's to manage.
		if sf.Mode == ptyhost.ModePipe {
			continue
		}
		if skipLive != nil && skipLive(repoID, branchID, tabID) {
			// Known-referenced — see the skipLive parameter doc comment.
			// Left completely alone: no dial, no counting.
			continue
		}
		if !PidAlive(sf.Pid) {
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
		hello, herr := SendHello(conn)
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

// SendOrphanShutdown dials sockPath directly (bypassing any Daemon —
// GCOrphans only ever reaches an UNREFERENCED ptyhost, so there is no live
// Daemon connection to disturb) and sends a SHUTDOWN frame (the ptyhost owns
// SIGTERM→SIGKILL escalation of the child it holds, same as
// Daemon.teardown). It does NOT touch the on-disk files — see
// claudetui.Manager.GCOrphans's doc comment for why cleanup is deliberately
// deferred to a later scan tick instead of being synchronous with this call.
//
// grace is the SIGTERM→SIGKILL escalation window to request (the caller's
// own gracefulShutdownTimeout — passed explicitly since that constant lives
// in claudetui/daemon.go, not here).
func SendOrphanShutdown(sockPath string, grace time.Duration) error {
	conn, err := net.DialTimeout("unix", sockPath, scanRunDirDialTimeout)
	if err != nil {
		return fmt.Errorf("dial for orphan shutdown: %w", err)
	}
	defer func() { _ = conn.Close() }()
	payload := ptyhost.EncodeShutdown(ptyhost.ShutdownPayload{
		GraceMillis: int(grace / time.Millisecond),
	})
	if err := ptyhost.WriteFrame(conn, ptyhost.MsgShutdown, payload); err != nil {
		return fmt.Errorf("send SHUTDOWN: %w", err)
	}
	return nil
}
