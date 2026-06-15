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
	"github.com/tjst-t/palmux2/internal/attachment"
	"github.com/tjst-t/palmux2/internal/auth"
	"github.com/tjst-t/palmux2/internal/commands"
	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/ghq"
	"github.com/tjst-t/palmux2/internal/gwq"
	"github.com/tjst-t/palmux2/internal/notify"
	"github.com/tjst-t/palmux2/internal/portman"
	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/runtime/incus"
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

	// `palmux runtime <install|doctor>` manages the palmux-ws Incus image and
	// host prerequisites.  Dispatch before server bootstrap.
	if len(os.Args) > 1 && os.Args[1] == "runtime" {
		os.Exit(runRuntime(os.Args[2:]))
	}

	// Some Linux distros ship a slim mime DB that doesn't know about
	// .webmanifest. Register the canonical type so PWAs install cleanly.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")

	addr := pflag.String("addr", "0.0.0.0:8080", "listen address (host:port)")
	configDir := pflag.String("config-dir", defaultConfigDir(), "config directory (repos.json / settings.json)")
	token := pflag.String("token", "", "auth token. empty = open access")
	basePath := pflag.String("base-path", "/", "URL base path (e.g. /palmux/)")
	maxConns := pflag.Int("max-connections", 0, "per-branch WS connection cap (0 = unlimited)")
	portmanURL := pflag.String("portman-url", "", "URL of a portman dashboard; when set, the header shows a link")
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

	// Apply the prefix BEFORE any other code reads from
	// domain.PalmuxSessionPrefix. After this call, every session name
	// generated/parsed by domain.{SessionName,ParseSessionName,…}
	// uses the configured prefix.
	domain.Configure(*tmuxPrefix)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(*addr, *configDir, *token, *basePath, *maxConns, *portmanURL, *claudeBin, *claudeArgs, *publicDomain, *caddyAdmin); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(addr, configDir, token, basePath string, maxConns int, portmanURL string, claudeBin string, claudeArgs []string, publicDomain, caddyAdmin string) error {
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

	// See8bd4-2: configure publishing of incus container ports as HTTPS
	// subdomains via the Caddy admin API. The public domain + edge basic-auth
	// creds come from flags / env (install.sh writes /etc/palmux/runtime.env
	// with PALMUX_PUBLIC_DOMAIN / BASIC_AUTH_USER / BASIC_AUTH_HASH). Empty
	// public domain leaves publishing disabled (local-dev / legacy snippet).
	resolvedPublicDomain := publicDomain
	if resolvedPublicDomain == "" {
		resolvedPublicDomain = os.Getenv("PALMUX_PUBLIC_DOMAIN")
	}
	// Sbe4eee: address Caddy uses to reach palmux for forward_auth (/auth/verify)
	// and per-port auth. Listening on 0.0.0.0 → dial 127.0.0.1.
	palmuxUpstream := loopbackUpstream(addr)
	runtimeRegistry.SetPublishDefaults(incus.PublishDefaults{
		BaseDomain:     resolvedPublicDomain,
		CaddyAdmin:     caddyAdmin,
		BasicUser:      os.Getenv("BASIC_AUTH_USER"),
		BasicHash:      os.Getenv("BASIC_AUTH_HASH"),
		PalmuxUpstream: palmuxUpstream,
	})

	// Sbe4eee: SSO provider. Disabled (no-op) when --public-domain is unset
	// (local dev keeps the existing --token/open auth). Signing key is stable
	// (PALMUX_SSO_SECRET, else derived from the password hash) so restarts don't
	// log the user out.
	ssoProvider := auth.NewSSOProvider(
		resolvedPublicDomain,
		os.Getenv("BASIC_AUTH_HASH"),
		os.Getenv("PALMUX_SSO_SECRET"),
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
		Binary:          "claude",
		AttachmentDirFn: attachmentDirFn,
		// S4d8b1c: run the agent-mode claude INSIDE the workspace's incus
		// container when supported (runtime.ExecCommander).
		RuntimeResolver: func(repoID, branchID string) runtime.ExecCommander {
			if ec, ok := st.CurrentRuntime(repoID, branchID).(runtime.ExecCommander); ok {
				return ec
			}
			return nil
		},
		NotifyURLInContainer: bridgeNotifyURL(addr, basePath),
		NotifyToken:          token,
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
	// Hook wiring: the palmux binary doubles as the Claude Code hook handler
	// (`palmux hook`). Resolve its absolute path so the injected --settings
	// command is unambiguous regardless of the claude subprocess's cwd.
	hookBinPath, err := os.Executable()
	if err != nil || hookBinPath == "" {
		hookBinPath = os.Args[0]
	}
	claudetuiMgr := claudetui.NewManager(claudetui.ManagerConfig{
		ClaudeBin:     claudeBin,
		ClaudeArgs:    claudeArgs,
		RingSize:      1 << 20, // 1 MiB ring buffer per branch
		ResumeOnDeath: true,    // Story 4: always resume on crash
		Store:         tuiStore,
		NotifyHub:     notifyHub, // S0fd64b-1: forward OSC 52 clipboard events
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

	st.Run(ctx)

	frontendFS, err := fs.Sub(palmux2.FrontendFS, "frontend/dist")
	if err != nil {
		return fmt.Errorf("frontend embed: %w", err)
	}

	mux := server.NewMux(server.Deps{
		Store:      st,
		Auth:       authn,
		SSO:        ssoProvider,
		Tmux:       tmuxClient,
		Commands:   commands.New(),
		Notify:     notifyHub,
		Portman:    portman.New(""),
		FrontendFS: frontendFS,
		// S010: serve bundled drawio webapp from internal/static via /static/*.
		// fs.Sub is applied inside server.staticHandler so the request path
		// `/static/drawio/...` resolves to `internal/static/drawio/...` in
		// the embed.
		StaticFS: palmux2.StaticFS,
		BasePath: basePath,
		Logger:   slog.Default(),
		HealthDetail: map[string]any{
			"version":    resolveVersion(),
			"open":       authn.Open(),
			"configDir":  configDir,
			"portmanURL": portmanURL,
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
	agentManager.Shutdown()
	if err := claudetuiMgr.ShutdownAll(shutdownCtx); err != nil {
		slog.Warn("claudetui shutdown", "err", err)
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
