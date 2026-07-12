package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	palmux2 "github.com/tjst-t/palmux2"
	"github.com/tjst-t/palmux2/internal/apps"
	"github.com/tjst-t/palmux2/internal/attachment"
	"github.com/tjst-t/palmux2/internal/auth"
	"github.com/tjst-t/palmux2/internal/commands"
	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/deploy"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/ghq"
	"github.com/tjst-t/palmux2/internal/gwq"
	"github.com/tjst-t/palmux2/internal/notify"
	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/runtime/incus"
	"github.com/tjst-t/palmux2/internal/selfupdate"
	"github.com/tjst-t/palmux2/internal/server"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tab/bash"
	"github.com/tjst-t/palmux2/internal/tab/browser"
	"github.com/tjst-t/palmux2/internal/tab/claudeagent"
	"github.com/tjst-t/palmux2/internal/tab/claudetui"
	"github.com/tjst-t/palmux2/internal/tab/files"
	gittab "github.com/tjst-t/palmux2/internal/tab/git"
	"github.com/tjst-t/palmux2/internal/tab/ports"
	"github.com/tjst-t/palmux2/internal/tab/sprint"
	"github.com/tjst-t/palmux2/internal/tmux"
	"github.com/tjst-t/palmux2/internal/worktree"
)

// worktreeList is a tiny adapter so the migrate helpers can call into
// the worktree package without each callsite repeating the import-by-
// alias dance.
func worktreeList(ctx context.Context, repoFullPath string) ([]worktree.Worktree, error) {
	return worktree.List(ctx, repoFullPath)
}

// Version is injected at build time via `-ldflags "-X main.Version=..."`
// (see Makefile). When unset — e.g. `go run` from a worktree — we fall back
// to the VCS info embedded by the Go toolchain.
var Version = ""

func resolveVersion() string {
	// Test seam (E2E rig only): PALMUX_FAKE_VERSION lets the self-update
	// reconnect-handshake live test restart the dev binary as a "new version"
	// without rebuilding (S6ab0ed). Never set in production.
	if v := os.Getenv("PALMUX_FAKE_VERSION"); v != "" {
		return v
	}
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, mod string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			mod = s.Value
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if mod == "true" {
		rev += "-dirty"
	}
	return "dev-" + rev
}

func main() {
	// `palmux hook ...` is the Claude Code hook handler invoked per claude-tui
	// subprocess (see internal/tab/claudetui/hooks.go). Dispatch it before any
	// flag parsing or server bootstrap: it must be a fast, side-effect-free
	// process that POSTs one notification and exits.
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		os.Exit(runHook(os.Args[2:]))
	}

	// S3f2658-1: `palmux ptyhost ...` is the thin, claude-agnostic process
	// holder (ADR-0001/0002) that survives palmux2 restarts. Dispatch before
	// server bootstrap — it is launched as its own detached process (see
	// internal/ptyhost/launch.go), never as part of a running palmux2.
	if len(os.Args) > 1 && os.Args[1] == "ptyhost" {
		os.Exit(runPtyHost(os.Args[2:]))
	}

	// `palmux runtime <install|doctor>` manages the palmux-ws Incus image and
	// host prerequisites.  Dispatch before server bootstrap.
	if len(os.Args) > 1 && os.Args[1] == "runtime" {
		os.Exit(runRuntime(os.Args[2:]))
	}

	// Sa53137-3: `palmux apply` re-reads the master config and applies the diff
	// (hot in-process where possible, `systemctl --user restart` for restart
	// fields, "needs privilege" guidance for root/Caddy changes).
	if len(os.Args) > 1 && os.Args[1] == "apply" {
		os.Exit(runApply(os.Args[2:]))
	}

	// Sa53137-4: `sudo palmux reconcile-system` renders /etc/caddy/Caddyfile
	// from a fixed template using the user-owned master and reloads Caddy. The
	// single privileged verb; takes no free-form input.
	if len(os.Args) > 1 && os.Args[1] == "reconcile-system" {
		os.Exit(runReconcileSystem(os.Args[2:]))
	}

	// Sfef725-2: `sudo palmux fix-incus-group` — the single privileged recover
	// verb. Restarts the user systemd manager so a freshly-added incus-admin
	// group is applied to the palmux service. Takes no free-form input.
	if len(os.Args) > 1 && os.Args[1] == "fix-incus-group" {
		os.Exit(runFixIncusGroup(os.Args[2:]))
	}

	// S6ab0ed: `palmux update [--check]` — one-click self-update (shares the
	// GUI "Update all" execution path) / detection-only. Dispatch before server
	// bootstrap.
	if len(os.Args) > 1 && os.Args[1] == "update" {
		os.Exit(runUpdate(os.Args[2:]))
	}

	// Sa53137-2 (PD-3): `palmux2 serve [flags]` runs the server. Accept and
	// strip the `serve` verb so config-driven systemd units can use it, while
	// the bare `palmux2 --addr=...` invocation (the live deploy VM) keeps
	// working — everything that is not a known subcommand falls through here.
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	// Some Linux distros ship a slim mime DB that doesn't know about
	// .webmanifest. Register the canonical type so PWAs install cleanly.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")

	addr := pflag.String("addr", "0.0.0.0:8080", "listen address (host:port)")
	configDir := pflag.String("config-dir", defaultConfigDir(), "config directory (repos.json / settings.json)")
	token := pflag.String("token", "", "auth token. empty = open access")
	basePath := pflag.String("base-path", "/", "URL base path (e.g. /palmux/)")
	maxConns := pflag.Int("max-connections", 0, "per-branch WS connection cap (0 = unlimited)")
	// S009-fix-3: tmux session prefix. Default `_palmux_` matches every
	// existing install. Override (e.g. `--tmux-prefix=_palmux_dev_`) to
	// run a second palmux process side-by-side with the host instance on
	// the same tmux server without the two `sync_tmux` loops fighting
	// over each other's sessions.
	tmuxPrefix := pflag.String("tmux-prefix", domain.DefaultPalmuxSessionPrefix, "tmux session prefix for sessions managed by this palmux process")
	publicDomain := pflag.String("public-domain", "", "public base domain for publishing incus container ports as <port>--<ws>--<repo>.<domain> (empty disables; env PALMUX_PUBLIC_DOMAIN)")
	caddyAdmin := pflag.String("caddy-admin", "http://localhost:2019", "Caddy admin API endpoint used to inject per-port routes")
	claudeBin := pflag.String("claude-bin", "claude", "path to claude binary used by the claude-tui tab")
	claudeArgs := pflag.StringArray("claude-arg", nil, "extra arguments passed to claude-tui on every spawn (repeatable)")
	versionFlag := pflag.BoolP("version", "v", false, "print version and exit")
	pflag.Parse()

	// Print version and exit early — lets users verify which build is
	// installed without starting the server (useful when "Drawer shows
	// the wrong version" is in question).
	if *versionFlag {
		fmt.Println(resolveVersion())
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Sa53137-2: layer the master config under the flags. Resolution order is
	// flag > env > config.toml/secrets.env > default. A flag that the user
	// passed explicitly (pflag .Changed) always wins, so dev invocations and
	// the live deploy VM's explicit --addr/--public-domain are never overridden
	// by a file. The master is read from configDir; missing files are fine.
	rc := resolveServerConfig(*configDir, resolveFlags{
		addr:         flagState{val: addr, changed: pflag.Lookup("addr").Changed},
		token:        flagState{val: token, changed: pflag.Lookup("token").Changed},
		basePath:     flagState{val: basePath, changed: pflag.Lookup("base-path").Changed},
		maxConns:     flagState{val: maxConns, changed: pflag.Lookup("max-connections").Changed},
		tmuxPrefix:   flagState{val: tmuxPrefix, changed: pflag.Lookup("tmux-prefix").Changed},
		publicDomain: flagState{val: publicDomain, changed: pflag.Lookup("public-domain").Changed},
		caddyAdmin:   flagState{val: caddyAdmin, changed: pflag.Lookup("caddy-admin").Changed},
		claudeBin:    flagState{val: claudeBin, changed: pflag.Lookup("claude-bin").Changed},
		claudeArgs:   flagState{val: claudeArgs, changed: pflag.Lookup("claude-arg").Changed},
	})

	// Apply the prefix BEFORE any other code reads from
	// domain.PalmuxSessionPrefix. After this call, every session name
	// generated/parsed by domain.{SessionName,ParseSessionName,…}
	// uses the configured prefix.
	domain.Configure(rc.tmuxPrefix)

	if err := run(rc); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// resolved holds the fully-layered server configuration (flag > env > file >
// default) passed to run().
type resolved struct {
	addr         string
	configDir    string
	token        string
	basePath     string
	maxConns     int
	tmuxPrefix   string
	publicDomain string
	caddyAdmin   string
	claudeBin    string
	claudeArgs   []string
	// secrets resolved from env (back-compat) or secrets.env.
	ssoSecret     string
	basicAuthHash string
	basicAuthUser string
	// Sd44947: [workspace] shared_dirs from config.toml (host paths, may contain ~).
	sharedDirs []string
}

type flagState struct {
	val     any
	changed bool
}

type resolveFlags struct {
	addr, token, basePath, maxConns, tmuxPrefix, publicDomain, caddyAdmin, claudeBin, claudeArgs flagState
}

// resolveServerConfig layers master config.toml + secrets.env under the parsed
// flags. For each parameter: if the flag was explicitly set, use it; otherwise
// fall back to env, then the file value, then the flag's default. Secrets
// resolve env-first (so a running systemd EnvironmentFile=runtime.env keeps
// working before migration) then secrets.env.
func resolveServerConfig(configDir string, f resolveFlags) resolved {
	mc, sec, err := config.LoadServerConfig(configDir)
	if err != nil {
		// A malformed config.toml is fatal — do not silently fall back to
		// defaults and surprise the operator.
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	pick := func(fs flagState, env, file, def string) string {
		if fs.changed {
			return *fs.val.(*string)
		}
		if env != "" {
			return env
		}
		if file != "" {
			return file
		}
		return def
	}
	pickInt := func(fs flagState, file, def int) int {
		if fs.changed {
			return *fs.val.(*int)
		}
		if file != 0 {
			return file
		}
		return def
	}

	r := resolved{configDir: configDir}
	r.addr = pick(f.addr, "", mc.Server.Addr, *f.addr.val.(*string))
	r.token = pick(f.token, sec.Token, "", *f.token.val.(*string))
	r.basePath = pick(f.basePath, "", mc.Server.BasePath, *f.basePath.val.(*string))
	r.maxConns = pickInt(f.maxConns, mc.Server.MaxConnections, *f.maxConns.val.(*int))
	r.tmuxPrefix = pick(f.tmuxPrefix, "", mc.Server.TmuxPrefix, *f.tmuxPrefix.val.(*string))
	r.publicDomain = pick(f.publicDomain, os.Getenv("PALMUX_PUBLIC_DOMAIN"), mc.Public.Domain, *f.publicDomain.val.(*string))
	r.caddyAdmin = pick(f.caddyAdmin, "", mc.Server.CaddyAdmin, *f.caddyAdmin.val.(*string))
	r.claudeBin = pick(f.claudeBin, "", mc.Server.ClaudeBin, *f.claudeBin.val.(*string))

	if f.claudeArgs.changed {
		r.claudeArgs = *f.claudeArgs.val.(*[]string)
	} else {
		r.claudeArgs = mc.Server.ClaudeArgs
	}

	// Secrets: env wins over file for back-compat with a live systemd
	// EnvironmentFile=runtime.env; the file is the post-migration source.
	r.ssoSecret = firstNonEmpty(os.Getenv("PALMUX_SSO_SECRET"), sec.SSOSecret)
	r.basicAuthHash = firstNonEmpty(os.Getenv("BASIC_AUTH_HASH"), sec.BasicAuthHash)
	r.basicAuthUser = firstNonEmpty(os.Getenv("BASIC_AUTH_USER"), mc.Public.BasicAuthUser)
	r.sharedDirs = mc.Workspace.SharedDirs
	return r
}

func run(rc resolved) error {
	addr := rc.addr
	configDir := rc.configDir
	token := rc.token
	basePath := rc.basePath
	maxConns := rc.maxConns
	claudeBin := rc.claudeBin
	claudeArgs := rc.claudeArgs
	publicDomain := rc.publicDomain
	caddyAdmin := rc.caddyAdmin
	if claudeBin == "" {
		claudeBin = "claude"
	}

	// Log the version up front so when a user sees "phase-X" or "dev"
	// in the Drawer they can confirm which build is actually running
	// without having to call /api/health.
	slog.Info("palmux2 starting", "version", resolveVersion(), "addr", addr)

	if err := requireBins("tmux", "ghq", "gwq", "git"); err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("mkdir config dir %s: %w", configDir, err)
	}

	// Sa53137-2-3 (PD-5): one-time migration of secrets from the root-owned
	// legacy /etc/palmux/runtime.env into the user-owned secrets.env (0600).
	// Idempotent: skipped once secrets.env exists. Best-effort — a failure is
	// logged, never fatal (env-var fallback still feeds the values).
	if migrated, merr := config.MigrateLegacySecrets(configDir); merr != nil {
		slog.Warn("secrets migration failed", "err", merr)
	} else if migrated {
		slog.Info("migrated secrets to user-owned secrets.env", "from", config.LegacyRuntimeEnvPath)
	}

	repoStore, err := config.NewRepoStore(configDir)
	if err != nil {
		return err
	}
	settingsStore, err := config.NewSettingsStore(configDir)
	if err != nil {
		return err
	}
	// S008-1-10: TTL cleanup of the attachment upload dir at startup.
	// Files older than `attachmentTtlDays` are removed and empty
	// per-branch / per-repo dirs collapse. We log the result so the
	// behaviour is visible in the standard log stream — the user
	// shouldn't have to wonder why a 31-day-old file disappeared.
	{
		s := settingsStore.Get()
		root := strings.TrimRight(s.AttachmentUploadDir, "/")
		ttlDays := s.AttachmentTtlDays
		if ttlDays > 0 && root != "" {
			ttl := time.Duration(ttlDays) * 24 * time.Hour
			files, dirs, err := attachment.CleanupOlderThan(root, ttl, slog.Default())
			if err != nil {
				slog.Warn("attachment cleanup failed", "root", root, "err", err)
			} else if files > 0 || dirs > 0 {
				slog.Info("attachment cleanup", "root", root, "files", files, "dirs", dirs, "ttlDays", ttlDays)
			}
		}
	}

	agentStore, err := claudeagent.NewStore(configDir)
	if err != nil {
		return err
	}
	agentOffsetStore, err := claudeagent.NewOffsetStore(configDir)
	if err != nil {
		return err
	}

	// Hook wiring: the palmux binary doubles as the Claude Code hook handler
	// (`palmux hook`) AND (S3f2658-2/S862203-3) the `palmux ptyhost` process
	// re-invoked to hold a claude-tui / claude-agent subprocess so it
	// survives a palmux2 restart (ADR-0001/0002). Resolve its absolute path
	// once, up front, so both Managers agree on the same binary.
	hookBinPath, err := os.Executable()
	if err != nil || hookBinPath == "" {
		hookBinPath = os.Args[0]
	}

	tmuxClient := tmux.NewExecClient()
	ghqClient := ghq.New()
	gwqClient := gwq.New()

	authn, err := auth.New(token)
	if err != nil {
		return err
	}

	// S8478ca-2: replace the host-only DefaultRegistry with the incus-aware
	// Registry.  For workspaces configured as "host" (the default) it returns
	// a host Runtime backed by tmuxClient — behaviour is byte-identical.  For
	// workspaces configured as "incus-container" it constructs and caches an
	// incusRuntime that routes all tmux calls through `incus exec`.
	runtimeRegistry := incus.NewRegistry(repoStore, settingsStore, tmuxClient, slog.Default())

	// Sd44947: seed the host-wide palmux-shared profile with the config-driven
	// [workspace] shared_dirs (expanded to absolute $HOME-scoped paths). The
	// profile is reconciled lazily when the first incus container launches and on
	// every scan tick thereafter.
	if home, herr := os.UserHomeDir(); herr == nil {
		if abs, aerr := config.ExpandSharedDirs(rc.sharedDirs, home); aerr == nil {
			runtimeRegistry.SharedProfileManager().SetSharedDirs(abs)
		} else {
			slog.Warn("ignoring invalid [workspace] shared_dirs", "err", aerr)
		}
	}

	// Share the attachment upload root into every incus container at the same host
	// path so Ctrl+V-pasted images (saved on the host) are readable by in-container
	// Claude at the exact absolute path the composer/terminal injects. MkdirAll so
	// the mount source exists before the first container launch (declaredDevices
	// skips an absent source; the 10s reconcile would otherwise add it only after
	// the first upload creates the dir).
	attachmentRoot := resolveAttachmentRoot(settingsStore.Get())
	if attachmentRoot != "" {
		if err := os.MkdirAll(attachmentRoot, 0o755); err != nil {
			slog.Warn("could not create attachment upload dir for container sharing", "dir", attachmentRoot, "err", err)
		}
		runtimeRegistry.SharedProfileManager().SetAttachmentDir(attachmentRoot)
	}

	// See8bd4-2: configure publishing of incus container ports as HTTPS
	// subdomains via the Caddy admin API. The public domain + edge basic-auth
	// creds come from flags / env (install.sh writes /etc/palmux/runtime.env
	// with PALMUX_PUBLIC_DOMAIN / BASIC_AUTH_USER / BASIC_AUTH_HASH). Empty
	// public domain leaves publishing disabled (local-dev / legacy snippet).
	// Sa53137-2: public domain + secrets now come from the resolved master
	// (flag > env > config.toml/secrets.env). The env-only path (Sbe4eee) was
	// folded into resolveServerConfig, so these no longer read os.Getenv here.
	resolvedPublicDomain := publicDomain
	// Sbe4eee: address Caddy uses to reach palmux for forward_auth (/auth/verify)
	// and per-port auth. Listening on 0.0.0.0 → dial 127.0.0.1.
	palmuxUpstream := loopbackUpstream(addr)
	runtimeRegistry.SetPublishDefaults(incus.PublishDefaults{
		BaseDomain:     resolvedPublicDomain,
		CaddyAdmin:     caddyAdmin,
		BasicUser:      rc.basicAuthUser,
		BasicHash:      rc.basicAuthHash,
		PalmuxUpstream: palmuxUpstream,
	})

	// Sbe4eee: SSO provider. Disabled (no-op) when public domain is unset
	// (local dev keeps the existing --token/open auth). Signing key is stable
	// (secrets.env PALMUX_SSO_SECRET, else derived from the password hash) so
	// restarts don't log the user out.
	ssoProvider := auth.NewSSOProvider(
		resolvedPublicDomain,
		rc.basicAuthHash,
		rc.ssoSecret,
		palmuxUpstream,
	)

	registry := tab.NewRegistry()
	st, err := store.New(store.Deps{
		Tmux:              tmuxClient,
		GHQ:               ghqClient,
		Gwq:               gwqClient,
		RepoStore:         repoStore,
		Settings:          settingsStore,
		Registry:          registry,
		Logger:            slog.Default(),
		MaxConnsPerBranch: maxConns,
		RuntimeRegistry:   runtimeRegistry,
	})
	if err != nil {
		return err
	}

	// Notify Hub is shared between the legacy `/api/notify` POST endpoint
	// (Claude Code Stop hook etc.) and the new in-process Claude-tab
	// publishers (permission requests, errors). Creating it here so the
	// Claude-tab Manager can hold a reference.
	notifyHub := notify.New(
		// Resolve a tmux session name back to a (repoID, branchID) the store knows.
		func(sessionName string) (string, string, bool) {
			rid, bid, ok := domain.ParseSessionName(sessionName)
			if !ok {
				return "", "", false
			}
			if _, err := st.Branch(rid, bid); err != nil {
				return "", "", false
			}
			return rid, bid, true
		},
		eventPublisher{hub: st.Hub()},
	)

	// Register providers in TabBar order: Claude / Bash / Files / Git.
	// Claude is the SDK-style stream-json tab; the previous tmux-backed
	// `claude` tab has been removed. Manager needs the Store for worktree
	// path lookups, so all providers are registered after store.New.
	// S008: hand the Manager a function that resolves the per-branch
	// attachment upload dir (`<attachmentUploadDir>/<repoId>/<branchId>`)
	// from current settings. The Manager passes that path on every CLI
	// spawn as `--add-dir <path>` so uploaded files are inside Claude's
	// tool surface without per-attachment respawn.
	attachmentDirFn := func(repoID, branchID string) string {
		root := settingsStore.Get().AttachmentUploadDir
		if root == "" {
			root = config.DefaultAttachmentUploadDir
		}
		root = strings.TrimRight(root, "/")
		if repoID == "" || branchID == "" {
			return ""
		}
		return filepath.Join(root, repoID, branchID)
	}
	agentManager := claudeagent.NewManager(claudeagent.Config{
		// Honour the resolved claude_bin (flag/env/config), same as the claude-tui
		// manager below. Hardcoding "claude" made the Agent tab ignore claude_bin
		// and fail LookPath on hosts where claude isn't on the process PATH (e.g.
		// the palmuxOS appliance: claude lives at ~/.local/bin, Sb14caa-5).
		Binary:          claudeBin,
		AttachmentDirFn: attachmentDirFn,
		// S4d8b1c: run the agent-mode claude INSIDE the workspace's incus
		// container when supported (runtime.ExecCommander).
		RuntimeResolver: func(repoID, branchID string) runtime.ExecCommander {
			if ec, ok := st.CurrentRuntime(repoID, branchID).(runtime.ExecCommander); ok {
				return ec
			}
			return nil
		},
		NotifyURLInContainer:  bridgeNotifyURL(addr, basePath),
		NotifyToken:           token,
		DefaultPermissionMode: settingsStore.Get().ClaudePermissionMode(), // global setting, default "auto"
		// S862203-3: claude now survives a palmux2 restart via a detached
		// `palmux ptyhost --mode pipe` process (ADR-0001/0002/0004) —
		// PalmuxBin is the same binary re-invoked as `<PalmuxBin> ptyhost
		// ...`. InstancePrefix is left empty (defaults to
		// domain.PalmuxSessionPrefix, already configured process-wide via
		// --tmux-prefix), same as claudetuiMgr below.
		PalmuxBin:   hookBinPath,
		OffsetStore: agentOffsetStore,
	},
		agentStore,
		branchResolver{store: st},
		agentEventPublisher{hub: st.Hub()},
		agentNotificationSink{hub: notifyHub},
		slog.Default(),
	)
	// S016: TabBar order is Claude / Files / Git / Sprint / Bash[].
	// Sprint is conditional on docs/ROADMAP.md so it slots after the
	// always-on Git tab and before the multi-instance Bash group.
	registry.Register(claudeagent.NewProvider(agentManager))
	registry.Register(files.New(st))
	gitProvider := gittab.New(st)
	registry.Register(gitProvider)
	sprintProvider := sprint.New(st)
	registry.Register(sprintProvider)
	registry.Register(ports.New(st))
	registry.Register(browser.New(st))
	registry.Register(bash.New())

	// claude-tui tab: interactive claude TUI via PTY (Sprint A Story 2).
	// The daemon spawn is lazy — the subprocess starts on first WS attach.
	// Story 4: wire a SessionStore so session IDs are detected via fsnotify
	// and persisted to claudetui-sessions.json across server restarts.
	tuiStore, err := claudetui.NewSessionStore(configDir)
	if err != nil {
		return fmt.Errorf("claudetui session store: %w", err)
	}
	claudetuiMgr := claudetui.NewManager(claudetui.ManagerConfig{
		ClaudeBin:      claudeBin,
		ClaudeArgs:     claudeArgs,
		PermissionMode: settingsStore.Get().ClaudePermissionMode(), // global setting, default "auto"
		RingSize:       1 << 20,                                    // 1 MiB ring buffer per branch
		ResumeOnDeath:  true,                                       // Story 4: always resume on crash
		Store:          tuiStore,
		NotifyHub:      notifyHub, // S0fd64b-1: forward OSC 52 clipboard events
		// Claude Code notification hooks injected per claude-tui subprocess.
		NotifyURL:   localNotifyURL(addr, basePath),
		NotifyToken: token,
		HookBinPath: hookBinPath,
		// S4d8b1c: run claude INSIDE the workspace's incus container when the
		// runtime supports it (runtime.PTYCommander). The bridge notify URL is
		// used for the in-container hook (127.0.0.1 would be the container).
		RuntimeResolver: func(repoID, branchID string) runtime.PTYCommander {
			if pc, ok := st.CurrentRuntime(repoID, branchID).(runtime.PTYCommander); ok {
				return pc
			}
			return nil
		},
		NotifyURLInContainer: bridgeNotifyURL(addr, basePath),
		// S3f2658-2: claude now survives a palmux2 restart via a detached
		// `palmux ptyhost` process (ADR-0001/0002) — PalmuxBin is the same
		// binary re-invoked as `<PalmuxBin> ptyhost ...`.
		PalmuxBin: hookBinPath,
	})
	claudetuiProvider := claudetui.New(claudetuiMgr)
	// Sadf90e: claudetui daemons live per-tab and are spawned lazily on the
	// first WS attach. The Provider needs to look up the branch worktree path
	// at attach time so the subprocess inherits the correct cmd.Dir. We pass
	// the live Store as a WorktreeResolver — it satisfies the interface
	// because *Store has BranchWorktreePath via the helper in store.go.
	claudetuiProvider.SetWorktreeResolver(storeWorktreeResolver{store: st})
	registry.Register(claudetuiProvider)

	// S009: wire the Claude tab as the per-branch multi-tab hook. The
	// store delegates non-tmux multi-instance AddTab/RemoveTab through
	// this so the bare server doesn't need to know about claudeagent
	// internals.
	st.SetMultiTabHook(claudeMultiTabHook{mgr: agentManager, registry: registry})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// S1e8d02: migrate sessions.json keys from the legacy branch-name-based
	// BranchID scheme to the new path-based WorkspaceID scheme. The
	// resolver walks every Open repo's worktree list and computes the
	// pre-S1e8d02 ID each branch would have had → maps it to the new
	// path-based ID. Idempotent: lastInit.workspaceMigrationV1 is set
	// after the first successful run, subsequent runs short-circuit.
	{
		resolver := buildLegacyBranchIDResolver(ctx, st)
		rewritten, dropped, err := agentStore.MigrateLegacyBranchIDs(
			resolver,
			func(format string, args ...any) {
				slog.Warn(fmt.Sprintf("[migrate-v1] "+format, args...))
			},
		)
		if err != nil {
			slog.Warn("workspace migration failed", "err", err)
		} else if rewritten > 0 || dropped > 0 {
			slog.Info("workspace migration complete",
				"rewritten", rewritten, "dropped", dropped)
		}
		// Best-effort tmux session rename: walk live sessions, look for
		// `_palmux_{repoId}_{oldBranchId}` shaped names that don't match
		// any current Open branch's expected session name, and try to
		// rename them to the new form. Failure is logged and ignored —
		// the recover loop in sync_tmux will rebuild missing sessions.
		renameLegacyTmuxSessions(ctx, tmuxClient, st)
	}

	// Build each branch's tab list now that every Provider is registered.
	// Without this the first GET /api/repos can return tabs:null for
	// branches whose tmux session was already alive at startup.
	st.PopulateTabs(ctx)

	// S015: drop entries in `repos.json#userOpenedBranches` whose
	// worktree was deleted while palmux2 was offline (e.g. via
	// `gwq remove` directly). Panic-safe — failures are logged per repo
	// and do not block startup.
	st.ReconcileUserOpenedBranches(ctx)

	// S023: drop `repos.json#last_active_branch` values whose worktree no
	// longer exists. Same idea as user_opened_branches reconcile but
	// independent so a failure in one path does not affect the other.
	st.ReconcileLastActiveBranches(ctx)

	// S3f2658-3: reconnect to any `palmux ptyhost` processes that survived a
	// PRIOR palmux2 lifetime (self-update / systemctl restart / `make serve`
	// re-run — ADR-0001/0002) BEFORE starting the background loops, so a
	// restart's very first frame already shows restored claude-tui tabs
	// rather than a blank one waiting for a lazy first WS attach. Dead /
	// unreachable ptyhost sockets left behind by an unclean prior exit are
	// cleaned up here too. Must run after tab/branch reconciliation above so
	// worktree lookups are accurate.
	if adopted, cleaned, derr := claudetui.DiscoverAndRestore(ctx, claudetuiMgr, storeWorktreeResolver{store: st}.BranchWorktreePath, slog.Default()); derr != nil {
		slog.Warn("claudetui: startup ptyhost discovery failed", "err", derr)
	} else if adopted > 0 || cleaned > 0 {
		slog.Info("claudetui: startup ptyhost discovery", "adopted", adopted, "cleanedStale", cleaned)
	}
	// S3f2658-3: wire orphan GC onto the store's existing 10s scan loop
	// (tmux-zombie-kill parity for ptyhosts whose tab/branch/worktree is
	// gone). Must be set before st.Run(ctx) starts that loop.
	st.SetTuiOrphanGC(claudetuiMgr)

	// S862203-3: same idea as the claudetui discovery pass above, for the
	// Claude AGENT tab's pipe-mode ptyhosts — re-adopt any that survived a
	// prior palmux2 lifetime so an in-flight turn's transcript (and any
	// permission that arrived during the restart window) is restored
	// before the first WS/REST request lands, not lazily on next message.
	if adopted, cleaned, derr := claudeagent.DiscoverAndRestore(ctx, agentManager, slog.Default()); derr != nil {
		slog.Warn("claudeagent: startup ptyhost discovery failed", "err", derr)
	} else if adopted > 0 || cleaned > 0 {
		slog.Info("claudeagent: startup ptyhost discovery", "adopted", adopted, "cleanedStale", cleaned)
	}

	st.Run(ctx)

	// Sa53137-1-2: watch settings.json on disk so direct edits (or `palmux
	// apply`) reload without a restart and fan out to every connected client
	// via the same WS event a PATCH emits. Reuses the SessionWatcher dir-watch
	// pattern. Failure to start the watcher is non-fatal — the server still
	// serves and GUI PATCH still works.
	if sw, werr := settingsStore.WatchFile(func(updated config.Settings) {
		slog.Info("settings.json changed on disk; reloaded")
		// Hot-apply the claude permission mode so a GUI/file change takes effect on
		// the next claude respawn without a server restart.
		claudetuiMgr.SetPermissionMode(updated.ClaudePermissionMode())
		// Hot-apply the attachment upload root to the shared profile so a GUI/file
		// change to AttachmentUploadDir re-points the container bind-mount without a
		// restart (the next scan-tick reconcile live-propagates it).
		if root := resolveAttachmentRoot(updated); root != "" {
			if err := os.MkdirAll(root, 0o755); err != nil {
				slog.Warn("could not create attachment upload dir for container sharing", "dir", root, "err", err)
			}
			runtimeRegistry.SharedProfileManager().SetAttachmentDir(root)
		}
		st.Hub().Publish(store.Event{Type: store.EventSettings, Payload: updated})
	}, slog.Default()); werr != nil {
		slog.Warn("settings file watch disabled", "err", werr)
	} else {
		defer func() { _ = sw.Close() }()
	}

	frontendFS, err := fs.Sub(palmux2.FrontendFS, "frontend/dist")
	if err != nil {
		return fmt.Errorf("frontend embed: %w", err)
	}

	// Sa53137: deploy controller seeded with the config this process launched
	// with. The masked view drives the GUI deploy tab; SaveAndClassify backs
	// `palmux apply` and the GUI Apply button.
	appliedMaster := config.MasterConfig{
		Server: config.ServerSection{
			Addr:           addr,
			BasePath:       basePath,
			MaxConnections: maxConns,
			TmuxPrefix:     domain.PalmuxSessionPrefix,
			CaddyAdmin:     caddyAdmin,
			ClaudeBin:      claudeBin,
			ClaudeArgs:     claudeArgs,
		},
		Public: config.PublicSection{
			Domain:        resolvedPublicDomain,
			BasicAuthUser: rc.basicAuthUser,
		},
		Workspace: config.WorkspaceSection{
			SharedDirs: rc.sharedDirs, // Sd44947 (expanded on read by CurrentView)
		},
	}
	deployCtl := deploy.New(configDir, appliedMaster, deploy.SecretPresence{
		SSOSecret:       rc.ssoSecret != "",
		BasicHash:       rc.basicAuthHash != "",
		Token:           token != "",
		CloudflareToken: cloudflareTokenPresent(),
	}, deployHotApplier{
		registry: runtimeRegistry,
		agentMgr: agentManager,
		tuiMgr:   claudetuiMgr,
	}, slog.Default())
	// On a NixOS appliance the privileged apply path is `nixos-rebuild switch`
	// (GUI-kickable via palmux-rebuild.service), not `palmux reconcile-system`.
	deployCtl.SetNixOSHost(selfupdate.IsNixOSHost())

	// S41bdf2: app card model. Install writes a generated home.packages/systemPackages
	// drop-in into the on-appliance flake's local/ dir and kicks the SAME S673a42
	// rebuild unit (deployRebuildPort). Share reuses the deploy controller's
	// shared_dirs single source (SharedDirsPort). The flake dir is the appliance
	// convention, overridable via env (priority_rule 7, no hardcode-only).
	nixosFlakeDir := os.Getenv("PALMUX_NIXOS_FLAKE_DIR")
	if nixosFlakeDir == "" {
		nixosFlakeDir = "/persist/palmux/nixos"
	}
	appsCtl := apps.New(configDir, filepath.Join(nixosFlakeDir, "local"),
		deployCtl, deployRebuildPort{nixOS: selfupdate.IsNixOSHost()}, slog.Default())

	// S6ab0ed: self-update service. Polls GitHub for new releases of the
	// managed components (palmux binary + palmux-ws image + declared tools) and
	// broadcasts an app.updateAvailable WS event on a state transition. The GUI
	// "Update all" button and `palmux update` CLI share its execution path.
	selfUpdateSvc, suErr := newSelfUpdateService(st, configDir)
	if suErr != nil {
		slog.Warn("self-update disabled", "err", suErr)
		selfUpdateSvc = nil
	} else {
		go selfUpdateSvc.Run(ctx)
	}

	mux := server.NewMux(server.Deps{
		Store:           st,
		Auth:            authn,
		SSO:             ssoProvider,
		Tmux:            tmuxClient,
		Commands:        commands.New(),
		Notify:          notifyHub,
		Deploy:          deployCtl,
		DeployConfigDir: configDir,
		Apps:            appsCtl,
		SelfUpdate:      selfUpdateSvc,
		FrontendFS:      frontendFS,
		// S010: serve bundled drawio webapp from internal/static via /static/*.
		// fs.Sub is applied inside server.staticHandler so the request path
		// `/static/drawio/...` resolves to `internal/static/drawio/...` in
		// the embed.
		StaticFS: palmux2.StaticFS,
		BasePath: basePath,
		Logger:   slog.Default(),
		HealthDetail: map[string]any{
			// resolveVersion() + the force-update synthetic suffix (empty unless the
			// env-gated test affordance is armed-and-applied). The suffix is read at
			// startup, so each restart picks up the advanced "+force.N" — that is the
			// version DELTA the self-update reconnect handshake reads (force.go).
			"version":   resolveVersion() + selfupdate.ForceVersionSuffix(configDir),
			"open":      authn.Open(),
			"configDir": configDir,
		},
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Additional listener on the incus bridge gateway so in-container processes
	// (the palmux-browser CLI, claude-tui notify hooks) can reach palmux's API at
	// their default gateway. The bridge IP is private to the incus network — NOT
	// publicly reachable — so this does not expose the API like binding 0.0.0.0
	// would. Skipped when incusbr0 is absent or already covered by addr. (S62374c)
	var bridgeSrv *http.Server
	if bAddr := incusBridgeListenAddr(addr); bAddr != "" && bAddr != addr {
		bridgeSrv = &http.Server{Addr: bAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			slog.Info("palmux2 also listening on incus bridge (in-container API access)", "addr", bAddr)
			if err := bridgeSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Warn("incus bridge listener error (in-container API access disabled)", "err", err)
			}
		}()
	}

	if err := writeEnvFile(configDir, addr, token); err != nil {
		slog.Warn("env file write", "err", err)
	}

	go func() {
		mode := "open"
		if !authn.Open() {
			mode = "token"
		}
		slog.Info("palmux2 listening", "addr", addr, "configDir", configDir, "auth", mode, "tmuxPrefix", domain.PalmuxSessionPrefix)
		if !authn.Open() {
			slog.Info("authenticate at", "url", fmt.Sprintf("http://localhost%s/auth?token=%s", listenLocalAddr(addr), token))
		}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// S3f2658-2 / S862203-3: DetachAll (NOT Shutdown) — palmux2 process exit
	// (SIGTERM, self-update restart) must leave every claude-tui / claude-agent
	// ptyhost running so a future palmux2 reconnects to it (ADR-0001/0002
	// restart survival). Only an intentional tab/branch close calls Shutdown.
	if err := agentManager.DetachAll(shutdownCtx); err != nil {
		slog.Warn("claudeagent detach", "err", err)
	}
	if err := claudetuiMgr.DetachAll(shutdownCtx); err != nil {
		slog.Warn("claudetui detach", "err", err)
	}
	// S012: stop the per-branch worktree watcher and release its
	// fsnotify file descriptors before the process exits.
	gitProvider.Close()
	sprintProvider.Close()
	if bridgeSrv != nil {
		_ = bridgeSrv.Shutdown(shutdownCtx)
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
	return nil
}

// loopbackUpstream returns the host:port Caddy should dial to reach this palmux
// process for forward_auth / per-port auth. A wildcard listen host (0.0.0.0/::)
// is rewritten to loopback. (Sbe4eee)
func loopbackUpstream(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "127.0.0.1:8080"
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func defaultConfigDir() string {
	// Spec default is ~/.config/palmux/, but during dev we run with
	// --config-dir ./tmp via the Makefile. Prefer the spec default for
	// production; the Makefile overrides for dev.
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/.config/palmux"
	}
	return "./tmp"
}

func requireBins(names ...string) error {
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			return fmt.Errorf("required binary %q not on PATH: %w", n, err)
		}
	}
	return nil
}

// writeEnvFile drops a small KEY=VALUE file under configDir for hook scripts
// (e.g. Claude Code's Stop hook) to source. Filename is env.{port} so multiple
// instances don't collide.
func writeEnvFile(configDir, addr, token string) error {
	port := portFromAddr(addr)
	if port == "" {
		return nil
	}
	host := "localhost"
	body := fmt.Sprintf(
		"PALMUX_URL=http://%s:%s\nPALMUX_TOKEN=%s\nPALMUX_PORT=%s\n",
		host, port, token, port,
	)
	return os.WriteFile(
		fmt.Sprintf("%s/env.%s", configDir, port),
		[]byte(body),
		0o600,
	)
}

func portFromAddr(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return ""
}

// localNotifyURL builds the loopback URL of the /api/notify endpoint that the
// injected Claude Code hooks POST to. The hook runs on the same host as the
// claude-tui subprocess, so 127.0.0.1 + the listen port + base path is always
// reachable regardless of the server's bind address. Returns "" if the port
// can't be derived (hook injection is then skipped).
// incusBridgeListenAddr returns "<incusbr0-ipv4>:<port>" (port from the main
// listen addr) so palmux can add a second listener reachable from inside
// workspace containers via their default gateway. Returns "" if the incusbr0
// bridge is absent (no incus on this host) — then no extra listener is added.
// The bridge IP is private to the incus network, so this never exposes the API
// publicly (unlike binding 0.0.0.0).
func incusBridgeListenAddr(addr string) string {
	port := portFromAddr(addr)
	if port == "" {
		return ""
	}
	// If the main listener already binds a wildcard address (0.0.0.0 / :: /
	// no host), it ALREADY covers the incus bridge IP — a second bridge listener
	// on <gateway>:<port> would collide with the wildcard bind ("address already
	// in use") and is redundant. Only add the bridge listener when the main addr
	// is a specific non-wildcard host (e.g. 127.0.0.1, the production default).
	if host, _, err := net.SplitHostPort(addr); err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" {
			return ""
		}
	}
	iface, err := net.InterfaceByName("incusbr0")
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil {
			return net.JoinHostPort(ip4.String(), port)
		}
	}
	return ""
}

func localNotifyURL(addr, basePath string) string {
	port := portFromAddr(addr)
	if port == "" {
		return ""
	}
	return notifyURLForHostPort("127.0.0.1:"+port, basePath)
}

// bridgeNotifyURL returns the notify endpoint reachable from INSIDE an incus
// container — `http://<incusbr0-ip>:<port><base>api/notify`. The container's
// default gateway is the incus bridge IP, so an in-container hook / palmux-browser
// CLI posts there instead of 127.0.0.1 (which is the container itself). Returns
// "" when there is no incus bridge or --addr is wildcard. (S4d8b1c)
func bridgeNotifyURL(addr, basePath string) string {
	b := incusBridgeListenAddr(addr)
	if b == "" {
		return ""
	}
	return notifyURLForHostPort(b, basePath)
}

func notifyURLForHostPort(hostPort, basePath string) string {
	bp := basePath
	if bp == "" {
		bp = "/"
	}
	if !strings.HasPrefix(bp, "/") {
		bp = "/" + bp
	}
	if !strings.HasSuffix(bp, "/") {
		bp += "/"
	}
	return "http://" + hostPort + bp + "api/notify"
}

// branchResolver adapts *store.Store into the small BranchResolver interface
// claudeagent.Manager wants. Keeps the agent package free of an import on
// internal/store.
type branchResolver struct{ store *store.Store }

func (b branchResolver) WorktreePath(repoID, branchID string) (string, error) {
	br, err := b.store.Branch(repoID, branchID)
	if err != nil {
		return "", err
	}
	return br.WorktreePath, nil
}

// agentEventPublisher adapts *store.EventHub to claudeagent.EventPublisher
// so per-branch agent state changes fan out to every connected browser
// (Drawer pip, Activity Inbox, etc.) via the existing /api/events WS.
type agentEventPublisher struct{ hub *store.EventHub }

func (p agentEventPublisher) Publish(eventType, repoID, branchID string, payload any) {
	p.hub.Publish(store.Event{
		Type:     store.EventType(eventType),
		RepoID:   repoID,
		BranchID: branchID,
		Payload:  payload,
	})
}

// agentNotificationSink adapts *notify.Hub to claudeagent.NotificationSink
// so the Claude tab can surface permission requests / errors in the global
// Activity Inbox.
type agentNotificationSink struct{ hub *notify.Hub }

func (s agentNotificationSink) IngestInternal(repoID, branchID string, n claudeagent.InternalNotification) {
	actions := make([]notify.NotificationAction, 0, len(n.Actions))
	for _, a := range n.Actions {
		actions = append(actions, notify.NotificationAction{Label: a.Label, Action: a.Action})
	}
	s.hub.IngestInternal(repoID, branchID, notify.InternalRequest{
		RequestID: n.RequestID,
		Type:      n.Type,
		Title:     n.Title,
		Message:   n.Message,
		Detail:    n.Detail,
		Actions:   actions,
		TabID:     n.TabID,
		TabName:   n.TabName,
	})
}

func (s agentNotificationSink) ClearByRequestID(repoID, branchID, requestID string) {
	s.hub.ClearByRequestID(repoID, branchID, requestID)
}

// claudeMultiTabHook adapts claudeagent.Manager into store.MultiTabHook
// so the generic AddTab/RemoveTab path can grow / shrink the per-branch
// Claude tab list without the store package importing claudeagent. The
// adapter lives in main.go (the only place that wires both pieces) so
// neither side has to declare a dependency on the other.
type claudeMultiTabHook struct {
	mgr      *claudeagent.Manager
	registry *tab.Registry
}

func (h claudeMultiTabHook) CreateTab(_ context.Context, repoID, branchID, providerType string) (domain.Tab, error) {
	if providerType != claudeagent.TabType {
		return domain.Tab{}, fmt.Errorf("claudeMultiTabHook: unsupported provider %q", providerType)
	}
	tabID, err := h.mgr.AddTabForBranch(repoID, branchID)
	if err != nil {
		return domain.Tab{}, err
	}
	return domain.Tab{
		ID:        tabID,
		Type:      claudeagent.TabType,
		Name:      claudeagent.DisplayNameForTab(tabID),
		Protected: true,
		Multiple:  true,
	}, nil
}

func (h claudeMultiTabHook) DeleteTab(ctx context.Context, repoID, branchID, tabID string) error {
	return h.mgr.RemoveTabForBranch(ctx, repoID, branchID, tabID)
}

// storeWorktreeResolver adapts *store.Store into claudetui.WorktreeResolver
// so the claudetui Provider can look up the cmd.Dir for a fresh daemon at
// WS-attach time. Kept in main.go (the wiring layer) so claudetui doesn't
// import internal/store.
type storeWorktreeResolver struct {
	store *store.Store
}

func (r storeWorktreeResolver) BranchWorktreePath(repoID, branchID string) string {
	b, err := r.store.Branch(repoID, branchID)
	if err != nil || b == nil {
		return ""
	}
	return b.WorktreePath
}

// deployHotApplier adapts the runtime registry + claude managers into
// deploy.HotApplier so the deploy controller can hot-apply caddy_admin /
// basic-auth / claude_bin / claude_args without a restart. (Sa53137-3)
type deployHotApplier struct {
	registry *incus.Registry
	agentMgr *claudeagent.Manager
	tuiMgr   *claudetui.Manager
}

func (a deployHotApplier) SetCaddyAdmin(addr string) {
	if a.registry != nil {
		a.registry.RefreshCaddyAdmin(addr)
	}
}

func (a deployHotApplier) SetBasicAuthDefaults(user, hash string) {
	if a.registry != nil {
		a.registry.RefreshBasicAuth(user, hash)
	}
}

// resolveAttachmentRoot returns the attachment upload ROOT from settings, falling
// back to the package default, with any trailing slash trimmed. Empty only if the
// default is somehow blanked. Shared by startup + the settings-watch hot-apply.
func resolveAttachmentRoot(s config.Settings) string {
	root := s.AttachmentUploadDir
	if root == "" {
		root = config.DefaultAttachmentUploadDir
	}
	return strings.TrimRight(root, "/")
}

func (a deployHotApplier) SetClaudeBin(bin string) {
	if a.agentMgr != nil {
		a.agentMgr.SetBinary(bin)
	}
	if a.tuiMgr != nil {
		a.tuiMgr.SetClaudeBin(bin)
	}
}

func (a deployHotApplier) SetClaudeArgs(args []string) {
	if a.tuiMgr != nil {
		a.tuiMgr.SetClaudeArgs(args)
	}
}

// SetSharedDirs (Sd44947) rewrites the host-wide palmux-shared incus profile
// with the new shared folders and returns the number of containers the profile
// is attached to (incus live-propagates the device add/remove). Host-only
// deployments (nil registry) are a no-op.
func (a deployHotApplier) SetSharedDirs(ctx context.Context, dirs []string) (int, error) {
	if a.registry == nil {
		return 0, nil
	}
	m := a.registry.SharedProfileManager()
	if m == nil {
		return 0, nil
	}
	m.SetSharedDirs(dirs)
	if err := m.Ensure(ctx); err != nil {
		return 0, err
	}
	return m.UsedByCount(ctx), nil
}

// deployRebuildPort adapts the S673a42 rebuild plumbing to apps.RebuildPort so
// the app card's install/uninstall reuses the SAME GUI-kick nixos-rebuild unit
// (priority_rule 6 — no duplicate rebuild plumbing). NixOS-only; on other hosts
// NixOSHost()==false so Install/Uninstall persist intent without rebuilding.
type deployRebuildPort struct{ nixOS bool }

func (p deployRebuildPort) NixOSHost() bool { return p.nixOS }

func (p deployRebuildPort) TriggerRebuild(ctx context.Context) error {
	return deploy.TriggerRebuild(ctx)
}

func (p deployRebuildPort) RebuildStatus(ctx context.Context) (running, failed bool, err error) {
	st, qerr := deploy.QueryRebuild(ctx)
	if qerr != nil {
		return false, false, qerr
	}
	return st.Running, st.Result == "exit-code" || st.Active == "failed", nil
}

// cloudflareTokenPresent reports whether a Cloudflare DNS token is configured
// for the system Caddy (root-owned /etc/caddy/palmux.env). palmux can't read it
// (root:caddy), so we only check file presence + a non-empty line. Best-effort —
// returns false when unreadable.
func cloudflareTokenPresent() bool {
	b, err := os.ReadFile("/etc/caddy/palmux.env")
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "CLOUDFLARE_API_TOKEN=") &&
		!strings.Contains(string(b), "CLOUDFLARE_API_TOKEN=\n")
}

// eventPublisher adapts *store.EventHub to notify.Publisher so the Hub can
// broadcast notification events without importing store.
type eventPublisher struct{ hub *store.EventHub }

func (p eventPublisher) Publish(eventType, repoID, branchID string, payload any) {
	p.hub.Publish(store.Event{
		Type:     store.EventType(eventType),
		RepoID:   repoID,
		BranchID: branchID,
		Payload:  payload,
	})
}

// buildLegacyBranchIDResolver (S1e8d02) returns a closure that maps
// `(repoID, oldBranchID)` to the new path-based workspace ID for every
// Open repo's currently-alive worktree. Returns ok=false when no live
// worktree corresponds to oldBranchID — caller drops the persisted
// entry as stale.
//
// `oldBranchID` here is what BranchSlugID(repoFullPath, branchName)
// produced before S1e8d02. We re-derive that ID for each live
// (worktree.Path, worktree.Branch) pair and key the map on it.
func buildLegacyBranchIDResolver(ctx context.Context, st *store.Store) func(string, string) (string, bool) {
	type entry struct {
		newID    string
		legacyID string
	}
	// repoID -> oldBranchID -> newID
	cache := map[string]map[string]string{}
	for _, r := range st.Repos() {
		live := map[string]string{}
		// Use the same worktree.List path the store uses internally.
		wts, err := worktreeList(ctx, r.FullPath)
		if err != nil {
			slog.Warn("[migrate-v1] worktree.List failed",
				"repo", r.GHQPath, "err", err)
		}
		for _, e := range wts {
			old := domain.BranchSlugID(r.FullPath, e.Branch)
			newID := domain.WorkspaceSlugIDFromPath(e.Path, e.IsPrimary, r.FullPath)
			live[old] = newID
			_ = entry{} // keep the type alive in case future expansion is needed
		}
		cache[r.ID] = live
	}
	return func(repoID, oldBranchID string) (string, bool) {
		m, ok := cache[repoID]
		if !ok {
			return "", false
		}
		newID, found := m[oldBranchID]
		if !found {
			return "", false
		}
		return newID, true
	}
}

// renameLegacyTmuxSessions (S1e8d02) walks live tmux sessions and renames
// any `_palmux_{repoId}_{oldBranchId}` shaped session to its new
// `_palmux_{repoId}_{newWorkspaceId}` form so attached pty processes
// (Claude / Bash) keep running across the upgrade. Failures are logged
// and ignored — the recover loop in sync_tmux will rebuild missing
// sessions.
func renameLegacyTmuxSessions(ctx context.Context, t tmux.Client, st *store.Store) {
	sessions, err := t.ListSessions(ctx)
	if err != nil {
		slog.Warn("[migrate-v1] ListSessions for rename failed", "err", err)
		return
	}
	// Build a set of "expected" current session names so we don't try to
	// rename a session that already matches the new layout.
	expected := map[string]struct{}{}
	for _, r := range st.Repos() {
		for _, b := range r.OpenBranches {
			expected[b.TabSet.TmuxSession] = struct{}{}
		}
	}
	resolver := buildLegacyBranchIDResolver(ctx, st)
	for _, s := range sessions {
		if _, want := expected[s.Name]; want {
			continue // already correct
		}
		if !domain.IsPalmuxSession(s.Name) {
			continue
		}
		repoID, oldBranchID, ok := domain.ParseSessionName(s.Name)
		if !ok {
			continue
		}
		newID, ok := resolver(repoID, oldBranchID)
		if !ok || newID == oldBranchID {
			continue
		}
		newName := domain.SessionName(repoID, newID)
		if err := t.RenameSession(ctx, s.Name, newName); err != nil {
			slog.Warn("[migrate-v1] tmux rename-session failed",
				"old", s.Name, "new", newName, "err", err)
			continue
		}
		slog.Info("[migrate-v1] tmux session renamed",
			"old", s.Name, "new", newName)
	}
}

// listenLocalAddr converts "0.0.0.0:8080" into ":8080" for friendlier
// localhost prompts.
func listenLocalAddr(addr string) string {
	if addr == "" {
		return ""
	}
	if addr[0] == ':' {
		return addr
	}
	for i := 0; i < len(addr); i++ {
		if addr[i] == ':' {
			return addr[i:]
		}
	}
	return ":" + addr
}
