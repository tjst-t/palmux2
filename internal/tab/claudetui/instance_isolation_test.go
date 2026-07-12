package claudetui

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
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
// confirmed pid.
func launchRealIsolationHost(t *testing.T, ctx context.Context, realBin, fakeBin, instancePrefix, seed string) isolationHost {
	t.Helper()
	runDir := ptyhost.RunDir(instancePrefix)
	sockPath := ptyhost.SocketPath(runDir, seed)
	statusPath := ptyhost.StatusPath(runDir, seed)

	req := PtyHostLaunchRequest{
		PalmuxBin:      realBin,
		InstancePrefix: instancePrefix,
		Seed:           seed,
		SocketPath:     sockPath,
		StatusPath:     statusPath,
		Argv:           []string{fakeBin},
		RingSize:       1 << 16,
	}
	if err := defaultLaunchPtyHost(ctx, req); err != nil {
		t.Fatalf("launch real ptyhost (prefix=%s seed=%s): %v", instancePrefix, seed, err)
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial real ptyhost (prefix=%s): %v", instancePrefix, err)
	}
	hello, herr := sendHello(conn)
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
	hello, herr := sendHello(conn)
	if herr != nil {
		return false, 0
	}
	return true, hello.Pid
}

func shutdownIsolationHost(h isolationHost) {
	shutdownRawPtyHost(h.sockPath)
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
	seedA1 := "isoA-repo__isoA-branch__claude:claude"
	seedB1 := "isoB-repo__isoB-branch__claude:claude"
	hostA1 := launchRealIsolationHost(t, ctx, realBin, fakeClaudeBin, prefixA, seedA1)
	hostB1 := launchRealIsolationHost(t, ctx, realBin, fakeClaudeBin, prefixB, seedB1)
	t.Cleanup(func() { shutdownIsolationHost(hostA1) })
	t.Cleanup(func() { shutdownIsolationHost(hostB1) })

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
	seedA2 := "isoA2-repo__isoA2-branch__claude:claude"
	seedB2 := "isoB2-repo__isoB2-branch__claude:claude"
	hostA2 := launchRealIsolationHost(t, ctx, realBin, fakeClaudeBin, prefixA, seedA2)
	hostB2 := launchRealIsolationHost(t, ctx, realBin, fakeClaudeBin, prefixB, seedB2)
	t.Cleanup(func() { shutdownIsolationHost(hostA2) })
	t.Cleanup(func() { shutdownIsolationHost(hostB2) })

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
