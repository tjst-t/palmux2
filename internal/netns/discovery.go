package netns

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Listener describes a TCP port actively listening inside a netns.
type Listener struct {
	Port        int    `json:"port"`
	ProcessName string `json:"processName,omitempty"`
	PID         int    `json:"pid,omitempty"`
	Exposed     bool   `json:"exposed,omitempty"`
	HostPort    int    `json:"hostPort,omitempty"`
}

// EventPublisher is called when the listener list changes.
type EventPublisher func(worktreeID string, listeners []Listener)

// discoveryLoop polls the netns for listening ports every 2 seconds and
// calls the EventPublisher when the list changes.
type discoveryLoop struct {
	mu          sync.Mutex
	worktreeID  string
	nsPath      string
	state       *state
	publish     EventPublisher
	logger      *slog.Logger
	cancelFn    context.CancelFunc
	lastSnapshot []Listener
}

func newDiscoveryLoop(worktreeID, nsPath string, st *state, publish EventPublisher, logger *slog.Logger) *discoveryLoop {
	return &discoveryLoop{
		worktreeID: worktreeID,
		nsPath:     nsPath,
		state:      st,
		publish:    publish,
		logger:     logger,
	}
}

func (dl *discoveryLoop) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	dl.mu.Lock()
	dl.cancelFn = cancel
	dl.mu.Unlock()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dl.poll(ctx)
		}
	}
}

func (dl *discoveryLoop) stop() {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	if dl.cancelFn != nil {
		dl.cancelFn()
		dl.cancelFn = nil
	}
}

func (dl *discoveryLoop) poll(ctx context.Context) {
	listeners, err := scanListeners(ctx, dl.nsPath)
	if err != nil {
		dl.logger.Debug("netns: discovery poll failed", "worktreeId", dl.worktreeID, "err", err)
		return
	}

	// Join with port forward state to annotate exposed ports.
	ws, ok := dl.state.Get(dl.worktreeID)
	if ok {
		for i, l := range listeners {
			for _, pm := range ws.Ports {
				if pm.InternalPort == l.Port {
					listeners[i].Exposed = true
					listeners[i].HostPort = pm.HostPort
					break
				}
			}
		}
	}

	dl.mu.Lock()
	changed := !listenersEqual(dl.lastSnapshot, listeners)
	if changed {
		dl.lastSnapshot = listeners
	}
	dl.mu.Unlock()

	if changed {
		dl.publish(dl.worktreeID, listeners)
	}
}

// Snapshot returns the most recent listener list without polling.
func (dl *discoveryLoop) Snapshot() []Listener {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	out := make([]Listener, len(dl.lastSnapshot))
	copy(out, dl.lastSnapshot)
	return out
}

// scanListeners runs `nsenter --net=<nsPath> ss -tlnH` and parses the output.
func scanListeners(ctx context.Context, nsPath string) ([]Listener, error) {
	cmd := exec.CommandContext(ctx, "nsenter", "--net="+nsPath, "--",
		"ss", "-tlnH")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}
	return parseSS(out), nil
}

// parseSS parses `ss -tlnH` output.
// Example line: LISTEN 0  128  0.0.0.0:8080  0.0.0.0:*
func parseSS(b []byte) []Listener {
	var listeners []Listener
	scanner := bufio.NewScanner(bytes.NewReader(b))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		// fields[3] is local address:port
		localAddr := fields[3]
		portStr := ""
		if idx := strings.LastIndex(localAddr, ":"); idx >= 0 {
			portStr = localAddr[idx+1:]
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 {
			continue
		}
		l := Listener{Port: port}
		// Try to get process name via /proc.
		l.ProcessName, l.PID = findProcessForPort(port)
		listeners = append(listeners, l)
	}
	return listeners
}

// findProcessForPort looks through /proc/*/net/tcp to find the pid owning a port.
// This is best-effort; returns empty string if not found.
func findProcessForPort(port int) (string, int) {
	hexPort := fmt.Sprintf("%04X", port)

	// Read /proc/net/tcp for the inode.
	inode := findInodeForPort(hexPort, "/proc/net/tcp")
	if inode == "" {
		inode = findInodeForPort(hexPort, "/proc/net/tcp6")
	}
	if inode == "" {
		return "", 0
	}

	// Find the process with this inode.
	return findProcessByInode(inode)
}

func findInodeForPort(hexPort, tcpFile string) string {
	data, err := os.ReadFile(tcpFile)
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		// Format: sl local_addr rem_addr st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode
		if len(fields) < 10 {
			continue
		}
		localAddr := fields[1]
		if idx := strings.LastIndex(localAddr, ":"); idx >= 0 {
			if strings.EqualFold(localAddr[idx+1:], hexPort) {
				return fields[9] // inode
			}
		}
	}
	return ""
}

func findProcessByInode(inode string) (string, int) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return "", 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fdDir := fmt.Sprintf("/proc/%d/fd", pid)
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(fmt.Sprintf("%s/%s", fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if link == "socket:["+inode+"]" {
				comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
				return strings.TrimSpace(string(comm)), pid
			}
		}
	}
	return "", 0
}

func listenersEqual(a, b []Listener) bool {
	if len(a) != len(b) {
		return false
	}
	aMap := map[int]Listener{}
	for _, l := range a {
		aMap[l.Port] = l
	}
	for _, l := range b {
		prev, ok := aMap[l.Port]
		if !ok || prev.Exposed != l.Exposed || prev.HostPort != l.HostPort {
			return false
		}
	}
	return true
}

// GetListeners returns the current listener snapshot for a worktree.
func (m *Manager) GetListeners(worktreeID string) ([]Listener, bool) {
	m.mu.Lock()
	dl, ok := m.discoveries[worktreeID]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	return dl.Snapshot(), true
}
