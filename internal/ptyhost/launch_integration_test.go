package ptyhost

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// dialRaw dials the ptyhost socket for a best-effort SHUTDOWN in test
// cleanup (separate from the testClient helper in server_test.go, which is
// t.Cleanup-coupled to a *testing.T rather than usable from a nested
// t.Cleanup closure here).
func dialRaw(sockPath string) (net.Conn, error) {
	return net.Dial("unix", sockPath)
}

func syscallKill0(pid int) error {
	return syscall.Kill(pid, 0)
}

func syscallKillTerm(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// buildRealPalmuxBin builds the actual cmd/palmux binary (the one that
// contains the `ptyhost` subcommand entry point wired up in
// cmd/palmux/main.go) so the launcher integration test exercises the real
// end-to-end path, not a stand-in.
func buildRealPalmuxBin(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// internal/ptyhost -> repo root -> cmd/palmux
	repoRoot := filepath.Join(wd, "..", "..")
	bin := filepath.Join(t.TempDir(), "palmux")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/palmux")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building cmd/palmux: %v\n%s", err, out)
	}
	return bin
}

// pidCgroup returns the contents of /proc/<pid>/cgroup, or an error if the
// process (or /proc entry) is gone.
func pidCgroup(pid int) (string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func pidPPID(t *testing.T, pid int) int {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatalf("read /proc/%d/stat: %v", pid, err)
	}
	// Format: pid (comm) state ppid ...  — comm may contain spaces/parens, so
	// split after the LAST ')'.
	s := string(b)
	idx := strings.LastIndex(s, ")")
	if idx < 0 {
		t.Fatalf("unexpected /proc/%d/stat format: %q", pid, s)
	}
	fields := strings.Fields(s[idx+1:])
	if len(fields) < 2 {
		t.Fatalf("unexpected /proc/%d/stat fields: %q", pid, s)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("parse ppid from /proc/%d/stat: %v", pid, err)
	}
	return ppid
}

// TestLaunch_RealHost_DetachesFromTestProcess is the AC-S3f2658-1-2
// "Integration (host, real)" check: actually launch a ptyhost via the
// Launcher on this box and confirm it is detached from the launching (test)
// process — reparented to init, and (when the systemd-run path is taken) in
// a cgroup independent of this test binary's own cgroup. Full restart/kill-9
// SURVIVAL is the separate AC-S3f2658-1-3 real-machine smoke
// (docs/sprint-logs/S3f2658/survival-S3f2658-1.json); this test proves the
// structural precondition for that survival (no PPID/cgroup relationship
// tying the ptyhost to its launcher) without needing to kill anything.
func TestLaunch_RealHost_DetachesFromTestProcess(t *testing.T) {
	bin := buildRealPalmuxBin(t)
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "ptyhost.sock")
	statusPath := filepath.Join(dir, "ptyhost.json")

	l := &Launcher{} // real RunSystemdScope / RunSetsid
	result, err := l.Launch(context.Background(), LaunchConfig{
		PalmuxBin:      bin,
		InstancePrefix: "test",
		Seed:           t.Name(),
		Args: []string{
			"--socket", sockPath,
			"--status", statusPath,
			"--", "sleep", "60",
		},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Logf("launch method = %s", result.Method)

	// Wait for the status file to appear and report the spawned pid.
	var sf StatusFile
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := ReadStatusFile(statusPath); err == nil && s.Pid > 0 {
			sf = s
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if sf.Pid == 0 {
		t.Fatal("ptyhost never wrote a status file with a pid within 10s")
	}
	t.Cleanup(func() {
		// Best-effort cleanup: SHUTDOWN over the socket kills the child and
		// lets the ptyhost self-terminate (and, for the systemd-run path,
		// --collect removes the transient scope once its process exits).
		if conn, derr := dialRaw(sockPath); derr == nil {
			_ = WriteFrame(conn, MsgShutdown, EncodeShutdown(ShutdownPayload{GraceMillis: 500}))
			_ = conn.Close()
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if err := syscallKill0(sf.Pid); err != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		_ = syscallKillTerm(sf.Pid)
	})

	// This test process (the "launcher") must NOT be the ptyhost's parent —
	// that's the whole point of the detach.
	testPID := os.Getpid()
	ptyhostPPID := pidPPID(t, sf.Pid)
	if ptyhostPPID == testPID {
		t.Fatalf("ptyhost pid %d is still a direct child of the test process (ppid=%d) — not detached", sf.Pid, ptyhostPPID)
	}

	switch result.Method {
	case MethodSystemdRun:
		cg, err := pidCgroup(sf.Pid)
		if err != nil {
			t.Fatalf("read ptyhost cgroup: %v", err)
		}
		if !strings.Contains(cg, result.UnitName) {
			t.Fatalf("ptyhost cgroup %q does not contain its scope unit name %q", cg, result.UnitName)
		}
		myCg, err := pidCgroup(testPID)
		if err != nil {
			t.Fatalf("read test process cgroup: %v", err)
		}
		if cg == myCg {
			t.Fatalf("ptyhost shares a cgroup with the test process: %q", cg)
		}
		t.Logf("systemd-run scope cgroup isolation confirmed: ptyhost=%q test=%q", strings.TrimSpace(cg), strings.TrimSpace(myCg))
	case MethodSetsid:
		if ptyhostPPID != 1 {
			t.Fatalf("setsid fallback: ptyhost ppid = %d, want 1 (reparented to init)", ptyhostPPID)
		}
		t.Log("setsid fallback detach confirmed (ppid=1); cgroup escape is NOT expected on this path under systemd (see spike-S3f2658-1-1.json)")
	default:
		t.Fatalf("unexpected launch method %q", result.Method)
	}
}
