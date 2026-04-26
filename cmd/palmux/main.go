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
	"syscall"
	"time"

	"github.com/spf13/pflag"

	palmux2 "github.com/tjst-t/palmux2"
	"github.com/tjst-t/palmux2/internal/auth"
	"github.com/tjst-t/palmux2/internal/commands"
	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/ghq"
	"github.com/tjst-t/palmux2/internal/gwq"
	"github.com/tjst-t/palmux2/internal/notify"
	"github.com/tjst-t/palmux2/internal/server"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tab/bash"
	"github.com/tjst-t/palmux2/internal/tab/claude"
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
	pflag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(*addr, *configDir, *token, *basePath); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(addr, configDir, token, basePath string) error {
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

	registry := tab.NewRegistry()
	registry.Register(claude.New(claude.Options{}))
	// Bash before Files? No — TabBar order is: Claude / Files / Git / Bash.
	// We wire the storeRef after Store.New, so Files Provider is added then.
	registry.Register(bash.New())
	// Git provider lands in Phase 5.

	tmuxClient := tmux.NewExecClient()
	ghqClient := ghq.New()
	gwqClient := gwq.New()

	authn, err := auth.New(token)
	if err != nil {
		return err
	}

	st, err := store.New(store.Deps{
		Tmux:      tmuxClient,
		GHQ:       ghqClient,
		Gwq:       gwqClient,
		RepoStore: repoStore,
		Settings:  settingsStore,
		Registry:  registry,
		Logger:    slog.Default(),
	})
	if err != nil {
		return err
	}
	// Files / Git Providers need a Store reference (handlers look up
	// worktree paths at request time). Register after Store.New.
	registry.Register(files.New(st))
	registry.Register(gittab.New(st))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st.Run(ctx)

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
		FrontendFS: frontendFS,
		BasePath:   basePath,
		Logger:     slog.Default(),
		HealthDetail: map[string]any{
			"version":   "phase-7",
			"open":      authn.Open(),
			"configDir": configDir,
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
