package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	palmux2 "github.com/tjst-t/palmux2"
	"github.com/tjst-t/palmux2/internal/auth"
	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/ghq"
	"github.com/tjst-t/palmux2/internal/gwq"
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

	frontendFS, err := fs.Sub(palmux2.FrontendFS, "frontend/dist")
	if err != nil {
		return fmt.Errorf("frontend embed: %w", err)
	}

	mux := server.NewMux(server.Deps{
		Store:      st,
		Auth:       authn,
		Tmux:       tmuxClient,
		FrontendFS: frontendFS,
		BasePath:   basePath,
		Logger:     slog.Default(),
		HealthDetail: map[string]any{
			"version":   "phase-2",
			"open":      authn.Open(),
			"configDir": configDir,
		},
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
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
