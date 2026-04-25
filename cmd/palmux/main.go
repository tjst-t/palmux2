package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	palmux2 "github.com/tjst-t/palmux2"
)

func main() {
	addr := pflag.String("addr", "0.0.0.0:8080", "listen address (host:port)")
	configDir := pflag.String("config-dir", "./tmp", "config directory (repos.json / settings.json)")
	token := pflag.String("token", "", "auth token. empty = open access")
	basePath := pflag.String("base-path", "/", "URL base path (e.g. /palmux/)")
	pflag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := os.MkdirAll(*configDir, 0o755); err != nil {
		slog.Error("create config dir", "dir", *configDir, "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","phase":0}`))
	})

	frontendFS, err := fs.Sub(palmux2.FrontendFS, "frontend/dist")
	if err != nil {
		slog.Error("frontend fs sub", "err", err)
		os.Exit(1)
	}
	mux.Handle("/", spaHandler(frontendFS, *basePath))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("palmux2 listening", "addr", *addr, "configDir", *configDir, "auth", authMode(*token))
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
}

func authMode(token string) string {
	if token == "" {
		return "open"
	}
	return "token"
}

// spaHandler serves static assets from the embedded frontend.
// Unknown paths fall back to index.html so the SPA router can take over.
// /api/* and /auth are handled separately by their own routes.
func spaHandler(frontendFS fs.FS, basePath string) http.Handler {
	fileServer := http.FileServer(http.FS(frontendFS))
	prefix := strings.TrimRight(basePath, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := r.URL.Path
		if prefix != "" {
			path = strings.TrimPrefix(path, prefix)
		}
		if path == "" {
			path = "/"
		}
		if strings.HasPrefix(path, "/api/") || path == "/auth" {
			http.NotFound(w, r)
			return
		}
		if path == "/" || !hasFile(frontendFS, strings.TrimPrefix(path, "/")) {
			serveIndex(w, r, frontendFS)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = path
		fileServer.ServeHTTP(w, r2)
	})
}

func hasFile(fsys fs.FS, name string) bool {
	if name == "" {
		return false
	}
	name = filepath.ToSlash(name)
	if _, err := fs.Stat(fsys, name); err == nil {
		return true
	}
	return false
}

func serveIndex(w http.ResponseWriter, _ *http.Request, fsys fs.FS) {
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.Error(w, fmt.Sprintf("index.html not found in embed: %v (run `make build`?)", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
