package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
	"github.com/tjst-t/palmux2/internal/tab/claudeagent"
	"github.com/tjst-t/palmux2/internal/tab/claudetui"
)

// This file is the [AC-Sfeed64-3-2] regression test: it proves, with REAL
// ptyhost.Server processes and REAL claudetui/claudeagent Managers pointed
// at the SAME on-disk run directory (exactly the shared-directory shape
// production uses — see the Sfeed64-3 file-level doc comments in both
// packages' discover.go), that the [ptyhost.StatusFile.Mode] ownership
// filter added by this story keeps each Manager's startup discovery from
// dialing/adopting the OTHER manager's ptyhost.
//
// This lives in cmd/palmux (package main) rather than inside either
// claudetui or claudeagent because it is inherently a CROSS-package
// scenario — main.go is the one place that already imports and wires both
// Managers together in production.
//
// Before the Sfeed64-3 fix, scanRunDir/scanAgentRunDir had no ownership
// filter at all: DiscoverAndRestore would dial and adopt EVERY live
// (*.sock, *.json) pair it found, regardless of which package spawned it.
// Since a ptyhost socket tolerates only ONE live connection at a time
// (ptyhost.Server.replaceConn evicts whatever connection was previously
// active the instant a new one dials in), both Managers adopting the SAME
// two ptyhosts means BOTH entries end up "adopted" by BOTH managers —
// exactly what this test's "both adopt" failure mode below demonstrates
// when the Mode filter is removed (verified by hand against a pre-fix
// checkout; see docs/sprint-logs/Sfeed64/decisions.json).

// buildOwnershipTestBin compiles a testdata helper binary (shared across
// packages) into a fresh temp dir and returns its path.
func buildOwnershipTestBin(t *testing.T, srcRelToRepoRoot, outName string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// cmd/palmux -> repo root is two levels up.
	repoRoot := filepath.Join(wd, "..", "..")
	src := filepath.Join(repoRoot, srcRelToRepoRoot)
	bin := filepath.Join(t.TempDir(), outName)
	cmd := exec.Command("go", "build", "-o", bin, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("compiling %s: %v\n%s", srcRelToRepoRoot, err, out)
	}
	return bin
}

// startRawOwnershipPtyHost starts a REAL [ptyhost.Server] in the given mode
// (holding a real child process — fake_claude for ModePTY, fake_ndjson for
// ModePipe) at the deterministic socket/status path for (repoID, branchID,
// tabID) under runDir, with the EXPLICIT RepoID/BranchID/TabID Config
// fields populated (S3f2658-3 identity discipline). Mirrors
// claudetui/discover_test.go's startRawPtyHost and
// claudeagent/discover_test.go's startRawAgentPtyHost, generalized over
// mode since this test needs both in the SAME run dir.
func startRawOwnershipPtyHost(t *testing.T, runDir, bin, mode, repoID, branchID, tabID string) (sockPath, statusPath string, done chan struct{}, pid int) {
	t.Helper()
	seed := repoID + "__" + branchID + "__" + tabID
	sockPath = ptyhost.SocketPath(runDir, seed)
	statusPath = ptyhost.StatusPath(runDir, seed)
	cfg := ptyhost.Config{
		Argv:       []string{bin},
		Env:        os.Environ(),
		Mode:       mode,
		SocketPath: sockPath,
		StatusPath: statusPath,
		RingSize:   1 << 16,
		Seed:       seed,
		RepoID:     repoID,
		BranchID:   branchID,
		TabID:      tabID,
	}
	if mode == ptyhost.ModePipe {
		cfg.GracePeriod = 2 * time.Second
		cfg.PostExitLinger = 500 * time.Millisecond
	}
	srv, err := ptyhost.NewServer(cfg)
	if err != nil {
		t.Fatalf("ptyhost.NewServer(%s, mode=%s): %v", seed, mode, err)
	}
	done = make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Run(context.Background())
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", seed, err)
	}
	hello, herr := ownershipTestHello(conn)
	_ = conn.Close()
	if herr != nil {
		t.Fatalf("HELLO to %s (mode=%s): %v", seed, mode, herr)
	}
	return sockPath, statusPath, done, hello
}

// ownershipTestHello sends a raw HELLO frame and returns the reported pid.
// Both ptyclient.go (claudetui) and launch.go (claudeagent) have
// package-private equivalents; this is a minimal reimplementation over the
// exported ptyhost wire-protocol helpers so this cross-package test doesn't
// need to reach into either package's internals.
func ownershipTestHello(conn net.Conn) (int, error) {
	if err := ptyhost.WriteFrame(conn, ptyhost.MsgHello, nil); err != nil {
		return 0, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	msgType, payload, err := ptyhost.ReadFrame(conn)
	if err != nil {
		return 0, err
	}
	if msgType != ptyhost.MsgHello {
		return 0, fmt.Errorf("unexpected reply msg type %v (want HELLO)", msgType)
	}
	hp, err := ptyhost.DecodeHello(payload)
	if err != nil {
		return 0, err
	}
	return hp.Pid, nil
}

// shutdownRawOwnershipPtyHost is a best-effort cleanup helper.
func shutdownRawOwnershipPtyHost(sockPath string) {
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = ptyhost.WriteFrame(conn, ptyhost.MsgShutdown, ptyhost.EncodeShutdown(ptyhost.ShutdownPayload{GraceMillis: 200}))
}

// stubOwnershipBranchResolver is a minimal claudeagent.BranchResolver —
// DiscoverAndRestore's EnsureAgent call needs a non-erroring resolver, but
// this test never exercises worktree-path-dependent behaviour.
type stubOwnershipBranchResolver struct{}

func (stubOwnershipBranchResolver) WorktreePath(string, string) (string, error) { return "", nil }

// TestPtyOwnership_ModeFilter is [AC-Sfeed64-3-2]: a REAL ModePTY ptyhost
// and a REAL ModePipe ptyhost sit side by side in the SAME run dir (the
// shared-directory shape production uses). Running BOTH
// claudetui.DiscoverAndRestore AND claudeagent.DiscoverAndRestore against
// that shared dir must result in EACH manager adopting ONLY its own-mode
// entry — never the other's, and never both entries by both managers
// (which is what the pre-fix code did, per the dogfood report this story
// fixes).
//
// (Test name kept short deliberately: runDir below is an AF_UNIX socket
// directory, capped at ~108 bytes of sun_path — see [ptyhost.FileKey]'s
// doc comment. testing.T.TempDir() embeds the full subtest name in its
// path, so a long Test function name here would itself blow that budget;
// this test therefore also uses its own short os.MkdirTemp rather than
// t.TempDir() for runDir specifically, for the same reason.)
func TestPtyOwnership_ModeFilter(t *testing.T) {
	fakeClaudeBin := buildOwnershipTestBin(t, filepath.Join("internal", "tab", "claudetui", "testdata", "fake_claude.go"), "fake_claude")
	fakeNDJSONBin := buildOwnershipTestBin(t, filepath.Join("internal", "ptyhost", "testdata", "fake_ndjson.go"), "fake_ndjson")

	runDir, err := os.MkdirTemp("", "sfeed64-3-own")
	if err != nil {
		t.Fatalf("MkdirTemp runDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	ctx := context.Background()

	const (
		ptyRepo, ptyBranch, ptyTab    = "owner-pty-repo", "owner-pty-branch", "claude:claude"
		pipeRepo, pipeBranch, pipeTab = "owner-pipe-repo", "owner-pipe-branch", "claude:claude"
	)

	ptySock, _, ptyDone, ptyPid := startRawOwnershipPtyHost(t, runDir, fakeClaudeBin, ptyhost.ModePTY, ptyRepo, ptyBranch, ptyTab)
	pipeSock, _, pipeDone, pipePid := startRawOwnershipPtyHost(t, runDir, fakeNDJSONBin, ptyhost.ModePipe, pipeRepo, pipeBranch, pipeTab)
	t.Cleanup(func() {
		shutdownRawOwnershipPtyHost(ptySock)
		shutdownRawOwnershipPtyHost(pipeSock)
		for _, d := range []chan struct{}{ptyDone, pipeDone} {
			select {
			case <-d:
			case <-time.After(5 * time.Second):
				t.Log("raw ownership ptyhost did not fully tear down within 5s during cleanup")
			}
		}
	})

	tuiMgr := claudetui.NewManager(claudetui.ManagerConfig{
		ClaudeBin:      fakeClaudeBin,
		RingSize:       1 << 16,
		RunDirOverride: runDir,
	})
	t.Cleanup(func() { _ = tuiMgr.ShutdownAll(ctx) })

	agentStore, err := claudeagent.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("claudeagent.NewStore: %v", err)
	}
	agentMgr := claudeagent.NewManager(claudeagent.Config{
		Binary:         fakeNDJSONBin,
		RunDirOverride: runDir,
	}, agentStore, stubOwnershipBranchResolver{}, nil, nil, nil)

	// Run BOTH managers' startup discovery against the SAME shared run dir —
	// this is the exact seam main.go's run() drives at boot (see
	// runDiscoveryAsync in main.go).
	tuiAdopted, tuiCleaned, err := claudetui.DiscoverAndRestore(ctx, tuiMgr, nil, nil)
	if err != nil {
		t.Fatalf("claudetui.DiscoverAndRestore: %v", err)
	}
	agentAdopted, agentCleaned, err := claudeagent.DiscoverAndRestore(ctx, agentMgr, nil)
	if err != nil {
		t.Fatalf("claudeagent.DiscoverAndRestore: %v", err)
	}

	// The whole point of the Mode filter: each manager adopts EXACTLY its
	// own entry, not both. Pre-fix, both managers found BOTH live entries
	// (HELLO doesn't care which package is asking) and adopted both —
	// tuiAdopted/agentAdopted would each be 2, not 1.
	if tuiAdopted != 1 {
		t.Fatalf("claudetui adopted = %d, want 1 (only its own pty-mode entry — a value of 2 means the Mode filter let it dial/adopt the pipe-mode entry too)", tuiAdopted)
	}
	if agentAdopted != 1 {
		t.Fatalf("claudeagent adopted = %d, want 1 (only its own pipe-mode entry — a value of 2 means the Mode filter let it dial/adopt the pty-mode entry too)", agentAdopted)
	}
	// Wrong-mode entries must be left COMPLETELY untouched — not even
	// counted as "cleaned" (they are not debris).
	if tuiCleaned != 0 {
		t.Fatalf("claudetui cleaned = %d, want 0 (the pipe-mode entry is live and not claudetui's to manage, must not be touched at all)", tuiCleaned)
	}
	if agentCleaned != 0 {
		t.Fatalf("claudeagent cleaned = %d, want 0 (the pty-mode entry is live and not claudeagent's to manage, must not be touched at all)", agentCleaned)
	}

	// claudetui must have adopted the PTY entry specifically, and must NOT
	// have an entry for the pipe identity.
	tuiDaemon := tuiMgr.Get(ptyRepo, ptyBranch, ptyTab)
	if tuiDaemon == nil {
		t.Fatal("claudetui did not adopt its own pty-mode ptyhost")
	}
	if got := tuiMgr.Get(pipeRepo, pipeBranch, pipeTab); got != nil {
		t.Fatal("claudetui wrongly adopted the pipe-mode ptyhost (cross-manager adoption regression)")
	}

	// claudeagent must have adopted the pipe entry specifically, and must
	// NOT have an entry for the pty identity.
	agentEntry := agentMgr.Get(pipeRepo, pipeBranch, pipeTab)
	if agentEntry == nil {
		t.Fatal("claudeagent did not adopt its own pipe-mode ptyhost")
	}
	if got := agentMgr.Get(ptyRepo, ptyBranch, ptyTab); got != nil {
		t.Fatal("claudeagent wrongly adopted the pty-mode ptyhost (cross-manager adoption regression)")
	}

	// The adopted Daemon must be ATTACHED to the pre-existing ptyhost (same
	// pid), not a fresh respawn — proof the pipe-mode discovery pass never
	// disturbed it via an eviction dial.
	waitForOwnershipTuiRunning(t, tuiDaemon, 5*time.Second)
	if got := tuiDaemon.CurrentStats().PID; got != ptyPid {
		t.Fatalf("adopted pty daemon pid = %d, want %d (the pre-existing ptyhost's pid) — a mismatch means it was evicted/respawned", got, ptyPid)
	}

	// Sanity: the raw ptyhost pids are still alive (neither side crashed
	// out from under the other during discovery).
	if !processAlive(ptyPid) {
		t.Fatal("pty-mode ptyhost child died during/after discovery")
	}
	if !processAlive(pipePid) {
		t.Fatal("pipe-mode ptyhost child died during/after discovery")
	}
}

// waitForOwnershipTuiRunning polls a claudetui.Daemon until CurrentStats
// reports Alive (State == StateRunning) or the timeout elapses.
func waitForOwnershipTuiRunning(t *testing.T, d *claudetui.Daemon, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last claudetui.Stats
	for time.Now().Before(deadline) {
		last = d.CurrentStats()
		if last.Alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon did not reach StateRunning within %s (last stats: %+v)", timeout, last)
}

// processAlive is the standard Unix "signal 0" existence probe, duplicated
// here (rather than importing either package's unexported pidAlive) since
// this test lives outside both.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
