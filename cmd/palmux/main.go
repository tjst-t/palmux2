package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	"github.com/tjst-t/palmux2/internal/server"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tab/bash"
	"github.com/tjst-t/palmux2/internal/tab/claudeagent"
	"github.com/tjst-t/palmux2/internal/tab/files"
	gittab "github.com/tjst-t/palmux2/internal/tab/git"
	"github.com/tjst-t/palmux2/internal/tmux"
)

func main() {
	// Some Linux distros ship a slim mime DB that doesn't know about
	// .webmanifest. Register the canonical type so PWAs install cleanly.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")

	addr := pflag.String("addr", "0.0.0.0:8080", "listen address (host:port)")
	configDir := pflag.String("config-dir", defaultConfigDir(), "config directory (repos.json / settings.json)")
	token := pflag.String("token", "", "auth token. empty = open access")
	basePath := pflag.String("base-path", "/", "URL base path (e.g. /palmux/)")
	maxConns := pflag.Int("max-connections", 0, "per-branch WS connection cap (0 = unlimited)")
	portmanURL := pflag.String("portman-url", "", "URL of a portman dashboard; when set, the header shows a link")
	pflag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(*addr, *configDir, *token, *basePath, *maxConns, *portmanURL); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(addr, configDir, token, basePath string, maxConns int, portmanURL string) error {
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
	},
		agentStore,
		branchResolver{store: st},
		agentEventPublisher{hub: st.Hub()},
		agentNotificationSink{hub: notifyHub},
		slog.Default(),
	)
	registry.Register(claudeagent.NewProvider(agentManager))
	registry.Register(bash.New())
	registry.Register(files.New(st))
	registry.Register(gittab.New(st))

	// S009: wire the Claude tab as the per-branch multi-tab hook. The
	// store delegates non-tmux multi-instance AddTab/RemoveTab through
	// this so the bare server doesn't need to know about claudeagent
	// internals.
	st.SetMultiTabHook(claudeMultiTabHook{mgr: agentManager, registry: registry})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Build each branch's tab list now that every Provider is registered.
	// Without this the first GET /api/repos can return tabs:null for
	// branches whose tmux session was already alive at startup.
	st.PopulateTabs(ctx)

	st.Run(ctx)

	frontendFS, err := fs.Sub(palmux2.FrontendFS, "frontend/dist")
	if err != nil {
		return fmt.Errorf("frontend embed: %w", err)
	}

	mux := server.NewMux(server.Deps{
		Store:      st,
		Auth:       authn,
		Tmux:       tmuxClient,
		Commands:   commands.New(),
		Notify:     notifyHub,
		Portman:    portman.New(""),
		FrontendFS: frontendFS,
		BasePath:   basePath,
		Logger:     slog.Default(),
		HealthDetail: map[string]any{
			"version":    "phase-10",
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

	if err := writeEnvFile(configDir, addr, token); err != nil {
		slog.Warn("env file write", "err", err)
	}

	go func() {
		mode := "open"
		if !authn.Open() {
			mode = "token"
		}
		slog.Info("palmux2 listening", "addr", addr, "configDir", configDir, "auth", mode)
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
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
	return nil
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
