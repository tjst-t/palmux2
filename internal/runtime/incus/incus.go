// Package incus implements the "incus-container" runtime: each Workspace gets
// its own Incus container.  Processes (tmux, claude, bash) run inside the
// container via `incus exec`.  Bind-mounts bring ~/ghq, ~/.claude and
// ~/.claude.json from the host into the container at the same absolute path so
// the claude binary finds its OAuth credentials without re-authentication.
//
// Implementation follows docs/workspace-runtime-design.md §3/§4/§0 and
// decisions D001-D011 in docs/sprint-logs/S8478ca/decisions.json.
//
// Key incus gotcha: the `incus` CLI reads stdin as YAML instance config.
// Every exec.Cmd that shells out to incus MUST have Stdin == nil (the default).
// Do NOT pipe heredocs or stdin to incus commands.
package incus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// runner is an injectable command executor: given a context and args it returns
// stdout, stderr, exit-code and any OS-level error (not non-zero exit).  This
// lets tests inject a fake without touching real incus.
type runner func(ctx context.Context, args ...string) (stdout, stderr string, code int, err error)

// defaultRunner executes `incus <args>` via exec.CommandContext.
// Stdin is intentionally nil — see package-level gotcha note.
func defaultRunner(ctx context.Context, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, "incus", args...) //nolint:gosec
	cmd.Stdin = nil                                   // CRITICAL: never pipe into incus
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	code := 0
	if err != nil {
		if xe, ok := err.(*exec.ExitError); ok {
			code = xe.ExitCode()
			return so.String(), se.String(), code, nil // non-zero exit is a result
		}
		return so.String(), se.String(), -1, err
	}
	return so.String(), se.String(), 0, nil
}

// incusRuntime is the Runtime implementation for incus-container.
type incusRuntime struct {
	cfg  runtime.Config
	inst string // stable DNS-safe instance name derived from (repoID, branchID)
	run  runner
	log  *slog.Logger

	mu     sync.RWMutex
	status runtime.Status
}

// New returns a runtime.Runtime that manages an Incus container.
//
// instName must be a DNS-safe string (≤63 chars, a-z0-9-).  Callers should use
// InstanceName to derive it from (repoID, branchID).  If r is nil, the real
// `incus` binary is invoked.
func New(cfg runtime.Config, instName string, r runner, log *slog.Logger) runtime.Runtime {
	if r == nil {
		r = defaultRunner
	}
	if log == nil {
		log = slog.Default()
	}
	image := cfg.Image
	if image == "" {
		image = "palmux-ws"
	}
	return &incusRuntime{
		cfg:  runtime.Config{Kind: runtime.KindIncusContainer, Image: image},
		inst: instName,
		run:  r,
		log:  log,
		status: runtime.Status{
			State: runtime.StateStopped,
		},
	}
}

// InstanceName derives a stable, DNS-safe instance name from (repoID,
// branchID).  The result is ≤63 characters and matches [a-z0-9][a-z0-9-]*.
// [AC-S8478ca-2-1]
func InstanceName(repoID, branchID string) string {
	raw := repoID + "/" + branchID
	h := sha256.Sum256([]byte(raw))
	hash := fmt.Sprintf("%x", h[:4]) // 8 hex chars
	// Sanitise: lower, replace non-alnum with -, collapse runs, strip leading/trailing -
	safe := strings.ToLower(repoID + "-" + branchID)
	var b strings.Builder
	prev := '-'
	for _, r := range safe {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prev = r
		} else if prev != '-' {
			b.WriteByte('-')
			prev = '-'
		}
	}
	prefix := strings.Trim(b.String(), "-")
	// Truncate prefix so total (prefix + "-" + 8 chars) ≤ 63
	maxPrefix := 54 // 63 - 1 ("-") - 8 (hash)
	if len(prefix) > maxPrefix {
		prefix = prefix[:maxPrefix]
	}
	prefix = strings.TrimRight(prefix, "-")
	if prefix == "" {
		prefix = "ws"
	}
	return prefix + "-" + hash
}

func (r *incusRuntime) Kind() runtime.Kind     { return runtime.KindIncusContainer }
func (r *incusRuntime) Config() runtime.Config { return r.cfg }

// Status returns the current lifecycle state without blocking.
// [AC-S8478ca-2-1]
func (r *incusRuntime) Status() runtime.Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

func (r *incusRuntime) setStatus(s runtime.Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = s
}

// Start launches the Incus container for this workspace.
//
// Steps: init → raw.idmap → device-add (~/ghq, ~/.claude, ~/.claude.json) →
// start → wait-for-agent.
// [AC-S8478ca-2-1] [AC-S8478ca-2-2]
func (r *incusRuntime) Start(ctx context.Context) error {
	r.setStatus(runtime.Status{State: runtime.StateStarting})

	image := r.cfg.Image
	if image == "" {
		image = "palmux-ws"
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("incus Start: get home dir: %w", err)
	}

	// 1. Init the instance (do not start yet).
	// [AC-S8478ca-2-1]
	if _, _, code, err := r.run(ctx, "init", image, r.inst); err != nil {
		r.setStatus(runtime.Status{State: runtime.StateError, Error: err.Error()})
		return fmt.Errorf("incus init %s %s: %w", image, r.inst, err)
	} else if code != 0 {
		// Already exists is code 1 with "already exists" in stderr — treat as
		// idempotent only if it's running; otherwise re-create below.
		if _, _, qcode, _ := r.run(ctx, "list", r.inst, "-f", "json"); qcode != 0 {
			msg := fmt.Sprintf("incus init exited %d", code)
			r.setStatus(runtime.Status{State: runtime.StateError, Error: msg})
			return fmt.Errorf("%s", msg)
		}
		// Instance already exists — proceed to start.
		r.log.Info("incus: instance already exists, proceeding", "inst", r.inst)
	}

	// 2. raw.idmap: map host UID/GID 1000 → container UID/GID 1000 so the
	// bind-mounted ~/ghq and ~/.claude are owned by the in-container `ubuntu`
	// user. The container stays UNPRIVILEGED — we never fall back to
	// security.privileged (that would defeat the isolation that is the whole
	// point of this runtime).
	//
	// HOST PREREQUISITE: the incus daemon runs as root, so root must be
	// allowed to map host uid/gid 1000. /etc/subuid and /etc/subgid must each
	// contain a `root:1000:1` line (in addition to the default
	// `root:1000000:...` range). Without it `incus start` fails at newuidmap
	// time. See docs/workspace-runtime-design.md §4.
	// [AC-S8478ca-2-2]
	if _, idmapStderr, idmapCode, idmapErr := r.run(ctx, "config", "set", r.inst, "raw.idmap", "both 1000 1000"); idmapErr != nil || idmapCode != 0 {
		msg := fmt.Sprintf("incus config set raw.idmap: code=%d stderr=%s "+
			"(ensure /etc/subuid and /etc/subgid contain `root:1000:1`, then restart incus)",
			idmapCode, idmapStderr)
		r.setStatus(runtime.Status{State: runtime.StateError, Error: msg})
		return fmt.Errorf("%s: %w", msg, idmapErr)
	}

	// 3. Bind-mount ~/ghq, ~/.claude, ~/.claude.json at same absolute path.
	// [AC-S8478ca-2-2]
	ghqPath := filepath.Join(home, "ghq")
	claudeDir := filepath.Join(home, ".claude")
	claudeJSON := filepath.Join(home, ".claude.json")

	mounts := []struct {
		name   string
		source string
		path   string
	}{
		{"ghq", ghqPath, ghqPath},
		{"dot-claude", claudeDir, claudeDir},
		{"dot-claude-json", claudeJSON, claudeJSON},
	}
	for _, m := range mounts {
		// Skip if source does not exist on host — silently omit to avoid
		// failing on fresh machines where ~/.claude.json may not exist yet.
		if _, statErr := os.Stat(m.source); os.IsNotExist(statErr) {
			r.log.Warn("incus: bind-mount source not found, skipping", "source", m.source)
			continue
		}
		_, stderr, code, err := r.run(ctx,
			"config", "device", "add", r.inst,
			m.name, "disk",
			"source="+m.source,
			"path="+m.path,
		)
		if err != nil || code != 0 {
			msg := fmt.Sprintf("incus device add %s: code=%d stderr=%s", m.name, code, stderr)
			r.setStatus(runtime.Status{State: runtime.StateError, Error: msg})
			return fmt.Errorf("%s: %w", msg, err)
		}
	}

	// 4. Start the instance (unprivileged).
	// [AC-S8478ca-2-1]
	// If start fails, the real cause (e.g. newuidmap rejecting the idmap range)
	// is usually only in `incus info --show-log`, not in start's stderr — so we
	// surface that log and point at the subuid/subgid prerequisite. We do NOT
	// fall back to a privileged container.
	if _, startStderr, startCode, startErr := r.run(ctx, "start", r.inst); startErr != nil || startCode != 0 {
		showLog, _, _, _ := r.run(ctx, "info", r.inst, "--show-log")
		msg := fmt.Sprintf("incus start %s: code=%d stderr=%s log=%s "+
			"(if newuidmap failed, ensure /etc/subuid+/etc/subgid contain `root:1000:1` and restart incus)",
			r.inst, startCode, startStderr, lastLines(showLog, 6))
		r.setStatus(runtime.Status{State: runtime.StateError, Error: msg})
		return fmt.Errorf("%s: %w", msg, startErr)
	}

	// 5. Wait until the container agent is ready (incus exec succeeds).
	// [AC-S8478ca-2-1]
	if err := r.waitReady(ctx); err != nil {
		r.setStatus(runtime.Status{State: runtime.StateError, Error: err.Error()})
		return err
	}

	// 6. Resolve the container's bridge IP.
	addr, err := r.containerIP(ctx)
	if err != nil {
		r.log.Warn("incus: could not resolve container IP", "inst", r.inst, "err", err)
		addr = ""
	}

	r.setStatus(runtime.Status{
		State:   runtime.StateReady,
		Address: addr,
	})
	r.log.Info("incus: container ready", "inst", r.inst, "addr", addr)
	return nil
}

// waitReady polls until `incus exec <inst> -- true` exits 0, meaning the
// in-container agent accepted the exec.  Times out after 60 s.
func (r *incusRuntime) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		_, _, code, err := r.run(ctx, "exec", r.inst, "--", "true")
		if err == nil && code == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("incus waitReady: context cancelled: %w", ctx.Err())
		case <-time.After(1 * time.Second):
		}
	}
	return fmt.Errorf("incus waitReady: timed out waiting for container agent on %s", r.inst)
}

// containerIP returns the first IPv4 address on eth0 in the container.
func (r *incusRuntime) containerIP(ctx context.Context) (string, error) {
	stdout, _, code, err := r.run(ctx, "list", r.inst, "-f", "json")
	if err != nil || code != 0 {
		return "", fmt.Errorf("incus list: code=%d err=%w", code, err)
	}
	type addrEntry struct {
		Family  string `json:"family"`
		Address string `json:"address"`
		Netmask string `json:"netmask"`
	}
	type ifaceState struct {
		Addresses []addrEntry `json:"addresses"`
	}
	type networkState struct {
		Eth0 *ifaceState `json:"eth0"`
	}
	type stateBlock struct {
		Network networkState `json:"network"`
	}
	type instEntry struct {
		Name  string     `json:"name"`
		State stateBlock `json:"state"`
	}
	var entries []instEntry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		return "", fmt.Errorf("incus list parse: %w", err)
	}
	for _, e := range entries {
		if e.Name != r.inst {
			continue
		}
		if e.State.Network.Eth0 == nil {
			break
		}
		for _, a := range e.State.Network.Eth0.Addresses {
			if a.Family == "inet" && a.Address != "" {
				return a.Address, nil
			}
		}
	}
	return "", nil
}

// Stop deletes the container with --force (idempotent).
// [AC-S8478ca-2-1] [AC-S8478ca-2-4]
func (r *incusRuntime) Stop(ctx context.Context) error {
	_, _, _, err := r.run(ctx, "delete", "--force", r.inst)
	// non-zero exit from delete --force when instance does not exist is acceptable
	r.setStatus(runtime.Status{State: runtime.StateStopped})
	if err != nil {
		r.log.Warn("incus delete --force returned OS error", "inst", r.inst, "err", err)
	}
	return nil
}

// Exec runs a command inside the container and captures output.
// [AC-S8478ca-2-3]
func (r *incusRuntime) Exec(ctx context.Context, cmd []string, opts runtime.ExecOpts) (runtime.ExecResult, error) {
	if len(cmd) == 0 {
		return runtime.ExecResult{}, fmt.Errorf("incus Exec: cmd must not be empty")
	}
	// Build: incus exec <inst> [--cwd <dir>] [-- env K=V ...] -- <cmd>
	args := []string{"exec", r.inst}
	if opts.Dir != "" {
		args = append(args, "--cwd", opts.Dir)
	}
	if len(opts.Env) > 0 {
		args = append(args, "--env")
		args = append(args, opts.Env...)
	}
	args = append(args, "--")
	args = append(args, cmd...)
	stdout, stderr, code, err := r.run(ctx, args...)
	if err != nil {
		return runtime.ExecResult{}, fmt.Errorf("incus Exec: %w", err)
	}
	return runtime.ExecResult{Stdout: stdout, Stderr: stderr, ExitCode: code}, nil
}

// ListListeningPorts runs `ss -tlnH` inside the container and parses the
// output.  Returns an empty list on error (non-fatal).
func (r *incusRuntime) ListListeningPorts(ctx context.Context) ([]runtime.ListeningPort, error) {
	res, err := r.Exec(ctx, []string{"ss", "-tlnH"}, runtime.ExecOpts{})
	if err != nil {
		return nil, fmt.Errorf("incus ListListeningPorts: %w", err)
	}
	var ports []runtime.ListeningPort
	for _, line := range strings.Split(res.Stdout, "\n") {
		// ss -tlnH format: State Recv-Q Send-Q Local-Address:Port Peer-Address:Port
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		local := fields[3]
		lastColon := strings.LastIndex(local, ":")
		if lastColon < 0 {
			continue
		}
		portStr := local[lastColon+1:]
		p, err := strconv.Atoi(portStr)
		if err != nil || p <= 0 {
			continue
		}
		ports = append(ports, runtime.ListeningPort{
			Port:  p,
			Proto: "tcp",
		})
	}
	return ports, nil
}

// ExposePort is a stub for the incus runtime (Story -4 implements the real
// proxy-device / Caddy work).
func (r *incusRuntime) ExposePort(_ context.Context, spec runtime.PortSpec) (runtime.PortMapping, error) {
	id := fmt.Sprintf("incus-%s-%s-%d", r.inst, strings.ToLower(spec.Proto), spec.Internal)
	addr := r.Status().Address
	if addr == "" {
		addr = "pending"
	}
	return runtime.PortMapping{
		ID:       id,
		Internal: spec.Internal,
		HostPort: spec.HostPort,
		Proto:    spec.Proto,
		Address:  addr,
		Public:   spec.Public,
	}, nil
}

// UnexposePort is a stub for the incus runtime (Story -4).
func (r *incusRuntime) UnexposePort(_ context.Context, _ string) error {
	return nil
}

// TmuxClient returns an incusTmuxClient that routes all tmux operations
// through `incus exec <inst> -- tmux ...`.  The client is constructed lazily
// and cached in a sync.Once so multiple calls return the same instance.
// [AC-S8478ca-2-3]
func (r *incusRuntime) TmuxClient() tmux.Client {
	return NewTmuxClient(r.inst, r.run)
}

// NewTmuxSession creates a tmux session inside the container via `incus exec`.
// [AC-S8478ca-2-3]
func (r *incusRuntime) NewTmuxSession(ctx context.Context, session string) error {
	// tmux new-session -d -s <session> -x 220 -y 50
	res, err := r.Exec(ctx, []string{
		"tmux", "new-session", "-d",
		"-s", session,
		"-x", "220",
		"-y", "50",
	}, runtime.ExecOpts{})
	if err != nil {
		return fmt.Errorf("incus NewTmuxSession: %w", err)
	}
	if res.ExitCode != 0 {
		// "already exists" is acceptable — treat as idempotent
		if strings.Contains(res.Stderr, "duplicate session") || strings.Contains(res.Stderr, "already exists") {
			return nil
		}
		return fmt.Errorf("incus NewTmuxSession tmux exit %d: %s", res.ExitCode, res.Stderr)
	}
	return nil
}

// AttachTmuxSession opens a PTY-backed io.ReadWriteCloser to a tmux session
// running inside the container.  It starts `incus exec -t <inst> -- tmux
// attach-session -t <session>` under a pty and returns the pty file + a resize
// callback, wrapped in a ResizeWriter.
// [AC-S8478ca-2-3]
func (r *incusRuntime) AttachTmuxSession(ctx context.Context, session string) (io.ReadWriteCloser, error) {
	cmd := exec.CommandContext(ctx, "incus", "exec", "-t", r.inst, "--",
		"tmux", "attach-session", "-t", session)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("incus AttachTmuxSession pty start: %w", err)
	}
	return &ptyConn{cmd: cmd, f: f}, nil
}

// ptyConn wraps the pty file + process so Close terminates the client.
type ptyConn struct {
	cmd *exec.Cmd
	f   *os.File
}

func (p *ptyConn) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *ptyConn) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *ptyConn) Close() error {
	_ = p.f.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_, _ = p.cmd.Process.Wait()
	return nil
}

// ---------------------------------------------------------------------------
// incusTmuxClient — tmux.Client implementation that routes all operations
// through `incus exec` into the container.
// ---------------------------------------------------------------------------

// incusTmuxClient implements tmux.Client for a container workspace.  All tmux
// commands are executed as `incus exec <inst> -- tmux <args>` so the store's
// ensureSession / recomputeTabs / CloseBranch paths continue to work unchanged.
// [AC-S8478ca-2-3]
type incusTmuxClient struct {
	inst string
	run  runner
}

// NewTmuxClient returns a tmux.Client whose operations are routed through
// `incus exec <inst>`.  This is the object wired into store.Deps.Tmux for an
// incus-container workspace.
func NewTmuxClient(inst string, r runner) tmux.Client {
	if r == nil {
		r = defaultRunner
	}
	return &incusTmuxClient{inst: inst, run: r}
}

// detachedTmuxCtx returns a context that is NOT cancelled when the caller's
// ctx is (e.g. an HTTP request finishing, or the sync_tmux loop iterating),
// but still has an upper time bound. This matters because `incus exec -- tmux
// new-session -d` spawns the in-container tmux SERVER as a child of the exec
// process: if the caller's ctx cancels and kills `incus exec` before tmux has
// finished daemonising, the freshly-created session dies with it (the host
// runtime never hit this because its tmux ops are local + instant). State-
// mutating tmux ops therefore run under a detached, bounded context.
func detachedTmuxCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}

func (c *incusTmuxClient) incus(ctx context.Context, args ...string) (string, error) {
	dctx, cancel := detachedTmuxCtx(ctx)
	defer cancel()
	argv := append([]string{"exec", c.inst, "--", "tmux"}, args...)
	stdout, stderr, code, err := c.run(dctx, argv...)
	if err != nil {
		return "", fmt.Errorf("incus exec tmux %s: %w", strings.Join(args, " "), err)
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("exit %d", code)
		}
		return stdout, fmt.Errorf("tmux %s: %s", strings.Join(args, " "), msg)
	}
	return stdout, nil
}

func (c *incusTmuxClient) ListSessions(ctx context.Context) ([]tmux.Session, error) {
	out, err := c.incus(ctx, "list-sessions", "-F", "#{session_name}\t#{session_created}\t#{session_attached}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return nil, nil
		}
		return nil, err
	}
	var sessions []tmux.Session
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 1 || fields[0] == "" {
			continue
		}
		s := tmux.Session{Name: fields[0]}
		if len(fields) > 1 {
			s.CreatedAt, _ = strconv.ParseInt(fields[1], 10, 64)
		}
		if len(fields) > 2 {
			s.Attached = fields[2] == "1"
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

func (c *incusTmuxClient) NewSession(ctx context.Context, opts tmux.NewSessionOpts) error {
	args := []string{"new-session", "-d", "-s", opts.Name, "-x", "220", "-y", "50"}
	if opts.WindowName != "" {
		args = append(args, "-n", opts.WindowName)
	}
	if opts.Cwd != "" {
		args = append(args, "-c", opts.Cwd)
	}
	if opts.Command != "" {
		args = append(args, opts.Command)
	}
	// Prepend env vars as VAR=VAL before the command when needed.
	// tmux does not have a direct env flag; pass via env-only if no command.
	// For the command case, wrap in sh -c "export ... && cmd" is fragile; leave
	// them as shell env — the caller sets them in the Command string already.
	_, err := c.incus(ctx, args...)
	return err
}

func (c *incusTmuxClient) KillSession(ctx context.Context, name string) error {
	_, err := c.incus(ctx, "kill-session", "-t", name)
	return err
}

func (c *incusTmuxClient) HasSession(ctx context.Context, name string) (bool, error) {
	dctx, cancel := detachedTmuxCtx(ctx)
	defer cancel()
	_, _, code, err := c.run(dctx, "exec", c.inst, "--", "tmux", "has-session", "-t", name)
	if err != nil {
		return false, err
	}
	return code == 0, nil
}

func (c *incusTmuxClient) RenameSession(ctx context.Context, oldName, newName string) error {
	_, err := c.incus(ctx, "rename-session", "-t", oldName, newName)
	return err
}

func (c *incusTmuxClient) ListWindows(ctx context.Context, session string) ([]tmux.Window, error) {
	out, err := c.incus(ctx, "list-windows", "-t", session, "-F", "#{window_index}\t#{window_name}")
	if err != nil {
		return nil, err
	}
	var windows []tmux.Window
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		idx, _ := strconv.Atoi(fields[0])
		windows = append(windows, tmux.Window{Index: idx, Name: fields[1]})
	}
	return windows, nil
}

func (c *incusTmuxClient) NewWindow(ctx context.Context, session string, opts tmux.NewWindowOpts) error {
	args := []string{"new-window", "-t", session, "-n", opts.Name}
	if opts.Cwd != "" {
		args = append(args, "-c", opts.Cwd)
	}
	if opts.Command != "" {
		args = append(args, opts.Command)
	}
	_, err := c.incus(ctx, args...)
	return err
}

func (c *incusTmuxClient) KillWindowByName(ctx context.Context, session, windowName string) error {
	_, err := c.incus(ctx, "kill-window", "-t", session+":"+windowName)
	return err
}

func (c *incusTmuxClient) RenameWindow(ctx context.Context, session, oldName, newName string) error {
	_, err := c.incus(ctx, "rename-window", "-t", session+":"+oldName, newName)
	return err
}

func (c *incusTmuxClient) WindowIndexByName(ctx context.Context, session, windowName string) (int, error) {
	wins, err := c.ListWindows(ctx, session)
	if err != nil {
		return 0, err
	}
	for _, w := range wins {
		if w.Name == windowName {
			return w.Index, nil
		}
	}
	return 0, fmt.Errorf("window %q not found in session %q", windowName, session)
}

func (c *incusTmuxClient) SendKeys(ctx context.Context, session, windowName, keys string) error {
	_, err := c.incus(ctx, "send-keys", "-t", session+":"+windowName, keys, "")
	return err
}

func (c *incusTmuxClient) RespawnWindow(ctx context.Context, session, windowName, command string) error {
	_, err := c.incus(ctx, "respawn-window", "-t", session+":"+windowName, "-k", command)
	return err
}

// Attach opens a PTY to a tmux window inside the container.
// It runs `incus exec -t <inst> -- tmux attach-session -t <session>:<idx>`.
// [AC-S8478ca-2-3]
func (c *incusTmuxClient) Attach(ctx context.Context, session, windowName string, opts tmux.AttachOpts) (io.ReadWriteCloser, tmux.ResizeFunc, error) {
	idx, err := c.WindowIndexByName(ctx, session, windowName)
	if err != nil {
		return nil, nil, err
	}
	return c.AttachByIndex(ctx, session, idx, opts)
}

// AttachByIndex opens a PTY to a tmux window by index inside the container.
func (c *incusTmuxClient) AttachByIndex(ctx context.Context, session string, idx int, opts tmux.AttachOpts) (io.ReadWriteCloser, tmux.ResizeFunc, error) {
	target := fmt.Sprintf("%s:%d", session, idx)
	cmd := exec.CommandContext(ctx, "incus", "exec", "-t", c.inst, "--",
		"tmux", "attach-session", "-t", target)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	var (
		f      *os.File
		ptyErr error
	)
	if opts.Cols > 0 && opts.Rows > 0 {
		f, ptyErr = pty.StartWithSize(cmd, &pty.Winsize{
			Cols: uint16(opts.Cols),
			Rows: uint16(opts.Rows),
		})
	} else {
		f, ptyErr = pty.Start(cmd)
	}
	if ptyErr != nil {
		return nil, nil, fmt.Errorf("incus AttachByIndex pty start: %w", ptyErr)
	}

	conn := &ptyConn{cmd: cmd, f: f}
	resize := func(cols, rows int) error {
		return pty.Setsize(f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	}
	return conn, resize, nil
}

// NewGroupSession creates a tmux group session inside the container.
func (c *incusTmuxClient) NewGroupSession(ctx context.Context, target, groupName string) error {
	_, err := c.incus(ctx, "new-session", "-d", "-t", target, "-s", groupName)
	return err
}

// ---------------------------------------------------------------------------
// palmux-lock helpers (AC-S8478ca-2-4)
// ---------------------------------------------------------------------------

// AcquireLock creates .palmux-lock in the project directory for the given
// worktree path under ~/.claude/projects/<encoded-path>/.  Returns a release
// function and nil on success; returns an error if the lock already exists.
// [AC-S8478ca-2-4]
func AcquireLock(worktreePath string) (release func(), err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("palmux lock: get home: %w", err)
	}
	// Claude encodes project paths by replacing slashes with %2F (URL encoding).
	encoded := strings.ReplaceAll(worktreePath, "/", "%2F")
	projectDir := filepath.Join(home, ".claude", "projects", encoded)
	if mkErr := os.MkdirAll(projectDir, 0o755); mkErr != nil {
		return nil, fmt.Errorf("palmux lock: mkdir %s: %w", projectDir, mkErr)
	}
	lockPath := filepath.Join(projectDir, ".palmux-lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("palmux lock: already locked at %s (another claude instance is running)", lockPath)
		}
		return nil, fmt.Errorf("palmux lock: create %s: %w", lockPath, err)
	}
	if _, werr := fmt.Fprintf(f, "%d\n", os.Getpid()); werr != nil {
		_ = f.Close()
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("palmux lock: write pid: %w", werr)
	}
	_ = f.Close()
	return func() {
		_ = os.Remove(lockPath)
	}, nil
}

// lastLines returns the last n non-empty lines of s, joined by " | ", for
// compact inclusion of `incus info --show-log` tails in error messages.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append([]string{strings.TrimSpace(lines[i])}, out...)
		}
	}
	return strings.Join(out, " | ")
}
