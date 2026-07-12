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
)

// This file implements S862203-3's startup discovery: re-adopting pipe-mode
// ptyhosts that survived a PRIOR palmux2 lifetime, so a restart's very
// first REST/WS response already shows a live, resumable Agent instead of
// waiting for a lazy first user message. It is a near-verbatim mirror of
// internal/tab/claudetui/discover.go's DiscoverAndRestore — same on-disk
// scan, same "explicit RepoID/BranchID/TabID status-file fields, never a
// parsed Seed" identity discipline (a repoId/branchId containing the
// literal substring "__" would otherwise mis-attribute a discovered
// ptyhost — a data-loss bug, not just a cosmetic one).
//
// Unlike claudetui, there is no orphan-GC half here yet (backlog — see
// decisions.json): claudeagent's ptyhosts are adopted opportunistically on
// startup and otherwise self-heal the next time a matching EnsureAgent
// happens to run; a leaked, never-reopened branch's ptyhost is not
// proactively reaped by this story.

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

// scanAgentRunDir enumerates runDir and classifies every (*.sock, *.json)
// pair found, exactly like claudetui's scanRunDir: unidentifiable/stale
// debris is removed, everything else LIVE is returned.
func scanAgentRunDir(runDir string, logger *slog.Logger) (live []discoveredAgentHost, cleaned int, err error) {
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
// running un-adopted (backlog: no orphan-GC half yet, see decisions.json);
// it self-heals the moment that branch is reopened, since the socket path
// is deterministic from (repoID, branchID, tabID) alone.
//
// Call this ONCE, early at boot, before the store's background loops
// start.
func DiscoverAndRestore(ctx context.Context, mgr *Manager, logger *slog.Logger) (adopted, cleaned int, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	runDir := mgr.RunDir()
	live, cleaned, serr := scanAgentRunDir(runDir, logger)
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
