// Package runtime defines the abstraction that lets Palmux run workspace
// terminals in different execution environments — the host machine (current
// behaviour) or an Incus container (future isolation sprint).
//
// Every place that today calls tmux.Client directly will eventually be
// re-routed through Runtime so the surrounding store/tab code never needs to
// know whether it is talking to the host or a container.  For Story S8478ca-1
// the ONLY implementation is "host", which delegates 1:1 to the existing
// tmux.Client — there is zero behaviour change.
package runtime

import (
	"context"
	"io"
	"os/exec"

	"github.com/tjst-t/palmux2/internal/tmux"
)

// Kind is a two-value discriminant for the runtime type.
type Kind string

const (
	// KindHost is the bare-metal / host runtime: processes run directly on
	// the machine where palmux is running.  Tmux sessions are created on the
	// host's tmux server.  Networking is not isolated.  This is the current
	// (pre-S8478ca) behaviour preserved 1:1.
	KindHost Kind = "host"

	// KindIncusContainer is the Incus-container runtime (Story S8478ca-2).
	// Each workspace gets a fresh container launched by `incus launch`.
	// Tmux sessions run inside the container via `incus exec`.
	KindIncusContainer Kind = "incus-container"
)

// IsValid reports whether k is a recognised Kind.
func (k Kind) IsValid() bool {
	return k == KindHost || k == KindIncusContainer
}

// Config is the serialisable description of a runtime.  It is stored in
// repos.json per-workspace and drives Kind resolution at start-up.
type Config struct {
	Kind  Kind   `json:"kind"`
	Image string `json:"image,omitempty"` // incus-container only
}

// State is the lifecycle state of a Runtime instance.
type State string

const (
	StateReady    State = "ready"
	StateStarting State = "starting"
	StateStopped  State = "stopped"
	StateError    State = "error"
)

// Status is a point-in-time snapshot of a Runtime's lifecycle state.
type Status struct {
	State   State
	Address string // e.g. "localhost" for host, container IP for incus
	Error   string // non-empty when State == StateError
}

// ExecOpts configures an Exec call.
type ExecOpts struct {
	Dir string
	Env []string
}

// ExecResult holds the captured output from an Exec call.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ListeningPort is a port observed to be listening inside the runtime.
type ListeningPort struct {
	Port     int
	Proto    string // "tcp" or "udp"
	BindAddr string // e.g. "0.0.0.0", "127.0.0.1", "*", "[::1]"
	PID      int
	Process  string
}

// PortView is the user-facing view of one container port: the observed
// listening port plus whether palmux has published it as an HTTPS subdomain.
// It is what GET .../ports returns and what the branch.portsChanged WS event
// carries. (See8bd4-3)
type PortView struct {
	Port          int    `json:"port"`
	Proto         string `json:"proto"`
	BindAddr      string `json:"bindAddr"`
	Process       string `json:"process"`
	LocalhostOnly bool   `json:"localhostOnly"` // bound to 127.0.0.1 — reachable via in-container relay
	Public        bool   `json:"public"`        // exposed without edge basic_auth
	Exposed       bool   `json:"exposed"`       // a public route exists for this port
	PublicURL     string `json:"publicUrl"`     // https://<port>--<ws>--<repo>.<base> when exposed, else ""

	// Host-port publishing (S4c591a) — the wildcard-DNS-less fallback. Only
	// meaningful when the runtime has NO public domain configured; in that mode
	// the FE shows host-port toggles instead of subdomain ones.
	HostPublished bool   `json:"hostPublished"` // an incus proxy device exposes this port on the host
	HostPort      int    `json:"hostPort"`      // host-side port the proxy device listens on (0 = none)
	HostURL       string `json:"hostUrl"`       // http://<hostIP>:<hostPort> when host-published, else ""
}

// PortSpec describes a port to expose from the runtime to the outside world.
// The shape is intentionally general so that future UDP / WebRTC (Neko, §7 of
// the workspace-runtime design) fits without retrofit:
//
//   - Proto must be "tcp" or "udp".
//   - Public=true means the mapping should be reachable outside the auth
//     boundary (e.g. via a public Caddy vhost).
//   - HostPort>0 reserves a specific host-side port; 0 means none (Caddy
//     direct-to-bridge, host-port-less path — §5.2 of the design).
type PortSpec struct {
	Internal int    // port inside the runtime
	Proto    string // "tcp" | "udp"
	Name     string // human label
	Public   bool   // expose beyond the auth boundary
	HostPort int    // 0 = no host-port reservation (bridge/Caddy path)
}

// PortMapping is the result of a successful ExposePort call.
type PortMapping struct {
	ID       string
	Internal int
	HostPort int
	Proto    string
	Address  string
	Public   bool
}

// Runtime is the single seam between palmux's workspace management logic and
// the execution environment.  host and incus-container are the only kinds
// supported in this sprint; the interface is sized to avoid retrofit when
// Neko / WebRTC is added later.
type Runtime interface {
	// Kind identifies the runtime type.
	Kind() Kind

	// Config returns the configuration snapshot that created this Runtime.
	Config() Config

	// Start brings the runtime up.  For host this is a no-op (immediately
	// ready).  For incus-container it runs `incus launch`.
	Start(ctx context.Context) error

	// Stop tears the runtime down.  For host this is a no-op.  For
	// incus-container it runs `incus delete --force`.
	Stop(ctx context.Context) error

	// Status returns the current lifecycle state without blocking.
	Status() Status

	// NewTmuxSession creates a tmux session inside the runtime.
	// For host this calls tmux.Client.NewSession on the host tmux server.
	// For incus-container it routes through `incus exec`.
	NewTmuxSession(ctx context.Context, session string) error

	// AttachTmuxSession opens a read/write/close PTY to a tmux session.
	// NOTE: the existing attach path (handler_ws.go) keeps going through
	// tmux.Client.Attach for host, so this method is a thin convenience
	// that is not yet load-bearing.  See Story notes on blast-radius.
	AttachTmuxSession(ctx context.Context, session string) (io.ReadWriteCloser, error)

	// Exec runs a command inside the runtime and captures its output.
	Exec(ctx context.Context, cmd []string, opts ExecOpts) (ExecResult, error)

	// ListListeningPorts returns the ports currently listening inside the
	// runtime.
	ListListeningPorts(ctx context.Context) ([]ListeningPort, error)

	// ExposePort makes an internal port reachable from outside the runtime.
	// Proto must be "tcp" or "udp".  For host runtimes this is a thin stub
	// (host networking is handled by portman externally).
	ExposePort(ctx context.Context, spec PortSpec) (PortMapping, error)

	// UnexposePort removes a previously-created port mapping by its ID.
	UnexposePort(ctx context.Context, mappingID string) error

	// TmuxClient returns the tmux.Client that should be used to drive tmux
	// operations inside this runtime.  For host this is the global
	// tmux.Client (unchanged behaviour).  For incus-container it is an
	// incusTmuxClient that routes all tmux calls through `incus exec`.
	//
	// The method must be cheap (cached / stateless); it must NOT start the
	// runtime.  The store calls Start() separately before first use.
	TmuxClient() tmux.Client
}

// ImageDriftChecker is an optional capability a Runtime may implement when it is
// backed by a versioned image (incus-container). The host runtime has no image
// concept and does not implement it, so callers type-assert and skip when
// absent — keeping the core Runtime interface free of image-specific methods
// (mirrors the optional tab.HeadChangedHook pattern). (S7364e3)
type ImageDriftChecker interface {
	// IsImageStale reports whether the running container was created from an
	// image older than the one the image alias currently resolves to. Returns
	// false (not an error) when there is no update target (alias absent) or the
	// base image is unknown.
	IsImageStale(ctx context.Context) (bool, error)
}

// PTYCommandOpts configures a PTYCommand: the working directory and the
// environment (KEY=VALUE pairs) to apply INSIDE the runtime. (S4d8b1c)
type PTYCommandOpts struct {
	Cwd string   // working dir inside the runtime
	Env []string // KEY=VALUE pairs set inside the runtime
}

// PTYCommander is an optional capability a Runtime may implement to build (but
// NOT start) a host *exec.Cmd that runs argv interactively INSIDE the runtime
// under a PTY. The caller starts it with a pty library and owns the master fd.
// For incus this wraps argv as `incus exec -t <inst> --user … --cwd … --env … --
// argv`, so the process (e.g. an interactive `claude` TUI) runs in the
// container. host does not implement it — the caller runs argv directly on the
// host. This lets the claude-tui daemon route its PTY subprocess into the
// container without importing the incus package. (S4d8b1c)
type PTYCommander interface {
	PTYCommand(ctx context.Context, argv []string, opts PTYCommandOpts) *exec.Cmd
}

// ExecCommander is an optional capability a Runtime may implement to build (but
// NOT start) a host *exec.Cmd that runs argv INSIDE the runtime over plain
// (non-PTY) pipes — `incus exec <inst> --user … --cwd … --env … -- argv` with
// NO -t. The caller wires StdinPipe/StdoutPipe/StderrPipe itself (separate
// stderr is preserved). Used by the claude-agent stream-json transport to run
// claude in the container. host does not implement it (runs argv directly).
// (S4d8b1c) Unlike PTYCommander (-t, for the TUI), this keeps stdout/stderr
// separate and binary-clean as the stream-json protocol requires.
type ExecCommander interface {
	ExecCommand(ctx context.Context, argv []string, opts PTYCommandOpts) *exec.Cmd
}

// ContainerRegenerator is an optional capability a Runtime may implement to
// recreate its backing container from the current image alias (image update).
// It must be transactional against realistic failures: verify the new image
// launches before destroying the existing container, and leave the old
// container intact on failure. host does not implement it. (S7364e3)
type ContainerRegenerator interface {
	// Regenerate recreates the container from the current image. On error the
	// previous container must remain usable.
	Regenerate(ctx context.Context) error
}
