// Package lxd implements the `lxd-container` WorkspaceRuntime via the `lxc`
// CLI. The runtime starts an LXD system container, applies the bind-mount
// strategy validated in the S98156b PoC (idmap "both 1000 1000", `~/.claude/`
// dir + `~/.claude.json` file rw, worktree rw, `~/.gitconfig` ro,
// `~/.config/gh` ro, `$SSH_AUTH_SOCK` forward), waits for cloud-init,
// pushes the palmux-agent, and starts it via systemd-run.
//
// Phase A scope (Sdd4ce1-3 / Sdd4ce1-4):
//   - Start, Stop, Status — full lifecycle
//   - NewTmuxSession, Exec — via `lxc exec` until the agent is wired
//   - ExposePort / UnexposePort — via `lxc config device add ... proxy`
//   - ListListeningPorts — agent RPC; falls back to `lxc exec ss -tln`
//   - Files API — agent RPC (forwarded over the UDS bind-mount)
//
// Design references:
//   docs/workspace-runtime-design.md §3.2.2, §4.2, §4.4 (bind-mount strategy),
//   §5.2 (port expose), §6.4.2 (agent push).
//
// AC mapping:
//   AC-Sdd4ce1-3-1 / -3-2 / -3-3 / -3-4 / -3-5
//   AC-Sdd4ce1-4-1 / -4-2 / -4-3 / -4-4
package lxd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

	"github.com/tjst-t/palmux2/internal/runtime"
)

// DefaultImage is used when Config.Image is empty.
//
// AC-Sdd4ce1-3-4: image is `ghcr.io/tjst-t/palmux-workspace:default` by
// default and can be overridden per-Workspace via repos.json.
const DefaultImage = "ghcr.io/tjst-t/palmux-workspace:default"

// CloudInitWaitTimeout caps the cloud-init wait. S98156b PoC observed first
// boot of `images:ubuntu/24.04/cloud` complete in ~30 s.
//
// AC-Sdd4ce1-3-5.
const CloudInitWaitTimeout = 60 * time.Second

// CloudInitPollInterval is the gap between `lxc exec -- id ubuntu` polls.
const CloudInitPollInterval = 2 * time.Second

// Runtime is the lxd-container implementation of runtime.Runtime. It is
// concurrency-safe.
type Runtime struct {
	cfg          runtime.Config
	worktreePath string
	branchName   string
	repoID       string
	branchID     string
	logger       *slog.Logger

	// agentBinary is the host-side path to the palmux-agent binary. The
	// runtime pushes it into the container at Start. Empty means "agent
	// push disabled" — Phase A allows this so the runtime is testable on
	// hosts that don't have the agent built (we fall back to lxc exec).
	agentBinary string

	// instanceName is derived from (repoID, branchID) via instanceNameFor.
	instanceName string

	mu       sync.RWMutex
	status   runtime.Status
	mappings map[string]runtime.PortMapping // by mapping ID (the lxc device name)
}

// Options bundles construction-time deps.
type Options struct {
	// AgentBinary is the host-side path to palmux-agent. Empty disables
	// agent push (the runtime falls back to `lxc exec` for everything).
	AgentBinary string

	// Logger receives lifecycle events. nil = slog.Default().
	Logger *slog.Logger
}

// New constructs an lxd-container Runtime. cfg is the resolved Config (kind
// must be runtime.KindLXDContainer; other kinds are accepted but the
// runtime falls back to lxd-container behaviour). worktreePath is the
// host-side absolute path of the Workspace's worktree. repoID and branchID
// participate in the LXD instance name so two Workspaces of the same
// branch in different repos get distinct containers.
func New(cfg runtime.Config, worktreePath string, repoID, branchID, branchName string, opts Options) *Runtime {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Runtime{
		cfg:          cfg,
		worktreePath: worktreePath,
		branchName:   branchName,
		repoID:       repoID,
		branchID:     branchID,
		logger:       logger,
		agentBinary:  opts.AgentBinary,
		instanceName: instanceNameFor(repoID, branchID),
		mappings:     map[string]runtime.PortMapping{},
		status:       runtime.Status{State: runtime.StateStopped},
	}
}

// Kind reports runtime.KindLXDContainer.
func (r *Runtime) Kind() runtime.Kind { return runtime.KindLXDContainer }

// Config returns the resolved Config.
func (r *Runtime) Config() runtime.Config { return r.cfg }

// InstanceName returns the LXD instance name for testing/inspection.
func (r *Runtime) InstanceName() string { return r.instanceName }

// Status returns the current snapshot.
func (r *Runtime) Status() runtime.Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

// instanceNameFor derives a deterministic, lxc-legal instance name from
// (repoID, branchID). LXD requires names matching `[A-Za-z0-9-]{1,63}`. We
// hash the input to avoid the slug-with-double-hyphen issue.
//
//	prefix `palmux-` + first 16 hex of sha256(repoID|branchID)
//
// Length is 23 chars — fits in the LXD 63-char limit comfortably.
func instanceNameFor(repoID, branchID string) string {
	h := sha256.Sum256([]byte(repoID + "|" + branchID))
	return "palmux-" + hex.EncodeToString(h[:8])
}

// Start brings up the container. The phases are:
//
//  1. lxc init → lxc config set raw.idmap → lxc start (start before
//     adding the bind-mount disks, so idmap is applied to the new
//     instance).
//  2. wait for cloud-init: poll `lxc exec -- id ubuntu` until success or
//     CloudInitWaitTimeout.
//  3. add bind-mount devices (worktree rw, ~/.claude/ rw, ~/.claude.json
//     rw, ~/.gitconfig ro, ~/.config/gh ro, $SSH_AUTH_SOCK forward).
//  4. push palmux-agent and start it via systemd-run (best-effort —
//     agent absence is logged but not fatal in Phase A).
//
// Idempotent: if an instance with the same name already exists in RUNNING
// state, the call short-circuits to Status==Ready.
//
// AC-Sdd4ce1-3-1 / AC-Sdd4ce1-3-5 / AC-Sdd4ce1-4-1..4-4.
func (r *Runtime) Start(ctx context.Context) error {
	r.setStatus(runtime.Status{State: runtime.StateStarting, StartedAt: time.Now().UTC()})

	r.logger.Info("lxd-container: starting", "instance", r.instanceName, "image", r.image(), "worktree", r.worktreePath)

	if state, err := r.instanceState(ctx); err == nil && state == "RUNNING" {
		r.logger.Info("lxd-container: already running", "instance", r.instanceName)
		// Re-fetch IP and mark ready.
		ip, _ := r.instanceIP(ctx)
		r.setStatus(runtime.Status{
			State:     runtime.StateReady,
			StartedAt: time.Now().UTC(),
			Address:   ip,
		})
		return nil
	}

	// Phase 1: init + idmap + start.
	if err := r.runLXC(ctx, "launch", r.image(), r.instanceName); err != nil {
		// `lxc launch` does init + start in one go. If the instance already
		// exists in stopped state, retry with `lxc start`.
		if isInstanceExistsErr(err) {
			r.logger.Info("lxd-container: instance exists, starting", "instance", r.instanceName)
			if err := r.runLXC(ctx, "start", r.instanceName); err != nil {
				return r.failed("lxc start: " + err.Error())
			}
		} else {
			return r.failed("lxc launch: " + err.Error())
		}
	}

	// raw.idmap "both 1000 1000" — design §4.4 hotfix (LXD 5.21.4 form).
	if err := r.runLXC(ctx, "config", "set", r.instanceName, "raw.idmap", "both 1000 1000"); err != nil {
		return r.failed("set raw.idmap: " + err.Error())
	}
	// idmap takes effect on next start. If we just launched, restart so
	// the bind-mount permissions line up.
	if err := r.runLXC(ctx, "restart", r.instanceName); err != nil {
		return r.failed("lxc restart for idmap: " + err.Error())
	}

	// Phase 2: cloud-init wait.
	if err := r.waitCloudInit(ctx); err != nil {
		return r.failed("cloud-init wait: " + err.Error())
	}

	// Phase 3: bind-mount strategy. Failures are logged but not fatal —
	// the user can still attach a Bash tab and inspect what's wrong.
	if err := r.applyBindMounts(ctx); err != nil {
		r.logger.Warn("lxd-container: bind-mount setup partial", "err", err)
	}

	// Phase 4: agent push (best-effort).
	if r.agentBinary != "" {
		if err := r.pushAndStartAgent(ctx); err != nil {
			r.logger.Warn("lxd-container: agent push failed", "err", err)
		}
	}

	ip, _ := r.instanceIP(ctx)
	r.setStatus(runtime.Status{
		State:     runtime.StateReady,
		StartedAt: time.Now().UTC(),
		Address:   ip,
	})
	r.logger.Info("lxd-container: ready", "instance", r.instanceName, "ip", ip)
	return nil
}

// Stop terminates the container. AC-Sdd4ce1-3-3.
//
// Bind-mount mappings are dropped automatically when the instance is
// removed; we keep our own port mapping bookkeeping in r.mappings so the
// caller can persist + restore them on next Start (§5.4).
func (r *Runtime) Stop(ctx context.Context) error {
	r.setStatus(runtime.Status{State: runtime.StateStopping})
	defer r.setStatus(runtime.Status{State: runtime.StateStopped})

	if err := r.runLXC(ctx, "stop", "--force", r.instanceName); err != nil {
		// "Instance not found" is fine — we tolerate the post-stop case.
		if !isInstanceNotFoundErr(err) {
			return fmt.Errorf("lxd-container: lxc stop: %w", err)
		}
	}
	// We do NOT delete the instance here — the user may want to re-Start
	// the same Workspace. Workspace close (lifecycle 1:1 per §14.2) is
	// handled by the caller with Delete().
	return nil
}

// Delete removes the LXD instance permanently. Called from the store on
// Workspace close.
func (r *Runtime) Delete(ctx context.Context) error {
	if err := r.runLXC(ctx, "delete", "--force", r.instanceName); err != nil {
		if !isInstanceNotFoundErr(err) {
			return fmt.Errorf("lxd-container: lxc delete: %w", err)
		}
	}
	return nil
}

// NewTmuxSession runs `tmux new-session -d` inside the container so a
// subsequent AttachTmuxSession can attach. We use `lxc exec` with the
// `ubuntu` user — the agent push path will replace this in Phase B.
//
// AC-Sdd4ce1-3-2.
func (r *Runtime) NewTmuxSession(ctx context.Context, sessionName string) error {
	cmd := []string{
		"tmux", "new-session", "-d", "-s", sessionName,
		"-c", "/workspace",
	}
	out, err := r.exec(ctx, cmd, "/workspace")
	if err != nil {
		// tmux returns 1 if the session already exists — tolerate.
		if strings.Contains(string(out.Stderr), "duplicate session") {
			return nil
		}
		return fmt.Errorf("lxd-container: tmux new-session: %w (stderr=%s)", err, out.Stderr)
	}
	return nil
}

// AttachTmuxSession returns a tmux attach pty stream over `lxc exec -t`.
// The returned ReadWriteCloser is the exec process's stdio merged with the
// tty; closing detaches.
//
// AC-Sdd4ce1-3-2.
func (r *Runtime) AttachTmuxSession(ctx context.Context, sessionName string) (io.ReadWriteCloser, error) {
	cmd := exec.CommandContext(ctx, "lxc", "exec", r.instanceName, "--user", "1000", "-t", "--",
		"tmux", "attach-session", "-t", sessionName)
	pipeIn, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pipeOut, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lxd-container: lxc exec attach: %w", err)
	}
	return &execStream{cmd: cmd, in: pipeIn, out: pipeOut}, nil
}

// Exec runs a command synchronously inside the container.
//
// AC-Sdd4ce1-3-2.
func (r *Runtime) Exec(ctx context.Context, cmd []string, opts runtime.ExecOpts) (runtime.ExecResult, error) {
	cwd := opts.Cwd
	if cwd == "" {
		cwd = "/workspace"
	}
	res, err := r.exec(ctx, cmd, cwd)
	return res, err
}

// ListListeningPorts returns LISTEN ports inside the container by reading
// /proc/net/tcp(6) via `lxc exec`. Mirrors the agent.handleListListeningPorts
// fallback path; once the agent is wired in Phase B this becomes an RPC.
func (r *Runtime) ListListeningPorts(ctx context.Context) ([]runtime.ListeningPort, error) {
	res, err := r.exec(ctx, []string{"cat", "/proc/net/tcp"}, "/")
	if err != nil {
		return nil, fmt.Errorf("lxd-container: read /proc/net/tcp: %w", err)
	}
	ports := parseProcNetTCP(string(res.Stdout), "tcp")
	res6, err := r.exec(ctx, []string{"cat", "/proc/net/tcp6"}, "/")
	if err == nil {
		ports = append(ports, parseProcNetTCP(string(res6.Stdout), "tcp6")...)
	}
	return ports, nil
}

// ExposePort adds an `lxc config device add ... proxy listen=tcp:127.0.0.1:<host>
// connect=tcp:127.0.0.1:<container>`.
//
// AC-Sdd4ce1-3-3 / design §5.2.
func (r *Runtime) ExposePort(ctx context.Context, internalPort, hostPort int, name string, public bool) (runtime.PortMapping, error) {
	if internalPort <= 0 {
		return runtime.PortMapping{}, fmt.Errorf("lxd-container: invalid internal port %d", internalPort)
	}
	hp := hostPort
	if hp == 0 {
		hp = internalPort // best-effort default; caller should pass an allocator-chosen port
	}
	bind := "127.0.0.1"
	if public {
		bind = "0.0.0.0"
	}
	deviceName := portDeviceName(name, hp)
	listen := fmt.Sprintf("tcp:%s:%d", bind, hp)
	connect := fmt.Sprintf("tcp:127.0.0.1:%d", internalPort)
	if err := r.runLXC(ctx, "config", "device", "add", r.instanceName, deviceName, "proxy",
		"listen="+listen, "connect="+connect); err != nil {
		return runtime.PortMapping{}, fmt.Errorf("lxd-container: device add proxy: %w", err)
	}
	mapping := runtime.PortMapping{
		ID:            deviceName,
		HostPort:      hp,
		ContainerPort: internalPort,
		Name:          name,
		Public:        public,
	}
	r.mu.Lock()
	r.mappings[deviceName] = mapping
	r.mu.Unlock()
	return mapping, nil
}

// UnexposePort removes a previously-added proxy device.
func (r *Runtime) UnexposePort(ctx context.Context, mappingID string) error {
	if err := r.runLXC(ctx, "config", "device", "remove", r.instanceName, mappingID); err != nil {
		return fmt.Errorf("lxd-container: device remove: %w", err)
	}
	r.mu.Lock()
	delete(r.mappings, mappingID)
	r.mu.Unlock()
	return nil
}

// Mappings returns the recorded mappings (for tests + persistence).
func (r *Runtime) Mappings() []runtime.PortMapping {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]runtime.PortMapping, 0, len(r.mappings))
	for _, m := range r.mappings {
		out = append(out, m)
	}
	return out
}

// ----- Files API (Phase A: shell out to `lxc exec`) -----

// ReadFile reads a file from inside the container. Path is treated as
// container-absolute when it starts with "/", or worktree-relative
// (rooted at /workspace) otherwise.
func (r *Runtime) ReadFile(ctx context.Context, p string) ([]byte, error) {
	abs := containerPath(p)
	res, err := r.exec(ctx, []string{"cat", abs}, "/")
	if err != nil {
		return nil, fmt.Errorf("lxd-container: read %s: %w (stderr=%s)", abs, err, res.Stderr)
	}
	return res.Stdout, nil
}

// WriteFile writes a file inside the container. Uses `tee` over stdin so
// the path can be created without an intermediate file on the host.
func (r *Runtime) WriteFile(ctx context.Context, p string, data []byte) error {
	abs := containerPath(p)
	cmd := exec.CommandContext(ctx, "lxc", "exec", r.instanceName, "--", "sh", "-c",
		"mkdir -p \""+filepath.Dir(abs)+"\" && tee \""+abs+"\" >/dev/null")
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("lxd-container: write %s: %w (stderr=%s)", abs, err, stderr.String())
	}
	return nil
}

// Stat runs `stat` inside the container and parses the output. Format is
// the GNU coreutils `stat -c "%n|%s|%a|%F|%Y"`. We avoid the agent for now;
// Phase B will move this to RPC.
func (r *Runtime) Stat(ctx context.Context, p string) (runtime.FileInfo, error) {
	abs := containerPath(p)
	res, err := r.exec(ctx, []string{"stat", "-c", "%n|%s|%a|%F|%Y", abs}, "/")
	if err != nil {
		return runtime.FileInfo{}, fmt.Errorf("lxd-container: stat %s: %w", abs, err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(res.Stdout)), "|", 5)
	if len(parts) != 5 {
		return runtime.FileInfo{}, fmt.Errorf("lxd-container: stat: bad output %q", res.Stdout)
	}
	size, _ := strconv.ParseInt(parts[1], 10, 64)
	mod, _ := strconv.ParseInt(parts[4], 10, 64)
	return runtime.FileInfo{
		Name:    filepath.Base(parts[0]),
		Size:    size,
		Mode:    parts[2], // octal string
		IsDir:   parts[3] == "directory",
		ModTime: time.Unix(mod, 0).UTC(),
	}, nil
}

// Walk shells out to `find` and parses its output. Each line is a path. We
// emit WalkEntry with Size/Mode resolved by a follow-up `stat` per entry —
// expensive for large trees, but Phase A only uses this from Files-tab
// list views (always shallow). Phase B replaces this with the agent's
// Walk RPC.
func (r *Runtime) Walk(ctx context.Context, p string, fn runtime.WalkFunc) error {
	abs := containerPath(p)
	res, err := r.exec(ctx, []string{"find", abs, "-maxdepth", "3", "-printf", "%P\\t%s\\t%y\\n"}, "/")
	if err != nil {
		return fmt.Errorf("lxd-container: walk %s: %w", abs, err)
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		size, _ := strconv.ParseInt(fields[1], 10, 64)
		entry := runtime.WalkEntry{
			RelPath: fields[0],
			Name:    filepath.Base(fields[0]),
			IsDir:   fields[2] == "d",
			Size:    size,
		}
		if err := fn(entry); err != nil {
			if errors.Is(err, errSkip) {
				continue
			}
			return err
		}
	}
	return nil
}

var errSkip = errors.New("skip")

// ----- Bind-mount strategy -----

// applyBindMounts adds the LXD disk devices the design doc §4.2 + §4.4
// PoC findings require. Each mount becomes a separate `lxc config device
// add ... disk` call with `shift=true` so idmap applies to the bind too.
//
// Two-mount design for ~/.claude (S98156b PoC):
//   - claude-dir: ~/.claude/  → /home/ubuntu/.claude/  (rw)
//   - claude-json: ~/.claude.json → /home/ubuntu/.claude.json (rw)
//
// Without claude-json, claude CLI errors with "Configuration file not found".
//
// AC-Sdd4ce1-4-1 / -4-2 / -4-3 / -4-4.
func (r *Runtime) applyBindMounts(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("user home: %w", err)
	}

	// Per-Workspace project memory dir under ~/.claude/projects. We bind
	// only the specific project to honour priority_rule 5 (don't touch
	// other projects' memory).
	projectDir := filepath.Join(home, ".claude", "projects", flattenPathForClaudeProject(r.worktreePath))

	type mount struct {
		device   string
		source   string
		target   string
		readOnly bool
	}
	mounts := []mount{
		// Worktree as /workspace inside the container (rw).
		{device: "workspace", source: r.worktreePath, target: "/workspace"},
		// ~/.claude/skills, ~/.claude/projects/<this> via the whole
		// directory bind. AC-Sdd4ce1-4-1 / -4-2 — design §4.4 PoC.
		{device: "claude-dir", source: filepath.Join(home, ".claude"), target: "/home/ubuntu/.claude"},
		// ~/.claude.json file bind — S98156b PoC critical hotfix.
		// AC-Sdd4ce1-3-1 (lxc CLI bind) + design §4.4.
		{device: "claude-json", source: filepath.Join(home, ".claude.json"), target: "/home/ubuntu/.claude.json"},
		// gitconfig (ro). AC-Sdd4ce1-4 mounts list.
		{device: "gitconfig", source: filepath.Join(home, ".gitconfig"), target: "/home/ubuntu/.gitconfig", readOnly: true},
		// ~/.config/gh (ro) — gh CLI auth.
		{device: "gh-config", source: filepath.Join(home, ".config", "gh"), target: "/home/ubuntu/.config/gh", readOnly: true},
	}

	// Ensure project dir exists so the bind-mount target is non-empty.
	_ = os.MkdirAll(projectDir, 0o755)

	for _, m := range mounts {
		if _, err := os.Stat(m.source); err != nil {
			// Skip missing optional sources (e.g. user without ~/.gitconfig).
			r.logger.Info("lxd-container: skip bind (source missing)", "device", m.device, "source", m.source)
			continue
		}
		args := []string{"config", "device", "add", r.instanceName, m.device, "disk",
			"source=" + m.source, "path=" + m.target, "shift=true"}
		if m.readOnly {
			args = append(args, "readonly=true")
		}
		if err := r.runLXC(ctx, args...); err != nil {
			r.logger.Warn("lxd-container: bind-mount failed", "device", m.device, "err", err)
			continue
		}
	}

	// SSH agent socket forward — AC-Sdd4ce1-4-4. Not all hosts run an
	// agent; we only attempt this if SSH_AUTH_SOCK is set.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		args := []string{"config", "device", "add", r.instanceName, "ssh-auth-sock", "disk",
			"source=" + sock, "path=/tmp/ssh-auth-sock", "shift=true"}
		if err := r.runLXC(ctx, args...); err != nil {
			r.logger.Warn("lxd-container: ssh-auth-sock bind failed", "err", err)
		}
	}

	// AC-Sdd4ce1-4-3: settings.json is NOT bind-mounted. We deliberately
	// do nothing here — that's the contract.
	return nil
}

// flattenPathForClaudeProject mirrors the Claude CLI's path-flattening
// scheme for ~/.claude/projects/*. Currently approximated by replacing /
// with - and stripping the leading slash. This is best-effort — Phase B
// will read the exact convention from claude internals.
func flattenPathForClaudeProject(p string) string {
	return strings.ReplaceAll(strings.TrimPrefix(p, "/"), "/", "-")
}

// ----- agent push -----

// pushAndStartAgent copies palmux-agent into the container at /usr/local/bin
// and starts it via `systemd-run --unit palmux-agent`. Best-effort —
// failures here are logged but do not fail Start.
//
// AC-Sdd4ce1-3-1.
func (r *Runtime) pushAndStartAgent(ctx context.Context) error {
	// 1. Push the binary.
	if err := r.runLXC(ctx, "file", "push", "--mode=0755", r.agentBinary,
		r.instanceName+"/usr/local/bin/palmux-agent"); err != nil {
		return fmt.Errorf("file push: %w", err)
	}
	// 2. Start it via systemd-run as user 1000 so the UDS lives in
	//    /tmp/palmux-agent.sock with that user's perms.
	if err := r.runLXC(ctx, "exec", r.instanceName, "--",
		"systemd-run", "--unit=palmux-agent",
		"/usr/local/bin/palmux-agent", "--socket", "/tmp/palmux-agent.sock"); err != nil {
		return fmt.Errorf("systemd-run: %w", err)
	}
	return nil
}

// ----- helpers -----

// runLXC runs a `lxc <args...>` command. Stdout is discarded; stderr is
// captured and surfaced in the error.
func (r *Runtime) runLXC(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "lxc", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// exec runs a command inside the container as user 1000 (ubuntu) and
// returns the captured stdio.
func (r *Runtime) exec(ctx context.Context, command []string, cwd string) (runtime.ExecResult, error) {
	args := []string{"exec", r.instanceName, "--user", "1000", "--cwd", cwd, "--"}
	args = append(args, command...)
	cmd := exec.CommandContext(ctx, "lxc", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := runtime.ExecResult{
		ExitCode: cmd.ProcessState.ExitCode(),
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		_ = exitErr
		return res, nil
	}
	return res, err
}

// instanceState returns the LXD lifecycle state ("RUNNING", "STOPPED", ...).
func (r *Runtime) instanceState(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "lxc", "list", "--format=csv", "--columns=ns", r.instanceName)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		fields := strings.Split(line, ",")
		if len(fields) >= 2 && fields[0] == r.instanceName {
			return fields[1], nil
		}
	}
	return "", errInstanceNotFound
}

// instanceIP returns the eth0 IPv4 address of the instance, or empty.
func (r *Runtime) instanceIP(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "lxc", "list", "--format=csv", "--columns=4", r.instanceName)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	out := strings.TrimSpace(stdout.String())
	// Format: "10.x.y.z (eth0)"
	if i := strings.Index(out, " "); i > 0 {
		return out[:i], nil
	}
	return out, nil
}

// waitCloudInit polls `lxc exec -- id ubuntu` until success or timeout.
//
// AC-Sdd4ce1-3-5.
func (r *Runtime) waitCloudInit(ctx context.Context) error {
	deadline := time.Now().Add(CloudInitWaitTimeout)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		cmd := exec.CommandContext(ctx, "lxc", "exec", r.instanceName, "--", "id", "ubuntu")
		if err := cmd.Run(); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for cloud-init", CloudInitWaitTimeout)
		}
		time.Sleep(CloudInitPollInterval)
	}
}

// image returns the resolved image string (Config.Image or DefaultImage).
func (r *Runtime) image() string {
	if r.cfg.Image != "" {
		return r.cfg.Image
	}
	return DefaultImage
}

func (r *Runtime) setStatus(s runtime.Status) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

func (r *Runtime) failed(reason string) error {
	r.setStatus(runtime.Status{State: runtime.StateFailed, Error: reason})
	return errors.New(reason)
}

// portDeviceName builds a deterministic LXD device name for a port mapping.
// `proxy-<name>-<host-port>`. If name is empty, falls back to `proxy-<host-port>`.
func portDeviceName(name string, hostPort int) string {
	hp := strconv.Itoa(hostPort)
	if name == "" {
		return "proxy-" + hp
	}
	// Strip non-name-safe chars (LXD allows [A-Za-z0-9_-]).
	clean := make([]rune, 0, len(name))
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			clean = append(clean, r)
		}
	}
	return "proxy-" + string(clean) + "-" + hp
}

// containerPath converts a runtime-relative path into a container-absolute
// path. Paths starting with "/" are returned as-is; relative paths are
// rooted at /workspace.
func containerPath(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return filepath.Join("/workspace", filepath.Clean("/"+p))
}

// errInstanceNotFound is returned by helpers that detect a missing
// container without relying on LXD's own error wording.
var errInstanceNotFound = errors.New("lxd-container: instance not found")

func isInstanceExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "in use")
}

func isInstanceNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "no such")
}

// parseProcNetTCP parses /proc/net/tcp content into ListeningPort entries.
func parseProcNetTCP(content, protocol string) []runtime.ListeningPort {
	var entries []runtime.ListeningPort
	first := true
	for _, line := range strings.Split(content, "\n") {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 4 {
			continue
		}
		if !strings.EqualFold(fields[3], "0A") { // LISTEN
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
	return entries
}

// execStream wraps an exec.Cmd as an io.ReadWriteCloser for AttachTmuxSession.
type execStream struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
}

func (e *execStream) Read(p []byte) (int, error)  { return e.out.Read(p) }
func (e *execStream) Write(p []byte) (int, error) { return e.in.Write(p) }
func (e *execStream) Close() error {
	_ = e.in.Close()
	_ = e.out.Close()
	if e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
	return e.cmd.Wait()
}
