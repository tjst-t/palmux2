// Package browser — unit tests for the chromium lifecycle manager.
//
// These tests use a fakeRuntime that records Exec calls (mirroring the pattern
// from internal/runtime/incus/incus_test.go) so no real incus binary is needed.
//
// [AC-S62374c-1-1] [AC-S62374c-1-2] [AC-S62374c-1-3] [AC-S62374c-1-4] [AC-S62374c-1-6]
package browser

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// ---------------------------------------------------------------------------
// fakeRuntime records Exec calls; all other methods are stubs.
// ---------------------------------------------------------------------------

type fakeExecCall struct {
	cmd  []string
	opts runtime.ExecOpts
}

type fakeRuntime struct {
	mu      sync.Mutex
	calls   []fakeExecCall
	results map[string]runtime.ExecResult // key = joined cmd prefix (first word)

	addr string // returned by Status().Address
}

func newFakeRuntime(addr string) *fakeRuntime {
	return &fakeRuntime{
		addr:    addr,
		results: map[string]runtime.ExecResult{},
	}
}

func (f *fakeRuntime) setResult(cmd string, r runtime.ExecResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[cmd] = r
}

func (f *fakeRuntime) Exec(_ context.Context, cmd []string, opts runtime.ExecOpts) (runtime.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeExecCall{cmd: append([]string(nil), cmd...), opts: opts})
	key := ""
	if len(cmd) > 0 {
		key = cmd[0]
	}
	if r, ok := f.results[key]; ok {
		return r, nil
	}
	// Default: exit 0, empty stdout.
	return runtime.ExecResult{ExitCode: 0}, nil
}

func (f *fakeRuntime) recorded() []fakeExecCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeExecCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// findExecContaining returns the first recorded Exec whose cmd contains all
// of the given fragments anywhere in the joined command string.
func (f *fakeRuntime) findExecContaining(fragments ...string) (fakeExecCall, bool) {
	for _, c := range f.recorded() {
		joined := strings.Join(c.cmd, " ")
		all := true
		for _, frag := range fragments {
			if !strings.Contains(joined, frag) {
				all = false
				break
			}
		}
		if all {
			return c, true
		}
	}
	return fakeExecCall{}, false
}

// Stub implementations of the runtime.Runtime interface (unused in browser tests).
func (f *fakeRuntime) Kind() runtime.Kind     { return runtime.KindIncusContainer }
func (f *fakeRuntime) Config() runtime.Config { return runtime.Config{Kind: runtime.KindIncusContainer} }
func (f *fakeRuntime) Start(_ context.Context) error { return nil }
func (f *fakeRuntime) Stop(_ context.Context) error  { return nil }
func (f *fakeRuntime) Status() runtime.Status {
	return runtime.Status{State: runtime.StateReady, Address: f.addr}
}
func (f *fakeRuntime) NewTmuxSession(_ context.Context, _ string) error { return nil }
func (f *fakeRuntime) AttachTmuxSession(_ context.Context, _ string) (io.ReadWriteCloser, error) {
	return nil, nil
}
func (f *fakeRuntime) ListListeningPorts(_ context.Context) ([]runtime.ListeningPort, error) {
	return nil, nil
}
func (f *fakeRuntime) ExposePort(_ context.Context, _ runtime.PortSpec) (runtime.PortMapping, error) {
	return runtime.PortMapping{}, nil
}
func (f *fakeRuntime) UnexposePort(_ context.Context, _ string) error { return nil }
func (f *fakeRuntime) TmuxClient() tmux.Client                         { return nil }

// ---------------------------------------------------------------------------
// fakeDeviceAdder records calls but does nothing real.
// ---------------------------------------------------------------------------

type fakeDeviceAdderCall struct {
	inst, devName, source, path string
}

type fakeDeviceAdder struct {
	mu    sync.Mutex
	calls []fakeDeviceAdderCall
}

func (a *fakeDeviceAdder) add(_ context.Context, inst, devName, source, path string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, fakeDeviceAdderCall{inst: inst, devName: devName, source: source, path: path})
	return nil
}

func (a *fakeDeviceAdder) recorded() []fakeDeviceAdderCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]fakeDeviceAdderCall, len(a.calls))
	copy(out, a.calls)
	return out
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestManager creates a Manager backed by a seqFakeRuntime. The runtime
// uses content-aware Exec routing (not index-based) so it handles the new
// multi-process launch sequence (Xvfb/chromium/fcitx5/x11vnc/relay) correctly.
func newTestManager(t *testing.T, containerAddr string) (*Manager, *seqFakeRuntime, *fakeDeviceAdder) {
	t.Helper()
	sr := newSeqFakeRuntime(containerAddr)
	da := &fakeDeviceAdder{}
	mgr := NewManager(func() runtime.Runtime { return sr }, "ws-test-ab12cd34", da.add, nil)
	return mgr, sr, da
}

// ---------------------------------------------------------------------------
// TestStart_LaunchesStackWithCorrectCommands
// Verifies that Start issues Exec calls for Xvfb, chromium (headful with CDP),
// x11vnc, and the CDP relay. Fcitx5 is optional (non-fatal).
// [AC-S62374c-1-1] [AC-S62374c-1-3]
// ---------------------------------------------------------------------------

func TestStart_LaunchesStackWithCorrectCommands(t *testing.T) {
	const containerAddr = "10.100.0.5"
	mgr, sr, _ := newTestManager(t, containerAddr)

	ctx := context.Background()
	resp, err := mgr.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if resp.State != StateStarting && resp.State != StateRunning {
		t.Errorf("Start response state = %q, want starting or running", resp.State)
	}

	sr.mu.Lock()
	calls := make([]fakeExecCall, len(sr.calls))
	copy(calls, sr.calls)
	sr.mu.Unlock()

	// Helper: find a sh -c call whose body contains all fragments.
	findCall := func(fragments ...string) bool {
		for _, c := range calls {
			if len(c.cmd) < 2 || c.cmd[0] != "sh" || c.cmd[1] != "-c" {
				continue
			}
			joined := strings.Join(c.cmd, " ")
			all := true
			for _, f := range fragments {
				if !strings.Contains(joined, f) {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
		return false
	}

	// 1. Xvfb must be launched on display :99.
	if !findCall("Xvfb", xDisplay) {
		t.Errorf("[AC-S62374c-1-1] Xvfb not launched on display %s. calls: %v", xDisplay, calls)
	}

	// 2. Chromium must be launched headful with DISPLAY=:99, CDP port, --no-sandbox,
	//    --remote-allow-origins=*, --user-data-dir=.
	//    Must NOT use --headless (noVNC rework: full UI).
	//    [AC-S62374c-1-1] [AC-S62374c-1-3]
	if !findCall(chromiumBin, "nohup", "--no-sandbox",
		"--remote-debugging-port=9222", "--remote-allow-origins=*", "--user-data-dir=") {
		t.Errorf("[AC-S62374c-1-1/3] chromium launch missing required flags. calls: %v", calls)
	}
	if findCall(chromiumBin, "--headless") {
		t.Errorf("[noVNC rework] chromium must NOT be launched headless")
	}

	// 3. x11vnc must be launched listening on VNC port 5900.
	if !findCall("x11vnc", "-rfbport") {
		t.Errorf("[noVNC rework] x11vnc not found in launch calls. calls: %v", calls)
	}

	// 3a. A session dbus-daemon must be launched at the fixed bus address so the
	//     GTK fcitx IM module can reach fcitx5 (Japanese input transport).
	if !findCall("dbus-daemon", "--session", dbusAddr) {
		t.Errorf("[noVNC IME] session dbus-daemon not launched at %s. calls: %v", dbusAddr, calls)
	}

	// 3b. fcitx5 AND chromium must both attach to that bus (shared
	//     DBUS_SESSION_BUS_ADDRESS) and set GTK_IM_MODULE=fcitx — without this
	//     pairing Japanese input silently does nothing.
	if !findCall("fcitx5", "DBUS_SESSION_BUS_ADDRESS="+dbusAddr) {
		t.Errorf("[noVNC IME] fcitx5 not launched on the shared session bus. calls: %v", calls)
	}
	if !findCall(chromiumBin, "DBUS_SESSION_BUS_ADDRESS="+dbusAddr, "GTK_IM_MODULE=fcitx") {
		t.Errorf("[noVNC IME] chromium missing shared bus / GTK_IM_MODULE. calls: %v", calls)
	}

	// 3c. The fcitx5 mozc profile must be written before fcitx5 starts.
	if !findCall(".config/fcitx5/profile") {
		t.Errorf("[noVNC IME] fcitx5 mozc profile not written. calls: %v", calls)
	}

	// 4. CDP must NOT bind to 0.0.0.0 explicitly [AC-S62374c-1-3].
	for _, c := range calls {
		joined := strings.Join(c.cmd, " ")
		if strings.Contains(joined, chromiumBin) &&
			strings.Contains(joined, "--remote-debugging-address=0.0.0.0") {
			t.Errorf("[AC-S62374c-1-3] chromium must NOT bind CDP to 0.0.0.0: %s", joined)
		}
	}
}

// ---------------------------------------------------------------------------
// TestStart_Idempotent
// A second Start while chromium is running must NOT launch a second process.
// [AC-S62374c-1-1]
// ---------------------------------------------------------------------------

func TestStart_Idempotent(t *testing.T) {
	const containerAddr = "10.100.0.5"
	// Content-aware seqFakeRuntime handles presence check, Xvfb/chromium/x11vnc
	// launches, waitLocalCDP, startRelay, and kill -0 automatically.
	sr := newSeqFakeRuntime(containerAddr)
	da := &fakeDeviceAdder{}
	mgr := NewManager(func() runtime.Runtime { return sr }, "ws-test-ab12cd34", da.add, nil)

	ctx := context.Background()
	if _, err := mgr.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// Force state to running with a known PID for the idempotency check.
	mgr.mu.Lock()
	mgr.state = StateRunning
	mgr.pid = "12345"
	mgr.mu.Unlock()

	sr.mu.Lock()
	callsBefore := len(sr.calls)
	sr.mu.Unlock()

	if _, err := mgr.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}

	sr.mu.Lock()
	newCalls := make([]fakeExecCall, len(sr.calls)-callsBefore)
	copy(newCalls, sr.calls[callsBefore:])
	callsAfter := len(sr.calls)
	sr.mu.Unlock()

	// The second Start should only add a `kill -0` check (isPIDAlive), NOT a new
	// launch sh -c ... chromium ... call.
	for _, c := range newCalls {
		if len(c.cmd) >= 2 && c.cmd[0] == "sh" && c.cmd[1] == "-c" {
			joined := strings.Join(c.cmd, " ")
			if strings.Contains(joined, chromiumBin) && strings.Contains(joined, "nohup") {
				t.Errorf("[AC-S62374c-1-1] idempotent Start issued a second launch command: %v (total calls: %d→%d)", c.cmd, callsBefore, callsAfter)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TestStop_KillsDaemons
// Stop must issue pkill for chromium, x11vnc, fcitx5, and Xvfb and set
// state=stopped. [AC-S62374c-1-1]
// ---------------------------------------------------------------------------

func TestStop_KillsDaemons(t *testing.T) {
	const containerAddr = "10.100.0.5"
	mgr, sr, _ := newTestManager(t, containerAddr)

	// Pre-set running state directly (no need to actually Start).
	mgr.mu.Lock()
	mgr.state = StateRunning
	mgr.pid = "9999"
	mgr.vncPID = "9998"
	mgr.cdpAddr = containerAddr
	mgr.mu.Unlock()

	ctx := context.Background()
	resp, err := mgr.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if resp.State != StateStopped {
		t.Errorf("Stop response state = %q, want stopped", resp.State)
	}
	if s := mgr.State(ctx).State; s != StateStopped {
		t.Errorf("State after Stop = %q, want stopped", s)
	}

	// Verify pkill for chromium and x11vnc were issued.
	sr.mu.Lock()
	calls := make([]fakeExecCall, len(sr.calls))
	copy(calls, sr.calls)
	sr.mu.Unlock()

	foundChromium := false
	foundVNC := false
	foundDBus := false
	for _, c := range calls {
		j := strings.Join(c.cmd, " ")
		if strings.Contains(j, "pkill") && strings.Contains(j, "remote-debugging-port=9222") {
			foundChromium = true
		}
		if strings.Contains(j, "pkill") && strings.Contains(j, "x11vnc") {
			foundVNC = true
		}
		if strings.Contains(j, "pkill") && strings.Contains(j, "dbus-daemon") {
			foundDBus = true
		}
	}
	if !foundChromium {
		t.Errorf("[AC-S62374c-1-1] Stop did not pkill chromium, recorded: %v", calls)
	}
	if !foundVNC {
		t.Errorf("[noVNC rework] Stop did not pkill x11vnc, recorded: %v", calls)
	}
	if !foundDBus {
		t.Errorf("[noVNC IME] Stop did not pkill the session dbus-daemon, recorded: %v", calls)
	}
}

// ---------------------------------------------------------------------------
// TestStop_WhenAlreadyStopped
// Stop when state=stopped is a no-op (no kill issued, returns stopped).
// ---------------------------------------------------------------------------

func TestStop_WhenAlreadyStopped(t *testing.T) {
	mgr, sr, _ := newTestManager(t, "10.100.0.1")
	ctx := context.Background()
	resp, err := mgr.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop (already stopped): %v", err)
	}
	if resp.State != StateStopped {
		t.Errorf("Stop (already stopped) response = %q, want stopped", resp.State)
	}
	// No kill call should have been issued.
	sr.mu.Lock()
	calls := make([]fakeExecCall, len(sr.calls))
	copy(calls, sr.calls)
	sr.mu.Unlock()
	for _, c := range calls {
		if len(c.cmd) > 0 && c.cmd[0] == "kill" {
			t.Errorf("Stop (already stopped) issued a kill: %v", c.cmd)
		}
	}
}

// ---------------------------------------------------------------------------
// TestStart_ChromiumMissing
// When chromium is not found in the container, Start returns a clear error.
// [AC-S62374c-1-4]
// ---------------------------------------------------------------------------

func TestStart_ChromiumMissing(t *testing.T) {
	// sh -c "command -v chromium ..." returns exit 1 + no "found" in stdout.
	sr := newSeqFakeRuntime("10.100.0.2")
	sr.noChromium = true // presence check → not found

	da := &fakeDeviceAdder{}
	mgr := NewManager(func() runtime.Runtime { return sr }, "ws-nochrome-ab12", da.add, nil)

	_, err := mgr.Start(context.Background())
	if err == nil {
		t.Fatal("Start should fail when chromium is absent")
	}
	if !strings.Contains(err.Error(), chromiumBin) {
		t.Errorf("error should mention %q, got: %v", chromiumBin, err)
	}
	if !strings.Contains(err.Error(), "build") && !strings.Contains(err.Error(), "images") {
		t.Errorf("error should give guidance, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestStart_NoPersistentMount_WhenNoAddr
// Start returns an error when the runtime has no container IP.
// ---------------------------------------------------------------------------

func TestStart_NoContainerIP(t *testing.T) {
	sr := newSeqFakeRuntime("") // no IP — Status().Address = ""
	da := &fakeDeviceAdder{}
	mgr := NewManager(func() runtime.Runtime { return sr }, "ws-noip-ab12", da.add, nil)

	_, err := mgr.Start(context.Background())
	if err == nil {
		t.Fatal("Start should fail when container IP is empty")
	}
	if !strings.Contains(err.Error(), "container IP") {
		t.Errorf("error should mention container IP, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestPersistenceMount
// Start must call the DeviceAdder with the correct profile path under
// ~/.local/share/palmux-browser/<inst>/
// [AC-S62374c-1-2]
// ---------------------------------------------------------------------------

// seqFakeRuntime is a simple runtime that returns predetermined results per
// Exec call index (first call, second call, …) rather than keyed by cmd[0].
// This lets us give different results to the presence-check sh call and the
// launch sh call.
type seqFakeRuntime struct {
	mu         sync.Mutex
	calls      []fakeExecCall
	results    []runtime.ExecResult // results[i] returned for the i-th Exec call
	addr       string
	noChromium bool // when true, the `command -v chromium` probe returns not-found
}

func newSeqFakeRuntime(addr string, results ...runtime.ExecResult) *seqFakeRuntime {
	return &seqFakeRuntime{addr: addr, results: results}
}

func (s *seqFakeRuntime) Exec(_ context.Context, cmd []string, opts runtime.ExecOpts) (runtime.ExecResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, fakeExecCall{cmd: append([]string(nil), cmd...), opts: opts})
	// Content-aware responses (robust to call ordering / added relay+CDP calls):
	joined := strings.Join(cmd, " ")
	switch {
	case strings.Contains(joined, "command -v "+chromiumBin):
		if s.noChromium {
			return runtime.ExecResult{Stdout: "", ExitCode: 1}, nil
		}
		return runtime.ExecResult{Stdout: "found\n"}, nil
	case strings.Contains(joined, "Xvfb"): // launchXvfb
		return runtime.ExecResult{Stdout: "1001\n"}, nil
	case strings.Contains(joined, "dbus-daemon"): // launchDBus
		return runtime.ExecResult{Stdout: "1004\n"}, nil
	case strings.Contains(joined, "fcitx5"): // launchFcitx5 + ensureFcitx5Config
		return runtime.ExecResult{Stdout: "1002\n"}, nil
	case strings.Contains(joined, "x11vnc"): // launchX11VNC
		return runtime.ExecResult{Stdout: "1003\n"}, nil
	case strings.Contains(joined, "/json/version"): // waitLocalCDP probe
		return runtime.ExecResult{Stdout: "200"}, nil
	case strings.Contains(joined, "python3") && strings.Contains(joined, "base64"): // startRelay
		return runtime.ExecResult{Stdout: "9001\n"}, nil
	case strings.Contains(joined, chromiumBin) && strings.Contains(joined, "nohup"): // launchChromium
		return runtime.ExecResult{Stdout: "12345\n"}, nil
	case len(cmd) >= 2 && cmd[0] == "kill" && cmd[1] == "-0": // isPIDAlive → alive
		return runtime.ExecResult{ExitCode: 0}, nil
	}
	// Allow explicit per-call overrides (rarely needed) by index for any leftover.
	idx := len(s.calls) - 1
	if idx < len(s.results) {
		return s.results[idx], nil
	}
	return runtime.ExecResult{ExitCode: 0}, nil
}
func (s *seqFakeRuntime) Kind() runtime.Kind     { return runtime.KindIncusContainer }
func (s *seqFakeRuntime) Config() runtime.Config { return runtime.Config{Kind: runtime.KindIncusContainer} }
func (s *seqFakeRuntime) Start(_ context.Context) error { return nil }
func (s *seqFakeRuntime) Stop(_ context.Context) error  { return nil }
func (s *seqFakeRuntime) Status() runtime.Status {
	return runtime.Status{State: runtime.StateReady, Address: s.addr}
}
func (s *seqFakeRuntime) NewTmuxSession(_ context.Context, _ string) error { return nil }
func (s *seqFakeRuntime) AttachTmuxSession(_ context.Context, _ string) (io.ReadWriteCloser, error) {
	return nil, nil
}
func (s *seqFakeRuntime) ListListeningPorts(_ context.Context) ([]runtime.ListeningPort, error) {
	return nil, nil
}
func (s *seqFakeRuntime) ExposePort(_ context.Context, _ runtime.PortSpec) (runtime.PortMapping, error) {
	return runtime.PortMapping{}, nil
}
func (s *seqFakeRuntime) UnexposePort(_ context.Context, _ string) error { return nil }
func (s *seqFakeRuntime) TmuxClient() tmux.Client                         { return nil }

func TestPersistenceMount(t *testing.T) {
	const containerAddr = "10.100.0.5"
	const inst = "ws-persist-ff001122"

	// Content-aware seqFakeRuntime handles all launch commands.
	sr := newSeqFakeRuntime(containerAddr)

	da := &fakeDeviceAdder{}
	mgr := NewManager(func() runtime.Runtime { return sr }, inst, da.add, nil)

	ctx := context.Background()
	_, _ = mgr.Start(ctx) // may succeed or fail; we care about da calls

	dadaCalls := da.recorded()
	if len(dadaCalls) == 0 {
		t.Fatalf("[AC-S62374c-1-2] DeviceAdder was not called — profile mount not attempted")
	}

	// The device must target the inst subdirectory and use the correct device name.
	c := dadaCalls[0]
	if c.inst != inst {
		t.Errorf("[AC-S62374c-1-2] DeviceAdder inst = %q, want %q", c.inst, inst)
	}
	if c.devName != "palmux-browser-profile" {
		t.Errorf("[AC-S62374c-1-2] DeviceAdder devName = %q, want %q", c.devName, "palmux-browser-profile")
	}
	if !strings.Contains(c.source, persistBaseName) {
		t.Errorf("[AC-S62374c-1-2] DeviceAdder source %q does not contain %q", c.source, persistBaseName)
	}
	if !strings.Contains(c.source, inst) {
		t.Errorf("[AC-S62374c-1-2] DeviceAdder source %q does not contain inst %q", c.source, inst)
	}
	if c.source != c.path {
		t.Errorf("[AC-S62374c-1-2] DeviceAdder source %q != path %q (must use same absolute path in host and container)", c.source, c.path)
	}
}

// ---------------------------------------------------------------------------
// TestStateLogic
// State() returns stopped when no chromium is running, running after a
// successful Start (mocked), starting when CDP is not yet up.
// [AC-S62374c-1-6]
// ---------------------------------------------------------------------------

func TestStateLogic_Stopped(t *testing.T) {
	mgr, _, _ := newTestManager(t, "10.100.0.1")
	sv := mgr.State(context.Background())
	if sv.State != StateStopped {
		t.Errorf("initial State = %q, want stopped", sv.State)
	}
	if !sv.Available {
		t.Errorf("Available should be true for incus runtime")
	}
	if sv.CDPReachable {
		t.Errorf("CDPReachable should be false when stopped")
	}
}

func TestStateLogic_Running(t *testing.T) {
	mgr, _, _ := newTestManager(t, "10.100.0.1")
	// Pre-set running state (skip actual launch).
	mgr.mu.Lock()
	mgr.state = StateRunning
	mgr.pid = "42"
	mgr.cdpAddr = "10.100.0.1"
	mgr.mu.Unlock()

	sv := mgr.State(context.Background())
	if sv.State != StateRunning {
		t.Errorf("State = %q, want running", sv.State)
	}
	// CDP will not be reachable in tests (no real container), but state is correct.
	if sv.Available != true {
		t.Errorf("Available should be true")
	}
}
