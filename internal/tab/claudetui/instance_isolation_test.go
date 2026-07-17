package claudetui

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
	"github.com/tjst-t/palmux2/internal/tab/agenttui"
)

// This file is the REAL-MACHINE half of AC-S3f2658-3-3 (instancePrefix
// separation): it launches REAL `palmux ptyhost` OS processes — via the same
// production spawn path (ADR-0003's Launcher: systemd-run --user --scope,
// falling back to setsid) that a genuine "host palmux2" vs
// "INSTANCE=dev palmux2" pair would use — under TWO different, test-unique
// instancePrefixes, and proves that a Manager scoped to ONE instancePrefix's
// startup discovery AND orphan GC never sees, adopts, or shuts down a
// ptyhost living under the OTHER instancePrefix's run directory.
//
// The instancePrefixes used here ("s3f2658-3-isoA-<pid>" /
// "s3f2658-3-isoB-<pid>") are deliberately NOT "_palmux_" or "_pmx_dev_" (the
// real production/dev-rig prefixes) and are further uniqued by this
// process's own pid — this test can never collide with, discover, or GC a
// real running host or INSTANCE=dev palmux2 on the same box (CLAUDE.md:
// "NEVER touch the host palmux2").

// buildRealPalmuxBinForIsolationTest builds the actual cmd/palmux binary
// (containing the `ptyhost` subcommand) so this test exercises the real
// end-to-end spawn path, not a stand-in. Mirrors
// internal/ptyhost's buildRealPalmuxBin (test-only helpers cannot be shared
// across packages).
func buildRealPalmuxBinForIsolationTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// internal/tab/claudetui -> repo root -> cmd/palmux
	repoRoot := filepath.Join(wd, "..", "..", "..")
	bin := filepath.Join(t.TempDir(), "palmux")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/palmux")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building cmd/palmux: %v\n%s", err, out)
	}
	return bin
}

// isolationHost is one REAL, production-spawned ptyhost used by this test.
type isolationHost struct {
	prefix     string
	seed       string
	sockPath   string
	statusPath string
	pid        int
}

// launchRealIsolationHost spawns a REAL `palmux ptyhost` process (production
// ADR-0003 spawn path — see [defaultLaunchPtyHost]) under instancePrefix,
// holding a real fake_claude child, and returns its identity + a HELLO-
// confirmed pid. The seed (socket/status filename source) is derived the
// SAME way production's [Daemon.ptyHostSeed] does; identity is carried
// in-band via the explicit RepoID/BranchID/TabID request fields.
func launchRealIsolationHost(t *testing.T, ctx context.Context, realBin, fakeBin, instancePrefix, repoID, branchID, tabID string) isolationHost {
	t.Helper()
	seed := repoID + "__" + branchID + "__" + tabID
	runDir := ptyhost.RunDir(instancePrefix)
	sockPath := ptyhost.SocketPath(runDir, seed)
	statusPath := ptyhost.StatusPath(runDir, seed)

	req := agenttui.PtyHostLaunchRequest{
		PalmuxBin:      realBin,
		InstancePrefix: instancePrefix,
		Seed:           seed,
		RepoID:         repoID,
		BranchID:       branchID,
		TabID:          tabID,
		SocketPath:     sockPath,
		StatusPath:     statusPath,
		Argv:           []string{fakeBin},
		RingSize:       1 << 16,
	}
	if err := agenttui.DefaultLaunchPtyHost(ctx, req); err != nil {
		t.Fatalf("launch real ptyhost (prefix=%s seed=%s): %v", instancePrefix, seed, err)
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial real ptyhost (prefix=%s): %v", instancePrefix, err)
	}
	hello, herr := agenttui.SendHello(conn)
	_ = conn.Close()
	if herr != nil {
		t.Fatalf("HELLO real ptyhost (prefix=%s): %v", instancePrefix, herr)
	}
	return isolationHost{prefix: instancePrefix, seed: seed, sockPath: sockPath, statusPath: statusPath, pid: hello.Pid}
}

// isolationHostAlive dials h.sockPath directly to confirm liveness. ONLY
// safe to call on a ptyhost NO Manager/Daemon has adopted yet (an orphan) —
// a ptyhost tolerates exactly one active connection at a time
// (ptyhost.Server.replaceConn), so dialing an ALREADY-adopted one would
// itself evict the adopting Daemon's real connection, manufacturing a false
// "died unexpectedly". For an adopted host, check via its Daemon's own
// CurrentStats() instead (see the test body).
func isolationHostAlive(t *testing.T, h isolationHost) (alive bool, pid int) {
	t.Helper()
	conn, err := net.Dial("unix", h.sockPath)
	if err != nil {
		return false, 0
	}
	defer func() { _ = conn.Close() }()
	hello, herr := agenttui.SendHello(conn)
	if herr != nil {
		return false, 0
	}
	return true, hello.Pid
}

func shutdownIsolationHost(h isolationHost) {
	shutdownRawPtyHost(h.sockPath)
}

// reapAndAssertNoLeak is TestParallelInstances_NeverClaimOrGCEachOther's
// [AC-S64c835-2-1] cleanup: EVERY real detached ptyhost (+ its real child)
// this test spawns via [launchRealIsolationHost] must be deterministically
// gone by the time the test finishes — not just "asked nicely to shut down
// and hoped". Register this as the FIRST t.Cleanup in the test (Cleanup runs
// LIFO, so registering it first makes it execute LAST, after every other
// cleanup in the test — mgrA/mgrB.ShutdownAll, the mid-test GCOrphans calls,
// the per-host best-effort SHUTDOWN sends — has already had its chance):
//
//  1. Sends (or re-sends) a graceful SHUTDOWN to every tracked host's socket
//     (harmless no-op if it was already shut down by production code paths
//     exercised earlier in the test body — see [probeExisting]).
//  2. Polls each host's captured child pid (returned by HELLO —
//     [ptyhost.HelloPayload.Pid], the actual spawned child, not the detached
//     `palmux ptyhost` supervisor's own pid) for exit, bounded.
//  3. Force-SIGKILLs any pid that survives the grace window — a deterministic
//     backstop so a bug in the graceful SHUTDOWN path (the ptyhost's own
//     SIGTERM→SIGKILL escalation, or a lost/delayed SHUTDOWN frame under
//     heavy CPU contention) can never leak a real OS process out of this
//     test's run. Test failure (not a silent swallow) if even the backstop
//     doesn't reap it.
//  4. As a final, independent confirmation — catching anything the pid-based
//     poll could miss, in particular the DETACHED ptyhost SUPERVISOR process
//     itself (this test never captures ITS pid, only its child's, via
//     HELLO) — pgreps for both this test's uniquely t.TempDir()-pathed
//     realBin and fakeClaudeBin. Both paths are unique to THIS test
//     invocation, so a match can only be a process this test itself spawned
//     (never a concurrently running instance of the same test, a real host
//     palmux2, or anything else on the box). Fails loudly on any match —
//     backlog #4 was ~63 ptyhosts leaking silently under repeated/loaded
//     runs because nothing ever asserted this.
func reapAndAssertNoLeak(t *testing.T, launched *[]isolationHost, realBin, fakeClaudeBin string) {
	t.Helper()
	for _, h := range *launched {
		shutdownIsolationHost(h)
	}
	const pollInterval = 50 * time.Millisecond
	const graceWindow = 5 * time.Second
	const killWindow = 3 * time.Second
	for _, h := range *launched {
		deadline := time.Now().Add(graceWindow)
		for agenttui.PidAlive(h.pid) && time.Now().Before(deadline) {
			time.Sleep(pollInterval)
		}
		if !agenttui.PidAlive(h.pid) {
			continue
		}
		t.Logf("ptyhost child pid %d (prefix=%s) still alive %s after graceful SHUTDOWN — force-killing as deterministic backstop [AC-S64c835-2-1]", h.pid, h.prefix, graceWindow)
		if proc, err := os.FindProcess(h.pid); err == nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
		killDeadline := time.Now().Add(killWindow)
		for agenttui.PidAlive(h.pid) && time.Now().Before(killDeadline) {
			time.Sleep(pollInterval)
		}
		if agenttui.PidAlive(h.pid) {
			t.Errorf("[AC-S64c835-2-1] ptyhost child pid %d (prefix=%s) still alive after graceful SHUTDOWN + SIGKILL backstop — leaked process", h.pid, h.prefix)
		}
	}

	// Independent, path-based confirmation — catches a leaked ptyhost
	// SUPERVISOR process too (its own pid is never captured above), and
	// serves as the ultimate "0 leaked" evidence this AC requires.
	assertNoProcessMatches(t, realBin)
	assertNoProcessMatches(t, fakeClaudeBin)
}

// assertNoProcessMatches fails t if any running process's cmdline still
// matches pattern (via `pgrep -f`). pattern is expected to be a unique,
// per-test t.TempDir()-rooted binary path, so a match can only be a process
// THIS test itself spawned.
func assertNoProcessMatches(t *testing.T, pattern string) {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", pattern).CombinedOutput()
	if err == nil {
		t.Errorf("[AC-S64c835-2-1] leaked process(es) still matching %q after cleanup:\n%s", pattern, out)
		return
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		// pgrep exits 1 when nothing matches — the expected, successful case.
		return
	}
	// pgrep itself failing to run (e.g. not installed) is a probe failure,
	// not evidence of a leak — log it but don't fail the test on it.
	t.Logf("pgrep -f %q: %v (output: %s) — treating as a non-fatal probe failure, not leak evidence", pattern, err, out)
}

// isoReport is written to docs/sprint-logs/S3f2658/e2e-S3f2658-3.json —
// this Sprint's convention (see e2e-S3f2658-2.json) for recording a
// real-machine acceptance run outside the Go test-pass/fail signal alone.
type isoReport struct {
	Task                string    `json:"task"`
	StartedAt           time.Time `json:"startedAt"`
	InstancePrefixA     string    `json:"instancePrefixA"`
	InstancePrefixB     string    `json:"instancePrefixB"`
	RunDirA             string    `json:"runDirA"`
	RunDirB             string    `json:"runDirB"`
	RunDirsDiffer       bool      `json:"runDirsDiffer"`
	DiscoveryAAdopted   int       `json:"discoveryA_adopted"`
	DiscoveryACleaned   int       `json:"discoveryA_cleaned"`
	DiscoveryBAdopted   int       `json:"discoveryB_adopted"`
	DiscoveryBCleaned   int       `json:"discoveryB_cleaned"`
	BSurvivedADiscovery bool      `json:"bSurvivedADiscovery"`
	ASurvivedBDiscovery bool      `json:"aSurvivedBDiscovery"`
	GCAShutdownCount    int       `json:"gcA_shutdownCount"`
	BSurvivedAGC        bool      `json:"bSurvivedAGC"`
	GCBShutdownCount    int       `json:"gcB_shutdownCount"`
	Verdict             string    `json:"verdict"`
	FinishedAt          time.Time `json:"finishedAt"`
}

// TestParallelInstances_NeverClaimOrGCEachOther is the real-machine half of
// AC-S3f2658-3-3.
func TestParallelInstances_NeverClaimOrGCEachOther(t *testing.T) {
	requireSurvivalSmoke(t)
	if os.Getenv("CI") == "" {
		// Still runs by default (priority_rule 0 — execute, don't skip) but
		// this comment documents intent: this test spawns real detached OS
		// processes via systemd-run/setsid and is slower than a unit test.
		t.Log("real-machine instance-isolation test: spawns real detached ptyhost processes")
	}
	report := isoReport{Task: "AC-S3f2658-3-3", StartedAt: time.Now()}
	reportPath := isoReportPath(t)

	writeReport := func(verdict string) {
		report.Verdict = verdict
		report.FinishedAt = time.Now()
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Logf("marshal isolation report: %v", err)
			return
		}
		if err := os.WriteFile(reportPath, b, 0o644); err != nil {
			t.Logf("write isolation report %s: %v", reportPath, err)
		}
	}

	realBin := buildRealPalmuxBinForIsolationTest(t)
	fakeClaudeBin := fakeBin(t)
	ctx := context.Background()

	// launched tracks EVERY real detached ptyhost this test spawns via
	// launchRealIsolationHost — see [reapAndAssertNoLeak] ([AC-S64c835-2-1]).
	// Registered FIRST so (Cleanup runs LIFO) it executes LAST, after every
	// other cleanup below has had its chance to gracefully shut its own
	// entries down first.
	var launched []isolationHost
	t.Cleanup(func() { reapAndAssertNoLeak(t, &launched, realBin, fakeClaudeBin) })

	uniq := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	prefixA := "s3f2658-3-isoA-" + uniq
	prefixB := "s3f2658-3-isoB-" + uniq
	report.InstancePrefixA = prefixA
	report.InstancePrefixB = prefixB
	report.RunDirA = ptyhost.RunDir(prefixA)
	report.RunDirB = ptyhost.RunDir(prefixB)
	report.RunDirsDiffer = report.RunDirA != report.RunDirB
	if !report.RunDirsDiffer {
		writeReport("FAIL")
		t.Fatalf("RunDir did not separate test prefixes: A=%s B=%s", report.RunDirA, report.RunDirB)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(report.RunDirA)
		_ = os.RemoveAll(report.RunDirB)
	})

	// ---- Phase 1: discovery isolation ----------------------------------
	hostA1 := launchRealIsolationHost(t, ctx, realBin, fakeClaudeBin, prefixA, "isoA-repo", "isoA-branch", "claude:claude")
	hostB1 := launchRealIsolationHost(t, ctx, realBin, fakeClaudeBin, prefixB, "isoB-repo", "isoB-branch", "claude:claude")
	launched = append(launched, hostA1, hostB1)

	mgrA := NewManager(ManagerConfig{ClaudeBin: fakeClaudeBin, PalmuxBin: realBin, InstancePrefix: prefixA, RingSize: 1 << 16})
	t.Cleanup(func() { _ = mgrA.ShutdownAll(ctx) })
	mgrB := NewManager(ManagerConfig{ClaudeBin: fakeClaudeBin, PalmuxBin: realBin, InstancePrefix: prefixB, RingSize: 1 << 16})
	t.Cleanup(func() { _ = mgrB.ShutdownAll(ctx) })

	adoptedA, cleanedA, err := DiscoverAndRestore(ctx, mgrA, nil, nil)
	if err != nil {
		writeReport("FAIL")
		t.Fatalf("instance A DiscoverAndRestore: %v", err)
	}
	report.DiscoveryAAdopted, report.DiscoveryACleaned = adoptedA, cleanedA
	if adoptedA != 1 {
		writeReport("FAIL")
		t.Fatalf("instance A adopted %d ptyhosts, want exactly 1 (its own) — instancePrefix isolation broken", adoptedA)
	}
	dA1 := mgrA.Get("isoA-repo", "isoA-branch", "claude:claude")
	if dA1 == nil || dA1.CurrentStats().PID != hostA1.pid {
		writeReport("FAIL")
		t.Fatalf("instance A did not attach to its own ptyhost (pid %d)", hostA1.pid)
	}

	// CRITICAL: instance B's ptyhost must be COMPLETELY untouched by A's
	// discovery pass. B has not been adopted by ANY Manager yet, so a raw
	// dial+HELLO is a safe, non-disturbing liveness probe here (once a
	// Manager DOES hold the connection, re-dialing would itself evict it —
	// see the isolationHostAlive doc comment).
	aliveB, pidB := isolationHostAlive(t, hostB1)
	report.BSurvivedADiscovery = aliveB && pidB == hostB1.pid
	if !report.BSurvivedADiscovery {
		writeReport("FAIL")
		t.Fatalf("instance B's ptyhost did not survive instance A's discovery pass: alive=%v pid=%d want=%d", aliveB, pidB, hostB1.pid)
	}

	adoptedB, cleanedB, err := DiscoverAndRestore(ctx, mgrB, nil, nil)
	if err != nil {
		writeReport("FAIL")
		t.Fatalf("instance B DiscoverAndRestore: %v", err)
	}
	report.DiscoveryBAdopted, report.DiscoveryBCleaned = adoptedB, cleanedB
	if adoptedB != 1 {
		writeReport("FAIL")
		t.Fatalf("instance B adopted %d ptyhosts, want exactly 1 (its own)", adoptedB)
	}
	dB1 := mgrB.Get("isoB-repo", "isoB-branch", "claude:claude")
	if dB1 == nil || dB1.CurrentStats().PID != hostB1.pid {
		writeReport("FAIL")
		t.Fatalf("instance B did not attach to its own ptyhost (pid %d)", hostB1.pid)
	}

	// CRITICAL: instance A's ptyhost — now ADOPTED (mgrA's Daemon dA1 holds
	// its one-and-only active connection) — must survive instance B's
	// discovery pass UNCHANGED. Check via dA1's OWN state, not a fresh raw
	// dial: dialing an already-adopted ptyhost's socket a second time would
	// itself evict dA1's connection (see docs/no-halt-agent-design.md §2 —
	// "socket は同時 1 接続で十分"; ptyhost.Server.replaceConn enforces this),
	// which is exactly the false-positive this test must not manufacture.
	report.ASurvivedBDiscovery = dA1.CurrentStats().Alive && dA1.CurrentStats().PID == hostA1.pid
	if !report.ASurvivedBDiscovery {
		writeReport("FAIL")
		t.Fatalf("instance A's adopted ptyhost did not survive instance B's discovery pass: stats=%+v want pid=%d", dA1.CurrentStats(), hostA1.pid)
	}

	// ---- Phase 2: orphan-GC isolation -----------------------------------
	// A fresh, un-adopted "orphan" ptyhost per instance (isLive reports
	// false only for THIS one — hostA1/hostB1, now adopted by mgrA/mgrB
	// respectively, are correctly reported live so GCOrphans's skipLive
	// path leaves them alone WITHOUT dialing them, exactly like a real
	// Store's isTuiTabLive would for a still-open tab).
	hostA2 := launchRealIsolationHost(t, ctx, realBin, fakeClaudeBin, prefixA, "isoA2-repo", "isoA2-branch", "claude:claude")
	hostB2 := launchRealIsolationHost(t, ctx, realBin, fakeClaudeBin, prefixB, "isoB2-repo", "isoB2-branch", "claude:claude")
	launched = append(launched, hostA2, hostB2)

	isLiveA := func(repoID, branchID, tabID string) bool {
		return repoID == "isoA-repo" && branchID == "isoA-branch" && tabID == "claude:claude"
	}
	isLiveB := func(repoID, branchID, tabID string) bool {
		return repoID == "isoB-repo" && branchID == "isoB-branch" && tabID == "claude:claude"
	}

	// B's GC pass (only B's hostB1 is "referenced") must reap ONLY hostB2,
	// and must never touch A's still-adopted/still-orphaned ptyhosts.
	shutdownB, _, err := mgrB.GCOrphans(ctx, isLiveB)
	if err != nil {
		writeReport("FAIL")
		t.Fatalf("instance B GCOrphans: %v", err)
	}
	report.GCBShutdownCount = shutdownB
	if shutdownB != 1 {
		writeReport("FAIL")
		t.Fatalf("instance B GCOrphans shut down %d ptyhosts, want exactly 1 (only its own orphan hostB2)", shutdownB)
	}
	// hostA2 is still un-adopted (orphan, nobody holds its connection) — a
	// raw dial is a safe liveness probe.
	aliveA2, pidA2 := isolationHostAlive(t, hostA2)
	if !aliveA2 || pidA2 != hostA2.pid {
		writeReport("FAIL")
		t.Fatalf("instance A's orphan ptyhost did not survive instance B's orphan GC: alive=%v pid=%d want=%d", aliveA2, pidA2, hostA2.pid)
	}
	// hostA1 is adopted by mgrA — check via dA1's own state.
	if !dA1.CurrentStats().Alive || dA1.CurrentStats().PID != hostA1.pid {
		writeReport("FAIL")
		t.Fatalf("instance A's adopted ptyhost did not survive instance B's orphan GC: stats=%+v want pid=%d", dA1.CurrentStats(), hostA1.pid)
	}
	report.BSurvivedAGC = true // records "A (both adopted and orphan hosts) survived B's GC pass"

	// A's GC pass (only A's hostA1 is "referenced") reaps ONLY hostA2 —
	// sanity that the mechanism actually fires, not just "coincidentally
	// never triggered".
	shutdownA, _, err := mgrA.GCOrphans(ctx, isLiveA)
	if err != nil {
		writeReport("FAIL")
		t.Fatalf("instance A GCOrphans: %v", err)
	}
	report.GCAShutdownCount = shutdownA
	if shutdownA != 1 {
		writeReport("FAIL")
		t.Fatalf("instance A GCOrphans shut down %d ptyhosts, want exactly 1 (only its own orphan hostA2)", shutdownA)
	}
	if !dB1.CurrentStats().Alive || dB1.CurrentStats().PID != hostB1.pid {
		writeReport("FAIL")
		t.Fatalf("instance B's adopted ptyhost did not survive instance A's orphan GC: stats=%+v want pid=%d", dB1.CurrentStats(), hostB1.pid)
	}

	writeReport("PASS")
	t.Logf("[AC-S3f2658-3-3] PASS — instancePrefix isolation confirmed: A/B run dirs differ (%s vs %s), each instance's discovery+GC only ever sees its own ptyhosts", report.RunDirA, report.RunDirB)
}

// isoReportPath returns docs/sprint-logs/S3f2658/e2e-S3f2658-3.json relative
// to the repo root.
func isoReportPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Join(wd, "..", "..", "..")
	return filepath.Join(repoRoot, "docs", "sprint-logs", "S3f2658", "e2e-S3f2658-3.json")
}
