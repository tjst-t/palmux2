// Package browser implements the lifecycle manager for a per-workspace Chromium
// instance running headful inside an incus container, rendered to the user via
// noVNC over a raw VNC byte-pipe.
//
// Design (noVNC rework):
//   - Chromium is launched ONLY via an explicit POST .../tabs/browser/start.
//     Workspace open / tab display do NOT auto-launch.
//   - Launch sequence: Xvfb :99 → headful chromium (full UI) → fcitx5 → x11vnc.
//     Each daemon is started via `sh -c "nohup CMD >/log 2>&1 & echo $!"` so it
//     survives after incus exec returns.
//   - x11vnc listens on ALL interfaces on port 5900 (no -listen flag) so the
//     palmux host can reach it via the container's bridge IP.
//   - CDP is kept (--remote-debugging-port=9222) for Claude/Story-3.
//     A relay forwards <bridgeIP>:9222 → 127.0.0.1:9222 inside the container.
//   - State machine: stopped → starting → running (or back to stopped on stop).
//   - VNC attach: raw binary byte-pipe, WS subprotocol "binary".
//   - The --user-data-dir is bind-mounted from the host for profile persistence.
package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// CDPPort is the fixed remote-debugging port chromium listens on.
const CDPPort = 9222

// VNCPort is the fixed VNC port x11vnc listens on.
const VNCPort = 5900

// xDisplay is the Xvfb display number used by all daemons.
const xDisplay = ":99"

// chromiumBin is the binary name inside the container.
const chromiumBin = "chromium"

// persistBaseName is the sub-path under the host home directory that holds
// per-workspace browser profiles. It is bind-mounted by Start into the
// container at the same absolute path (same pattern as ~/.claude).
// e.g. ~/.local/share/palmux-browser/<inst>/
const persistBaseName = ".local/share/palmux-browser"

// BrowserState is the externally-visible lifecycle state of the in-container
// chromium.
type BrowserState string

const (
	StateStopped  BrowserState = "stopped"
	StateStarting BrowserState = "starting"
	StateRunning  BrowserState = "running"
)

// StateView is the JSON payload returned by GET .../tabs/browser/state.
type StateView struct {
	State        BrowserState `json:"state"`
	CDPReachable bool         `json:"cdpReachable"`
	URL          string       `json:"url,omitempty"`
	Available    bool         `json:"available"`
}

// StartResponse is the JSON payload returned by POST .../tabs/browser/start.
type StartResponse struct {
	State BrowserState `json:"state"`
}

// StopResponse is the JSON payload returned by POST .../tabs/browser/stop.
type StopResponse struct {
	State BrowserState `json:"state"`
}

// DeviceAdder adds a disk device to an incus container instance. It is
// injectable so tests can stub the incus management call.
// Returns (stdout+stderr, error). "already exists" in output is tolerated.
type DeviceAdder func(ctx context.Context, inst, devName, source, path string) error

// DefaultDeviceAdder runs `incus config device add` via os/exec.
// It tolerates "already exists" (idempotent re-open of a pre-existing container).
func DefaultDeviceAdder(ctx context.Context, inst, devName, source, path string) error {
	cmd := exec.CommandContext(ctx, "incus", //nolint:gosec
		"config", "device", "add", inst,
		devName, "disk",
		"source="+source,
		"path="+path,
	)
	cmd.Stdin = nil // critical: never pipe into incus
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	combined := so.String() + se.String()
	if err != nil {
		if strings.Contains(combined, "already exists") {
			return nil // idempotent
		}
		return fmt.Errorf("incus config device add %s: %w (stderr: %s)", devName, err, strings.TrimSpace(se.String()))
	}
	return nil
}

// Manager controls the browser daemon lifecycle for one workspace.
// It is safe for concurrent use.
//
// Daemon startup order:
//  1. Xvfb :99                       — virtual framebuffer
//  2. chromium (headful, full UI)     — CDP + real browser UI on :99
//  3. fcitx5                          — server-side Japanese IME
//  4. x11vnc -display :99             — VNC server on port 5900
//  5. CDP relay (bridgeIP:9222→127.0.0.1:9222) — for palmux/Claude host CDP reach
type Manager struct {
	// getRT resolves the CURRENT incus runtime for this workspace. It must be
	// called fresh on every op — a runtime switch (host↔incus) evicts and
	// recreates the registry's runtime, so a cached reference goes stale
	// (Status() would report the old, stopped runtime even though the live
	// container is ready). [AC-S62374c-1-1/1-3 real-mode fix]
	getRT       func() runtime.Runtime
	inst        string // incus instance name (for bind-mount device naming)
	log         *slog.Logger
	deviceAdder DeviceAdder // injectable for tests

	mu        sync.Mutex
	state     BrowserState
	xvfbPID   string // PID of Xvfb inside the container
	pid       string // PID of chromium inside the container
	fcitxPID  string // PID of fcitx5 inside the container
	vncPID    string // PID of x11vnc inside the container
	relayPID  string // PID of the in-container CDP relay
	cdpAddr   string // containerIP used for CDP and VNC; set on start
}

// cdpRelayScript is a raw TCP forwarder (same pattern as the incus runtime's
// localhost relay). Modern Chrome ignores --remote-debugging-address and binds
// the CDP port to 127.0.0.1 ONLY, so palmux on the bridge cannot reach it
// directly. We run this relay inside the container to forward
// <bridgeIP>:<port> → 127.0.0.1:<port>, exactly the localhost-bind rescue used
// for dev servers (workspace-runtime-design §5.3). argv: <listenIP> <port>.
const cdpRelayScript = `
import socket,threading,sys,os
def fwd(a,b):
 try:
  while 1:
   d=a.recv(65536)
   if not d:break
   b.sendall(d)
 except:pass
 try:a.close()
 except:pass
 try:b.close()
 except:pass
srv=socket.socket()
srv.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
srv.bind((sys.argv[1],int(sys.argv[2])))
srv.listen(128)
print(os.getpid(),flush=True)
while 1:
 c,_=srv.accept()
 b=socket.socket()
 b.connect(('127.0.0.1',int(sys.argv[2])))
 threading.Thread(target=fwd,args=(c,b),daemon=True).start()
 threading.Thread(target=fwd,args=(b,c),daemon=True).start()
`

// NewManager returns a Manager that resolves its incus runtime via getRT on
// every operation (so a runtime switch does not leave a stale reference).
// inst is the incus instance name (used for bind-mount device naming).
// da may be nil (DefaultDeviceAdder is used).
func NewManager(getRT func() runtime.Runtime, inst string, da DeviceAdder, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if da == nil {
		da = DefaultDeviceAdder
	}
	return &Manager{
		getRT:       getRT,
		inst:        inst,
		log:         log,
		deviceAdder: da,
		state:       StateStopped,
	}
}

// rt resolves the current runtime (never cached). Returns nil if unavailable.
func (m *Manager) rtNow() runtime.Runtime {
	if m.getRT == nil {
		return nil
	}
	return m.getRT()
}

// State returns the current browser state view.
func (m *Manager) State(ctx context.Context) StateView {
	m.mu.Lock()
	s := m.state
	addr := m.cdpAddr
	m.mu.Unlock()

	cdpOK := false
	if s == StateRunning && addr != "" {
		cdpOK = CheckCDP(ctx, addr)
	}
	return StateView{
		State:        s,
		CDPReachable: cdpOK,
		Available:    true,
	}
}

// Start launches the browser daemon stack if not already running. Idempotent.
// Startup order: Xvfb → chromium (headful) → fcitx5 → x11vnc → CDP relay.
// [AC-S62374c-1-1] [AC-S62374c-1-2] [AC-S62374c-1-3]
func (m *Manager) Start(ctx context.Context) (StartResponse, error) {
	m.mu.Lock()
	if m.state == StateRunning && m.pid != "" {
		// Check chromium process still alive (may have died without us knowing).
		pid := m.pid
		m.mu.Unlock()
		alive, err := m.isPIDAlive(ctx, pid)
		if err == nil && alive {
			m.log.Info("browser: already running (idempotent)", "inst", m.inst, "pid", pid)
			return StartResponse{State: StateRunning}, nil
		}
		// Process gone — restart.
		m.log.Info("browser: chromium PID no longer alive, restarting", "inst", m.inst, "pid", pid)
		m.mu.Lock()
		m.pid = ""
		m.state = StateStopped
		// fall through
	}
	m.state = StateStarting
	m.mu.Unlock()

	// Resolve container bridge IP from the CURRENT runtime status.
	rt := m.rtNow()
	if rt == nil {
		m.mu.Lock()
		m.state = StateStopped
		m.mu.Unlock()
		return StartResponse{}, fmt.Errorf("browser Start: runtime unavailable")
	}
	status := rt.Status()
	if status.Address == "" {
		// The runtime is resolved but its container IP isn't populated yet — e.g.
		// no terminal has attached since a palmux restart (the IP is set by
		// rt.Start). Ensure the runtime is up (idempotent) so the browser doesn't
		// require a prior terminal attach to work.
		if err := rt.Start(ctx); err != nil {
			m.mu.Lock()
			m.state = StateStopped
			m.mu.Unlock()
			return StartResponse{}, fmt.Errorf("browser Start: ensure runtime: %w", err)
		}
		status = rt.Status()
	}
	addr := status.Address
	if addr == "" {
		m.mu.Lock()
		m.state = StateStopped
		m.mu.Unlock()
		return StartResponse{}, fmt.Errorf("browser Start: container IP not available (runtime state=%s)", status.State)
	}

	// Verify chromium is available in the container.
	if err := m.ensureChromiumPresent(ctx); err != nil {
		m.mu.Lock()
		m.state = StateStopped
		m.mu.Unlock()
		return StartResponse{}, err
	}

	// Ensure the persistent profile dir bind-mount is in place (idempotent).
	profileDir, err := m.ensureProfileMount(ctx)
	if err != nil {
		m.mu.Lock()
		m.state = StateStopped
		m.mu.Unlock()
		return StartResponse{}, fmt.Errorf("browser Start: profile mount: %w", err)
	}

	// ── 1. Launch Xvfb ───────────────────────────────────────────────────────
	xvfbPID, err := m.launchXvfb(ctx)
	if err != nil {
		m.mu.Lock()
		m.state = StateStopped
		m.mu.Unlock()
		return StartResponse{}, fmt.Errorf("browser Start: xvfb: %w", err)
	}
	m.mu.Lock()
	m.xvfbPID = xvfbPID
	m.cdpAddr = addr
	m.mu.Unlock()

	// Brief pause for Xvfb to initialise its socket.
	select {
	case <-ctx.Done():
		return StartResponse{}, ctx.Err()
	case <-time.After(500 * time.Millisecond):
	}

	// ── 2. Launch chromium (headful) ─────────────────────────────────────────
	chrPID, err := m.launchChromium(ctx, addr, profileDir)
	if err != nil {
		m.mu.Lock()
		m.state = StateStopped
		m.mu.Unlock()
		_, _ = m.Stop(ctx)
		return StartResponse{}, fmt.Errorf("browser Start: chromium: %w", err)
	}
	m.mu.Lock()
	m.pid = chrPID
	m.mu.Unlock()

	// ── 3. Launch fcitx5 (server-side Japanese IME) ──────────────────────────
	fcitxPID, err := m.launchFcitx5(ctx)
	if err != nil {
		// fcitx5 failure is non-fatal: log and continue (ASCII input still works).
		m.log.Warn("browser: fcitx5 launch failed (Japanese IME unavailable)", "inst", m.inst, "err", err)
	}
	m.mu.Lock()
	m.fcitxPID = fcitxPID
	m.mu.Unlock()

	// ── 4. Launch x11vnc ─────────────────────────────────────────────────────
	vncPID, err := m.launchX11VNC(ctx)
	if err != nil {
		m.mu.Lock()
		m.state = StateStopped
		m.mu.Unlock()
		_, _ = m.Stop(ctx)
		return StartResponse{}, fmt.Errorf("browser Start: x11vnc: %w", err)
	}
	m.mu.Lock()
	m.vncPID = vncPID
	m.mu.Unlock()

	// ── 5. CDP relay (bridgeIP:9222 → 127.0.0.1:9222) ───────────────────────
	// Wait for local CDP first (chromium takes a few seconds to bind in real mode).
	if err := m.waitLocalCDP(ctx, 8*time.Second); err != nil {
		m.log.Warn("browser: local CDP not up after chromium launch", "inst", m.inst, "err", err)
	}
	relayPID, relErr := m.startRelay(ctx, addr)
	if relErr != nil {
		m.log.Warn("browser: CDP relay failed (Claude CDP may be unavailable)", "inst", m.inst, "err", relErr)
	}
	m.mu.Lock()
	m.relayPID = relayPID
	m.mu.Unlock()

	// ── Wait for VNC port to open ─────────────────────────────────────────────
	// VNC readiness is the gate for running state (user can attach via noVNC).
	// 5 s is ample for x11vnc inside the container to start; the frontend polls
	// state every 5 s and will pick up "running" on the next tick if we miss it.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	finalState := StateStarting
	if m.waitVNC(waitCtx, addr) {
		finalState = StateRunning
	}

	m.mu.Lock()
	m.state = finalState
	m.mu.Unlock()

	m.log.Info("browser: stack launched", "inst", m.inst,
		"xvfbPID", xvfbPID, "chromiumPID", chrPID,
		"vncPID", vncPID, "addr", addr, "state", finalState)
	return StartResponse{State: finalState}, nil
}

// Stop kills all browser daemons: relay, x11vnc, fcitx5, chromium, Xvfb.
// [AC-S62374c-1-1]
func (m *Manager) Stop(ctx context.Context) (StopResponse, error) {
	m.mu.Lock()
	relayPID := m.relayPID
	m.mu.Unlock()

	rt := m.rtNow()
	if rt != nil {
		// Kill the CDP relay (single PID).
		if relayPID != "" {
			_, _ = rt.Exec(ctx, []string{"kill", relayPID}, runtime.ExecOpts{})
		}
		// Kill x11vnc, fcitx5, chromium (whole process tree), Xvfb.
		killCmds := []string{
			"pkill -f x11vnc || true",
			"pkill fcitx5 || true",
			fmt.Sprintf("pkill -f 'remote-debugging-port=%d' || true", CDPPort),
			fmt.Sprintf("pkill -f 'Xvfb %s' || true", xDisplay),
		}
		for _, cmd := range killCmds {
			res, err := rt.Exec(ctx, []string{"sh", "-c", cmd}, runtime.ExecOpts{})
			if err != nil {
				m.log.Warn("browser Stop: exec error", "inst", m.inst, "cmd", cmd, "err", err)
			} else if res.ExitCode != 0 {
				m.log.Debug("browser Stop: pkill non-zero (process may not have been running)",
					"inst", m.inst, "cmd", cmd, "exit", res.ExitCode)
			}
		}
	}

	m.mu.Lock()
	m.state = StateStopped
	m.xvfbPID = ""
	m.pid = ""
	m.fcitxPID = ""
	m.vncPID = ""
	m.relayPID = ""
	m.cdpAddr = ""
	m.mu.Unlock()

	m.log.Info("browser: stack stopped", "inst", m.inst)
	return StopResponse{State: StateStopped}, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// ensureChromiumPresent checks that the chromium binary exists in the container.
// Returns a user-friendly error with guidance if missing.
// [AC-S62374c-1-4]
func (m *Manager) ensureChromiumPresent(ctx context.Context) error {
	rt := m.rtNow()
	if rt == nil {
		return fmt.Errorf("browser: runtime unavailable")
	}
	res, err := rt.Exec(ctx,
		[]string{"sh", "-c", "command -v " + chromiumBin + " >/dev/null 2>&1 && echo found"},
		runtime.ExecOpts{},
	)
	if err != nil {
		return fmt.Errorf("browser: checking chromium presence in container %q: %w", m.inst, err)
	}
	if !strings.Contains(res.Stdout, "found") {
		return fmt.Errorf("browser: %q not found in container %q"+
			" — rebuild the palmux-ws image with chromium installed"+
			" (see images/workspace-default/build.sh and install with"+
			" `incus image import dist/palmux-ws.tar.gz --alias palmux-ws`)",
			chromiumBin, m.inst)
	}
	return nil
}

// ensureProfileMount ensures the persistent browser profile directory is
// bind-mounted into the container. Creates the host-side dir if absent and
// adds an incus disk device (idempotent via "already exists" tolerance).
//
// Host path:      ~/.local/share/palmux-browser/<inst>/
// Container path: same absolute path (mirrors the ~/.claude mount pattern).
//
// [AC-S62374c-1-2]
func (m *Manager) ensureProfileMount(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ensureProfileMount: get home dir: %w", err)
	}

	// Per-workspace subdirectory uses the instance name to avoid collisions.
	hostDir := filepath.Join(home, persistBaseName, m.inst)
	if err := os.MkdirAll(hostDir, 0o700); err != nil {
		return "", fmt.Errorf("ensureProfileMount: mkdir %s: %w", hostDir, err)
	}

	// Device name is fixed per workspace; ≤64 chars.
	// Using "palmux-browser-profile" (22 chars) is safe.
	devName := "palmux-browser-profile"

	// Add disk device (idempotent — DefaultDeviceAdder tolerates "already exists").
	if err := m.deviceAdder(ctx, m.inst, devName, hostDir, hostDir); err != nil {
		return "", fmt.Errorf("ensureProfileMount: add disk device: %w", err)
	}

	return hostDir, nil
}

// launchXvfb starts Xvfb on display :99 inside the container and returns its PID.
// Xvfb provides the virtual framebuffer that chromium renders into.
func (m *Manager) launchXvfb(ctx context.Context) (string, error) {
	rt := m.rtNow()
	if rt == nil {
		return "", fmt.Errorf("launchXvfb: runtime unavailable")
	}
	cmd := fmt.Sprintf(
		"nohup Xvfb %s -screen 0 1600x1000x24 >/tmp/xvfb.log 2>&1 & echo $!",
		xDisplay,
	)
	res, err := rt.Exec(ctx, []string{"sh", "-c", cmd}, runtime.ExecOpts{})
	if err != nil {
		return "", fmt.Errorf("launchXvfb: exec: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("launchXvfb: exit %d: %s", res.ExitCode, res.Stderr)
	}
	return parsePID(res.Stdout, "launchXvfb")
}

// launchChromium starts headful chromium on display :99 in the container.
// Flags kept from the headless era: --no-sandbox (unprivileged container),
// --remote-debugging-port=9222 (CDP for Claude), --remote-allow-origins=*,
// --user-data-dir (persistent profile). IME env vars enable fcitx5.
// [AC-S62374c-1-1] [AC-S62374c-1-3]
func (m *Manager) launchChromium(ctx context.Context, containerIP, profileDir string) (string, error) {
	// containerIP is unused here (CDP is reached via relay on bridge IP); kept in
	// signature for symmetry with other launchers and logging.
	_ = containerIP

	cmd := fmt.Sprintf(
		"DISPLAY=%s"+
			" GTK_IM_MODULE=fcitx"+
			" QT_IM_MODULE=fcitx"+
			" XMODIFIERS=@im=fcitx"+
			" nohup %s"+
			" --no-sandbox"+
			" --remote-debugging-port=%d"+
			" --remote-allow-origins=*"+
			" --user-data-dir=%s"+
			" >/tmp/chromium.log 2>&1 & echo $!",
		xDisplay,
		chromiumBin,
		CDPPort,
		profileDir,
	)

	rt := m.rtNow()
	if rt == nil {
		return "", fmt.Errorf("launchChromium: runtime unavailable")
	}
	res, err := rt.Exec(ctx, []string{"sh", "-c", cmd}, runtime.ExecOpts{})
	if err != nil {
		return "", fmt.Errorf("launchChromium: exec: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("launchChromium: exit %d: %s", res.ExitCode, res.Stderr)
	}
	return parsePID(res.Stdout, "launchChromium")
}

// launchFcitx5 starts the fcitx5 server-side IME on display :99.
// Non-fatal: if fcitx5 is missing the browser still works for ASCII input.
func (m *Manager) launchFcitx5(ctx context.Context) (string, error) {
	rt := m.rtNow()
	if rt == nil {
		return "", fmt.Errorf("launchFcitx5: runtime unavailable")
	}
	cmd := fmt.Sprintf(
		"DISPLAY=%s nohup fcitx5 --disable=notifications >/tmp/fcitx5.log 2>&1 & echo $!",
		xDisplay,
	)
	res, err := rt.Exec(ctx, []string{"sh", "-c", cmd}, runtime.ExecOpts{})
	if err != nil {
		return "", fmt.Errorf("launchFcitx5: exec: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("launchFcitx5: exit %d: %s", res.ExitCode, res.Stderr)
	}
	return parsePID(res.Stdout, "launchFcitx5")
}

// launchX11VNC starts x11vnc on display :99, listening on all interfaces on
// port 5900. The -forever and -shared flags keep it running across client
// disconnects and allow multiple simultaneous viewers.
// No -listen flag: x11vnc must bind 0.0.0.0:5900 so both localhost (inside
// container) and the bridge IP (from palmux host) can reach it.
func (m *Manager) launchX11VNC(ctx context.Context) (string, error) {
	rt := m.rtNow()
	if rt == nil {
		return "", fmt.Errorf("launchX11VNC: runtime unavailable")
	}
	cmd := fmt.Sprintf(
		"nohup x11vnc -display %s -forever -shared -nopw -rfbport %d -loop >/tmp/x11vnc.log 2>&1 & echo $!",
		xDisplay,
		VNCPort,
	)
	res, err := rt.Exec(ctx, []string{"sh", "-c", cmd}, runtime.ExecOpts{})
	if err != nil {
		return "", fmt.Errorf("launchX11VNC: exec: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("launchX11VNC: exit %d: %s", res.ExitCode, res.Stderr)
	}
	return parsePID(res.Stdout, "launchX11VNC")
}

// parsePID extracts the first non-empty line from stdout as the PID string.
func parsePID(stdout, caller string) (string, error) {
	for _, line := range strings.Split(stdout, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: no PID in stdout %q", caller, stdout)
}

// waitVNC polls the VNC port on the container bridge IP until it opens or the
// context expires. Returns true when the port is reachable.
func (m *Manager) waitVNC(ctx context.Context, addr string) bool {
	target := net.JoinHostPort(addr, fmt.Sprintf("%d", VNCPort))
	for {
		conn, err := net.DialTimeout("tcp", target, 1*time.Second)
		if err == nil {
			_ = conn.Close()
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// waitLocalCDP polls (inside the container) for chromium's CDP on
// 127.0.0.1:9222 to come up.
func (m *Manager) waitLocalCDP(ctx context.Context, timeout time.Duration) error {
	rt := m.rtNow()
	if rt == nil {
		return fmt.Errorf("runtime unavailable")
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, err := rt.Exec(ctx, []string{"sh", "-c",
			fmt.Sprintf("curl -s -o /dev/null -w '%%{http_code}' http://127.0.0.1:%d/json/version", CDPPort)},
			runtime.ExecOpts{})
		if err == nil && strings.Contains(res.Stdout, "200") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fmt.Errorf("local CDP not reachable within %s", timeout)
}

// startRelay launches the in-container TCP relay forwarding
// <listenIP>:9222 → 127.0.0.1:9222 and returns its PID. [AC-S62374c-1-3]
func (m *Manager) startRelay(ctx context.Context, listenIP string) (string, error) {
	rt := m.rtNow()
	if rt == nil {
		return "", fmt.Errorf("startRelay: runtime unavailable")
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(cdpRelayScript))
	cmd := fmt.Sprintf("echo '%s' | base64 -d | nohup python3 - %s %d >/dev/null 2>&1 & echo $!",
		b64, listenIP, CDPPort)
	res, err := rt.Exec(ctx, []string{"sh", "-c", cmd}, runtime.ExecOpts{})
	if err != nil {
		return "", fmt.Errorf("startRelay exec: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("startRelay exit %d: %s", res.ExitCode, res.Stderr)
	}
	return parsePID(res.Stdout, "startRelay")
}

// isPIDAlive checks whether a process with the given PID is alive in the
// container using `kill -0 <pid>` (signal 0 = existence check, no kill).
func (m *Manager) isPIDAlive(ctx context.Context, pid string) (bool, error) {
	rt := m.rtNow()
	if rt == nil {
		return false, fmt.Errorf("isPIDAlive: runtime unavailable")
	}
	res, err := rt.Exec(ctx, []string{"kill", "-0", pid}, runtime.ExecOpts{})
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// CheckCDP is defined in cdp.go.
