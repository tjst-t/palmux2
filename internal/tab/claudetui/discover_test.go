package claudetui

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tjst-t/palmux2/internal/ptyhost"
)

// This file covers Sprint S3f2658 Story 3's Go-level acceptance scenarios:
//   - AC-S3f2658-3-1: startup discovery adopts live ptyhosts (attach, no
//     respawn) and cleans dead/unreachable socket+status pairs.
//   - AC-S3f2658-3-2: orphan GC (the store's 10s scan-loop piggyback) shuts
//     down a ptyhost with no matching tab, and leaves a referenced one alone
//     across several ticks.

// startRawPtyHost starts a REAL [ptyhost.Server] (holding a real fake_claude
// child) at the deterministic socket/status path for seed under runDir, with
// the EXPLICIT RepoID/BranchID/TabID Config fields populated (S3f2658-3 — the
// authoritative, unambiguous identity discovery/GC reads; NOT the Seed
// label, which is not reliably parseable when an ID contains "__"). The
// seed (socket/status filename hash source) is derived the SAME way
// production's [Daemon.ptyHostSeed] does. Returns the paths, a channel
// closed when Run() returns, and the child pid (via a throwaway HELLO probe).
func startRawPtyHost(t *testing.T, runDir, bin, repoID, branchID, tabID string, extraArgs ...string) (sockPath, statusPath string, done chan struct{}, pid int) {
	t.Helper()
	seed := repoID + "__" + branchID + "__" + tabID
	sockPath = ptyhost.SocketPath(runDir, seed)
	statusPath = ptyhost.StatusPath(runDir, seed)
	srv, err := ptyhost.NewServer(ptyhost.Config{
		Argv:       append([]string{bin}, extraArgs...),
		SocketPath: sockPath,
		StatusPath: statusPath,
		RingSize:   1 << 16,
		Seed:       seed,
		RepoID:     repoID,
		BranchID:   branchID,
		TabID:      tabID,
	})
	if err != nil {
		t.Fatalf("ptyhost.NewServer(%s): %v", seed, err)
	}
	done = make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Run(context.Background())
	}()
	if err := waitForSocket(context.Background(), sockPath, 5*time.Second, nil); err != nil {
		t.Fatalf("ptyhost %s never started listening: %v", seed, err)
	}
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial %s: %v", seed, err)
	}
	hello, herr := sendHello(conn)
	_ = conn.Close()
	if herr != nil {
		t.Fatalf("HELLO to %s: %v", seed, herr)
	}
	return sockPath, statusPath, done, hello.Pid
}

// shutdownRawPtyHost is a best-effort cleanup helper (t.Cleanup) that sends
// SHUTDOWN to a still-listening socket. Safe to call on an already-gone
// socket (probeExisting just reports false).
func shutdownRawPtyHost(sockPath string) {
	if conn, ok := probeExisting(sockPath); ok {
		_ = ptyhost.WriteFrame(conn, ptyhost.MsgShutdown, ptyhost.EncodeShutdown(ptyhost.ShutdownPayload{GraceMillis: 200}))
		_ = conn.Close()
	}
}

// writeDeadPidStatus writes a syntactically valid StatusFile whose Pid is
// guaranteed to no longer exist (a just-reaped `true` child) with a valid
// explicit identity — simulating a ptyhost that was hard-killed (SIGKILL,
// e.g. an OOM or `kill -9`) before its own teardown could remove its files.
func writeDeadPidStatus(t *testing.T, statusPath, repoID, branchID, tabID string) {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running throwaway `true`: %v", err)
	}
	deadPid := cmd.Process.Pid
	sf := ptyhost.StatusFile{
		Pid:      deadPid,
		Mode:     "pty",
		Alive:    true, // as if teardown never got to update this
		Seed:     repoID + "__" + branchID + "__" + tabID,
		RepoID:   repoID,
		BranchID: branchID,
		TabID:    tabID,
	}
	writeStatusFileForTest(t, statusPath, sf)
}

func writeStatusFileForTest(t *testing.T, path string, sf ptyhost.StatusFile) {
	t.Helper()
	b, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		t.Fatalf("marshal status file: %v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write status file %s: %v", path, err)
	}
}

// TestDiscoverAndRestore_AdoptsLiveCleansStale is AC-S3f2658-3-1.
func TestDiscoverAndRestore_AdoptsLiveCleansStale(t *testing.T) {
	bin := fakeBin(t)
	runDir := t.TempDir()
	ctx := context.Background()

	type spec struct{ repoID, branchID, tabID string }
	specs := []spec{
		{"disc-repo-1", "disc-branch-1", "claude:claude"},
		{"disc-repo-2", "disc-branch-2", "claude:claude"},
	}
	pidsBefore := make(map[string]int, len(specs))
	var socks []string
	var dones []chan struct{}
	for _, sp := range specs {
		key := sp.repoID + "/" + sp.branchID + "/" + sp.tabID
		sock, _, done, pid := startRawPtyHost(t, runDir, bin, sp.repoID, sp.branchID, sp.tabID)
		pidsBefore[key] = pid
		socks = append(socks, sock)
		dones = append(dones, done)
	}
	// Wait for each raw ptyhost.Server's Run() to FULLY return (not just for
	// mgr.ShutdownAll to observe the client-side connection loss) before
	// t.TempDir()'s own cleanup runs RemoveAll on runDir — otherwise, under
	// heavy system load, the server's still-finishing internal teardown can
	// race t.TempDir()'s directory walk ("directory not empty").
	t.Cleanup(func() {
		for _, s := range socks {
			shutdownRawPtyHost(s)
		}
		for _, d := range dones {
			select {
			case <-d:
			case <-time.After(5 * time.Second):
				t.Log("raw ptyhost did not fully tear down within 5s during cleanup")
			}
		}
	})

	// STALE 1: dead pid (simulates a hard-killed ptyhost whose own teardown
	// never ran, so its .sock+.json linger).
	staleSeed := "stale-repo__stale-branch__claude:claude"
	staleSock := ptyhost.SocketPath(runDir, staleSeed)
	staleStatus := ptyhost.StatusPath(runDir, staleSeed)
	if err := os.WriteFile(staleSock, []byte("not a real socket"), 0o600); err != nil {
		t.Fatalf("write stale sock placeholder: %v", err)
	}
	writeDeadPidStatus(t, staleStatus, "stale-repo", "stale-branch", "claude:claude")

	// STALE 2: socket present but refuses connections (not actually a unix
	// socket — a HELLO can never succeed), with a status file reporting a
	// definitely-alive pid (this test process itself) so only the
	// dial/HELLO check catches it.
	unreachSeed := "unreach-repo__unreach-branch__claude:claude"
	unreachSock := ptyhost.SocketPath(runDir, unreachSeed)
	unreachStatus := ptyhost.StatusPath(runDir, unreachSeed)
	if err := os.WriteFile(unreachSock, []byte("not a real socket"), 0o600); err != nil {
		t.Fatalf("write unreachable sock placeholder: %v", err)
	}
	writeStatusFileForTest(t, unreachStatus, ptyhost.StatusFile{
		Pid: os.Getpid(), Mode: "pty", Alive: true, Seed: unreachSeed,
		RepoID: "unreach-repo", BranchID: "unreach-branch", TabID: "claude:claude",
	})

	mgr := NewManager(ManagerConfig{ClaudeBin: bin, RingSize: 1 << 16, RunDirOverride: runDir})
	t.Cleanup(func() { _ = mgr.ShutdownAll(ctx) })

	adopted, cleaned, err := DiscoverAndRestore(ctx, mgr, nil, nil)
	if err != nil {
		t.Fatalf("DiscoverAndRestore: %v", err)
	}
	if adopted != 2 {
		t.Fatalf("adopted = %d, want 2", adopted)
	}
	if cleaned != 2 {
		t.Fatalf("cleaned = %d, want 2", cleaned)
	}

	// Both live ptyhosts must be re-adopted as Daemon entries, ATTACHED (not
	// respawned — same pid as before discovery ran).
	var d0 *Daemon
	for _, sp := range specs {
		key := sp.repoID + "/" + sp.branchID + "/" + sp.tabID
		d := mgr.Get(sp.repoID, sp.branchID, sp.tabID)
		if d == nil {
			t.Fatalf("no Daemon adopted for %+v", sp)
		}
		waitForState(t, d, StateRunning, 5*time.Second)
		got := d.CurrentStats().PID
		if got != pidsBefore[key] {
			t.Fatalf("%+v: adopted pid = %d, want %d (the pre-existing ptyhost's pid) — discovery spawned a NEW ptyhost instead of attaching", sp, got, pidsBefore[key])
		}
		if d0 == nil {
			d0 = d
		}
	}

	// Stale entries must be removed from disk.
	for _, p := range []string{staleSock, staleStatus, unreachSock, unreachStatus} {
		if _, serr := os.Stat(p); !os.IsNotExist(serr) {
			t.Errorf("expected %s removed by discovery cleanup, stat err = %v", p, serr)
		}
	}

	// A subsequent WS attach to a re-adopted tab must replay the ring
	// (screen restore — ties to AC-S3f2658-2-2).
	ts := httptest.NewServer(AttachHandler(d0))
	defer ts.Close()
	wsURL := "ws" + ts.URL[len("http"):]
	wsCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(wsCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()
	_, got, err := conn.Read(wsCtx)
	if err != nil {
		t.Fatalf("ws read snapshot: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("WS client received an empty initial snapshot after re-adoption")
	}
	if !bytes.Contains(got, []byte("Claude")) && !bytes.Contains(got, []byte("fake_claude")) {
		t.Errorf("re-adopted tab's WS snapshot does not look like the ptyhost's rendered content: %q", got[:min(len(got), 300)])
	}
}

// TestGCOrphans_ShutsDownUnreferenced_LeavesReferenced is AC-S3f2658-3-2.
func TestGCOrphans_ShutsDownUnreferenced_LeavesReferenced(t *testing.T) {
	bin := fakeBin(t)
	runDir := t.TempDir()

	refSock, _, refDone, refPid := startRawPtyHost(t, runDir, bin, "gc-repo", "gc-branch-ref", "claude:claude")
	orphSock, orphStatus, orphDone, orphPid := startRawPtyHost(t, runDir, bin, "gc-repo", "gc-branch-orphan", "claude:claude")
	// Wait for BOTH raw ptyhost.Server goroutines to fully return before
	// t.TempDir()'s own cleanup runs RemoveAll on runDir — see the analogous
	// comment in TestDiscoverAndRestore_AdoptsLiveCleansStale.
	t.Cleanup(func() {
		shutdownRawPtyHost(refSock)
		shutdownRawPtyHost(orphSock)
		for _, d := range []chan struct{}{refDone, orphDone} {
			select {
			case <-d:
			case <-time.After(5 * time.Second):
				t.Log("raw ptyhost did not fully tear down within 5s during cleanup")
			}
		}
	})

	mgr := NewManager(ManagerConfig{ClaudeBin: bin, RunDirOverride: runDir})

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
	if pidAlive(orphPid) {
		t.Errorf("orphan child pid %d still alive after orphan GC SHUTDOWN", orphPid)
	}

	// A SECOND tick observes the now-dead orphan (no socket, or dead
	// pid/unreachable) and self-heals by pruning the leftover files —
	// "socket + json are cleaned up" (AC-S3f2658-3-2), just not
	// synchronously with the tick that sent SHUTDOWN.
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

	// Referenced ptyhost must survive UNTOUCHED across several more ticks.
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
	if !pidAlive(refPid) {
		t.Fatal("referenced ptyhost pid died unexpectedly across GC ticks")
	}
	conn, ok := probeExisting(refSock)
	if !ok {
		t.Fatal("referenced ptyhost socket no longer listening after several GC ticks")
	}
	hello, herr := sendHello(conn)
	_ = conn.Close()
	if herr != nil {
		t.Fatalf("HELLO to referenced ptyhost after GC ticks: %v", herr)
	}
	if hello.Pid != refPid {
		t.Fatalf("referenced ptyhost pid changed across GC ticks: got %d, want %d", hello.Pid, refPid)
	}
}

// TestGCOrphans_IDContainingDoubleUnderscore_SurvivesGC is the regression
// guard for the S3f2658-3 orphan-GC data-loss bug: a repoID (or branchID)
// that itself contains the literal substring "__" must still round-trip
// through the ptyhost status file exactly, so GC correctly recognizes a
// LIVE, referenced tab as live and NEVER shuts it down.
//
// Before the fix, discovery parsed identity by splitting the "__"-joined
// Seed with strings.SplitN(seed, "__", 3): for repoID "my__tool--ab12" the
// seed "my__tool--ab12__main--cd34__claude" split to the WRONG tuple
// (repoID="my", branchID="tool--ab12", tabID="main--cd34__claude"), so the
// store's isLive lookup for the REAL ("my__tool--ab12", "main--cd34",
// "claude") tuple returned false, skipLive didn't skip, and GC dialed +
// SHUTDOWN'd the user's running claude on the very first tick. The fix
// carries identity as explicit status-file fields (no join, no parse), so
// this test fails against the old splitSeed logic and passes with the fix.
func TestGCOrphans_IDContainingDoubleUnderscore_SurvivesGC(t *testing.T) {
	bin := fakeBin(t)
	runDir := t.TempDir()

	// A realistic repo dir like github.com/org/my__tool yields this repoID.
	const (
		repoID   = "my__tool--ab12"
		branchID = "main--cd34"
		tabID    = "claude"
	)

	sock, _, done, pid := startRawPtyHost(t, runDir, bin, repoID, branchID, tabID)
	t.Cleanup(func() {
		shutdownRawPtyHost(sock)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("raw ptyhost did not fully tear down within 5s during cleanup")
		}
	})

	mgr := NewManager(ManagerConfig{ClaudeBin: bin, RunDirOverride: runDir})

	// isLive is the EXACT-tuple predicate a real store.Tab lookup provides:
	// it returns true ONLY for the genuine (repoID, branchID, tabID). A
	// misparse that hands GC ("my", "tool--ab12", "main--cd34__claude")
	// would make this return false → GC would (wrongly) shut the tab down.
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
	if !pidAlive(pid) {
		t.Fatal("referenced ptyhost (repoID with \"__\") pid died across GC ticks")
	}
	conn, ok := probeExisting(sock)
	if !ok {
		t.Fatal("referenced ptyhost (repoID with \"__\") socket no longer listening after GC ticks — it was wrongly reaped")
	}
	hello, herr := sendHello(conn)
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
