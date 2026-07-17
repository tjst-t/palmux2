package agenttui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/runtime/incus"
)

// This file is the REAL-INCUS half of Sprint S3f2658 Story 4
// (docs/sprint-logs/S3f2658/scenario-S3f2658-4.json AC-S3f2658-4-1 /
// AC-S3f2658-4-2). It is NOT part of the default `go test ./...` run — it
// creates and destroys a REAL, uniquely-named throwaway incus container on
// whatever host runs it, which is too heavy/invasive to force on every
// contributor's machine or CI (this project's other real-incus verification
// similarly lives in tests/acceptance/*.py, run manually against a live
// host, never auto-triggered). Opt in explicitly:
//
//	PALMUX_REALINCUS_SMOKE=1 go test ./internal/tab/claudetui/ \
//	  -run TestRealIncus_InContainerProcessSurvivesRestartAndIsReaped -v
//
// Unlike the Story S4d8b1c/S52fc2c acceptance scripts (tests/acceptance/
// s4d8b1c_incontainer.py), which drive a REAL palmux2 HTTP/WS server and a
// REAL claude, this test deliberately does NOT: (a) it holds a CHEAP bash
// counter loop instead of claude — ptyhost is process-agnostic by design
// (ADR-0002), and this avoids API quota/auth entirely; (b) it calls the
// SAME production entry points a full palmux2 process would
// (incus.New(...).PTYCommand — the exact incus-exec argv assembly a real
// workspace uses — and [defaultLaunchPtyHost] — the exact ADR-0003
// systemd-run cgroup-escape spawn a real Daemon.launchAndAttach uses)
// directly, rather than driving them through a second full HTTP server
// instance. This is a deliberate substitution, not a shortcut: the
// hardcoded containerClaudeBin path
// (/home/ubuntu/.local/bin/claude) is BIND-MOUNTED FROM THE HOST
// (S8478ca/S4d8b1c) — invoking it for real would either burn API
// quota/auth against the real claude CLI or require writing over a
// path shared with the live host, which risks the very host palmux2
// this session runs under. Calling incus.PTYCommand/ptyhost.Launcher
// directly with a synthetic argv sidesteps both hazards while still
// exercising 100% real incus + real ADR-0003 spawn + real
// runtime.ContainerProcessKiller code — the exact primitives Task 1/2
// wire together (see spawnWithArgs / teardown / GCOrphans in
// daemon.go / discover.go). Task 1's argv-ASSEMBLY correctness (the
// --settings/--plugin-dir/--permission-mode injection, hardcoded
// containerClaudeBin substitution) is separately covered at the unit
// level by TestSpawnWithArgs_IncusWrapperHandedOpaquelyToPtyhost /
// TestDaemonInContainerInjectsPluginDir with a fake PTYCommander — this
// test's job is the REAL-incus-only claim: does the actual `incus exec
// -t` mechanism keep the container-side process alive independent of
// the local wrapper across a reconnect (AC-4-1), and does the real
// runtime.ContainerProcessKiller genuinely reap it (AC-4-2)?
func TestRealIncus_InContainerProcessSurvivesRestartAndIsReaped(t *testing.T) {
	if os.Getenv("PALMUX_REALINCUS_SMOKE") == "" {
		t.Skip("real-incus smoke — creates/destroys a throwaway incus container; set PALMUX_REALINCUS_SMOKE=1 to run (not part of default `go test ./...`, matching this project's other real-incus verification)")
	}
	if _, err := exec.LookPath("incus"); err != nil {
		t.Fatalf("[AC-S3f2658-4-1/2] escalate: no `incus` binary on PATH — cannot run the real-incus smoke: %v", err)
	}

	report := &realIncusReport{Task: "AC-S3f2658-4-1/AC-S3f2658-4-2", StartedAt: time.Now().UTC()}
	reportPath := realIncusReportPath(t)
	verdict := "FAILED (see test log)"
	// Registered FIRST so — t.Cleanup runs in LIFO order, like defer — this
	// runs LAST, after the container-delete-and-recheck cleanup below has
	// populated report.PostContainers/ExistingContainersUntouched. A plain
	// `defer` here would fire before ANY t.Cleanup (defers unwind the
	// function body; Cleanups run only once the test function has fully
	// returned), writing a stale report that never reflects the final
	// isolation check.
	t.Cleanup(func() {
		report.Verdict = verdict
		report.FinishedAt = time.Now().UTC()
		writeRealIncusReport(t, reportPath, report)
	})

	// --- Step 0: snapshot pre-existing containers — the hard isolation rule. ---
	before, err := incusContainerNames(t)
	if err != nil {
		t.Fatalf("[AC-S3f2658-4-1] escalate: `incus list` failed, cannot safely proceed: %v", err)
	}
	report.PreExistingContainers = before
	t.Logf("pre-existing containers (never touched): %v", before)

	inst := fmt.Sprintf("s3f2658-4-survtest-%s", strconv.FormatInt(time.Now().UnixNano(), 36))
	report.ThrowawayInstance = inst

	// --- Step 1: launch the throwaway container from the same palmux-ws image
	// production workspaces use. Cleanup ALWAYS runs (even on failure/panic via
	// t.Cleanup, registered before anything else that could fail). ---
	t.Cleanup(func() {
		// Best-effort: SHUTDOWN any live ptyhost first (also attempted inline
		// below), then always force-delete the throwaway container regardless
		// of how far the test got.
		out, derr := exec.Command("incus", "delete", "--force", inst).CombinedOutput()
		if derr != nil && !strings.Contains(string(out), "not found") && !strings.Contains(string(out), "doesn't exist") {
			t.Logf("cleanup: incus delete --force %s: %v\n%s", inst, derr, out)
		}
		after, aerr := incusContainerNames(t)
		if aerr != nil {
			t.Logf("cleanup: incus list (final check) failed: %v", aerr)
			return
		}
		report.PostContainers = after
		report.ExistingContainersUntouched = sameStringSet(before, after)
		if !report.ExistingContainersUntouched {
			t.Errorf("[SAFETY] pre-existing container set changed! before=%v after=%v", before, after)
		} else {
			t.Logf("confirmed: pre-existing container set unchanged (%v)", after)
		}
	})

	if out, err := exec.Command("incus", "launch", "palmux-ws", inst).CombinedOutput(); err != nil {
		t.Fatalf("[AC-S3f2658-4-1] escalate: `incus launch palmux-ws %s` failed (image missing/degraded incus?): %v\n%s", inst, err, out)
	}

	// Wait for the container to be exec-able as uid 1000 (network/agent
	// readiness varies).
	deadline := time.Now().Add(30 * time.Second)
	var ready bool
	for time.Now().Before(deadline) {
		if out, err := exec.Command("incus", "exec", inst, "--user", "1000", "--group", "1000", "--", "true").CombinedOutput(); err == nil {
			ready = true
			break
		} else {
			t.Logf("waiting for container readiness: %v\n%s", err, out)
		}
		time.Sleep(1 * time.Second)
	}
	if !ready {
		t.Fatalf("[AC-S3f2658-4-1] escalate: container %s never became exec-able within 30s", inst)
	}

	// --- Step 2: build the REAL incus-exec wrapper argv via the SAME
	// production runtime.PTYCommander code path daemon.spawnWithArgs uses
	// (Task 1: palmux2 assembles the argv; ptyhost holds it opaquely). ---
	rt := incus.New(runtime.Config{Kind: runtime.KindIncusContainer}, inst, nil, slog.Default())
	pc, ok := rt.(runtime.PTYCommander)
	if !ok {
		t.Fatal("incus.New did not return a runtime.PTYCommander — cannot build the exec wrapper")
	}
	killer, ok := rt.(runtime.ContainerProcessKiller)
	if !ok {
		t.Fatal("incus.New did not return a runtime.ContainerProcessKiller — cannot exercise AC-S3f2658-4-2")
	}

	// A CHEAP, distinctively-marked counter loop stands in for claude
	// (ADR-0002 process-agnostic; avoids API quota/auth AND the
	// host-bind-mounted containerClaudeBin hazard — see file doc comment).
	marker := fmt.Sprintf("s3f2658_4_marker_%s", strconv.FormatInt(time.Now().UnixNano(), 36))
	counterScript := fmt.Sprintf(`i=0; while true; do i=$((i+1)); echo "%s counter=$i"; sleep 0.2; done`, marker)
	wrapperArgv := []string{"/bin/bash", "-c", counterScript}

	cmd := pc.PTYCommand(context.Background(), wrapperArgv, runtime.PTYCommandOpts{})
	argv := append([]string{cmd.Path}, cmd.Args[1:]...)
	env := cmd.Env
	report.WrapperArgv = argv
	t.Logf("real incus-exec wrapper argv: %v", argv)

	// --- Step 3: hand the argv OPAQUELY to the REAL ptyhost Launcher — the
	// EXACT production spawn path (ADR-0003 systemd-run --user --scope
	// cgroup-escape) [defaultLaunchPtyHost] uses in daemon.go. ---
	realBin := buildRealPalmuxBinForIsolationTest(t)
	instancePrefix := "s3f2658-4-survtest"
	seed := "survtest-" + inst
	runDir := ptyhost.RunDir(instancePrefix)
	sockPath := ptyhost.SocketPath(runDir, seed)
	statusPath := ptyhost.StatusPath(runDir, seed)
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })

	req := PtyHostLaunchRequest{
		PalmuxBin:      realBin,
		InstancePrefix: instancePrefix,
		Seed:           seed,
		RepoID:         "s3f2658-4-repo",
		BranchID:       "s3f2658-4-branch",
		TabID:          "claude:claude",
		SocketPath:     sockPath,
		StatusPath:     statusPath,
		Argv:           argv,
		Env:            env,
		RingSize:       1 << 16,
	}
	ctx := context.Background()
	if err := DefaultLaunchPtyHost(ctx, req); err != nil {
		t.Fatalf("[AC-S3f2658-4-1] escalate: real ptyhost launch (holding incus-exec wrapper) failed: %v", err)
	}
	t.Cleanup(func() {
		if conn, derr := dialUnix(sockPath); derr == nil {
			_ = ptyhost.WriteFrame(conn, ptyhost.MsgShutdown, ptyhost.EncodeShutdown(ptyhost.ShutdownPayload{GraceMillis: 500}))
			_ = conn.Close()
		}
	})

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("[AC-S3f2658-4-1] escalate: dial real ptyhost: %v", err)
	}
	hello, herr := SendHello(conn)
	_ = conn.Close()
	if herr != nil {
		t.Fatalf("[AC-S3f2658-4-1] escalate: HELLO real ptyhost: %v", herr)
	}
	report.PtyhostHeldWrapperPid = hello.Pid
	t.Logf("ptyhost holds local incus-exec wrapper pid=%d", hello.Pid)

	// Confirm the container-side process is genuinely running.
	containerPidBefore, err := waitForContainerPid(t, inst, marker, 15*time.Second)
	if err != nil {
		t.Fatalf("[AC-S3f2658-4-1] escalate: container-side marker process never appeared: %v", err)
	}
	report.ContainerPidBeforeReconnect = containerPidBefore
	t.Logf("in-container marker process pid=%d", containerPidBefore)

	ringBefore, err := queryRingTotal(sockPath)
	if err != nil {
		t.Fatalf("queryRingTotal (before): %v", err)
	}

	// --- Step 4 (AC-S3f2658-4-1): simulate a palmux2 restart. The ptyhost
	// itself is NEVER killed by a palmux2 restart (that generic guarantee is
	// Story S3f2658-1's own SURVIVAL smoke, docs/sprint-logs/S3f2658/
	// survival-S3f2658-1.json) — what a restart actually does to a live
	// attach is: the CLIENT (palmux2) connection drops and a fresh palmux2
	// process reconnects. Reproduce exactly that: close this connection,
	// wait, then dial fresh — and assert BOTH the ptyhost-held wrapper pid
	// AND the in-container marker pid are UNCHANGED, with output having
	// continued (no gap) across the disconnected interval. ---
	time.Sleep(2 * time.Second) // let the "disconnected interval" actually elapse

	conn2, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("[AC-S3f2658-4-1] escalate: reconnect to ptyhost after simulated restart: %v", err)
	}
	hello2, herr := SendHello(conn2)
	_ = conn2.Close()
	if herr != nil {
		t.Fatalf("[AC-S3f2658-4-1] escalate: HELLO on reconnect: %v", herr)
	}
	report.PtyhostHeldWrapperPidAfter = hello2.Pid

	containerPidAfter, err := waitForContainerPid(t, inst, marker, 5*time.Second)
	if err != nil {
		t.Fatalf("[AC-S3f2658-4-1] escalate: in-container marker process gone after simulated restart: %v", err)
	}
	report.ContainerPidAfterReconnect = containerPidAfter

	ringAfter, err := queryRingTotal(sockPath)
	if err != nil {
		t.Fatalf("queryRingTotal (after): %v", err)
	}
	report.RingGrewAcrossReconnect = ringAfter > ringBefore

	survivalPass := hello.Pid == hello2.Pid && containerPidBefore == containerPidAfter && ringAfter > ringBefore
	report.Ac41Pass = survivalPass
	if !survivalPass {
		t.Fatalf("[AC-S3f2658-4-1] FAILED: wrapper pid %d->%d, container pid %d->%d, ring grew=%v",
			hello.Pid, hello2.Pid, containerPidBefore, containerPidAfter, report.RingGrewAcrossReconnect)
	}
	t.Logf("[AC-S3f2658-4-1] PASS: wrapper pid unchanged (%d), in-container pid unchanged (%d), ring grew across simulated restart",
		hello2.Pid, containerPidAfter)

	// --- Step 5 (AC-S3f2658-4-2): SHUTDOWN the ptyhost (TERM->KILL of the
	// LOCAL wrapper only — the ptyhost protocol has zero incus knowledge,
	// ADR-0002), then assert the explicit runtime.ContainerProcessKiller reap
	// (the SAME call daemon.teardown/GCOrphans make) is what actually clears
	// the container-side process. ---
	shutConn, err := dialUnix(sockPath)
	if err != nil {
		t.Fatalf("dial for SHUTDOWN: %v", err)
	}
	if err := ptyhost.WriteFrame(shutConn, ptyhost.MsgShutdown, ptyhost.EncodeShutdown(ptyhost.ShutdownPayload{GraceMillis: 2000})); err != nil {
		t.Fatalf("send SHUTDOWN: %v", err)
	}
	_ = shutConn.Close()
	time.Sleep(3 * time.Second) // let ptyhost's own TERM->KILL escalation of the LOCAL wrapper finish

	survivedHostShutdownAlone := containerProcessAlive(t, inst, marker)
	report.ContainerAliveAfterPtyhostShutdownAlone = survivedHostShutdownAlone
	t.Logf("in-container marker process still alive after host-side ptyhost SHUTDOWN alone: %v (this is exactly why S3f2658-4's explicit reap exists)", survivedHostShutdownAlone)

	// The explicit reap — reusing the EXACT production call
	// (reapContainerClaude / daemon.teardown / GCOrphans all funnel here).
	kCtx, kCancel := context.WithTimeout(context.Background(), 5*time.Second)
	killErr := killer.KillContainerProcesses(kCtx, "TERM", marker)
	kCancel()
	if killErr != nil {
		t.Fatalf("[AC-S3f2658-4-2] escalate: KillContainerProcesses returned an error: %v", killErr)
	}

	deadline = time.Now().Add(5 * time.Second)
	var reaped bool
	for time.Now().Before(deadline) {
		if !containerProcessAlive(t, inst, marker) {
			reaped = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	report.Ac42Pass = reaped
	if !reaped {
		t.Fatalf("[AC-S3f2658-4-2] FAILED: in-container marker process still alive after explicit KillContainerProcesses(TERM, %q)", marker)
	}
	t.Logf("[AC-S3f2658-4-2] PASS: explicit runtime.ContainerProcessKiller reaped the in-container process")

	verdict = "PASS"
}

// --- helpers ---------------------------------------------------------------

func dialUnix(sockPath string) (net.Conn, error) {
	return net.Dial("unix", sockPath)
}

func incusContainerNames(t *testing.T) ([]string, error) {
	t.Helper()
	out, err := exec.Command("incus", "list", "-f", "csv", "-c", "n").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("incus list: %w: %s", err, out)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]int{}
	for _, s := range a {
		set[s]++
	}
	for _, s := range b {
		set[s]--
	}
	for _, v := range set {
		if v != 0 {
			return false
		}
	}
	return true
}

// containerProcessAlive reports whether a process matching pattern is
// currently running inside inst, via `incus exec <inst> -- pgrep -f
// <pattern>` — the exact ground-truth check runtime.ContainerProcessKiller's
// own doc comment describes.
func containerProcessAlive(t *testing.T, inst, pattern string) bool {
	t.Helper()
	out, err := exec.Command("incus", "exec", inst, "--", "pgrep", "-f", pattern).CombinedOutput()
	if err != nil {
		// pgrep exit 1 == no match == not alive; any other error is logged but
		// still treated as "not confirmed alive" (conservative for a liveness
		// check — an unreachable container has nothing alive in it either).
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// waitForContainerPid polls containerProcessAlive-equivalent pgrep until a
// pid appears (or timeout), returning it.
func waitForContainerPid(t *testing.T, inst, pattern string, timeout time.Duration) (int, error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("incus", "exec", inst, "--", "pgrep", "-f", pattern).CombinedOutput()
		if err == nil {
			fields := strings.Fields(strings.TrimSpace(string(out)))
			if len(fields) > 0 {
				pid, perr := strconv.Atoi(fields[0])
				if perr == nil && pid > 0 {
					return pid, nil
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return 0, fmt.Errorf("no process matching %q found in %s within %s", pattern, inst, timeout)
}

// queryRingTotal is a thin STATUS query returning the ptyhost's ring total
// offset (the same signal Story S3f2658-1's survival test uses to prove
// output continued across an event, not just that pids survived).
func queryRingTotal(sockPath string) (int64, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }()
	if err := ptyhost.WriteFrame(conn, ptyhost.MsgStatus, ptyhost.EncodeStatusRequest()); err != nil {
		return 0, err
	}
	mt, payload, err := ptyhost.ReadFrame(conn)
	if err != nil {
		return 0, err
	}
	if mt != ptyhost.MsgStatus {
		return 0, fmt.Errorf("unexpected reply type %v", mt)
	}
	st, err := ptyhost.DecodeStatusResponse(payload)
	if err != nil {
		return 0, err
	}
	return st.RingTotalOffset, nil
}

// realIncusReport is written to docs/sprint-logs/S3f2658/e2e-S3f2658-4.json.
type realIncusReport struct {
	Task                                    string    `json:"task"`
	StartedAt                               time.Time `json:"startedAt"`
	PreExistingContainers                   []string  `json:"preExistingContainers"`
	ThrowawayInstance                       string    `json:"throwawayInstance"`
	WrapperArgv                             []string  `json:"wrapperArgv"`
	PtyhostHeldWrapperPid                   int       `json:"ptyhostHeldWrapperPid"`
	ContainerPidBeforeReconnect             int       `json:"containerPidBeforeReconnect"`
	PtyhostHeldWrapperPidAfter              int       `json:"ptyhostHeldWrapperPidAfter"`
	ContainerPidAfterReconnect              int       `json:"containerPidAfterReconnect"`
	RingGrewAcrossReconnect                 bool      `json:"ringGrewAcrossReconnect"`
	Ac41Pass                                bool      `json:"ac41Pass"`
	ContainerAliveAfterPtyhostShutdownAlone bool      `json:"containerAliveAfterPtyhostShutdownAlone"`
	Ac42Pass                                bool      `json:"ac42Pass"`
	PostContainers                          []string  `json:"postContainers"`
	ExistingContainersUntouched             bool      `json:"existingContainersUntouched"`
	Verdict                                 string    `json:"verdict"`
	FinishedAt                              time.Time `json:"finishedAt"`
}

func realIncusReportPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Join(wd, "..", "..", "..")
	return filepath.Join(repoRoot, "docs", "sprint-logs", "S3f2658", "e2e-S3f2658-4.json")
}

func writeRealIncusReport(t *testing.T, path string, report *realIncusReport) {
	t.Helper()
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Logf("marshal real-incus report: %v", err)
		return
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Logf("write real-incus report %s: %v", path, err)
		return
	}
	t.Logf("real-incus report written to %s", path)
}
