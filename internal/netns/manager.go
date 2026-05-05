package netns

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

const (
	// hostPortRangeMin / Max is the auto-allocate range for exposed ports.
	hostPortRangeMin = 13000
	hostPortRangeMax = 13999
)

// worktreeProcs holds the live process handles for a worktree's netns processes.
type worktreeProcs struct {
	anchor *exec.Cmd
	slirp  *exec.Cmd
}

// Manager is the central controller for netns lifecycles.
// It is safe for concurrent use.
type Manager struct {
	mu          sync.Mutex
	available   bool   // false when slirp4netns is missing
	state       *state // persistent state
	runtimeDir  string // /run/user/<uid>/palmux/netns or /tmp/palmux-netns
	dataDir     string // tmp/ directory for netns-state.json
	logger      *slog.Logger
	discoveries map[string]*discoveryLoop // worktreeID → discovery loop
	caddy       *CaddyIntegration         // nil if Caddy disabled
	procs       map[string]*worktreeProcs // worktreeID → live process handles
}

// New creates a Manager. dataDir is the "tmp/" directory for netns-state.json.
// If slirp4netns is not found in PATH, the manager operates in no-op mode.
func New(dataDir string, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Check slirp4netns availability.
	_, slirpErr := exec.LookPath("slirp4netns")
	available := slirpErr == nil
	if !available {
		logger.Warn("slirp4netns not found in PATH — network isolation disabled; repos.json settings preserved",
			"hint", "install slirp4netns to enable per-worktree network isolation")
	}

	// Also do a quick smoke-test of unprivileged userns to catch AppArmor
	// restrictions early.
	if available {
		available = smokeTestUserns(logger)
	}

	st, err := newState(StatePath(dataDir))
	if err != nil {
		return nil, fmt.Errorf("netns.New: %w", err)
	}

	runtimeDir := runtimeNetnsDir()

	m := &Manager{
		available:   available,
		state:       st,
		runtimeDir:  runtimeDir,
		dataDir:     dataDir,
		logger:      logger,
		discoveries: map[string]*discoveryLoop{},
		procs:       map[string]*worktreeProcs{},
	}
	return m, nil
}

// SetCaddy attaches a CaddyIntegration so port-expose calls also update Caddy.
func (m *Manager) SetCaddy(c *CaddyIntegration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.caddy = c
}

// Available reports whether network isolation is usable on this host.
func (m *Manager) Available() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.available
}

// Create creates a new user+net namespace for the given worktree and starts
// slirp4netns for outbound connectivity. If isolation is not available
// (slirp4netns missing or AppArmor restricted), it logs a warning and returns
// a no-op WorktreeState.
//
// parentWorktreeID, if non-empty, means this worktree should inherit the
// parent's netns (subagent inherit mode).
func (m *Manager) Create(ctx context.Context, worktreeID, parentWorktreeID string) (*WorktreeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.available {
		m.logger.Warn("netns: skipping isolation (not available)", "worktreeId", worktreeID)
		ws := &WorktreeState{WorktreeID: worktreeID, IsolateNetwork: false}
		_ = m.state.Upsert(*ws)
		return ws, nil
	}

	// If a parent is specified and it has a netns, inherit it.
	if parentWorktreeID != "" {
		parent, ok := m.state.Get(parentWorktreeID)
		if ok && parent.NetnsPath != "" {
			ws := WorktreeState{
				WorktreeID:       worktreeID,
				NetnsPath:        parent.NetnsPath,
				SlirpPID:         parent.SlirpPID,
				SlirpSocketPath:  parent.SlirpSocketPath,
				IsolateNetwork:   true,
				ParentWorktreeID: parentWorktreeID,
			}
			if err := m.state.Upsert(ws); err != nil {
				return nil, err
			}
			return &ws, nil
		}
	}

	// Check for existing (pre-existing from a crash/restart).
	if existing, ok := m.state.Get(worktreeID); ok && existing.NetnsPath != "" {
		if nsExists(existing.NetnsPath) {
			// Already up — re-use.
			return &existing, nil
		}
	}

	nsPath, anchorPID, anchorCmd, err := m.createNetns(worktreeID)
	if err != nil {
		return nil, fmt.Errorf("netns.Create: create netns: %w", err)
	}

	slirpPID, socketPath, slirpCmd, err := m.startSlirp(ctx, worktreeID, anchorPID)
	if err != nil {
		// Best-effort cleanup: kill anchor process.
		_ = anchorCmd.Process.Kill()
		_ = anchorCmd.Wait()
		return nil, fmt.Errorf("netns.Create: start slirp4netns: %w", err)
	}

	// Store process handles for proper lifecycle management (Wait on kill).
	m.procs[worktreeID] = &worktreeProcs{anchor: anchorCmd, slirp: slirpCmd}

	ws := WorktreeState{
		WorktreeID:      worktreeID,
		NetnsPath:       nsPath,
		AnchorPID:       anchorPID,
		SlirpPID:        slirpPID,
		SlirpSocketPath: socketPath,
		IsolateNetwork:  true,
	}
	if err := m.state.Upsert(ws); err != nil {
		return nil, err
	}
	m.logger.Info("netns: created", "worktreeId", worktreeID, "nsPath", nsPath, "slirpPID", slirpPID)
	return &ws, nil
}

// Destroy tears down the netns and slirp4netns for a worktree.
// If the worktree inherited a parent's netns, only the state entry is deleted.
func (m *Manager) Destroy(ctx context.Context, worktreeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop any running discovery loop.
	if dl, ok := m.discoveries[worktreeID]; ok {
		dl.stop()
		delete(m.discoveries, worktreeID)
	}

	ws, ok := m.state.Get(worktreeID)
	if !ok {
		return nil
	}

	// If this worktree inherited a parent's netns, only clean up state.
	if ws.ParentWorktreeID != "" {
		m.logger.Info("netns: cleanup inherited ns state", "worktreeId", worktreeID, "parent", ws.ParentWorktreeID)
		return m.state.Delete(worktreeID)
	}

	// Retrieve stored process handles for proper reaping.
	procs := m.procs[worktreeID]
	delete(m.procs, worktreeID)

	// Kill slirp4netns if it's our own. Send SIGKILL to ensure immediate
	// termination, then Wait() to reap the zombie.
	if procs != nil && procs.slirp != nil {
		_ = syscall.Kill(-procs.slirp.Process.Pid, syscall.SIGKILL)
		_ = procs.slirp.Process.Kill()
		go func() { _ = procs.slirp.Wait() }()
	} else if ws.SlirpPID > 0 {
		// Fallback for processes not tracked in-memory (e.g. after restart).
		_ = syscall.Kill(-ws.SlirpPID, syscall.SIGKILL)
		if p, err := os.FindProcess(ws.SlirpPID); err == nil {
			_ = p.Kill()
		}
	}

	// Kill the anchor process (this destroys the netns when no other processes
	// reference it). Use SIGKILL to ensure immediate cleanup.
	if procs != nil && procs.anchor != nil {
		_ = syscall.Kill(-procs.anchor.Process.Pid, syscall.SIGKILL)
		_ = procs.anchor.Process.Kill()
		go func() { _ = procs.anchor.Wait() }()
	} else if ws.AnchorPID > 0 {
		// Fallback for processes not tracked in-memory.
		_ = syscall.Kill(-ws.AnchorPID, syscall.SIGKILL)
		if p, err := os.FindProcess(ws.AnchorPID); err == nil {
			_ = p.Kill()
		}
	}

	// Remove the caddy snippet for all ports before cleanup.
	if m.caddy != nil && len(ws.Ports) > 0 {
		for _, pm := range ws.Ports {
			_ = m.caddy.RemoveRoute(pm.HostPort)
		}
		_ = m.caddy.Reload()
	}

	// Unmount the bind-mounted ns file.
	if ws.NetnsPath != "" {
		if err := bindUnmountNs(ws.NetnsPath); err != nil {
			m.logger.Warn("netns: unmount ns failed", "path", ws.NetnsPath, "err", err)
		}
	}

	if err := m.state.Delete(worktreeID); err != nil {
		return err
	}
	m.logger.Info("netns: destroyed", "worktreeId", worktreeID)
	return nil
}

// Get returns the current WorktreeState for a worktree.
func (m *Manager) Get(worktreeID string) (*WorktreeState, bool) {
	ws, ok := m.state.Get(worktreeID)
	if !ok {
		return nil, false
	}
	return &ws, true
}

// NsenterArgs returns the args to prepend before a command to run it inside
// a worktree's netns. Returns nil if isolation is not active for this worktree.
func (m *Manager) NsenterArgs(worktreeID string) []string {
	ws, ok := m.state.Get(worktreeID)
	if !ok || !ws.IsolateNetwork || ws.NetnsPath == "" {
		return nil
	}
	return []string{"nsenter", "--net=" + ws.NetnsPath, "--"}
}

// Reconcile checks state vs. actual kernel state and cleans up orphaned entries.
func (m *Manager) Reconcile(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

	all := m.state.All()
	for _, ws := range all {
		if ws.ParentWorktreeID != "" {
			continue // inherited; parent owns the lifecycle
		}
		if ws.NetnsPath == "" || nsExists(ws.NetnsPath) {
			continue
		}
		// Orphaned ns entry.
		m.logger.Info("netns: reconcile: removing orphan", "worktreeId", ws.WorktreeID, "nsPath", ws.NetnsPath)
		_ = m.state.Delete(ws.WorktreeID)
	}
}

// StartDiscovery starts a polling loop that detects listening ports in a
// worktree's netns and broadcasts events via the provided publish function.
// The EventPublisher is called with worktreeID and the listener list.
func (m *Manager) StartDiscovery(ctx context.Context, worktreeID string, publish EventPublisher) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if dl, ok := m.discoveries[worktreeID]; ok {
		dl.stop()
	}

	ws, ok := m.state.Get(worktreeID)
	if !ok || !ws.IsolateNetwork || ws.NetnsPath == "" {
		return
	}

	dl := newDiscoveryLoop(worktreeID, ws.NetnsPath, m.state, publish, m.logger)
	m.discoveries[worktreeID] = dl
	go dl.run(ctx)
}

// StopDiscovery stops the polling loop for a worktree.
func (m *Manager) StopDiscovery(worktreeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dl, ok := m.discoveries[worktreeID]; ok {
		dl.stop()
		delete(m.discoveries, worktreeID)
	}
}

// AllocateHostPort finds a free port in the auto-allocate range (13000–13999).
// Must be called with no lock held (it takes one internally).
func (m *Manager) AllocateHostPort() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allocateHostPortLocked()
}

func (m *Manager) allocateHostPortLocked() (int, error) {
	for port := hostPortRangeMin; port <= hostPortRangeMax; port++ {
		if m.state.IsHostPortInUse(port) {
			continue
		}
		// Probe with net.Listen to confirm the port is free on the host.
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		ln.Close()
		return port, nil
	}
	return 0, fmt.Errorf("netns: no free port in range %d-%d", hostPortRangeMin, hostPortRangeMax)
}

// IsHostPortInUse returns true if any worktree has mapped the given hostPort.
func (m *Manager) IsHostPortInUse(hostPort int) bool {
	return m.state.IsHostPortInUse(hostPort)
}

// AddPortMapping records a port mapping and calls slirp4netns' add_hostfwd.
func (m *Manager) AddPortMapping(ctx context.Context, worktreeID string, internalPort, hostPort int) (PortMapping, error) {
	ws, ok := m.state.Get(worktreeID)
	if !ok {
		return PortMapping{}, fmt.Errorf("netns: worktree %q not found", worktreeID)
	}
	if ws.SlirpSocketPath == "" {
		return PortMapping{}, fmt.Errorf("netns: worktree %q has no slirp socket", worktreeID)
	}

	pm, err := addHostFwd(ws.SlirpSocketPath, hostPort, internalPort)
	if err != nil {
		return PortMapping{}, fmt.Errorf("netns: add_hostfwd: %w", err)
	}

	if err := m.state.AddPort(worktreeID, pm); err != nil {
		return PortMapping{}, err
	}

	// If Caddy integration is active, add a reverse_proxy route.
	if m.caddy != nil && m.caddy.Enabled() {
		publicURL, err := m.caddy.AddRoute(ws, pm)
		if err != nil {
			m.logger.Warn("netns: caddy route failed", "worktreeId", worktreeID, "hostPort", hostPort, "err", err)
		} else {
			pm.PublicURL = publicURL
			_ = m.state.UpdatePortPublicURL(worktreeID, hostPort, publicURL)
		}
	}

	return pm, nil
}

// RemovePortMapping removes a port mapping and calls slirp4netns' remove_hostfwd.
func (m *Manager) RemovePortMapping(ctx context.Context, worktreeID string, hostPort int) error {
	ws, ok := m.state.Get(worktreeID)
	if !ok {
		return fmt.Errorf("netns: worktree %q not found", worktreeID)
	}

	// Find the mapping to get the internal port.
	var internalPort int
	for _, pm := range ws.Ports {
		if pm.HostPort == hostPort {
			internalPort = pm.InternalPort
			break
		}
	}
	if internalPort == 0 {
		return fmt.Errorf("netns: host port %d not found for worktree %q", hostPort, worktreeID)
	}

	if ws.SlirpSocketPath != "" {
		if err := removeHostFwd(ws.SlirpSocketPath, hostPort, internalPort); err != nil {
			m.logger.Warn("netns: remove_hostfwd failed", "err", err)
		}
	}

	if m.caddy != nil {
		if err := m.caddy.RemoveRoute(hostPort); err != nil {
			m.logger.Warn("netns: caddy remove route failed", "hostPort", hostPort, "err", err)
		} else {
			_ = m.caddy.Reload()
		}
	}

	return m.state.RemovePort(worktreeID, hostPort)
}

// GetPorts returns all port mappings for a worktree.
func (m *Manager) GetPorts(worktreeID string) ([]PortMapping, error) {
	ws, ok := m.state.Get(worktreeID)
	if !ok {
		return nil, fmt.Errorf("netns: worktree %q not found", worktreeID)
	}
	if ws.Ports == nil {
		return []PortMapping{}, nil
	}
	return ws.Ports, nil
}

// ─── internals ────────────────────────────────────────────────────────────────

func runtimeNetnsDir() string {
	uid := os.Getuid()
	rtDir := fmt.Sprintf("/run/user/%d/palmux/netns", uid)
	if err := os.MkdirAll(rtDir, 0o700); err == nil {
		return rtDir
	}
	// Fallback.
	fb := fmt.Sprintf("/tmp/palmux-netns-%d", uid)
	_ = os.MkdirAll(fb, 0o700)
	return fb
}

func nsPath(runtimeDir, worktreeID string) string {
	// Truncate worktreeID to stay well within the 108-byte Unix socket path limit
	// when used as a filename (ns path itself can be longer, but keep it sane).
	if len(worktreeID) > 60 {
		worktreeID = worktreeID[:60]
	}
	return filepath.Join(runtimeDir, worktreeID)
}

// createNetns creates a network namespace by launching a persistent "anchor"
// process inside it via `unshare -Urn`. The anchor is a long-lived `sleep`
// that keeps the namespace alive. The ns path is `/proc/<anchorPID>/ns/net`.
//
// This approach avoids bind-mounts (which require CAP_SYS_ADMIN inside user
// namespaces) and works fully rootless.
//
// Returns (nsPath, anchorPID, anchorCmd, error).
func (m *Manager) createNetns(worktreeID string) (nsPath string, anchorPID int, anchorCmd *exec.Cmd, err error) {
	// Launch the anchor: unshare -Urn <bring up lo; sleep>
	// We use a shell with explicit PID echo so we can recover the child PID.
	// The shell itself runs inside the userns so all capabilities are available
	// for network setup (ip link set lo up). After setup it execs sleep to
	// minimise resource usage.
	cmd := exec.Command("unshare", "-Urn",
		"sh", "-c", "ip link set lo up && exec sleep infinity")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		if isAppArmorError(err, nil) {
			return "", 0, nil, fmt.Errorf("AppArmor restriction detected — run 'sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0' or install AppArmor profile — see docs/INSTALL.md: %w", err)
		}
		return "", 0, nil, fmt.Errorf("unshare start: %w", err)
	}

	pid := cmd.Process.Pid

	// Wait briefly for the namespace to be set up (ip link set lo up needs a moment).
	// Poll for the ns/net file to appear.
	netNSPath := fmt.Sprintf("/proc/%d/ns/net", pid)
	for i := 0; i < 30; i++ { // up to 3 seconds
		if _, statErr := os.Stat(netNSPath); statErr == nil {
			break
		}
		syscall.Nanosleep(&syscall.Timespec{Nsec: 100_000_000}, nil) // 100ms
	}
	if _, statErr := os.Stat(netNSPath); statErr != nil {
		cmd.Process.Kill()
		_ = cmd.Wait()
		return "", 0, nil, fmt.Errorf("anchor process /proc/%d/ns/net not found: %w", pid, statErr)
	}

	m.logger.Info("netns: anchor process started", "worktreeId", worktreeID, "pid", pid, "nsPath", netNSPath)
	return netNSPath, pid, cmd, nil
}

func nsExists(nsPath string) bool {
	// For anchor-based ns paths (/proc/<pid>/ns/net), check the file exists.
	// For legacy bind-mounted paths, also stat.
	_, err := os.Stat(nsPath)
	return err == nil
}

func bindUnmountNs(nsPath string) error {
	// Only try to umount if it looks like a bind-mounted file (not /proc/…/ns/net).
	if len(nsPath) > 6 && nsPath[:6] == "/proc/" {
		// Anchor-based: nothing to unmount. The anchor process dying cleans the ns.
		return nil
	}
	cmd := exec.Command("umount", nsPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("umount %s: %s: %w", nsPath, out, err)
	}
	_ = os.Remove(nsPath)
	return nil
}

func smokeTestUserns(logger *slog.Logger) bool {
	cmd := exec.Command("unshare", "-Un", "true")
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	if err := cmd.Run(); err != nil {
		logger.Warn("netns: unprivileged userns smoke test failed — network isolation disabled",
			"hint", "run 'sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0' or see docs/INSTALL.md",
			"err", err)
		return false
	}
	return true
}

func isAppArmorError(err error, output []byte) bool {
	if err == nil {
		return false
	}
	s := string(output)
	return contains(s, "permission denied") || contains(s, "Operation not permitted")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// startSlirp starts slirp4netns for the given anchor process and returns
// the slirp4netns PID, API socket path, and the exec.Cmd for lifecycle management.
// anchorPID is the parent-namespace PID of the process whose network namespace
// we want to bridge.
func (m *Manager) startSlirp(ctx context.Context, worktreeID string, anchorPID int) (int, string, *exec.Cmd, error) {
	socketPath := slirpSocketPath(worktreeID)

	// Clean up any existing socket.
	_ = os.Remove(socketPath)

	// Start slirp4netns using the anchor process's PID as the target.
	// Use context.Background() — slirp4netns is a long-running daemon that
	// must outlive the HTTP request context that triggered its creation.
	// Lifecycle is managed explicitly via Destroy().
	slirpCmd := exec.CommandContext(context.Background(), "slirp4netns",
		"--configure",
		"--mtu=65520",
		"--disable-host-loopback",
		"--api-socket", socketPath,
		strconv.Itoa(anchorPID), "tap0",
	)
	slirpCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := slirpCmd.Start(); err != nil {
		return 0, "", nil, fmt.Errorf("start slirp4netns: %w", err)
	}

	// Wait for the API socket to appear (up to 5 seconds).
	if err := waitForSocket(socketPath, 5); err != nil {
		_ = slirpCmd.Process.Kill()
		_ = slirpCmd.Wait()
		return 0, "", nil, fmt.Errorf("slirp4netns socket timeout: %w", err)
	}

	m.logger.Info("netns: slirp4netns started", "worktreeId", worktreeID, "slirpPID", slirpCmd.Process.Pid, "anchorPID", anchorPID)
	return slirpCmd.Process.Pid, socketPath, slirpCmd, nil
}

func slirpSocketPath(worktreeID string) string {
	if len(worktreeID) > 40 {
		worktreeID = worktreeID[:40]
	}
	return fmt.Sprintf("/tmp/palmux-slirp4-%s.sock", worktreeID)
}

func waitForSocket(path string, maxSecs int) error {
	for i := 0; i < maxSecs*10; i++ {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		// syscall.FUTEX_WAIT is overkill; use a simple loop.
		syscall.Nanosleep(&syscall.Timespec{Nsec: 100_000_000}, nil) // 100ms
	}
	return fmt.Errorf("socket %s did not appear within %ds", path, maxSecs)
}
