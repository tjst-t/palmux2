// Package browser implements the lifecycle manager for a per-workspace headless
// Chromium instance running inside an incus container.
//
// Design (S62374c-1):
//   - Chromium is launched ONLY via an explicit POST .../tabs/browser/start.
//     Workspace open / tab display do NOT auto-launch.
//   - The --user-data-dir is placed under a host path that is bind-mounted into
//     the container (persistence across container re-creation).
//   - CDP is bound only to the container's bridge IP (never 0.0.0.0).
//     The palmux host can reach containerIP:9222; Caddy never gets a route for it.
//   - State machine: stopped → starting → running (or back to stopped on stop).
//
// Story 2 (CDP screencast / navigate) — TODO: add WS proxy and navigate endpoint
// once the screencast Story is scheduled.
package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
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

// Manager controls the chromium lifecycle for one workspace.
// It is safe for concurrent use.
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

	mu       sync.Mutex
	state    BrowserState
	pid      string // PID of the chromium process inside the container (as string)
	relayPID string // PID of the in-container CDP relay (bridgeIP:9222 → 127.0.0.1:9222)
	cdpAddr  string // containerIP used for CDP; set on start
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
	url := ""
	if s == StateRunning && addr != "" {
		cdpOK = CheckCDP(ctx, addr)
		if cdpOK {
			url = fmt.Sprintf("http://%s:%d", addr, CDPPort)
		}
	}
	return StateView{
		State:        s,
		CDPReachable: cdpOK,
		URL:          url,
		Available:    true,
	}
}

// Start launches chromium if not already running. Idempotent.
// [AC-S62374c-1-1] [AC-S62374c-1-2] [AC-S62374c-1-3]
func (m *Manager) Start(ctx context.Context) (StartResponse, error) {
	m.mu.Lock()
	if m.state == StateRunning && m.pid != "" {
		// Check process still alive (may have died without us knowing).
		pid := m.pid
		m.mu.Unlock()
		alive, err := m.isPIDAlive(ctx, pid)
		if err == nil && alive {
			m.log.Info("browser: chromium already running (idempotent)", "inst", m.inst, "pid", pid)
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

	// Launch chromium in the background, capture PID.
	pid, err := m.launchChromium(ctx, addr, profileDir)
	if err != nil {
		m.mu.Lock()
		m.state = StateStopped
		m.mu.Unlock()
		return StartResponse{}, fmt.Errorf("browser Start: launch: %w", err)
	}

	m.mu.Lock()
	m.pid = pid
	m.cdpAddr = addr
	m.state = StateStarting
	m.mu.Unlock()

	// chromium binds CDP to 127.0.0.1 inside the container. Wait for it to come
	// up locally, then start the relay so the palmux host (on the bridge) can
	// reach it at <bridgeIP>:9222.
	if err := m.waitLocalCDP(ctx, 10*time.Second); err != nil {
		m.log.Warn("browser: local CDP not up after launch", "inst", m.inst, "err", err)
	}
	relayPID, relErr := m.startRelay(ctx, addr)
	if relErr != nil {
		m.mu.Lock()
		m.state = StateStopped
		m.mu.Unlock()
		_, _ = m.Stop(ctx) // best-effort: kill the chromium we just launched
		return StartResponse{}, fmt.Errorf("browser Start: cdp relay: %w", relErr)
	}
	m.mu.Lock()
	m.relayPID = relayPID
	m.mu.Unlock()

	// Wait briefly for CDP to become reachable before returning the state.
	// We do a short poll so the response reflects the true state when possible.
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	finalState := StateStarting
outer:
	for {
		if CheckCDP(waitCtx, addr) {
			finalState = StateRunning
			break outer
		}
		select {
		case <-waitCtx.Done():
			break outer
		case <-time.After(300 * time.Millisecond):
		}
	}

	m.mu.Lock()
	m.state = finalState
	m.mu.Unlock()

	m.log.Info("browser: chromium launched", "inst", m.inst, "pid", pid, "addr", addr, "state", finalState)
	return StartResponse{State: finalState}, nil
}

// Stop kills the chromium process.
// [AC-S62374c-1-1]
func (m *Manager) Stop(ctx context.Context) (StopResponse, error) {
	m.mu.Lock()
	relayPID := m.relayPID
	m.mu.Unlock()

	rt := m.rtNow()
	if rt != nil {
		// Kill the relay (single PID) and ALL chromium processes. `kill <pid>` on
		// the launcher PID does not reliably reap chromium's zygote/renderer tree,
		// so pkill by the unique remote-debugging-port flag catches the whole set.
		if relayPID != "" {
			_, _ = rt.Exec(ctx, []string{"kill", relayPID}, runtime.ExecOpts{})
		}
		res, err := rt.Exec(ctx, []string{"sh", "-c",
			fmt.Sprintf("pkill -f 'remote-debugging-port=%d' || true", CDPPort)}, runtime.ExecOpts{})
		if err != nil {
			m.log.Warn("browser Stop: pkill chromium exec error", "inst", m.inst, "err", err)
		} else if res.ExitCode != 0 {
			m.log.Warn("browser Stop: pkill chromium non-zero", "inst", m.inst, "exit", res.ExitCode, "stderr", res.Stderr)
		}
	}

	m.mu.Lock()
	m.state = StateStopped
	m.pid = ""
	m.relayPID = ""
	m.cdpAddr = ""
	m.mu.Unlock()

	m.log.Info("browser: chromium stopped", "inst", m.inst)
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

// launchChromium starts chromium in the container background and returns its PID.
//
// Chromium launch flags (verified):
//
//	--headless=new               : new headless mode (Chrome 112+)
//	--no-sandbox                 : required inside an unprivileged container
//	--disable-gpu                : headless has no GPU
//	--remote-debugging-port=9222 : CDP port
//	--remote-debugging-address=<IP> : bind only to bridge IP, NOT 0.0.0.0
//	--remote-allow-origins=*     : REQUIRED — Chrome rejects raw CDP WS otherwise
//	--user-data-dir=<path>       : persistent profile under bind-mounted host path
//	about:blank                  : no default page load
//
// [AC-S62374c-1-1] [AC-S62374c-1-3]
func (m *Manager) launchChromium(ctx context.Context, containerIP, profileDir string) (string, error) {
	// NOTE: --remote-debugging-address is intentionally omitted. Modern Chrome
	// ignores it and binds CDP to 127.0.0.1 only; we reach it via the relay
	// (startRelay) on the bridge IP. containerIP is unused here but kept in the
	// signature for clarity/logging.
	_ = containerIP
	launchCmd := fmt.Sprintf(
		"nohup %s"+
			" --headless=new"+
			" --no-sandbox"+
			" --disable-gpu"+
			" --remote-debugging-port=%d"+
			" --remote-allow-origins=*"+
			" --user-data-dir=%s"+
			" about:blank"+
			" >/dev/null 2>&1 & echo $!",
		chromiumBin,
		CDPPort,
		profileDir,
	)

	rt := m.rtNow()
	if rt == nil {
		return "", fmt.Errorf("launchChromium: runtime unavailable")
	}
	res, err := rt.Exec(ctx, []string{"sh", "-c", launchCmd}, runtime.ExecOpts{})
	if err != nil {
		return "", fmt.Errorf("launchChromium: exec: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("launchChromium: exit %d: %s", res.ExitCode, res.Stderr)
	}

	// Parse PID from stdout (first non-empty line from `echo $!`).
	for _, line := range strings.Split(res.Stdout, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf("launchChromium: no PID in stdout %q", res.Stdout)
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
	for _, line := range strings.Split(res.Stdout, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf("startRelay: no PID in stdout %q", res.Stdout)
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

// CheckCDP does a quick HTTP GET to /json/version on the CDP endpoint and
// returns true if the response is HTTP 200. Used for state detection.
// [AC-S62374c-1-6]
func CheckCDP(ctx context.Context, addr string) bool {
	url := fmt.Sprintf("http://%s:%d/json/version", addr, CDPPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
