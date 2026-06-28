// Package incus implements the "incus-container" runtime: each Workspace gets
// its own Incus container.  Processes (tmux, claude, bash) run inside the
// container via `incus exec`.  Bind-mounts bring ~/ghq, ~/.claude,
// ~/.claude.json, ~/.local/share/claude and ~/.local/bin from the host into the
// container at the same absolute paths so the claude binary finds its OAuth
// credentials without re-authentication and always uses the host's native
// version (via ~/.local/bin/claude → ~/.local/share/claude/versions/<v>).
// /usr/local/bin/claude is intentionally NOT mounted — it is a host-specific
// bash cgroup wrapper that is not portable into the container.
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
	"encoding/base64"
	"encoding/json"
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

	// startMu serialises Start so concurrent callers (e.g. a runtime switch and
	// the sync_tmux recovery loop hitting tmuxFor at the same time) cannot both
	// drive `incus init/start` and trip an "Instance is busy running a create
	// operation" error. The second caller blocks, then sees State=ready and
	// returns without re-doing the work.
	startMu sync.Mutex

	// Caddy runner — injectable for tests; nil means defaultCaddyRunner.
	caddyRun caddyRunner

	// activeMappings tracks proxy devices added via ExposePort so UnexposePort
	// can issue the right `incus config device remove` command.
	// Key: PortMapping.ID, Value: device name used in `incus config device add`.
	activeMappingsMu sync.RWMutex
	activeMappings   map[string]string // mappingID → device name

	// pub is the public-subdomain publishing config for THIS workspace. nil or
	// disabled → legacy conf.d snippet behaviour (local dev). Set by the
	// Registry after construction. (See8bd4-2)
	pub       *publishConfig
	caddy     *caddyAdminClient // lazily created from pub.caddyAdmin
	caddyOnce sync.Once         // guards lazy caddy client init (data-race fix)

	// portsMu guards the last-scan port list and the user-controlled exposure
	// state used by the Ports tab. (See8bd4-3)
	portsMu   sync.RWMutex
	lastPorts []runtime.ListeningPort
	exposed   map[int]exposeState // port → subdomain exposure state
	// hostExposed tracks host-port publishing (incus proxy device) state, keyed
	// by container port. Used in the wildcard-DNS-less fallback mode. (S4c591a)
	hostExposed map[int]hostExposeState
	// hostOpLocks serialises ExposePortHost/UnexposePortHost per container port
	// so a concurrent unexpose during an in-flight expose cannot leak a proxy
	// device. hostOpMapMu guards the map itself. (S4c591a review)
	hostOpMapMu sync.Mutex
	hostOpLocks map[int]*sync.Mutex
}

// hostExposeState records a container port published on the host via an incus
// proxy device (S4c591a). hostPort is the host-side listen port (may differ
// from the container port when the original was busy and got reassigned).
type hostExposeState struct {
	hostPort  int
	mappingID string // ExposePort mapping ID, for UnexposePort
}

// exposeState records whether a port has a published Caddy route.
type exposeState struct {
	public bool   // exposed without basic_auth
	url    string // public https URL
}

// Compile-time assertions that incusRuntime implements the optional capability
// interfaces the store type-asserts for image drift detection + regeneration.
var (
	_ runtime.Runtime              = (*incusRuntime)(nil)
	_ runtime.ImageDriftChecker    = (*incusRuntime)(nil)
	_ runtime.ContainerRegenerator = (*incusRuntime)(nil)
	_ runtime.PTYCommander         = (*incusRuntime)(nil)
	_ runtime.ExecCommander        = (*incusRuntime)(nil)
)

// DefaultImageAlias is the incus image alias palmux containers are created from
// when runtime.Config.Image is empty. Image drift detection (S7364e3) compares a
// container's volatile.base_image against the fingerprint this alias currently
// resolves to.
const DefaultImageAlias = "palmux-ws"

// New returns a runtime.Runtime that manages an Incus container.
//
// instName must be a DNS-safe string (≤63 chars, a-z0-9-).  Callers should use
// InstanceName to derive it from (repoID, branchID).  If r is nil, the real
// `incus` binary is invoked.
func New(cfg runtime.Config, instName string, r runner, log *slog.Logger) runtime.Runtime {
	return NewWithCaddy(cfg, instName, r, nil, log)
}

// NewWithCaddy is like New but also accepts an injectable caddy runner for
// unit tests that need to assert Caddy arguments without a real caddy binary.
func NewWithCaddy(cfg runtime.Config, instName string, r runner, cr caddyRunner, log *slog.Logger) runtime.Runtime {
	if r == nil {
		r = defaultRunner
	}
	if log == nil {
		log = slog.Default()
	}
	image := cfg.Image
	if image == "" {
		image = DefaultImageAlias
	}
	return &incusRuntime{
		cfg:            runtime.Config{Kind: runtime.KindIncusContainer, Image: image},
		inst:           instName,
		run:            r,
		caddyRun:       cr, // nil → defaultCaddyRunner used on first call
		log:            log,
		status:         runtime.Status{State: runtime.StateStopped},
		activeMappings: map[string]string{},
		exposed:        map[int]exposeState{},
		hostExposed:    map[int]hostExposeState{},
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
	// Serialise Start: concurrent callers block here. The first does the work;
	// the rest see State=ready below and return immediately (idempotent).
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if r.Status().State == runtime.StateReady {
		// S4d8b1c: a container created before the palmux-hook-bin mount existed
		// would otherwise never get it (the mounts loop below is skipped on this
		// early return). Hot-plug it idempotently so in-container claude's
		// `palmux hook` works without requiring a full container regenerate.
		r.ensureHookBinMount(ctx)
		// Keep the home-area bind-mount parent dirs writable by the workspace
		// user on already-running containers too (browser/XDG fix — see
		// ensureHomeXDGOwnership). Idempotent.
		r.ensureHomeXDGOwnership(ctx)
		// If the cached container IP is empty (captured before the container
		// finished networking — e.g. a DHCP race right after a regenerate), the
		// browser tab's VNC/CDP can't reach it. Re-resolve it here so a consumer
		// calling Start() to "ensure up" gets a usable address.
		if r.Status().Address == "" {
			if ip, ipErr := r.containerIP(ctx); ipErr == nil && ip != "" {
				r.setStatus(runtime.Status{State: runtime.StateReady, Address: ip})
			}
		}
		return nil
	}

	r.setStatus(runtime.Status{State: runtime.StateStarting})

	image := r.cfg.Image
	if image == "" {
		image = DefaultImageAlias
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("incus Start: get home dir: %w", err)
	}

	// 1. Init the instance (do not start yet).
	// [AC-S8478ca-2-1]
	if _, initStderr, code, initErr := r.run(ctx, "init", image, r.inst); initErr != nil {
		r.setStatus(runtime.Status{State: runtime.StateError, Error: initErr.Error()})
		return fmt.Errorf("incus init %s %s: %w", image, r.inst, initErr)
	} else if code != 0 {
		// `incus init` exited non-zero. The only case we tolerate is "instance
		// already exists" (idempotent re-open). Anything else — most commonly a
		// missing image — is a hard, user-actionable error. Note: `incus list
		// <inst>` exits 0 even when the instance is absent (it just returns an
		// empty set), so we MUST decide on stderr, not on a follow-up list.
		stderr := strings.TrimSpace(initStderr)
		if !strings.Contains(stderr, "already exists") {
			detail := stderr
			if detail == "" {
				detail = fmt.Sprintf("exited %d", code)
			}
			msg := fmt.Sprintf("incus init %s: %s", image, detail)
			if strings.Contains(stderr, "not found") || strings.Contains(stderr, "No such") {
				msg += fmt.Sprintf(" (is the %q image imported on this host? build it with the workspace-default image, or set runtime.image)", image)
			}
			r.setStatus(runtime.Status{State: runtime.StateError, Error: msg})
			return fmt.Errorf("%s", msg)
		}
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
		detail := strings.TrimSpace(idmapStderr)
		if detail == "" && idmapErr != nil {
			detail = idmapErr.Error()
		}
		msg := fmt.Sprintf("incus config set raw.idmap: %s "+
			"(ensure /etc/subuid and /etc/subgid contain `root:1000:1`, then restart incus)", detail)
		r.setStatus(runtime.Status{State: runtime.StateError, Error: msg})
		return errors.New(msg)
	}

	// 2b. security.nesting: allow nested cgroups/containers so the workspace can
	// run Docker (and other nested containers) INSIDE this unprivileged incus
	// container. With nesting + a modern kernel's overlay2-on-overlayfs (or
	// fuse-overlayfs fallback), `docker build` / compose / app+DB containers work
	// at near-native speed — no VM needed for ordinary dev use. See
	// docs/workspace-runtime-design.md §9.1. The container stays unprivileged;
	// nesting does NOT grant host kernel-module / privileged-Docker / nested-KVM
	// (those remain VM-only).
	//
	// Non-fatal: this is an additive capability. If it can't be set (old incus,
	// odd policy), the workspace still starts — only in-container Docker is
	// unavailable — so we warn instead of failing Start.
	if _, nestStderr, nestCode, nestErr := r.run(ctx, "config", "set", r.inst, "security.nesting", "true"); nestErr != nil || nestCode != 0 {
		detail := strings.TrimSpace(nestStderr)
		if detail == "" && nestErr != nil {
			detail = nestErr.Error()
		}
		r.log.Warn("incus: could not enable security.nesting (in-container Docker disabled)",
			"inst", r.inst, "detail", detail)
	}

	// 2c. apparmor relaxation for nested Docker. nesting (2b) lets dockerd start,
	// but modern runc (1.2+) writes net.ipv4.ip_unprivileged_port_start for every
	// container it launches, and the unprivileged container's apparmor profile
	// blocks that proc-sysctl write → `docker run` dies with
	// "open sysctl net.ipv4.ip_unprivileged_port_start: permission denied".
	// Unconfining the container's apparmor profile makes `docker run` work
	// (verified on the deploy VM). This is a SECURITY TRADEOFF — it removes the
	// apparmor confinement layer — but the container stays UNPRIVILEGED with the
	// raw.idmap userns boundary intact, and the user opted in to full in-container
	// Docker. See docs/workspace-runtime-design.md §9.1.
	//
	// raw.lxc applies to the LXC driver (containers) only; it is a no-op concept
	// for the future VM runtime, which doesn't have this apparmor cage at all.
	// Non-fatal for the same reason as nesting.
	if _, aaStderr, aaCode, aaErr := r.run(ctx, "config", "set", r.inst, "raw.lxc", "lxc.apparmor.profile=unconfined"); aaErr != nil || aaCode != 0 {
		detail := strings.TrimSpace(aaStderr)
		if detail == "" && aaErr != nil {
			detail = aaErr.Error()
		}
		r.log.Warn("incus: could not relax apparmor (in-container `docker run` may fail on sysctl EPERM)",
			"inst", r.inst, "detail", detail)
	}

	// 3. Bind-mount ~/ghq, ~/.claude, ~/.claude.json, ~/.local/share/claude,
	// ~/.local/bin at same absolute paths.
	// [AC-S8478ca-2-2]
	//
	// ~/.local/share/claude holds the versioned native claude ELF binaries
	// (e.g. ~/.local/share/claude/versions/2.1.170).
	// ~/.local/bin contains the `claude` symlink → versions/<v>.
	//
	// Together these two mounts make `~/.local/bin/claude` inside the container
	// resolve to the host's native binary, always matching the host version.
	// We do NOT mount /usr/local/bin/claude because that is a host-specific bash
	// cgroup wrapper that is not portable into the container.
	ghqPath := filepath.Join(home, "ghq")
	claudeDir := filepath.Join(home, ".claude")
	claudeJSON := filepath.Join(home, ".claude.json")
	claudeShareDir := filepath.Join(home, ".local", "share", "claude")
	claudeBinDir := filepath.Join(home, ".local", "bin")

	// S4d8b1c: the running palmux binary (static linux amd64) is bind-mounted at
	// /usr/local/bin/palmux so an in-container claude can run `palmux hook` for
	// Claude Code notifications (the host hookBinPath does not exist in the
	// container). Resolved via os.Executable(); skipped if it can't be resolved.
	palmuxBin, palmuxBinErr := os.Executable()

	// S5818e8: also share the host's dev environment so the container's
	// interactive shell matches the host (shell dotfiles), and the Claude agent
	// can do GitHub operations (gh token, git identity, SSH keys). Same
	// philosophy as the ~/.claude mount: full capability, no re-auth. Each is
	// skipped if absent (loop below os.Stat-guards every entry), so hosts
	// without e.g. ~/.ssh still start cleanly.
	mj := func(p ...string) string { return filepath.Join(append([]string{home}, p...)...) }

	mounts := []struct {
		name   string
		source string
		path   string
	}{
		{"ghq", ghqPath, ghqPath},
		{"dot-claude", claudeDir, claudeDir},
		{"dot-claude-json", claudeJSON, claudeJSON},
		// claude native binary — bind-mount so in-container `~/.local/bin/claude`
		// is always the same version as the host (no re-download, no re-auth).
		{"dot-local-share-claude", claudeShareDir, claudeShareDir},
		{"dot-local-bin", claudeBinDir, claudeBinDir},
		// Shell dotfiles → the container shell matches the host (starship prompt,
		// aliases, ~/.bashrc.d functions). The shell-UX tools they invoke are
		// baked into the palmux-ws image (S5818e8-2).
		{"dot-bashrc", mj(".bashrc"), mj(".bashrc")},
		{"dot-profile", mj(".profile"), mj(".profile")},
		{"dot-bash-profile", mj(".bash_profile"), mj(".bash_profile")},
		{"dot-bashrc-d", mj(".bashrc.d"), mj(".bashrc.d")},
		// GitHub: identity + gh token + SSH keys so the agent can git/gh push.
		// Only ~/.config/gh is shared, NOT all of ~/.config (avoids chrome /
		// pulse / systemd / incus host-specific dirs and their state).
		{"dot-gitconfig", mj(".gitconfig"), mj(".gitconfig")},
		{"dot-config-gh", mj(".config", "gh"), mj(".config", "gh")},
		{"dot-ssh", mj(".ssh"), mj(".ssh")},
	}
	// S4d8b1c: palmux binary → /usr/local/bin/palmux (in-container `palmux hook`).
	if palmuxBinErr == nil && palmuxBin != "" {
		mounts = append(mounts, struct {
			name   string
			source string
			path   string
		}{"palmux-hook-bin", palmuxBin, "/usr/local/bin/palmux"})
	}
	for _, m := range mounts {
		// Skip if source does not exist on host — silently omit to avoid
		// failing on fresh machines where ~/.claude.json may not exist yet.
		if _, statErr := os.Stat(m.source); os.IsNotExist(statErr) {
			r.log.Warn("incus: bind-mount source not found, skipping", "source", m.source)
			continue
		}
		// Skip dotfiles that symlink OUTSIDE the home dir (e.g. Nix/home-manager
		// dotfiles → /nix/store). Bind-mounting such a symlink yields a broken
		// link in the container (the target isn't mounted) and breaks the shell
		// on login. On those hosts the container falls back to its image-default
		// shell instead. Hosts with real dotfiles (the common case) are unaffected.
		// ghq/.claude are intentionally exempt — they are real dirs we always want.
		if m.name != "ghq" && m.name != "palmux-hook-bin" && !strings.HasPrefix(m.name, "dot-claude") && !strings.HasPrefix(m.name, "dot-local") {
			if tgt, lerr := filepath.EvalSymlinks(m.source); lerr == nil {
				if rel, rerr := filepath.Rel(home, tgt); rerr != nil || strings.HasPrefix(rel, "..") {
					r.log.Info("incus: skipping dotfile that symlinks outside home (e.g. Nix store)",
						"source", m.source, "target", tgt)
					continue
				}
			}
		}
		_, stderr, code, err := r.run(ctx,
			"config", "device", "add", r.inst,
			m.name, "disk",
			"source="+m.source,
			"path="+m.path,
		)
		// "already exists" means a prior Start already added this device — the
		// re-add is a no-op (idempotent re-open of a pre-existing container),
		// not a failure. Without this, restarting palmux against a still-running
		// container leaves the runtime stuck in StateError.
		if err != nil || (code != 0 && !strings.Contains(stderr, "already exists")) {
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
	// "already running" means a prior Start (or an external `incus start`)
	// already brought the instance up — idempotent re-open, not a failure.
	if _, startStderr, startCode, startErr := r.run(ctx, "start", r.inst); startErr != nil ||
		(startCode != 0 && !strings.Contains(startStderr, "already running")) {
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

	// 5b. Make the home-area bind-mount PARENT dirs writable by the workspace
	// user. incus auto-creates them as container-root (see ensureHomeXDGOwnership).
	r.ensureHomeXDGOwnership(ctx)

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

// PTYCommand builds (does NOT start) a host *exec.Cmd that runs argv
// interactively inside this container under a PTY: `incus exec -t <inst>
// --user 1000 … --cwd <cwd> --env K=V … -- argv`. The caller (claude-tui
// daemon) starts it with creack/pty and owns the master fd. This is the same
// shape AttachByIndex uses for bash tabs. (S4d8b1c) [AC-S4d8b1c-1-1]
func (r *incusRuntime) PTYCommand(ctx context.Context, argv []string, opts runtime.PTYCommandOpts) *exec.Cmd {
	args := append([]string{"exec", "-t", r.inst}, userExecFlags()...)
	if opts.Cwd != "" {
		args = append(args, "--cwd", opts.Cwd)
	}
	for _, kv := range opts.Env {
		args = append(args, "--env", kv)
	}
	args = append(args, "--")
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, "incus", args...) //nolint:gosec
	// cmd.Env here is the env of the host-side `incus` CLI process; the
	// container env is delivered via the --env flags above. TERM for the CLI is
	// harmless; the container TERM is passed in opts.Env by the caller.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	// Do NOT set Stdin/Stdout/Stderr — the caller's pty.Start wires them to the
	// PTY slave.
	return cmd
}

// ensureHookBinMount idempotently bind-mounts the running palmux binary at
// /usr/local/bin/palmux inside the container (for in-container `palmux hook`).
// Safe to call on an already-running container — incus hot-plugs the disk device
// and "already exists" is tolerated. Best-effort: failures are logged, not
// fatal. (S4d8b1c)
func (r *incusRuntime) ensureHookBinMount(ctx context.Context) {
	palmuxBin, err := os.Executable()
	if err != nil || palmuxBin == "" {
		return
	}
	_, stderr, code, runErr := r.run(ctx,
		"config", "device", "add", r.inst,
		"palmux-hook-bin", "disk",
		"source="+palmuxBin,
		"path=/usr/local/bin/palmux",
	)
	if runErr != nil || (code != 0 && !strings.Contains(stderr, "already exists")) {
		r.log.Debug("incus: ensureHookBinMount (non-fatal)", "inst", r.inst, "code", code, "stderr", strings.TrimSpace(stderr))
	}
}

// ensureHomeXDGOwnership makes the home-area bind-mount PARENT directories
// writable by the workspace user (uid 1000). The nested bind-mounts
// (~/.config/gh, ~/.local/bin, ~/.local/share/claude, the browser profile
// ~/.local/share/palmux-browser/<inst>) cause incus to AUTO-CREATE their parent
// dirs (~/.config, ~/.local, ~/.local/share, ~/.local/share/palmux-browser) in
// the container rootfs as container-root (uid 0) — even though `raw.idmap both
// 1000 1000` + the subuid/subgid `root:1000:1` line are all correct. uid 1000
// then cannot create XDG dirs there, so chromium's wrapper dies on launch
// ("mkdir ~/.local/share/applications: Permission denied") and the Browser tab
// shows a black noVNC screen; any other XDG-writing app is affected too.
//
// We chown ONLY the parent dirs themselves, NON-recursively — their children are
// the bind-mounts, and a recursive chown would alter the HOST files through the
// idmap. The exec runs as container-root (no --user), which is required for
// chown. Idempotent + best-effort: failures are logged, never fatal. Runs on
// both fresh Start and the already-ready early-return path so existing
// containers are fixed on the next workspace open / palmux restart.
func (r *incusRuntime) ensureHomeXDGOwnership(ctx context.Context) {
	const dirs = wsHome + "/.config " + wsHome + "/.local " + wsHome + "/.local/share " + wsHome + "/.local/share/palmux-browser"
	// mkdir -p first so a parent that a skipped mount never created still ends up
	// owned by the user; chown is non-recursive (no -R) on purpose.
	script := fmt.Sprintf("mkdir -p %s && chown %s:%s %s", dirs, wsUID, wsGID, dirs)
	if _, stderr, code, err := r.run(ctx, "exec", r.inst, "--", "sh", "-c", script); err != nil || code != 0 {
		r.log.Warn("incus: ensureHomeXDGOwnership (browser/XDG writes may fail)",
			"inst", r.inst, "code", code, "stderr", strings.TrimSpace(stderr), "err", err)
	}
}

// ExecCommand builds (does NOT start) a host *exec.Cmd that runs argv inside
// this container over plain pipes: `incus exec <inst> --user … --cwd … --env …
// -- argv` with NO -t. The caller wires StdinPipe/StdoutPipe/StderrPipe; incus
// exec preserves binary-clean bidirectional stdio and a separate stderr (spike-
// verified), as the claude-agent stream-json transport requires. (S4d8b1c)
func (r *incusRuntime) ExecCommand(ctx context.Context, argv []string, opts runtime.PTYCommandOpts) *exec.Cmd {
	args := append([]string{"exec", r.inst}, userExecFlags()...) // no -t
	if opts.Cwd != "" {
		args = append(args, "--cwd", opts.Cwd)
	}
	for _, kv := range opts.Env {
		args = append(args, "--env", kv)
	}
	args = append(args, "--")
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, "incus", args...) //nolint:gosec
	// Do NOT set Stdin/Stdout/Stderr — the caller pipes them (StdinPipe etc.).
	// cmd.Env is the host incus-CLI env; container env comes via --env above.
	return cmd
}

// imageAlias returns the image alias this container is created from
// (cfg.Image, which the constructor defaults to DefaultImageAlias).
func (r *incusRuntime) imageAlias() string {
	if r.cfg.Image != "" {
		return r.cfg.Image
	}
	return DefaultImageAlias
}

// aliasFingerprint returns the full fingerprint the image alias currently
// resolves to, or "" if the alias is not present on this host. This is the
// "latest" image a fresh container would be created from. (S7364e3)
func (r *incusRuntime) aliasFingerprint(ctx context.Context) (string, error) {
	alias := r.imageAlias()
	stdout, _, code, err := r.run(ctx, "image", "list", alias, "-f", "json")
	if err != nil {
		return "", fmt.Errorf("incus image list %s: %w", alias, err)
	}
	if code != 0 {
		// Non-zero here means the query failed; treat as "alias absent".
		return "", nil
	}
	type aliasEntry struct {
		Name string `json:"name"`
	}
	type imgEntry struct {
		Fingerprint string       `json:"fingerprint"`
		Aliases     []aliasEntry `json:"aliases"`
	}
	var imgs []imgEntry
	if err := json.Unmarshal([]byte(stdout), &imgs); err != nil {
		return "", fmt.Errorf("incus image list parse: %w", err)
	}
	// `incus image list <alias>` filters by alias/fingerprint substring, so it
	// can return unrelated images (e.g. "palmux-ws-staging" or a fingerprint
	// that prefix-matches). Only trust an EXACT alias-name match — otherwise
	// return "" (no update target → not stale), never a wrong fingerprint.
	for _, im := range imgs {
		for _, a := range im.Aliases {
			if a.Name == alias {
				return im.Fingerprint, nil
			}
		}
	}
	return "", nil
}

// containerBaseImage returns the fingerprint of the image this container was
// created from (volatile.base_image), or "" if the container does not exist or
// the key is unset. (S7364e3)
func (r *incusRuntime) containerBaseImage(ctx context.Context) (string, error) {
	stdout, _, code, err := r.run(ctx, "config", "get", r.inst, "volatile.base_image")
	if err != nil {
		return "", fmt.Errorf("incus config get %s volatile.base_image: %w", r.inst, err)
	}
	if code != 0 {
		// Container absent or key unset → no base image to compare.
		return "", nil
	}
	return strings.TrimSpace(stdout), nil
}

// IsImageStale reports whether this container was created from an image older
// than the one the image alias currently resolves to (i.e. the palmux-ws image
// was rebuilt + re-aliased after this container was created). Returns false when
// the alias is absent (no update target) or the container's base image is
// unknown. (S7364e3) [AC-S7364e3-1-1] [AC-S7364e3-1-4]
func (r *incusRuntime) IsImageStale(ctx context.Context) (bool, error) {
	aliasFP, err := r.aliasFingerprint(ctx)
	if err != nil {
		return false, err
	}
	if aliasFP == "" {
		return false, nil // alias not present → nothing to update to
	}
	baseFP, err := r.containerBaseImage(ctx)
	if err != nil {
		return false, err
	}
	if baseFP == "" {
		return false, nil // unknown base → don't claim stale
	}
	return baseFP != aliasFP, nil
}

// probeInstanceName derives a DNS-safe throwaway instance name used to verify
// the image launches before the real container is destroyed during Regenerate.
func (r *incusRuntime) probeInstanceName() string {
	const suffix = "-imgprobe"
	base := r.inst
	if len(base)+len(suffix) > 63 {
		base = base[:63-len(suffix)]
		base = strings.TrimRight(base, "-")
	}
	return base + suffix
}

// verifyImageLaunchable launches a throwaway ephemeral container from the image
// alias and tears it down, confirming the (possibly rebuilt) image actually
// boots before Regenerate destroys the real container. This is the
// transactional gate: if it fails, the caller keeps the old container. (S7364e3)
func (r *incusRuntime) verifyImageLaunchable(ctx context.Context) error {
	alias := r.imageAlias()
	probe := r.probeInstanceName()
	// Best-effort pre-clean in case a prior probe leaked.
	_, _, _, _ = r.run(ctx, "delete", "--force", probe)
	_, stderr, code, err := r.run(ctx, "launch", alias, probe, "--ephemeral")
	// Always tear the probe down (ephemeral auto-deletes on stop, but force-delete
	// guards against a half-started instance lingering).
	defer func() { _, _, _, _ = r.run(ctx, "delete", "--force", probe) }()
	if err != nil {
		return fmt.Errorf("probe launch %s: %w", alias, err)
	}
	if code != 0 {
		return fmt.Errorf("probe launch %s failed: %s", alias, strings.TrimSpace(stderr))
	}
	return nil
}

// Regenerate recreates this workspace's container from the current image alias
// (used after the palmux-ws image is rebuilt + re-aliased). It is transactional
// against the realistic failure mode: it first verifies the new image launches
// in a throwaway probe and only then destroys + recreates the real container.
// If the probe fails (image missing/broken/host misconfigured), the old
// container is left untouched and an error is returned. The container rootfs is
// disposable — all real state (~/ghq, ~/.claude, dotfiles) is bind-mounted, so a
// fresh container re-acquires it; the caller recreates the tmux session
// (claude --resume). (S7364e3) [AC-S7364e3-1-2]
func (r *incusRuntime) Regenerate(ctx context.Context) error {
	// 1. Transactional gate: prove the new image launches before destroying old.
	if err := r.verifyImageLaunchable(ctx); err != nil {
		return fmt.Errorf("regenerate %s: new image not launchable, kept existing container: %w", r.inst, err)
	}
	// 2. Destroy the old container (also clears its Caddy routes / port state).
	if err := r.Stop(ctx); err != nil {
		return fmt.Errorf("regenerate %s: stop old: %w", r.inst, err)
	}
	// Stop sets status=stopped, so Start below re-provisions instead of
	// short-circuiting on the ready-idempotency guard.
	// 3. Recreate on the current alias (fresh rootfs from the new image).
	if err := r.Start(ctx); err != nil {
		return fmt.Errorf("regenerate %s: recreate: %w", r.inst, err)
	}
	return nil
}

// Stop deletes the container with --force (idempotent), and cleans up all
// Caddy snippets for this workspace.
// [AC-S8478ca-2-1] [AC-S8478ca-2-4] [AC-S8478ca-4-2]
func (r *incusRuntime) Stop(ctx context.Context) error {
	_, _, _, err := r.run(ctx, "delete", "--force", r.inst)
	// non-zero exit from delete --force when instance does not exist is acceptable
	r.setStatus(runtime.Status{State: runtime.StateStopped})
	if err != nil {
		r.log.Warn("incus delete --force returned OS error", "inst", r.inst, "err", err)
	}
	// Remove all Caddy snippets for this workspace (AC-S8478ca-4-2).
	clearSnippets(ctx, r.inst, r.caddyRun, r.log)
	// Remove all published admin-API routes for this workspace (See8bd4-2).
	r.unpublishAll(ctx)
	// Remove all host-port proxy devices for this workspace (S4c591a). The
	// container delete above already drops its devices; this also clears the
	// in-memory hostExposed state and is safe if Stop is reached without a
	// successful delete.
	r.unpublishAllHost(ctx)
	// S7364e3: the container is gone, so all port-rescue state is invalid. Reset
	// it so a re-created container (Regenerate reuses this same runtime object)
	// re-runs ExposePort for localhost-only ports instead of seeing stale
	// activeMappings entries and skipping the relay (which would leave dev
	// servers unreachable). unpublishAll already cleared r.exposed.
	r.activeMappingsMu.Lock()
	r.activeMappings = map[string]string{}
	r.activeMappingsMu.Unlock()
	r.portsMu.Lock()
	r.lastPorts = nil
	// hostExposed was already cleared by unpublishAllHost above. (S4c591a)
	r.portsMu.Unlock()
	return nil
}

// PortScanInterval is the cadence at which the port scan loop polls listening
// ports inside the container.
const PortScanInterval = 10 * time.Second

// PortsDetectedEvent is the payload broadcast on the WS event
// "branch.portsDetected".  It carries the full current list of listening ports
// so the FE can diff without needing to track previous state.
type PortsDetectedEvent struct {
	Inst  string                  `json:"inst"`
	Ports []runtime.ListeningPort `json:"ports"`
}

// PortScanCallback is called each time the scan loop detects a change in the
// set of listening ports.  The caller (store sync loop) broadcasts a WS event.
// If callback is nil the loop still runs but is silent.
type PortScanCallback func(inst string, ports []runtime.ListeningPort)

// isLocalhostBind returns true for addresses that indicate a server is only
// listening on the container's loopback interface.  These servers need the
// bind=instance proxy device rescue (AC-S8478ca-4-3).  Servers already bound
// to 0.0.0.0 or :: are already reachable on containerIP and do NOT need a
// proxy device (adding one would fail with "address already in use").
func isLocalhostBind(addr string) bool {
	return addr == "127.0.0.1" || addr == "::1" || addr == "localhost"
}

// isLoopbackBind is a broader loopback check than isLocalhostBind: it also covers
// the whole 127.0.0.0/8 block (e.g. systemd-resolved's 127.0.0.53/54 stubs) and
// zone-suffixed forms (::1%lo, [::1]). Used when aggregating the Ports view so a
// port that only ever binds loopback is correctly flagged localhost-only.
func isLoopbackBind(addr string) bool {
	a := strings.Trim(addr, "[]")
	if i := strings.IndexByte(a, '%'); i >= 0 {
		a = a[:i] // strip zone id like "%lo"
	}
	return a == "localhost" || a == "::1" || strings.HasPrefix(a, "127.")
}

// isGlobalBind returns true for addresses that already allow connections from
// outside the container (0.0.0.0, *, ::, [::]).
func isGlobalBind(addr string) bool {
	switch addr {
	case "0.0.0.0", "*", "::", "":
		return true
	}
	return false
}

// ScanPortsOnce runs one port scan and, for each TCP port that needs
// intervention, takes the appropriate action:
//
//   - Port bound to 127.0.0.1 (localhost-only): adds a bind=instance proxy
//     device so the port becomes reachable on containerIP.  (AC-S8478ca-4-3)
//   - Port bound to 0.0.0.0/* (global): already reachable on containerIP —
//     no proxy device needed, only Caddy snippet for routing.  (AC-S8478ca-4-2)
//
// System ports (< 1024) that are not user dev-server candidates (DNS :53,
// SSH :22, etc.) are skipped to avoid conflicting with well-known services.
//
// It is idempotent: already-exposed ports are skipped.
//
// Returns the current list of listening ports (may be empty).
//
// [AC-S8478ca-4-1] [AC-S8478ca-4-2] [AC-S8478ca-4-3]
func (r *incusRuntime) ScanPortsOnce(ctx context.Context) ([]runtime.ListeningPort, error) {
	if r.Status().State != runtime.StateReady {
		return nil, nil
	}
	ports, err := r.ListListeningPorts(ctx)
	if err != nil {
		return nil, fmt.Errorf("incus ScanPortsOnce: %w", err)
	}
	// Always record the latest list for the Ports tab view (See8bd4-3).
	r.recordPorts(ports)

	// If the container IP wasn't resolved at Start() time (DHCP race / regenerate),
	// re-resolve it now and update the cached status. MUST run before the
	// public-mode early return below, otherwise the cached Address stays empty in
	// public mode and the Browser tab can't reach the container's VNC/CDP.
	addr := r.Status().Address
	if addr == "" {
		if ip, ipErr := r.containerIP(ctx); ipErr == nil && ip != "" {
			addr = ip
			r.setStatus(runtime.Status{State: runtime.StateReady, Address: addr})
			r.log.Info("incus ScanPortsOnce: resolved container IP (deferred)",
				"inst", r.inst, "addr", addr)
		}
	}

	// Publish mode (--public-domain set): exposure is user-controlled via the
	// Ports tab (ExposePortPublic), so the scan does NOT auto-create Caddy
	// routes or relays here — it only records what is listening. It DOES
	// re-inject the already-exposed routes so they self-heal after a Caddy
	// reload (admin-API routes aren't persisted to the Caddyfile). (See8bd4-2)
	if r.pub.enabled() {
		r.resyncExposedRoutes(ctx)
		return ports, nil
	}

	for _, p := range ports {
		// Skip well-known system ports to avoid collisions with DNS (:53),
		// SSH (:22), systemd-resolved, etc.  Dev servers almost never use <1024.
		if p.Port < 1024 {
			continue
		}

		id := fmt.Sprintf("incus-%s-%s-%d", r.inst, strings.ToLower(p.Proto), p.Port)
		// Check if already tracked.
		r.activeMappingsMu.RLock()
		_, alreadyMapped := r.activeMappings[id]
		r.activeMappingsMu.RUnlock()
		if alreadyMapped {
			// Port already tracked — still write Caddy snippet if needed.
			if addr != "" && p.Proto == "tcp" {
				_, _ = writeSnippet(ctx, r.inst, addr, p.Port, r.caddyRun, r.log)
			}
			continue
		}

		if isLocalhostBind(p.BindAddr) {
			// In-container Python relay rescue (AC-S8478ca-4-3).
			// ExposePort starts a Python 3 relay inside the container to forward
			// <containerIP>:<port> → 127.0.0.1:<port>.
			_, exposeErr := r.ExposePort(ctx, runtime.PortSpec{
				Internal: p.Port,
				Proto:    p.Proto,
				Name:     fmt.Sprintf("auto-%d", p.Port),
				Public:   false,
				HostPort: 0,
			})
			if exposeErr != nil {
				r.log.Warn("incus ScanPortsOnce: ExposePort (localhost rescue) failed (non-fatal)",
					"inst", r.inst, "port", p.Port, "bind", p.BindAddr, "err", exposeErr)
			} else if addr != "" {
				r.log.Info("incus: auto-exposed localhost port via relay",
					"inst", r.inst, "port", p.Port, "containerIP", addr)
			}
		} else if isGlobalBind(p.BindAddr) {
			// Port is already reachable on containerIP — just write Caddy snippet.
			// Track it as mapped so we don't re-process it.
			r.activeMappingsMu.Lock()
			r.activeMappings[id] = "" // sentinel: exposed via Caddy, no device
			r.activeMappingsMu.Unlock()
			if addr != "" && p.Proto == "tcp" {
				if _, caddyErr := writeSnippet(ctx, r.inst, addr, p.Port, r.caddyRun, r.log); caddyErr != nil {
					r.log.Warn("incus ScanPortsOnce: Caddy snippet failed (non-fatal)",
						"inst", r.inst, "port", p.Port, "err", caddyErr)
				} else {
					r.log.Info("incus: auto-exposed port (global bind, Caddy snippet)",
						"inst", r.inst, "port", p.Port, "containerIP", addr)
				}
			}
		}
		// Other bind addresses (e.g. specific container IP) — skip.
	}
	return ports, nil
}

// RunPortScanLoop is a blocking loop that scans listening ports every
// PortScanInterval until ctx is Done.  On each scan it calls cb with the
// current port list so the store can broadcast a WS event.
//
// This is started as a goroutine by the store's Run() method after the
// container is ready.
//
// [AC-S8478ca-4-1]
func (r *incusRuntime) RunPortScanLoop(ctx context.Context, cb PortScanCallback) {
	ticker := time.NewTicker(PortScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ports, err := r.ScanPortsOnce(ctx)
			if err != nil {
				r.log.Warn("incus RunPortScanLoop: scan error", "inst", r.inst, "err", err)
				continue
			}
			if cb != nil {
				cb(r.inst, ports)
			}
		}
	}
}

// Workspace user identity inside the container. `incus exec` defaults to
// root/uid 0, but every workspace process must run as the bind-mounted host
// user (ubuntu, uid 1000) so it sees the shared HOME at /home/ubuntu: the rich
// ~/.bashrc (starship et al.), ~/.claude auth, ~/.config/gh and ~/.gitconfig
// all live there. Running as root yields /root — none of those — i.e. a plain
// shell with no starship prompt, an unauthenticated claude, and missing gh/git
// credentials. The idmap maps host 1000 → container 1000, so uid 1000 also
// owns every bind-mounted file.
const (
	wsUID  = "1000"
	wsGID  = "1000"
	wsHome = "/home/ubuntu"
	wsUser = "ubuntu"
)

// userExecFlags are the `incus exec` flags that switch the executed command to
// the workspace user. They go immediately after the instance name and before
// the `--` separator, matching incus's `exec <inst> [flags] -- <cmd>` grammar.
// All in-container exec paths (Exec, the tmux client, attach, the port relay)
// must carry these so the single in-container tmux SERVER is owned by uid 1000
// and every later op reaches the same /tmp/tmux-1000 socket.
func userExecFlags() []string {
	f := []string{
		"--user", wsUID,
		"--group", wsGID,
		"--env", "HOME=" + wsHome,
		"--env", "USER=" + wsUser,
	}
	return append(f, gitCredentialHelperEnv()...)
}

// gitCredentialHelperEnv repairs git HTTPS auth inside the container. The host's
// ~/.gitconfig is bind-mounted in, but on a Nix/home-manager host `gh auth
// setup-git` writes a github-SCOPED credential.helper that is an ABSOLUTE Nix path
// (e.g. ~/.nix-profile/bin/.gh-wrapped — gh wrapped by wrapProgram). The container
// has no /nix or ~/.nix-profile, so that helper resolves to "not found" and every
// plain `git` HTTPS pull/push fails. We must NOT edit the shared ~/.gitconfig (it
// is the host's live file, correct on the host). Instead inject high-precedence
// GIT_CONFIG_* env (applied after all config files) that RESETS the github/gist-
// scoped helper and re-points it at a PATH-RELATIVE `!gh auth git-credential`,
// which resolves to the container's own gh (/usr/bin/gh, authed via the bind-
// mounted ~/.config/gh). The reset MUST be url-scoped — an unscoped reset does not
// clear a url-scoped helper (verified). Identity/aliases still come from
// ~/.gitconfig; the host is untouched; non-github remotes are unaffected (the gh
// helper no-ops for them).
func gitCredentialHelperEnv() []string {
	hosts := []string{"https://github.com", "https://gist.github.com"}
	env := []string{"--env", fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(hosts)*2)}
	i := 0
	for _, h := range hosts {
		key := "credential." + h + ".helper"
		// reset (empty value clears the accumulated helper list for this url)…
		env = append(env, "--env", fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, key), "--env", fmt.Sprintf("GIT_CONFIG_VALUE_%d=", i))
		i++
		// …then the PATH-relative gh helper.
		env = append(env, "--env", fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, key), "--env", fmt.Sprintf("GIT_CONFIG_VALUE_%d=!gh auth git-credential", i))
		i++
	}
	return env
}

// Exec runs a command inside the container and captures output.
// [AC-S8478ca-2-3]
func (r *incusRuntime) Exec(ctx context.Context, cmd []string, opts runtime.ExecOpts) (runtime.ExecResult, error) {
	if len(cmd) == 0 {
		return runtime.ExecResult{}, fmt.Errorf("incus Exec: cmd must not be empty")
	}
	// Build: incus exec <inst> --user 1000 ... [--cwd <dir>] [--env K=V ...] -- <cmd>
	args := []string{"exec", r.inst}
	args = append(args, userExecFlags()...)
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
// [AC-S8478ca-4-1]
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
		// Extract bind address (everything before the last colon).
		bindAddr := strings.TrimSpace(local[:lastColon])
		// Strip IPv6 brackets if present.
		bindAddr = strings.Trim(bindAddr, "[]")
		if bindAddr == "" {
			bindAddr = "*"
		}
		ports = append(ports, runtime.ListeningPort{
			Port:     p,
			Proto:    "tcp",
			BindAddr: bindAddr,
		})
	}
	return ports, nil
}

// localhostRelayScript is the Python 3 relay script injected into the container
// via `incus exec -- python3 -c ...` to make a 127.0.0.1-only service reachable
// on the container's bridge IP.
//
// Design rationale:
// The Incus forkproxy process that backs proxy devices always connects to the
// "connect=" address from the HOST network namespace.  This means
// `connect=tcp:127.0.0.1:<port>` hits the HOST's loopback, not the container's,
// so `bind=instance` proxy devices cannot forward traffic to in-container
// localhost-only services.
//
// Verified empirically: strace on the forkproxy pid shows
//
//	connect(fd, {sin_addr=127.0.0.1, port=<N>}) = ECONNREFUSED
//
// because the forkproxy pid is in the HOST net namespace (net:[4026531840]),
// not the container's namespace (net:[4026532XXX]).
//
// The workaround: start a Python 3 relay INSIDE the container via `incus exec`.
// Python is always available in the palmux-ws image and the relay process runs
// in the container's own network namespace, so 127.0.0.1:<port> IS reachable.
//
// The relay script: listens on listenIP:port, forks one goroutine-pair per
// connection, forwards bytes bidirectionally between the client and the backend.
// stdout receives the relay PID so palmux can kill it via `incus exec -- kill <pid>`.
//
// [AC-S8478ca-4-3]
// localhostRelayScript is the Python 3 relay script used to make a
// 127.0.0.1-only service reachable on the container's bridge IP.
//
// The script is delivered to the container via base64 encoding to avoid all
// shell-quoting issues.  It reads the listen address and port from sys.argv[1]
// and sys.argv[2], listens on that address, and forwards each connection to
// 127.0.0.1:<port> (the in-container loopback where the user's dev server runs).
//
// The script prints os.getpid() to stdout before entering the accept loop so
// the caller can capture the PID and kill the relay later.
//
// [AC-S8478ca-4-3]
const localhostRelayScript = `
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

// ExposePort makes an internal container port reachable from outside the
// container, using one of two mechanisms depending on PortSpec:
//
//  1. In-container Python relay (AC-S8478ca-4-3): starts a Python 3 relay
//     process INSIDE the container that listens on <containerIP>:<port> and
//     forwards to 127.0.0.1:<port>.  No host-side port is consumed.
//     The relay PID is tracked so UnexposePort can kill it.
//     Command: incus exec <inst> -- sh -c
//     'nohup python3 -c "<relay>" <containerIP> <port> >/dev/null 2>&1 & echo $!'
//
//     NOTE: Incus proxy devices with bind=instance CANNOT be used here.
//     The Incus forkproxy process always connects to the "connect=" address
//     from the HOST network namespace, so connect=tcp:127.0.0.1:<port> hits
//     the HOST's loopback, not the container's.  Verified by strace on the
//     forkproxy pid: connect() returns ECONNREFUSED because the forkproxy is
//     in net namespace net:[4026531840] (host) not the container's namespace.
//
//  2. Caddy snippet (AC-S8478ca-4-2): writes /etc/caddy/conf.d/<inst>-<port>.caddy
//     routing <inst>-<port>.palmux.local → <containerIP>:<port> and reloads
//     Caddy.  Gracefully degrades if caddy binary is not on PATH.
//
// For UDP / HostPort>0 the Incus proxy device is added with the given proto/host
// port (§5.5 of the workspace-runtime design, Neko/WebRTC future path).
// Those devices connect FROM the HOST network namespace, which is fine for
// host-side forwarding because the backend is the container's bridge IP, not
// 127.0.0.1.
//
// [AC-S8478ca-4-2] [AC-S8478ca-4-3]
func (r *incusRuntime) ExposePort(ctx context.Context, spec runtime.PortSpec) (runtime.PortMapping, error) {
	proto := strings.ToLower(spec.Proto)
	if proto == "" {
		proto = "tcp"
	}

	id := fmt.Sprintf("incus-%s-%s-%d", r.inst, proto, spec.Internal)

	addr := r.Status().Address
	if addr == "" {
		// Try to resolve the IP if not set yet.
		if ip, err := r.containerIP(ctx); err == nil && ip != "" {
			addr = ip
		} else {
			addr = "pending"
		}
	}

	if spec.HostPort > 0 {
		// Host-side forwarding (future Neko/UDP path §5.5).
		// Use a standard Incus proxy device with no bind=instance so the proxy
		// runs in the HOST network namespace and forwards to containerIP:port.
		// The backend address must be the container's bridge IP (already reachable
		// from host), NOT 127.0.0.1 (which is the host's loopback).
		devName := fmt.Sprintf("p%c%d", proto[0], spec.Internal)
		listenAddr := fmt.Sprintf("%s:0.0.0.0:%d", proto, spec.HostPort)
		backendAddr := addr
		if backendAddr == "" || backendAddr == "pending" {
			backendAddr = "127.0.0.1" // fallback (best-effort)
		}
		connectAddr := fmt.Sprintf("%s:%s:%d", proto, backendAddr, spec.Internal)

		_, stderr, code, err := r.run(ctx, "config", "device", "add", r.inst,
			devName, "proxy",
			"listen="+listenAddr,
			"connect="+connectAddr,
		)
		if err != nil {
			return runtime.PortMapping{}, fmt.Errorf("incus ExposePort device add: %w", err)
		}
		if code != 0 && !strings.Contains(stderr, "already exists") {
			return runtime.PortMapping{}, fmt.Errorf("incus ExposePort device add: exit %d: %s", code, stderr)
		}
		r.activeMappingsMu.Lock()
		r.activeMappings[id] = devName
		r.activeMappingsMu.Unlock()
		m := runtime.PortMapping{
			ID:       id,
			Internal: spec.Internal,
			HostPort: spec.HostPort,
			Proto:    proto,
			Address:  addr,
			Public:   spec.Public,
		}
		r.log.Info("incus: port exposed (host-side device)", "inst", r.inst, "port", spec.Internal, "hostPort", spec.HostPort)
		return m, nil
	}

	// HostPort == 0 → localhost-rescue relay path (AC-S8478ca-4-3).
	//
	// Idempotency: if a relay/mapping for this id already exists (e.g. the scan
	// loop started it, or ExposePortPublic is toggled twice), do not spawn a
	// second relay — return the existing mapping. (See8bd4-2)
	r.activeMappingsMu.RLock()
	_, alreadyMapped := r.activeMappings[id]
	r.activeMappingsMu.RUnlock()
	if alreadyMapped {
		return runtime.PortMapping{ID: id, Internal: spec.Internal, Proto: proto, Address: addr, Public: spec.Public}, nil
	}
	//
	// Start a Python 3 relay INSIDE the container that listens on
	// <containerIP>:<port> and forwards to 127.0.0.1:<port>.
	// The relay runs in the container's network namespace, so 127.0.0.1 is
	// the CONTAINER's loopback — the place where the user's dev server is bound.
	//
	// Delivery: base64-encode the script and decode it inside the container
	// via `echo '<b64>' | base64 -d | python3 - LISTENIP PORT & echo $!`.
	// This avoids all shell-quoting issues (no single/double-quote conflicts).
	// `base64` is present in Ubuntu 24.04 (coreutils).
	listenIP := addr
	if listenIP == "" || listenIP == "pending" {
		listenIP = "0.0.0.0" // best-effort fallback
	}

	b64Script := base64.StdEncoding.EncodeToString([]byte(localhostRelayScript))
	// sh command: decode + run in background, print PID.
	// The relay script prints its own PID before the accept loop,
	// and `echo $!` also prints the background job PID — both are the same.
	// We pick the first non-empty line from stdout.
	shCmd := fmt.Sprintf(
		"echo '%s' | base64 -d | nohup python3 - %s %d >/dev/null 2>&1 & echo $!",
		b64Script, listenIP, spec.Internal,
	)
	relayArgs := append([]string{"exec", r.inst}, userExecFlags()...)
	relayArgs = append(relayArgs, "--", "sh", "-c", shCmd)
	stdout, stderr, code, err := r.run(ctx, relayArgs...)
	if err != nil {
		return runtime.PortMapping{}, fmt.Errorf("incus ExposePort relay start: %w", err)
	}
	if code != 0 {
		return runtime.PortMapping{}, fmt.Errorf("incus ExposePort relay start: exit %d: %s", code, stderr)
	}

	// Parse the relay PID from stdout.
	// stdout may contain multiple lines: the shell's `echo $!` PID and the
	// python script's own `print(os.getpid())` — both are the same value.
	// Take the first non-empty line to be safe.
	relayPID := ""
	for _, line := range strings.Split(stdout, "\n") {
		if pid := strings.TrimSpace(line); pid != "" {
			relayPID = pid
			break
		}
	}
	// Track the relay PID so UnexposePort can send SIGTERM.
	// We store "relay:<pid>" to distinguish from device names (which start with "p").
	r.activeMappingsMu.Lock()
	r.activeMappings[id] = "relay:" + relayPID
	r.activeMappingsMu.Unlock()

	// Write a Caddy snippet for TCP ports (HTTP routing).  UDP/Neko goes
	// through the HostPort path and does not use Caddy.
	// In publish mode (--public-domain) the public route is created explicitly
	// by ExposePortPublic via the admin API, so skip the legacy snippet here.
	if proto == "tcp" && !r.pub.enabled() {
		if _, caddyErr := writeSnippet(ctx, r.inst, addr, spec.Internal, r.caddyRun, r.log); caddyErr != nil {
			// Caddy failure is non-fatal: the relay is already running.
			r.log.Warn("incus ExposePort: caddy snippet failed (non-fatal)",
				"inst", r.inst, "port", spec.Internal, "err", caddyErr)
		}
	}

	m := runtime.PortMapping{
		ID:       id,
		Internal: spec.Internal,
		HostPort: 0,
		Proto:    proto,
		Address:  addr,
		Public:   spec.Public,
	}
	r.log.Info("incus: port exposed (localhost relay)", "inst", r.inst, "port", spec.Internal, "proto", proto, "relayPID", relayPID)
	return m, nil
}

// UnexposePort removes a port mapping created by ExposePort.  It stops the
// in-container relay process (if any) or removes the Incus proxy device, and
// deletes the Caddy snippet.
//
// tracking value formats in activeMappings:
//
//	"" (empty)        — global-bind port, Caddy-only, no relay/device to remove
//	"relay:<pid>"     — localhost relay path (AC-S8478ca-4-3): kill relay in container
//	"p<c><port>"      — Incus proxy device name (HostPort>0 path)
//
// [AC-S8478ca-4-2] [AC-S8478ca-4-3]
func (r *incusRuntime) UnexposePort(ctx context.Context, mappingID string) error {
	r.activeMappingsMu.RLock()
	tracking, ok := r.activeMappings[mappingID]
	r.activeMappingsMu.RUnlock()
	if !ok {
		// Already removed or unknown — not an error.
		return nil
	}

	switch {
	case tracking == "":
		// Caddy-only (global-bind port) — nothing to kill.
	case strings.HasPrefix(tracking, "relay:"):
		// In-container Python relay — kill it by PID inside the container.
		pid := strings.TrimPrefix(tracking, "relay:")
		if pid != "" {
			_, _, _, err := r.run(ctx, "exec", r.inst, "--", "kill", pid)
			if err != nil {
				r.log.Warn("incus UnexposePort: relay kill error (non-fatal)",
					"id", mappingID, "pid", pid, "err", err)
			}
		}
	default:
		// Incus proxy device (HostPort>0 path) — remove it.
		_, _, _, err := r.run(ctx, "config", "device", "remove", r.inst, tracking)
		if err != nil {
			r.log.Warn("incus UnexposePort: device remove error (non-fatal)",
				"id", mappingID, "dev", tracking, "err", err)
		}
	}

	r.activeMappingsMu.Lock()
	delete(r.activeMappings, mappingID)
	r.activeMappingsMu.Unlock()

	// Parse port from ID to remove the Caddy snippet.
	// ID format: incus-<inst>-<proto>-<port>
	parts := strings.Split(mappingID, "-")
	if len(parts) >= 1 {
		portStr := parts[len(parts)-1]
		var port int
		if _, scanErr := fmt.Sscanf(portStr, "%d", &port); scanErr == nil && port > 0 {
			removeSnippet(ctx, r.inst, port, r.caddyRun, r.log)
		}
	}
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
	attachArgs := append([]string{"exec", "-t", r.inst}, userExecFlags()...)
	attachArgs = append(attachArgs, "--", "tmux", "attach-session", "-t", session)
	cmd := exec.CommandContext(ctx, "incus", attachArgs...)
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
	argv := append([]string{"exec", c.inst}, userExecFlags()...)
	argv = append(argv, "--", "tmux")
	argv = append(argv, args...)
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
	if err != nil {
		if strings.Contains(err.Error(), "duplicate session") {
			// Idempotent: the session already exists (e.g. a concurrent
			// ensureSession from the sync_tmux recovery loop created it first
			// during a runtime switch). The goal — a session with this name — is
			// met; still (re)apply the per-session options.
			c.applyPalmuxSessionOptions(ctx, opts.Name)
			return nil
		}
		return err
	}
	c.applyPalmuxSessionOptions(ctx, opts.Name)
	return nil
}

// applyPalmuxSessionOptions sets Palmux's per-session tmux defaults INSIDE the
// container — mirror of the host execClient's helper, so the container bash tab
// gets the same UX: mouse on (wheel scroll / click-select / pane resize), status
// line (footer bar) off, set-clipboard on (mouse selection's OSC52 reaches the
// browser clipboard). Routed through c.incus so they run as `incus exec <inst> --
// tmux set-option …` against the container's tmux server. Best-effort: a failed
// option must never abort session creation.
func (c *incusTmuxClient) applyPalmuxSessionOptions(ctx context.Context, session string) {
	for _, opt := range [][2]string{
		{"mouse", "on"},
		{"status", "off"},
		{"set-clipboard", "on"},
	} {
		_, _ = c.incus(ctx, "set-option", "-t", session, opt[0], opt[1]) //nolint:errcheck // best-effort cosmetics
	}
}

func (c *incusTmuxClient) KillSession(ctx context.Context, name string) error {
	_, err := c.incus(ctx, "kill-session", "-t", name)
	return err
}

func (c *incusTmuxClient) HasSession(ctx context.Context, name string) (bool, error) {
	dctx, cancel := detachedTmuxCtx(ctx)
	defer cancel()
	hsArgs := append([]string{"exec", c.inst}, userExecFlags()...)
	hsArgs = append(hsArgs, "--", "tmux", "has-session", "-t", name)
	_, _, code, err := c.run(dctx, hsArgs...)
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
	attachArgs := append([]string{"exec", "-t", c.inst}, userExecFlags()...)
	attachArgs = append(attachArgs, "--", "tmux", "attach-session", "-t", target)
	cmd := exec.CommandContext(ctx, "incus", attachArgs...)
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
	if _, err := c.incus(ctx, "new-session", "-d", "-t", target, "-s", groupName); err != nil {
		return err
	}
	// The grouped session (the one the browser client attaches to) gets default
	// options, so apply Palmux's per-session defaults here too.
	c.applyPalmuxSessionOptions(ctx, groupName)
	return nil
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
