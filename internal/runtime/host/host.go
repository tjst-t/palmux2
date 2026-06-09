// Package host implements the "host" runtime: processes run directly on the
// machine where palmux is running, and tmux sessions live on the host's tmux
// server.  This is a behaviour-preserving wrapper around the existing
// tmux.Client — every tmux operation it receives is forwarded 1:1 to the
// injected client so that there is zero observable difference from the
// pre-S8478ca code paths.
package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// hostRuntime is the "host" Runtime implementation.
type hostRuntime struct {
	t      tmux.Client
	mu     sync.RWMutex
	status runtime.Status
}

// NewHost returns a runtime.Runtime that delegates all tmux operations to t
// and runs Exec commands directly on the host OS.  Start/Stop are no-ops
// (the host is always "ready").
func NewHost(t tmux.Client) runtime.Runtime {
	return &hostRuntime{
		t: t,
		status: runtime.Status{
			State:   runtime.StateReady,
			Address: "localhost",
		},
	}
}

func (h *hostRuntime) Kind() runtime.Kind { return runtime.KindHost }

func (h *hostRuntime) Config() runtime.Config {
	return runtime.Config{Kind: runtime.KindHost}
}

// Start is a no-op for host: the machine is always ready.
func (h *hostRuntime) Start(_ context.Context) error { return nil }

// Stop is a no-op for host: we do not tear down the machine.
func (h *hostRuntime) Stop(_ context.Context) error { return nil }

// Status returns the current status.  For host this is always ready/localhost
// and never changes.
func (h *hostRuntime) Status() runtime.Status {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.status
}

// NewTmuxSession creates a tmux session by delegating to the injected
// tmux.Client.  The window name is derived from the session name using the
// palmux canonical form (palmux:placeholder:placeholder) so the session is
// valid without needing caller-provided window details.  NOTE (S8478ca-1):
// this is NOT yet wired into the store's ensureSession path — production
// session creation still calls tmux.Client directly. The store integration
// that routes ensureSession through the resolved Runtime lands with the
// incus runtime (Story S8478ca-2); until then this method is exercised only
// by unit tests.
func (h *hostRuntime) NewTmuxSession(ctx context.Context, session string) error {
	err := h.t.NewSession(ctx, tmux.NewSessionOpts{
		Name:       session,
		WindowName: "palmux:placeholder:placeholder",
	})
	if err != nil {
		return fmt.Errorf("host NewTmuxSession: %w", err)
	}
	return nil
}

// AttachTmuxSession is a thin helper that attaches to the first window of a
// session.  NOTE: the production attach path (handler_ws.go → tmux.Client.Attach
// with group session + resize) does NOT go through this method in Story
// S8478ca-1 — it continues to call tmux.Client.Attach directly.  This method
// exists to satisfy the Runtime interface and is not yet load-bearing for host.
// Callers that need the full attach experience (group session, PTY size, resize
// callback) should continue using tmux.Client.Attach.
func (h *hostRuntime) AttachTmuxSession(_ context.Context, _ string) (io.ReadWriteCloser, error) {
	return nil, errors.New("host.AttachTmuxSession: use tmux.Client.Attach for the full host attach path (group session + resize support)")
}

// Exec runs cmd on the host OS using os/exec and captures stdout/stderr.
func (h *hostRuntime) Exec(ctx context.Context, cmd []string, opts runtime.ExecOpts) (runtime.ExecResult, error) {
	if len(cmd) == 0 {
		return runtime.ExecResult{}, fmt.Errorf("host Exec: cmd must not be empty")
	}
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...) //nolint:gosec // callers are internal
	if opts.Dir != "" {
		c.Dir = opts.Dir
	}
	if len(opts.Env) > 0 {
		// Append to the inherited environment rather than replacing it, so the
		// command keeps PATH/HOME etc. (replacing wholesale would break e.g.
		// `claude --resume`). Mirrors how the incus runtime injects env.
		c.Env = append(os.Environ(), opts.Env...)
	}
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	result := runtime.ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil // non-zero exit is a result, not a Go error
		}
		return result, fmt.Errorf("host Exec: %w", err)
	}
	return result, nil
}

// ListListeningPorts returns an empty list for the host runtime.  Host
// networking is handled externally via portman; palmux does not enumerate host
// ports itself.
func (h *hostRuntime) ListListeningPorts(_ context.Context) ([]runtime.ListeningPort, error) {
	return nil, nil
}

// ExposePort is a no-op stub for the host runtime.  Host port exposure is
// managed by portman / the repository's own Makefile (see design §5.4).  The
// stub returns a PortMapping that echoes the spec so callers can still build
// against the interface.
func (h *hostRuntime) ExposePort(_ context.Context, spec runtime.PortSpec) (runtime.PortMapping, error) {
	id := fmt.Sprintf("host-%s-%d", strings.ToLower(spec.Proto), spec.Internal)
	return runtime.PortMapping{
		ID:       id,
		Internal: spec.Internal,
		HostPort: spec.HostPort,
		Proto:    spec.Proto,
		Address:  "localhost",
		Public:   spec.Public,
	}, nil
}

// UnexposePort is a no-op stub for the host runtime.
func (h *hostRuntime) UnexposePort(_ context.Context, _ string) error {
	return nil
}

// TmuxClient returns the host tmux.Client injected at construction.
// For host this IS the global tmux.Client, so store behaviour is byte-identical
// to the pre-S8478ca code paths.
func (h *hostRuntime) TmuxClient() tmux.Client { return h.t }

// DefaultRegistry is a Registry implementation that returns a host Runtime
// for every (repoID, branchID) pair.  It is the default wired into the store
// until Story S8478ca-3 adds per-workspace resolution.
type DefaultRegistry struct {
	rt runtime.Runtime
}

// NewDefaultRegistry returns a Registry that always resolves to the given
// host Runtime.
func NewDefaultRegistry(t tmux.Client) *DefaultRegistry {
	return &DefaultRegistry{rt: NewHost(t)}
}

// Get always returns the single host Runtime regardless of the workspace IDs.
func (r *DefaultRegistry) Get(_, _ string) runtime.Runtime { return r.rt }
