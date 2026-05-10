// Package host implements the `host` WorkspaceRuntime: in-process execution
// on the local host with no isolation. This is the backward-compatible
// default — every Workspace that existed before Phase A continues running on
// it without migration.
//
// AC-Sdd4ce1-2-1: NewTmuxSession delegates to internal/tmux.Client (no direct
// `exec.Command("tmux", ...)` calls).
// AC-Sdd4ce1-2-2: ExposePort is a no-op (host-bound ports are already
// reachable) and returns a PortMapping where HostPort == ContainerPort.
// AC-Sdd4ce1-7-1: a zero Config decodes to host runtime via the priority
// chain in runtime.Config.WithDefaults.
package host

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// Runtime is the in-process host implementation of runtime.Runtime.
//
// It carries the original config so callers can read it back, but since
// host runtime has no asynchronous bring-up Status() always reports Ready
// after Start.
type Runtime struct {
	cfg          runtime.Config
	worktreePath string
	tmuxClient   tmux.Client

	mu      sync.RWMutex
	status  runtime.Status
	// AC-Sdd4ce1-2-2: track which ports the Workspace has "exposed" so the
	// store can list them in the Ports panel. host runtime's Expose is a
	// pure bookkeeping op — the service is already on the host.
	mappings map[string]runtime.PortMapping
}

// New constructs a host Runtime for the given worktree path. The tmux client
// is required (lazy nil-check at Start, so tests can pass nil if they don't
// exercise NewTmuxSession).
func New(cfg runtime.Config, worktreePath string, tmuxClient tmux.Client) *Runtime {
	return &Runtime{
		cfg:          cfg,
		worktreePath: worktreePath,
		tmuxClient:   tmuxClient,
		mappings:     map[string]runtime.PortMapping{},
		status:       runtime.Status{State: runtime.StateStopped},
	}
}

// Kind returns runtime.KindHost.
func (r *Runtime) Kind() runtime.Kind { return runtime.KindHost }

// Config returns the resolved Config used at construction time.
func (r *Runtime) Config() runtime.Config { return r.cfg }

// Start moves the runtime to State==Ready. host has no asynchronous
// bring-up — this just updates bookkeeping.
func (r *Runtime) Start(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = runtime.Status{
		State:     runtime.StateReady,
		StartedAt: time.Now().UTC(),
		Address:   "localhost",
	}
	return nil
}

// Stop releases bookkeeping (port mappings tracked for this Workspace).
// Process tracking / cleanup is Phase H scope and is intentionally a no-op
// here.
func (r *Runtime) Stop(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = runtime.Status{State: runtime.StateStopped}
	r.mappings = map[string]runtime.PortMapping{}
	return nil
}

// Status returns the current lifecycle snapshot.
func (r *Runtime) Status() runtime.Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

// NewTmuxSession delegates to internal/tmux.Client. AC-Sdd4ce1-2-1.
func (r *Runtime) NewTmuxSession(ctx context.Context, sessionName string) error {
	if r.tmuxClient == nil {
		return fmt.Errorf("host runtime: tmux client not configured")
	}
	return r.tmuxClient.NewSession(ctx, tmux.NewSessionOpts{
		Name:       sessionName,
		WindowName: "palmux:bash:bash",
		Cwd:        r.worktreePath,
	})
}

// AttachTmuxSession delegates to internal/tmux.Client.Attach. The default
// window name follows the bash convention; the caller usually uses higher-
// level helpers (claudeagent / bash provider) and not this directly.
func (r *Runtime) AttachTmuxSession(ctx context.Context, sessionName string) (io.ReadWriteCloser, error) {
	if r.tmuxClient == nil {
		return nil, fmt.Errorf("host runtime: tmux client not configured")
	}
	rw, _, err := r.tmuxClient.Attach(ctx, sessionName, "palmux:bash:bash", tmux.AttachOpts{})
	return rw, err
}

// Exec runs a command on the host inside the Workspace's worktree path.
func (r *Runtime) Exec(ctx context.Context, cmd []string, opts runtime.ExecOpts) (runtime.ExecResult, error) {
	if len(cmd) == 0 {
		return runtime.ExecResult{}, fmt.Errorf("host runtime: empty command")
	}
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	if opts.Cwd != "" {
		c.Dir = opts.Cwd
	} else {
		c.Dir = r.worktreePath
	}
	if len(opts.Env) > 0 {
		c.Env = append(os.Environ(), opts.Env...)
	}
	if opts.Stdin != nil {
		c.Stdin = opts.Stdin
	}
	var outBuf, errBuf bytes.Buffer
	c.Stdout = &outBuf
	c.Stderr = &errBuf
	err := c.Run()
	res := runtime.ExecResult{
		ExitCode: c.ProcessState.ExitCode(),
		Stdout:   outBuf.Bytes(),
		Stderr:   errBuf.Bytes(),
	}
	// We deliberately return a non-nil error only when the command failed
	// to start (not for non-zero exit). Non-zero exit is signalled via
	// ExitCode so callers can decide.
	if exitErr, ok := err.(*exec.ExitError); ok {
		_ = exitErr
		return res, nil
	}
	return res, err
}

// ListListeningPorts reads /proc/net/tcp(6) directly. Mirrors
// internal/agent/methods.go's logic so the runtime API is uniform across
// host and container.
//
// host implementations may scope to PIDs they know about — Phase A returns
// every LISTEN port. Workspace-specific scoping is Phase H (host cleanup).
func (r *Runtime) ListListeningPorts(_ context.Context) ([]runtime.ListeningPort, error) {
	ports, err := parseProcNetTCP("/proc/net/tcp", "tcp")
	if err != nil {
		return nil, err
	}
	if more, err := parseProcNetTCP("/proc/net/tcp6", "tcp6"); err == nil {
		ports = append(ports, more...)
	}
	return ports, nil
}

// ExposePort is a no-op for host runtime: the service is already on the
// host. AC-Sdd4ce1-2-2 — returns a PortMapping where HostPort==ContainerPort
// and records it in the runtime's bookkeeping for later UnexposePort.
func (r *Runtime) ExposePort(_ context.Context, internalPort int, hostPort int, name string, public bool) (runtime.PortMapping, error) {
	if internalPort <= 0 {
		return runtime.PortMapping{}, fmt.Errorf("host runtime: invalid internal port %d", internalPort)
	}
	// host runtime: the service is already host-bound. The "host port"
	// equals the internal port. We honour an explicit hostPort if given
	// (must equal internalPort to make sense) but otherwise default.
	hp := hostPort
	if hp == 0 {
		hp = internalPort
	}
	if hp != internalPort {
		// Allowing different host/container ports on host runtime would
		// be a lie — we'd have to spin up a real proxy. Refuse instead.
		return runtime.PortMapping{}, fmt.Errorf("host runtime: hostPort=%d differs from internal=%d (host has no proxy)", hp, internalPort)
	}
	id := strconv.Itoa(internalPort)
	mapping := runtime.PortMapping{
		ID:            id,
		HostPort:      hp,
		ContainerPort: internalPort,
		Name:          name,
		Public:        public,
	}
	r.mu.Lock()
	r.mappings[id] = mapping
	r.mu.Unlock()
	return mapping, nil
}

// UnexposePort drops a previously-tracked mapping.
func (r *Runtime) UnexposePort(_ context.Context, mappingID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.mappings[mappingID]; !ok {
		return fmt.Errorf("host runtime: mapping %q not found", mappingID)
	}
	delete(r.mappings, mappingID)
	return nil
}

// Mappings exposes the tracked port mappings (test/inspection helper).
func (r *Runtime) Mappings() []runtime.PortMapping {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]runtime.PortMapping, 0, len(r.mappings))
	for _, m := range r.mappings {
		out = append(out, m)
	}
	return out
}

// resolvePath joins p with the worktree path and rejects traversal escapes.
// Mirrors agent/methods.go's securePath but operates directly on host
// filesystem (no symlink eval — the worktree itself may be a symlink and
// host runtime is happy to follow it).
func (r *Runtime) resolvePath(p string) (string, error) {
	if r.worktreePath == "" {
		return "", fmt.Errorf("host runtime: worktree path not set")
	}
	if filepath.IsAbs(p) {
		// Permit absolute paths only if they live under worktree.
		clean := filepath.Clean(p)
		root := filepath.Clean(r.worktreePath)
		if !strings.HasPrefix(clean+string(filepath.Separator), root+string(filepath.Separator)) && clean != root {
			return "", fmt.Errorf("host runtime: path %q escapes worktree", p)
		}
		return clean, nil
	}
	if strings.Contains(p, "..") {
		return "", fmt.Errorf("host runtime: path %q contains ..", p)
	}
	return filepath.Join(r.worktreePath, filepath.Clean("/"+p)), nil
}

// ReadFile reads a file under the worktree.
func (r *Runtime) ReadFile(_ context.Context, p string) ([]byte, error) {
	abs, err := r.resolvePath(p)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(abs)
}

// WriteFile writes a file under the worktree (mode 0644).
func (r *Runtime) WriteFile(_ context.Context, p string, data []byte) error {
	abs, err := r.resolvePath(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, data, 0o644)
}

// Stat returns metadata for a path under the worktree.
func (r *Runtime) Stat(_ context.Context, p string) (runtime.FileInfo, error) {
	abs, err := r.resolvePath(p)
	if err != nil {
		return runtime.FileInfo{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return runtime.FileInfo{}, err
	}
	return runtime.FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		IsDir:   info.IsDir(),
		ModTime: info.ModTime().UTC(),
	}, nil
}

// Walk visits every entry under p, invoking fn for each.
func (r *Runtime) Walk(_ context.Context, p string, fn runtime.WalkFunc) error {
	abs, err := r.resolvePath(p)
	if err != nil {
		return err
	}
	return filepath.Walk(abs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return nil
		}
		return fn(runtime.WalkEntry{
			RelPath: rel,
			Name:    info.Name(),
			IsDir:   info.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
		})
	})
}

// parseProcNetTCP is a slim copy of internal/agent/methods.go's parser. We
// duplicate it (10 LOC) rather than expose it via /agent/proto so the
// runtime package stays free of agent imports. If the parser ever grows we
// can move both to a shared util package.
func parseProcNetTCP(path, protocol string) ([]runtime.ListeningPort, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []runtime.ListeningPort
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		line := strings.TrimSpace(sc.Text())
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if !strings.EqualFold(fields[3], "0A") {
			continue
		}
		parts := strings.SplitN(fields[1], ":", 2)
		if len(parts) != 2 {
			continue
		}
		portNum, err := strconv.ParseUint(parts[1], 16, 16)
		if err != nil {
			continue
		}
		entries = append(entries, runtime.ListeningPort{
			Port:         uint16(portNum),
			Protocol:     protocol,
			LocalAddress: parts[0],
		})
	}
	return entries, sc.Err()
}
