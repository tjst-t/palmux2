// Package runtime defines the WorkspaceRuntime abstraction that all runtime
// kinds (host / lxd-container / lxd-vm / lxd-remote / ssh-remote) implement.
//
// Phase A (Sdd4ce1) implements `host` and `lxd-container` only. The interface
// is shaped so that subsequent kinds slot in without API churn — the only
// difference between non-host kinds is how the agent (palmux-agent) is reached.
//
// See docs/workspace-runtime-design.md §3 for the full design.
package runtime

import (
	"context"
	"io"
	"time"
)

// Kind enumerates the supported runtime backends.
//
// Phase A implements `host` and `lxd-container`. The other constants are
// reserved so callers can switch on the literal without risking a "missing
// case" lint at the time later runtimes land.
type Kind string

const (
	// KindHost runs the Workspace in-process on the host (no isolation).
	// Backward-compatible default for repos.json entries that pre-date the
	// runtime field.
	KindHost Kind = "host"

	// KindLXDContainer runs the Workspace inside a local LXD system
	// container. Phase A target.
	KindLXDContainer Kind = "lxd-container"

	// KindLXDVM runs the Workspace inside a local LXD virtual machine.
	// Phase D.
	KindLXDVM Kind = "lxd-vm"

	// KindLXDRemote runs the Workspace inside an LXD container/VM on a
	// remote LXD host. Phase E.
	KindLXDRemote Kind = "lxd-remote"

	// KindSSHRemote runs the Workspace via SSH on a remote host with the
	// agent pushed via scp. Phase F.
	KindSSHRemote Kind = "ssh-remote"
)

// IsValid reports whether k is one of the recognised runtime kinds.
func (k Kind) IsValid() bool {
	switch k {
	case KindHost, KindLXDContainer, KindLXDVM, KindLXDRemote, KindSSHRemote:
		return true
	}
	return false
}

// State enumerates the lifecycle phases a runtime can be in. The host
// runtime collapses Stopped/Starting/Stopping into Ready since there is no
// asynchronous bring-up.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

// Status is the public lifecycle snapshot reported by Runtime.Status().
type Status struct {
	State     State     `json:"state"`
	StartedAt time.Time `json:"started_at,omitempty"`
	// Address is "localhost" for host, the container IP for lxd-container,
	// or the remote host:port for ssh-remote / lxd-remote. Empty when not
	// yet determined.
	Address string `json:"address,omitempty"`
	// Error carries the most recent failure reason when State == failed.
	Error string `json:"error,omitempty"`
}

// NetworkPolicy expresses the network attachment chosen for a runtime.
//
// Phase A only honours `bridged` (LXD's default lxdbr0). Other modes are
// reserved for Phase G (host-netns / tailnet).
type NetworkPolicy struct {
	// Mode is one of "bridged", "host-netns", "tailnet".
	Mode string `json:"mode,omitempty"`

	// TailnetAuthKey is referenced when Mode == "tailnet". It points at a
	// keychain-stored secret rather than holding the secret itself, so
	// settings.json never carries credentials.
	TailnetAuthKey string `json:"tailnet_auth_key,omitempty"`
}

// Resources carries optional VM-only sizing knobs. Ignored by container/host
// runtimes.
type Resources struct {
	// MemoryMiB is the memory ceiling for `lxd-vm`. 0 = LXD default.
	MemoryMiB int `json:"memory_mib,omitempty"`
	// CPUCount is the CPU count for `lxd-vm`. 0 = LXD default.
	CPUCount int `json:"cpu_count,omitempty"`
}

// Config is the persisted, JSON-serialisable runtime descriptor stored in
// repos.json (per-Workspace) or settings.json (global default).
//
// AC-Sdd4ce1-1-2: a missing entry decodes as Kind="" — callers must apply
// the priority chain (per-Workspace → per-repo → global → auto) and treat a
// remaining empty Kind as KindHost for backward compatibility with pre-Phase-A
// repos.json files.
type Config struct {
	// Kind selects the runtime backend.
	Kind Kind `json:"kind"`

	// Image is the container/VM image. Honoured by lxd-container / lxd-vm.
	// Empty = use the project default (`ghcr.io/tjst-t/palmux-workspace:default`).
	Image string `json:"image,omitempty"`

	// Network is the network attachment (Phase A: bridged only).
	Network NetworkPolicy `json:"network,omitempty"`

	// Remote names a registered `lxc remote add` entry. Honoured by
	// lxd-remote / ssh-remote. Phase A: ignored.
	Remote string `json:"remote,omitempty"`

	// Resources carries VM sizing knobs. Phase A: ignored except by lxd-vm
	// (Phase D).
	Resources Resources `json:"resources,omitempty"`
}

// WithDefaults returns c with empty fields filled from defaults.
//
// This is the unifying step for the priority chain in design §9.6:
// per-Workspace → per-repo → global → built-in. Callers compose by calling
// WithDefaults from the most specific to the least specific (each merge fills
// only the empty fields).
func (c Config) WithDefaults(defaults Config) Config {
	if c.Kind == "" {
		c.Kind = defaults.Kind
	}
	if c.Image == "" {
		c.Image = defaults.Image
	}
	if c.Network.Mode == "" {
		c.Network.Mode = defaults.Network.Mode
	}
	if c.Network.TailnetAuthKey == "" {
		c.Network.TailnetAuthKey = defaults.Network.TailnetAuthKey
	}
	if c.Remote == "" {
		c.Remote = defaults.Remote
	}
	if c.Resources.MemoryMiB == 0 {
		c.Resources.MemoryMiB = defaults.Resources.MemoryMiB
	}
	if c.Resources.CPUCount == 0 {
		c.Resources.CPUCount = defaults.Resources.CPUCount
	}
	return c
}

// IsZero reports whether the config carries no information at all (used to
// decide whether to walk the priority chain).
func (c Config) IsZero() bool {
	return c.Kind == "" && c.Image == "" && c.Network == (NetworkPolicy{}) &&
		c.Remote == "" && c.Resources == (Resources{})
}

// PortMapping records a host:container port forward. Returned by ExposePort
// so the caller can persist it in repos.json (§5.4 lifecycle).
type PortMapping struct {
	// ID uniquely identifies this mapping inside the runtime so the caller
	// can later UnexposePort by ID. For host runtime the ID equals the
	// host port as a string ("3000"); for lxd-container it equals the
	// `lxc config device` name.
	ID string `json:"id"`

	// HostPort is the externally-reachable host port (always set).
	HostPort int `json:"host_port"`

	// ContainerPort is the port the service binds to inside the runtime.
	// Equal to HostPort for host runtime.
	ContainerPort int `json:"container_port"`

	// Name is a human-readable label (e.g. "vite", "api"). Optional.
	Name string `json:"name,omitempty"`

	// Public reports whether the mapping was bound to 0.0.0.0 (LAN
	// reachable) or only 127.0.0.1.
	Public bool `json:"public,omitempty"`
}

// ListeningPort describes a port the runtime currently sees in LISTEN state.
// Returned by ListListeningPorts. Mirrors agent/proto.PortEntry but is
// re-declared so the runtime package does not pull in agent/proto.
type ListeningPort struct {
	Port         uint16 `json:"port"`
	Protocol     string `json:"protocol"` // "tcp" or "tcp6"
	LocalAddress string `json:"local_address"`
	// PID/Process are best-effort and may be empty.
	PID     int    `json:"pid,omitempty"`
	Process string `json:"process,omitempty"`
}

// FileInfo is a runtime-agnostic stat snapshot. Re-declared (rather than
// importing agent/proto) so the runtime package can be used by the store
// without dragging the agent JSON envelope into core code.
type FileInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
}

// WalkEntry is one entry returned by Walk.
type WalkEntry struct {
	RelPath string `json:"rel_path"`
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
}

// WalkFunc is invoked for each entry visited by Walk. Returning a non-nil
// error stops the walk; the runtime returns that error from Walk.
type WalkFunc func(WalkEntry) error

// ExecOpts tunes how a command is run inside the runtime.
type ExecOpts struct {
	// Cwd is the working directory inside the runtime (worktree-relative
	// or absolute). Empty = runtime default.
	Cwd string

	// Env adds environment variables to the spawned process (runtime
	// default env still applies for keys not listed here).
	Env []string

	// Stdin, if non-nil, is forwarded to the process.
	Stdin io.Reader
}

// ExecResult is the outcome of Exec.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Runtime is the WorkspaceRuntime abstraction. It hides the difference
// between host / container / VM / remote backends behind a uniform API.
//
// Implementations must be safe for concurrent use — palmux invokes runtime
// methods from multiple goroutines (HTTP handlers, websocket handlers, the
// store's reconciliation loop).
//
// AC-Sdd4ce1-1-1.
type Runtime interface {
	// Kind reports the underlying backend (does not block).
	Kind() Kind

	// Config returns the resolved Config used at Start time.
	Config() Config

	// --- Lifecycle ---

	// Start brings the runtime to State==Ready. For host this is mostly
	// no-op bookkeeping; for lxd-container it launches the instance,
	// applies idmap + bind-mounts, waits for cloud-init, and pushes +
	// starts the palmux-agent.
	Start(ctx context.Context) error

	// Stop tears down the runtime. For host this is process-tracking
	// cleanup; for lxd-container it stops the LXD instance.
	Stop(ctx context.Context) error

	// Status returns the current lifecycle snapshot (cheap, non-blocking).
	Status() Status

	// --- Tmux / Exec ---

	// NewTmuxSession creates a tmux session inside the runtime so the
	// caller can attach via AttachTmuxSession. For host this calls the
	// in-process tmux client; for lxd-container the call is forwarded to
	// the agent (or `lxc exec -- tmux …` as a fallback).
	NewTmuxSession(ctx context.Context, sessionName string) error

	// AttachTmuxSession returns a duplex pty-style stream attached to the
	// named session. Closing the returned ReadWriteCloser detaches.
	AttachTmuxSession(ctx context.Context, sessionName string) (io.ReadWriteCloser, error)

	// Exec runs a command inside the runtime synchronously and returns
	// captured stdout/stderr. For long-running interactive commands, use
	// NewTmuxSession + AttachTmuxSession instead.
	Exec(ctx context.Context, cmd []string, opts ExecOpts) (ExecResult, error)

	// --- Ports ---

	// ListListeningPorts returns ports currently in LISTEN state inside
	// the runtime. host implementations may scope the result to PIDs they
	// know belong to the Workspace; container implementations return
	// every port in the netns.
	ListListeningPorts(ctx context.Context) ([]ListeningPort, error)

	// ExposePort forwards a runtime-internal port to a host port so a
	// browser running on the host can reach it. For host runtime this
	// records the binding for cleanup tracking and returns the same port.
	// For container runtime it adds an `lxc config device add ... proxy`.
	//
	// hostPort==0 means "let the runtime allocate". The chosen port is
	// returned in PortMapping.HostPort.
	ExposePort(ctx context.Context, internal int, hostPort int, name string, public bool) (PortMapping, error)

	// UnexposePort removes a previously-created mapping.
	UnexposePort(ctx context.Context, mappingID string) error

	// --- Files ---

	// ReadFile reads a regular file from inside the runtime. The path is
	// runtime-relative (worktree-rooted for sandbox enforcement).
	ReadFile(ctx context.Context, path string) ([]byte, error)

	// WriteFile writes data to a regular file inside the runtime.
	WriteFile(ctx context.Context, path string, data []byte) error

	// Stat returns metadata for the given path.
	Stat(ctx context.Context, path string) (FileInfo, error)

	// Walk visits every entry under path, invoking fn for each. The walk
	// is depth-first and skips entries fn returns an error for; an error
	// other than fs.SkipDir terminates the walk.
	Walk(ctx context.Context, path string, fn WalkFunc) error
}
