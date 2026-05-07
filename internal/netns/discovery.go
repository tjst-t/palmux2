package netns

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
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
	inode       string // socket inode, populated by parseProcNetTCP for findProcessForInode lookup
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
	// Look up anchor PID from state — /proc/<anchorPID>/net/tcp is the
	// listener source of truth for the netns and is readable without
	// nsenter / sudo (only the netns's own userns ownership matters,
	// which palmux satisfies as the parent process).
	ws, ok := dl.state.Get(dl.worktreeID)
	if !ok || ws.AnchorPID == 0 {
		return
	}
	listeners, err := scanListenersFromProc(ctx, ws.AnchorPID)
	if err != nil {
		dl.logger.Debug("netns: discovery poll failed", "worktreeId", dl.worktreeID, "err", err)
		return
	}

	// Join with port forward state to annotate exposed ports.
	for i, l := range listeners {
		for _, pm := range ws.Ports {
			if pm.InternalPort == l.Port {
				listeners[i].Exposed = true
				listeners[i].HostPort = pm.HostPort
				break
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

// scanListenersFromProc reads /proc/<anchorPID>/net/tcp{,6} which exposes
// the netns's TCP socket table from outside the netns. This avoids the
// `nsenter --net=...` route which fails with EPERM for unprivileged user
// namespaces (we'd need both --user and --net plus sudo; reading the
// netns's procfs view is privilege-free since the calling process owns
// the anchor PID).
func scanListenersFromProc(ctx context.Context, anchorPID int) ([]Listener, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	seen := map[int]bool{}
	var listeners []Listener
	for _, path := range []string{
		fmt.Sprintf("/proc/%d/net/tcp", anchorPID),
		fmt.Sprintf("/proc/%d/net/tcp6", anchorPID),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			// tcp6 is optional; tcp absence is real failure but we keep going
			continue
		}
		for _, l := range parseProcNetTCP(data) {
			if seen[l.Port] {
				continue
			}
			seen[l.Port] = true
			// Look up the process inside the netns by inode (best-effort —
			// proc inode is global so we can scan /proc/<anchorPID>/task/...,
			// or just use the inode+/proc/*/fd tour for quick names).
			l.ProcessName, l.PID = findProcessForInode(l.inode, anchorPID)
			listeners = append(listeners, l)
		}
	}
	return listeners, nil
}

// parseProcNetTCP parses /proc/<pid>/net/tcp{,6}. Format:
//
//	  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ...
//	   0: 00000000:1435 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 17491 ...
//
// state 0A = TCP_LISTEN. local_address is hex little-endian IP : hex port.
func parseProcNetTCP(b []byte) []Listener {
	var listeners []Listener
	scanner := bufio.NewScanner(bytes.NewReader(b))
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			continue
		}
		// fields[3] = state in hex; "0A" = LISTEN
		if !strings.EqualFold(fields[3], "0A") {
			continue
		}
		idx := strings.LastIndex(fields[1], ":")
		if idx < 0 {
			continue
		}
		port64, err := strconv.ParseInt(fields[1][idx+1:], 16, 32)
		if err != nil || port64 <= 0 {
			continue
		}
		listeners = append(listeners, Listener{
			Port:  int(port64),
			inode: fields[9],
		})
	}
	return listeners
}

// findProcessForInode finds the process (PID + comm) that owns the given socket
// inode by scanning /proc/*/fd. Restricts the search to processes inside the
// same netns as anchorPID for faster + more accurate results — a socket inode
// only matters within its netns, and any process listening on it must be in
// that netns.
func findProcessForInode(inode string, anchorPID int) (string, int) {
	if inode == "" {
		return "", 0
	}
	target := "socket:[" + inode + "]"
	// Resolve the anchor's net ns inode so we can filter candidate processes.
	var anchorNS string
	if l, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", anchorPID)); err == nil {
		anchorNS = l
	}
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
		if anchorNS != "" {
			if l, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", pid)); err != nil || l != anchorNS {
				continue
			}
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
			if link == target {
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
