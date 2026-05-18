package claudetui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBin compiles the testdata/fake_claude.go helper and returns its path.
// It caches the compiled binary in a per-test TempDir.
func fakeBin(t *testing.T) string {
	t.Helper()
	src := filepath.Join(testdataDir(t), "fake_claude.go")
	bin := filepath.Join(t.TempDir(), "fake_claude")
	out, err := execBuildCmd(src, bin).CombinedOutput()
	if err != nil {
		t.Fatalf("compiling fake_claude: %v\n%s", err, out)
	}
	return bin
}

// testdataDir returns the absolute path to testdata/ relative to this package.
func testdataDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(wd, "testdata")
}

// newTestDaemon creates a Daemon backed by the fake_claude binary.
// The subprocess is NOT started yet.
func newTestDaemon(t *testing.T, extraArgs ...string) *Daemon {
	t.Helper()
	bin := fakeBin(t)
	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		ClaudeArgs:    extraArgs,
		RingSize:      1 << 16, // 64 KiB — enough for tests
		ResumeOnDeath: false,   // most tests don't want auto-respawn
	})
	t.Cleanup(func() { d.Shutdown() })
	return d
}

// ---- Fix 1: godoc + error wrapping -------------------------------------------

// TestErrorWrapping verifies that errors from daemon methods include the
// "claudetui daemon:" prefix required by Fix 1.
func TestErrorWrapping(t *testing.T) {
	d := NewDaemon(DaemonConfig{ClaudeBin: "/nonexistent-binary"})
	t.Cleanup(func() { d.Shutdown() })
	err := d.EnsureStarted(context.Background())
	if err == nil {
		t.Fatal("expected error for nonexistent binary, got nil")
	}
	if !strings.Contains(err.Error(), "claudetui daemon:") {
		t.Fatalf("error %q should contain 'claudetui daemon:'", err.Error())
	}
}

// ---- Fix 2: Shutdown sync.Once + Wait race ------------------------------------

// TestShutdownIdempotent calls Shutdown() twice concurrently from separate
// goroutines.  Both must return cleanly without panicking or hanging (Fix 2).
func TestShutdownIdempotent(t *testing.T) {
	d := newTestDaemon(t)
	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}

	// Wait briefly for the subprocess to produce some output.
	time.Sleep(50 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Shutdown()
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// both Shutdown() calls returned cleanly
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown() deadlocked when called twice concurrently")
	}
}

// TestShutdownBeforeStart ensures Shutdown() on an unstarted daemon does not
// panic or deadlock.
func TestShutdownBeforeStart(t *testing.T) {
	d := NewDaemon(DaemonConfig{ClaudeBin: "claude"})
	done := make(chan struct{})
	go func() {
		d.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown() on unstarted daemon timed out")
	}
}

// ---- Fix 4: respawnLoop -------------------------------------------------------

// TestRespawnLoop verifies that after an unexpected subprocess exit, the
// daemon auto-respawns with --resume <id> once SetSessionID has been called.
// EnsureStarted now auto-starts the respawn goroutine (no manual wiring).
func TestRespawnLoop(t *testing.T) {
	bin := fakeBin(t)
	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		ClaudeArgs:    []string{"--exit-immediately"},
		RingSize:      1 << 16,
		ResumeOnDeath: true, // enable respawn
	})
	t.Cleanup(func() { d.Shutdown() })

	// EnsureStarted spawns the subprocess AND auto-starts respawnLoop.
	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}

	// Let the subprocess die (it exits immediately).
	waitForState(t, d, StateDead, 5*time.Second)

	// Set a session ID — this unblocks respawnLoop.
	const wantSessionID = "ses-abc123"
	d.SetSessionID(wantSessionID)

	// Wait for the daemon to transition back to StateRunning.
	waitForState(t, d, StateRunning, 10*time.Second)

	// Read ring output to confirm the re-spawned binary received --resume.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("ring buffer does not contain 'resume: %s' after respawn; ring=%q",
				wantSessionID, d.ring.Bytes())
		default:
		}
		if bytes.Contains(d.ring.Bytes(), []byte("resume: "+wantSessionID)) {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Logf("respawn confirmed: ring contains 'resume: %s'", wantSessionID)
}

// TestRespawnLoopArgContainsResume is a targeted test for the argv shape:
// assert exact presence of --resume <id> in respawn argv.
func TestRespawnLoopArgContainsResume(t *testing.T) {
	bin := fakeBin(t)
	const sessionID = "ses-xyz999"

	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		ClaudeArgs:    []string{"--exit-immediately"},
		RingSize:      1 << 16,
		ResumeOnDeath: true,
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}

	waitForState(t, d, StateDead, 5*time.Second)
	d.SetSessionID(sessionID)
	waitForState(t, d, StateRunning, 10*time.Second)

	// The ring should contain "resume: ses-xyz999".
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("ring does not contain 'resume: %s'; ring=%q", sessionID, d.ring.Bytes())
		default:
		}
		if bytes.Contains(d.ring.Bytes(), []byte("resume: "+sessionID)) {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// ---- Fix 6: PTY read goroutine timeout ----------------------------------------

// TestPTYReadTimeout verifies that starting a slow subprocess (/bin/sleep 30)
// and then immediately shutting down the daemon completes within a reasonable
// time — i.e. the read loop does not block forever on a SetReadDeadline-less
// PTY fd (Fix 6 — goroutine-based read pattern).
func TestPTYReadTimeout(t *testing.T) {
	if _, err := os.Stat("/bin/sleep"); err != nil {
		t.Log("TestPTYReadTimeout: /bin/sleep not available; timing the shutdown of a never-writing daemon")
		// Even without /bin/sleep, the goroutine-based pattern must not block.
	}

	d := NewDaemon(DaemonConfig{
		ClaudeBin:  "/bin/sleep",
		ClaudeArgs: []string{"30"},
		RingSize:   1 << 16,
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}

	start := time.Now()
	done := make(chan struct{})
	go func() {
		d.Shutdown()
		close(done)
	}()
	select {
	case <-done:
		elapsed := time.Since(start)
		// Shutdown should complete well within gracefulShutdownTimeout (5s) + 2s kill buffer.
		if elapsed > 8*time.Second {
			t.Fatalf("Shutdown() took %v; goroutine-based read should not block", elapsed)
		}
		t.Logf("Shutdown completed in %v (Fix 6 goroutine-based read confirmed)", elapsed)
	case <-time.After(15 * time.Second):
		t.Fatal("Shutdown() did not complete; PTY read loop appears to be blocking (Fix 6 regression)")
	}
}

// ---- Fix 7: daemonCtx isolation -----------------------------------------------

// TestRequestCancelDoesNotKillProcess verifies that cancelling a request-like
// context does NOT kill the subprocess (Fix 7 — daemonCtx isolation).
func TestRequestCancelDoesNotKillProcess(t *testing.T) {
	d := newTestDaemon(t)

	// Simulate a "request context" — analogous to an HTTP handler context.
	reqCtx, reqCancel := context.WithCancel(context.Background())

	// EnsureStarted should use daemonCtx, not reqCtx.
	if err := d.EnsureStarted(reqCtx); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}

	waitForState(t, d, StateRunning, 5*time.Second)
	pidBefore := d.CurrentStats().PID

	// Cancel the "request" context — WS handler would do this on disconnect.
	reqCancel()

	// Give a short window for any erroneous context propagation.
	time.Sleep(200 * time.Millisecond)

	// Subprocess must still be alive.
	stats := d.CurrentStats()
	if !stats.Alive {
		t.Fatalf("subprocess died after request context cancel (pid=%d); Fix 7 regression — daemonCtx not isolated", pidBefore)
	}
	if stats.PID != pidBefore {
		t.Fatalf("subprocess PID changed from %d to %d after request cancel", pidBefore, stats.PID)
	}

	// Verify the process actually exists in the OS via /proc/<pid>.
	procPath := "/proc/" + pidString(pidBefore)
	if _, statErr := os.Stat(procPath); statErr != nil {
		t.Fatalf("process %d not alive after request cancel (/proc check): %v", pidBefore, statErr)
	}
	t.Logf("subprocess pid=%d still alive after request context cancel (Fix 7 confirmed)", pidBefore)
}

// TestSetSessionID verifies SetSessionID and SessionID round-trip.
func TestSetSessionID(t *testing.T) {
	d := NewDaemon(DaemonConfig{ClaudeBin: "claude"})
	t.Cleanup(func() { d.Shutdown() })
	if d.SessionID() != "" {
		t.Fatal("initial session ID should be empty")
	}
	d.SetSessionID("ses-test-001")
	if got := d.SessionID(); got != "ses-test-001" {
		t.Fatalf("SessionID() = %q, want %q", got, "ses-test-001")
	}
}

// TestResize verifies that Resize returns an error before start and succeeds
// after the subprocess is running.
func TestResize(t *testing.T) {
	d := newTestDaemon(t)
	// Before start: should error.
	if err := d.Resize(80, 24); err == nil {
		t.Fatal("Resize before start should return error")
	}

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	if err := d.Resize(120, 40); err != nil {
		t.Fatalf("Resize after start: %v", err)
	}
}

// ---- Helpers ------------------------------------------------------------------

// waitForState polls until d reaches the target state or times out.
func waitForState(t *testing.T, d *Daemon, want State, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("waitForState(%v): timed out; current state = %v", want, State(d.state.Load()))
		default:
		}
		if State(d.state.Load()) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// pidString returns the string form of an int.
func pidString(pid int) string {
	return strconv.Itoa(pid)
}

// execBuildCmd compiles src (a Go source file) to bin using `go build`.
func execBuildCmd(src, bin string) buildResult {
	return buildResult{src: src, bin: bin}
}

type buildResult struct {
	src string
	bin string
}

func (b buildResult) CombinedOutput() ([]byte, error) {
	cmd := goCmd("build", "-o", b.bin, b.src)
	return cmd.CombinedOutput()
}

var _ = strings.Contains // confirm import used
var _ = sync.Mutex{}     // confirm import used

// TestSpawnUsesWorktreeAsCwd is a regression test for the cwd bug observed in
// production after Sprint S7ce250 landed: the daemon was inheriting palmux2
// server's cwd because DaemonConfig.Worktree was not plumbed to cmd.Dir. As a
// result claude inside the tab always reported palmux2's own path, no matter
// which repo the user opened.
//
// This test passes Worktree=<tempDir> to NewDaemon and verifies the fake
// subprocess reports os.Getwd() == tempDir.
func TestSpawnUsesWorktreeAsCwd(t *testing.T) {
	bin := fakeBin(t)
	workDir := t.TempDir()
	// Resolve symlinks so the comparison is robust on macOS where /tmp is a
	// symlink to /private/tmp; on Linux this is usually a no-op.
	resolvedWork, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatalf("eval workDir: %v", err)
	}

	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		ClaudeArgs:    []string{"--print-cwd"},
		Worktree:      resolvedWork,
		RingSize:      1 << 16,
		ResumeOnDeath: false,
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	// Drain a few hundred ms of output so fake_claude's "cwd: <path>" line
	// lands in the ring buffer.
	deadline := time.Now().Add(2 * time.Second)
	want := "cwd: " + resolvedWork
	for time.Now().Before(deadline) {
		snap, sub := d.ring.SnapshotAndSubscribe()
		_ = sub
		if strings.Contains(string(snap), want) {
			d.ring.Unsubscribe(sub)
			return
		}
		d.ring.Unsubscribe(sub)
		time.Sleep(50 * time.Millisecond)
	}
	snap, sub := d.ring.SnapshotAndSubscribe()
	d.ring.Unsubscribe(sub)
	t.Fatalf("expected %q in ring buffer; got:\n%s", want, snap)
}
