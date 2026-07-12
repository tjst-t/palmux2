package ptyhost

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSurvival_RealSystemd_PtyhostOutlivesLauncherRestartAndKill9 is the
// AC-S3f2658-1-3 [SURVIVAL] real-machine smoke.
//
// Scope note: Story S3f2658-1 delivers ONLY internal/ptyhost + the launcher
// (ADR-0002 thin holder) — claudetui is not wired to ptyhost yet (that is
// Story S3f2658-2). This smoke therefore validates the Story 1 deliverable
// directly: a "fake palmux2" driver
// (testdata/fake_palmux2_launcher.go) that calls the REAL
// ptyhost.Launcher.Launch() — the exact production code path — to spawn a
// `palmux ptyhost` holding a CHEAP counter script (never real claude,
// avoiding API quota/auth per ADR-0002's process-agnostic design). The fake
// palmux2 driver itself runs as its own throwaway systemd --user service
// unit (never the real host palmux2.service — this test creates and tears
// down its own uniquely-named unit) so the test can freely `systemctl --user
// restart` and `kill -9` it, exactly like Sa8e7d0's SURVIVAL_PASS shape.
//
// Results are also written to
// docs/sprint-logs/S3f2658/survival-S3f2658-1.json by this test.
func TestSurvival_RealSystemd_PtyhostOutlivesLauncherRestartAndKill9(t *testing.T) {
	if os.Getenv("PALMUX_SKIP_SURVIVAL") != "" {
		t.Fatal("PALMUX_SKIP_SURVIVAL is set — SURVIVAL ACs must never be silently skipped (priority_rule 0); unset it to run this test")
	}

	env := newSurvivalEnv(t)
	if !env.dbusAvailable {
		t.Fatalf("no systemd --user / D-Bus session available on this host (%v) — cannot run the systemd-run SURVIVAL variant; escalate at milestone per the story's instructions rather than silently mark done", env.probeErr)
	}

	dir := t.TempDir()
	palmuxBin := buildRealPalmuxBin(t)
	launcherBin := buildFakePalmux2Launcher(t)
	counterScript := writeCounterScript(t, dir)

	sockPath := filepath.Join(dir, "survival.sock")
	statusPath := filepath.Join(dir, "survival.json")

	unitSuffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	launcherUnit := "s3f2658-survtest-" + unitSuffix

	result := survivalResult{
		Task:      "AC-S3f2658-1-3",
		Host:      hostname(),
		StartedAt: time.Now().UTC(),
	}

	t.Cleanup(func() {
		// Best-effort teardown: SHUTDOWN the ptyhost over its socket (kills
		// the counter + ptyhost self-exits), then stop the launcher unit and
		// clear any failed-unit residue. Never touches host palmux2.
		if conn, derr := dialRaw(sockPath); derr == nil {
			_ = WriteFrame(conn, MsgShutdown, EncodeShutdown(ShutdownPayload{GraceMillis: 500}))
			_ = conn.Close()
		}
		_, _ = runSystemctlUser(env, "stop", launcherUnit+".service")
		_, _ = runSystemctlUser(env, "reset-failed")
		writeSurvivalResult(t, result)
	})

	// --- Launch: fake palmux2 driver as its own throwaway systemd --user service ---
	launchArgs := []string{
		"--user", "--collect", "--unit", launcherUnit, "--",
		launcherBin,
		"--palmux-bin", palmuxBin,
		"--instance-prefix", "survtest",
		"--seed", "survival-" + unitSuffix,
		"--socket", sockPath,
		"--status", statusPath,
		"--", counterScript,
	}
	out, err := runSystemdRunUser(env, launchArgs...)
	if err != nil {
		t.Fatalf("systemd-run (launcher unit): %v\n%s", err, out)
	}
	result.LauncherUnit = launcherUnit + ".service"

	// Wait for the ptyhost status file (child pid) and for the scope unit
	// name printed by the fake launcher's journal (used to find the ptyhost
	// pid's own cgroup for isolation evidence).
	var sf StatusFile
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if s, rerr := ReadStatusFile(statusPath); rerr == nil && s.Pid > 0 {
			sf = s
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if sf.Pid == 0 {
		t.Fatal("ptyhost never wrote a status file with a pid within 15s")
	}
	result.ChildPid = sf.Pid

	ptyhostPid, err := findPtyhostPidInScopeOf(env, sf.Pid)
	if err != nil {
		t.Fatalf("locate ptyhost pid: %v", err)
	}
	result.PtyhostPid = ptyhostPid
	t.Logf("child (counter) pid=%d, ptyhost pid=%d, launcher unit=%s", sf.Pid, ptyhostPid, launcherUnit)

	assertAlive := func(label string, pid int) bool {
		alive := syscallKill0(pid) == nil
		t.Logf("%s: pid %d alive=%v", label, pid, alive)
		return alive
	}
	ringTotal := func() int64 {
		st, rerr := queryStatus(sockPath)
		if rerr != nil {
			t.Fatalf("queryStatus: %v", rerr)
		}
		return st.RingTotalOffset
	}

	before := ringTotal()
	if !assertAlive("pre-restart child", sf.Pid) || !assertAlive("pre-restart ptyhost", ptyhostPid) {
		t.Fatal("child/ptyhost not alive before SURVIVAL-A even started")
	}

	// --- SURVIVAL-A: restart the launcher unit (simulates palmux2 restart) ---
	if out, err := runSystemctlUser(env, "restart", launcherUnit+".service"); err != nil {
		t.Fatalf("systemctl --user restart %s: %v\n%s", launcherUnit, err, out)
	}
	time.Sleep(1 * time.Second)

	survivalA := assertAlive("SURVIVAL-A child", sf.Pid) && assertAlive("SURVIVAL-A ptyhost", ptyhostPid)
	afterA := ringTotal()
	result.SurvivalA = survivalAResult{
		Pass:               survivalA,
		RingGrewAfterEvent: afterA > before,
		RingTotalBefore:    before,
		RingTotalAfter:     afterA,
	}
	if !survivalA {
		t.Fatal("SURVIVAL-A FAILED: child or ptyhost died after systemctl --user restart of the launcher unit")
	}
	if afterA <= before {
		t.Fatalf("SURVIVAL-A: ring did not grow after restart (before=%d after=%d) — counter may have stalled", before, afterA)
	}

	// --- SURVIVAL-B: kill -9 the launcher unit's current main pid ---
	mainPID, err := launcherMainPID(env, launcherUnit)
	if err != nil {
		t.Fatalf("get launcher MainPID: %v", err)
	}
	if err := syscallKillNine(mainPID); err != nil {
		t.Fatalf("kill -9 launcher main pid %d: %v", mainPID, err)
	}
	time.Sleep(1 * time.Second)

	survivalB := assertAlive("SURVIVAL-B child", sf.Pid) && assertAlive("SURVIVAL-B ptyhost", ptyhostPid)
	afterB := ringTotal()
	result.SurvivalB = survivalBResult{
		Pass:                  survivalB,
		LauncherMainPidKilled: mainPID,
		RingTotalBefore:       afterA,
		RingTotalAfter:        afterB,
		RingGrewAfterEvent:    afterB > afterA,
	}
	if !survivalB {
		t.Fatal("SURVIVAL-B FAILED: child or ptyhost died after kill -9 of the launcher's main pid")
	}
	if afterB <= afterA {
		t.Fatalf("SURVIVAL-B: ring did not grow after kill -9 (before=%d after=%d) — counter may have stalled", afterA, afterB)
	}

	// Socket/status file still answer after both events.
	st, err := queryStatus(sockPath)
	if err != nil {
		t.Fatalf("final queryStatus: %v", err)
	}
	if !st.Alive {
		t.Fatal("final STATUS reports alive=false — expected the counter to still be running")
	}

	result.FinalStatusAlive = st.Alive
	result.Verdict = "SURVIVAL_PASS x2"
	result.FinishedAt = time.Now().UTC()

	t.Log("SURVIVAL_PASS x2: ptyhost + held child survived both systemctl --user restart and kill -9 of the launching process")
}

// --- helpers -----------------------------------------------------------

type survivalEnv struct {
	xdgRuntimeDir string
	dbusAddr      string
	dbusAvailable bool
	probeErr      error
}

func newSurvivalEnv(t *testing.T) survivalEnv {
	t.Helper()
	uid := os.Getuid()
	env := survivalEnv{
		xdgRuntimeDir: fmt.Sprintf("/run/user/%d", uid),
		dbusAddr:      fmt.Sprintf("unix:path=/run/user/%d/bus", uid),
	}
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		env.xdgRuntimeDir = v
	}
	if v := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); v != "" {
		env.dbusAddr = v
	}
	cmd := exec.Command("systemctl", "--user", "status")
	cmd.Env = append(os.Environ(),
		"XDG_RUNTIME_DIR="+env.xdgRuntimeDir,
		"DBUS_SESSION_BUS_ADDRESS="+env.dbusAddr,
	)
	if err := cmd.Run(); err != nil {
		env.probeErr = err
		env.dbusAvailable = false
	} else {
		env.dbusAvailable = true
	}
	return env
}

func (e survivalEnv) commandEnv() []string {
	return append(os.Environ(),
		"XDG_RUNTIME_DIR="+e.xdgRuntimeDir,
		"DBUS_SESSION_BUS_ADDRESS="+e.dbusAddr,
	)
}

func runSystemctlUser(env survivalEnv, args ...string) (string, error) {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	cmd.Env = env.commandEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runSystemdRunUser(env survivalEnv, args ...string) (string, error) {
	cmd := exec.Command("systemd-run", args...)
	cmd.Env = env.commandEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func launcherMainPID(env survivalEnv, unit string) (int, error) {
	cmd := exec.Command("systemctl", "--user", "show", unit+".service", "-p", "MainPID", "--value")
	cmd.Env = env.commandEnv()
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse MainPID %q: %w", out, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("launcher unit %s has no live MainPID", unit)
	}
	return pid, nil
}

// findPtyhostPidInScopeOf locates the ptyhost process's own pid given the
// pid of the child it holds (sf.Pid), by scanning /proc for a process whose
// cgroup matches the child's cgroup and whose argv contains "ptyhost".
func findPtyhostPidInScopeOf(env survivalEnv, childPid int) (int, error) {
	_ = env
	childCg, err := pidCgroup(childPid)
	if err != nil {
		return 0, fmt.Errorf("read child cgroup: %w", err)
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil {
			continue
		}
		cg, cgErr := pidCgroup(pid)
		if cgErr != nil || cg != childCg {
			continue
		}
		cmdline, clErr := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if clErr != nil {
			continue
		}
		if strings.Contains(string(cmdline), "ptyhost") {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("no ptyhost process found sharing cgroup %q", strings.TrimSpace(childCg))
}

func queryStatus(sockPath string) (StatusPayload, error) {
	conn, err := dialRaw(sockPath)
	if err != nil {
		return StatusPayload{}, err
	}
	defer func() { _ = conn.Close() }()
	if err := WriteFrame(conn, MsgStatus, EncodeStatusRequest()); err != nil {
		return StatusPayload{}, err
	}
	mt, payload, err := ReadFrame(conn)
	if err != nil {
		return StatusPayload{}, err
	}
	if mt != MsgStatus {
		return StatusPayload{}, fmt.Errorf("unexpected reply type %v", mt)
	}
	return DecodeStatusResponse(payload)
}

func syscallKillNine(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func buildFakePalmux2Launcher(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	src := filepath.Join(wd, "testdata", "fake_palmux2_launcher.go")
	bin := filepath.Join(t.TempDir(), "fake_palmux2_launcher")
	cmd := exec.Command("go", "build", "-o", bin, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("compiling fake_palmux2_launcher: %v\n%s", err, out)
	}
	return bin
}

func writeCounterScript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "counter.sh")
	script := "#!/bin/bash\ni=0\nwhile true; do\n  i=$((i+1))\n  echo \"counter: $i\"\n  sleep 0.2\ndone\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write counter script: %v", err)
	}
	return path
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// --- result recording ----------------------------------------------------

type survivalAResult struct {
	Pass               bool  `json:"pass"`
	RingGrewAfterEvent bool  `json:"ringGrewAfterEvent"`
	RingTotalBefore    int64 `json:"ringTotalBefore"`
	RingTotalAfter     int64 `json:"ringTotalAfter"`
}

type survivalBResult struct {
	Pass                  bool  `json:"pass"`
	LauncherMainPidKilled int   `json:"launcherMainPidKilled"`
	RingTotalBefore       int64 `json:"ringTotalBefore"`
	RingTotalAfter        int64 `json:"ringTotalAfter"`
	RingGrewAfterEvent    bool  `json:"ringGrewAfterEvent"`
}

type survivalResult struct {
	Task             string          `json:"task"`
	Host             string          `json:"host"`
	LauncherUnit     string          `json:"launcherUnit"`
	ChildPid         int             `json:"childPid"`
	PtyhostPid       int             `json:"ptyhostPid"`
	SurvivalA        survivalAResult `json:"survivalA"`
	SurvivalB        survivalBResult `json:"survivalB"`
	FinalStatusAlive bool            `json:"finalStatusAlive"`
	Verdict          string          `json:"verdict"`
	StartedAt        time.Time       `json:"startedAt"`
	FinishedAt       time.Time       `json:"finishedAt"`
}

// writeSurvivalResult writes result to
// docs/sprint-logs/S3f2658/survival-S3f2658-1.json (repo-root-relative from
// this package's directory). Best-effort: a write failure is logged, not
// fatal — it must never mask the actual pass/fail assertions above, which
// have already run by the time this is called (from t.Cleanup).
func writeSurvivalResult(t *testing.T, result survivalResult) {
	t.Helper()
	if result.Verdict == "" {
		result.Verdict = "FAILED (see test log)"
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now().UTC()
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Logf("writeSurvivalResult: getwd: %v", err)
		return
	}
	out := filepath.Join(wd, "..", "..", "docs", "sprint-logs", "S3f2658", "survival-S3f2658-1.json")
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Logf("writeSurvivalResult: marshal: %v", err)
		return
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Logf("writeSurvivalResult: write %s: %v", out, err)
		return
	}
	t.Logf("survival result written to %s", out)
}
