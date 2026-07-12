package claudeagent

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
)

// This file covers Sprint S64c835 Story 3's Go-level acceptance scenarios
// (claudeagent's orphan-GC — claudetui parity, see S3f2658-3's
// discover_test.go for the pattern this mirrors):
//   - AC-S64c835-3-1: orphan GC (the store's 10s scan-loop piggyback) shuts
//     down a pipe-mode ptyhost with no matching tab, and leaves a
//     referenced one alone across several ticks.
//   - AC-S64c835-3-2: identity is resolved from the ptyhost status file's
//     EXPLICIT RepoID/BranchID/TabID fields, never by re-splitting a
//     delimited seed string — proven with a repoID containing the literal
//     substring "__".
//   - AC-S64c835-3-3: verified with REAL processes (the fake NDJSON
//     stream-json emitter used across this package's other pipe-mode
//     tests), not real claude.

// startRawAgentPtyHost starts a REAL [ptyhost.Server] in ModePipe (holding a
// real fake_ndjson child) at the deterministic socket/status path for seed
// under runDir, with the EXPLICIT RepoID/BranchID/TabID Config fields
// populated — the authoritative, unambiguous identity discovery/GC reads
// (S3f2658-3 / S64c835-3), NOT a parsed Seed label. Mirrors claudetui's
// discover_test.go startRawPtyHost, adapted to pipe mode. Returns the
// paths, a channel closed when Run() returns, and the child pid (via a
// throwaway HELLO probe).
func startRawAgentPtyHost(t *testing.T, runDir, bin, repoID, branchID, tabID string) (sockPath, statusPath string, done chan struct{}, pid int) {
	t.Helper()
	seed := repoID + "__" + branchID + "__" + tabID
	sockPath = ptyhost.SocketPath(runDir, seed)
	statusPath = ptyhost.StatusPath(runDir, seed)
	srv, err := ptyhost.NewServer(ptyhost.Config{
		Argv:           []string{bin},
		Env:            os.Environ(),
		Mode:           ptyhost.ModePipe,
		SocketPath:     sockPath,
		StatusPath:     statusPath,
		RingSize:       1 << 16,
		Seed:           seed,
		RepoID:         repoID,
		BranchID:       branchID,
		TabID:          tabID,
		GracePeriod:    2 * time.Second,
		PostExitLinger: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ptyhost.NewServer(%s): %v", seed, err)
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
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial %s: %v", seed, err)
	}
	pc := &PipeClient{conn: conn}
	hello, herr := pc.Hello()
	_ = conn.Close()
	if herr != nil {
		t.Fatalf("HELLO to %s: %v", seed, herr)
	}
	return sockPath, statusPath, done, hello.Pid
}

// shutdownRawAgentPtyHost is a best-effort cleanup helper (t.Cleanup) that
// sends SHUTDOWN to a still-listening socket. Safe to call on an
// already-gone socket (probeExisting just reports false).
func shutdownRawAgentPtyHost(sockPath string) {
	if conn, ok := probeExisting(sockPath); ok {
		pc := &PipeClient{conn: conn}
		_ = pc.Shutdown(ptyhost.ShutdownPayload{GraceMillis: 200})
	}
}

// TestGCOrphans_ShutsDownUnreferenced_LeavesReferenced is AC-S64c835-3-1 /
// AC-S64c835-3-3 (real processes: fake NDJSON emitter, not real claude).
func TestGCOrphans_ShutsDownUnreferenced_LeavesReferenced(t *testing.T) {
	bin := fakeNDJSONBin(t)
	runDir := t.TempDir()

	refSock, _, refDone, refPid := startRawAgentPtyHost(t, runDir, bin, "gc-repo", "gc-branch-ref", "claude:claude")
	orphSock, orphStatus, orphDone, orphPid := startRawAgentPtyHost(t, runDir, bin, "gc-repo", "gc-branch-orphan", "claude:claude")
	// Wait for BOTH raw ptyhost.Server goroutines to fully return before
	// t.TempDir()'s own cleanup runs RemoveAll on runDir — avoids a
	// "directory not empty" race under load, same rationale as claudetui's
	// analogous test.
	t.Cleanup(func() {
		shutdownRawAgentPtyHost(refSock)
		shutdownRawAgentPtyHost(orphSock)
		for _, d := range []chan struct{}{refDone, orphDone} {
			select {
			case <-d:
			case <-time.After(5 * time.Second):
				t.Log("raw agent ptyhost did not fully tear down within 5s during cleanup")
			}
		}
	})

	mgr := newTestManagerForGC(t, bin, runDir)

	isLive := func(repoID, branchID, tabID string) bool {
		return repoID == "gc-repo" && branchID == "gc-branch-ref" && tabID == "claude:claude"
	}

	shutdown, _, err := mgr.GCOrphans(context.Background(), isLive)
	if err != nil {
		t.Fatalf("GCOrphans: %v", err)
	}
	if shutdown != 1 {
		t.Fatalf("shutdown = %d, want 1 (only the orphan)", shutdown)
	}

	// Orphan: SHUTDOWN sent, child terminated. The ptyhost's own teardown
	// then removes its .sock (its .json final-status write races our own
	// cleanup, so cleanup is deliberately deferred to a LATER tick — see
	// [Manager.GCOrphans]'s doc comment).
	select {
	case <-orphDone:
	case <-time.After(5 * time.Second):
		t.Fatal("orphan ptyhost Run() did not return after orphan GC SHUTDOWN")
	}
	if agentPidAlive(orphPid) {
		t.Errorf("orphan child pid %d still alive after orphan GC SHUTDOWN", orphPid)
	}

	// A SECOND tick observes the now-dead orphan (no socket, or dead
	// pid/unreachable) and self-heals by pruning the leftover files.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, cleaned, err2 := mgr.GCOrphans(context.Background(), isLive)
		if err2 != nil {
			t.Fatalf("GCOrphans (cleanup tick): %v", err2)
		}
		_, sockErr := os.Stat(orphSock)
		_, statusErr := os.Stat(orphStatus)
		if os.IsNotExist(sockErr) && os.IsNotExist(statusErr) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("orphan files not cleaned up after a follow-up GC tick (cleaned=%d): sock err=%v status err=%v", cleaned, sockErr, statusErr)
		}
		time.Sleep(30 * time.Millisecond)
	}

	// Referenced ptyhost must survive UNTOUCHED across several more ticks —
	// never dialed at all (skipLive-before-dial), never shut down.
	for i := 0; i < 3; i++ {
		shutdown2, _, err2 := mgr.GCOrphans(context.Background(), isLive)
		if err2 != nil {
			t.Fatalf("GCOrphans tick %d: %v", i, err2)
		}
		if shutdown2 != 0 {
			t.Fatalf("tick %d: referenced entry was shut down (shutdown=%d) — false GC", i, shutdown2)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !agentPidAlive(refPid) {
		t.Fatal("referenced ptyhost pid died unexpectedly across GC ticks")
	}
	conn, ok := probeExisting(refSock)
	if !ok {
		t.Fatal("referenced ptyhost socket no longer listening after several GC ticks")
	}
	pc := &PipeClient{conn: conn}
	hello, herr := pc.Hello()
	_ = conn.Close()
	if herr != nil {
		t.Fatalf("HELLO to referenced ptyhost after GC ticks: %v", herr)
	}
	if hello.Pid != refPid {
		t.Fatalf("referenced ptyhost pid changed across GC ticks: got %d, want %d", hello.Pid, refPid)
	}
}

// TestGCOrphans_IDContainingDoubleUnderscore_SurvivesGC is AC-S64c835-3-2's
// regression guard, mirroring claudetui's identically-named test: a repoID
// (or branchID) that itself contains the literal substring "__" must still
// round-trip through the ptyhost status file exactly, so GC correctly
// recognizes a LIVE, referenced tab as live and NEVER shuts it down. This
// proves identity comes from the explicit status-file fields, not from
// splitting a delimited Seed string (which would mis-attribute the ptyhost
// to the wrong tuple and — since GCOrphans SHUTDOWNs everything it does not
// recognize as live — silently kill the user's running claude).
func TestGCOrphans_IDContainingDoubleUnderscore_SurvivesGC(t *testing.T) {
	bin := fakeNDJSONBin(t)
	runDir := t.TempDir()

	// A realistic repo dir like github.com/org/my__tool yields this repoID.
	const (
		repoID   = "my__tool--ab12"
		branchID = "main--cd34"
		tabID    = "claude:claude"
	)

	sock, _, done, pid := startRawAgentPtyHost(t, runDir, bin, repoID, branchID, tabID)
	t.Cleanup(func() {
		shutdownRawAgentPtyHost(sock)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("raw agent ptyhost did not fully tear down within 5s during cleanup")
		}
	})

	mgr := newTestManagerForGC(t, bin, runDir)

	// isLive is the EXACT-tuple predicate a real store.Tab lookup provides:
	// it returns true ONLY for the genuine (repoID, branchID, tabID). A
	// misparse that hands GC a wrongly-split tuple would make this return
	// false → GC would (wrongly) shut the tab down.
	isLive := func(r, b, tb string) bool {
		return r == repoID && b == branchID && tb == tabID
	}

	// Several GC ticks: the referenced live tab must be recognized as live
	// (via exact identity round-trip) and left completely untouched every
	// time — never dialed, never shut down.
	for i := 0; i < 3; i++ {
		shutdown, _, err := mgr.GCOrphans(context.Background(), isLive)
		if err != nil {
			t.Fatalf("GCOrphans tick %d: %v", i, err)
		}
		if shutdown != 0 {
			t.Fatalf("tick %d: GC shut down %d ptyhost(s) — a LIVE referenced tab whose repoID contains \"__\" was misparsed and killed (data-loss regression)", i, shutdown)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Positively confirm the ptyhost is still alive, still listening, same pid.
	if !agentPidAlive(pid) {
		t.Fatal("referenced ptyhost (repoID with \"__\") pid died across GC ticks")
	}
	conn, ok := probeExisting(sock)
	if !ok {
		t.Fatal("referenced ptyhost (repoID with \"__\") socket no longer listening after GC ticks — it was wrongly reaped")
	}
	pc := &PipeClient{conn: conn}
	hello, herr := pc.Hello()
	_ = conn.Close()
	if herr != nil {
		t.Fatalf("HELLO to referenced ptyhost after GC ticks: %v", herr)
	}
	if hello.Pid != pid {
		t.Fatalf("referenced ptyhost pid changed across GC ticks: got %d, want %d", hello.Pid, pid)
	}

	// And the status file the fix relies on must carry the identity EXACTLY
	// (not a "__"-mangled version) — a direct assertion the round-trip is
	// lossless.
	statusPath := ptyhost.StatusPath(runDir, repoID+"__"+branchID+"__"+tabID)
	sf, rerr := ptyhost.ReadStatusFile(statusPath)
	if rerr != nil {
		t.Fatalf("ReadStatusFile: %v", rerr)
	}
	if sf.RepoID != repoID || sf.BranchID != branchID || sf.TabID != tabID {
		t.Fatalf("status file identity mangled: got (%q,%q,%q), want (%q,%q,%q)",
			sf.RepoID, sf.BranchID, sf.TabID, repoID, branchID, tabID)
	}
}

// TestGCOrphans_NilIsLive_IsNoOp verifies the documented defensive
// behaviour: a nil isLive (the shape callers get when SetAgentOrphanGC was
// never wired) must never dial or touch anything, even when orphaned
// ptyhosts are present on disk.
func TestGCOrphans_NilIsLive_IsNoOp(t *testing.T) {
	bin := fakeNDJSONBin(t)
	runDir := t.TempDir()

	sock, _, done, _ := startRawAgentPtyHost(t, runDir, bin, "noop-repo", "noop-branch", "claude:claude")
	t.Cleanup(func() {
		shutdownRawAgentPtyHost(sock)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})

	mgr := newTestManagerForGC(t, bin, runDir)

	shutdown, cleaned, err := mgr.GCOrphans(context.Background(), nil)
	if err != nil {
		t.Fatalf("GCOrphans(nil): %v", err)
	}
	if shutdown != 0 || cleaned != 0 {
		t.Fatalf("GCOrphans(nil) = (shutdown=%d, cleaned=%d), want (0, 0)", shutdown, cleaned)
	}
	if _, ok := probeExisting(sock); !ok {
		t.Fatal("GCOrphans(nil) touched the ptyhost — should be a complete no-op")
	}
}

// stubBranchResolver is a minimal BranchResolver for tests that construct a
// Manager directly (GCOrphans doesn't exercise EnsureAgent, but NewManager
// requires a non-nil resolver).
type stubBranchResolver struct{}

func (stubBranchResolver) WorktreePath(string, string) (string, error) { return "", nil }

// newTestManagerForGC builds a bare Manager wired ONLY for GCOrphans tests:
// a real on-disk *Store (NewStore requires a writable dir), a
// stubBranchResolver (GCOrphans never calls EnsureAgent), and
// RunDirOverride pinned to runDir so [Manager.RunDir] agrees with where
// startRawAgentPtyHost placed its sockets.
func newTestManagerForGC(t *testing.T, bin, runDir string) *Manager {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return NewManager(Config{Binary: bin, RunDirOverride: runDir}, st, stubBranchResolver{}, nil, nil, nil)
}
