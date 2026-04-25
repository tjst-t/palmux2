// Package server wires HTTP handlers around Store + Auth. It is intentionally
// thin — most logic lives in Store; handlers translate URL parameters and
// JSON bodies into Store method calls.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/tjst-t/palmux2/internal/auth"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// Deps bundles dependencies for NewMux.
type Deps struct {
	Store        *store.Store
	Auth         *auth.Authenticator
	Tmux         tmux.Client
	FrontendFS   fs.FS // embedded SPA bundle
	BasePath     string
	Logger       *slog.Logger
	HealthDetail map[string]any // optional fields appended to /api/health
}

// NewMux builds the top-level mux: /auth, /api/* (auth-required) and the SPA
// fallback for everything else.
func NewMux(deps Deps) *http.ServeMux {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.BasePath == "" {
		deps.BasePath = "/"
	}

	apiMux := http.NewServeMux()
	registerRoutes(apiMux, deps)
	if deps.Tmux != nil {
		registerTerminalWS(apiMux, deps)
	}

	// Let registered tab providers attach their REST endpoints under their
	// own prefix. (Files / Git providers — Phase 4 / 5.)
	for _, p := range deps.Store.Registry().Providers() {
		prefix := "/api/repos/{repoId}/branches/{branchId}/" + p.Type()
		p.RegisterRoutes(apiMux, prefix)
	}

	root := http.NewServeMux()
	root.HandleFunc("/auth", deps.Auth.AuthHandler)
	root.Handle("/api/", deps.Auth.Middleware(apiMux))
	root.Handle("/", spaHandler(deps.FrontendFS, deps.BasePath))
	return root
}

// registerRoutes attaches every Phase 1 API endpoint to the mux. Phase 2+
// add their handlers in their own files but use the same mux.
func registerRoutes(mux *http.ServeMux, deps Deps) {
	h := &handlers{store: deps.Store, logger: deps.Logger, healthDetail: deps.HealthDetail}

	mux.HandleFunc("GET /api/health", h.health)

	mux.HandleFunc("GET /api/repos", h.listRepos)
	mux.HandleFunc("GET /api/repos/available", h.availableRepos)
	mux.HandleFunc("POST /api/repos/{repoId}/open", h.openRepo)
	mux.HandleFunc("POST /api/repos/{repoId}/close", h.closeRepo)
	mux.HandleFunc("POST /api/repos/{repoId}/star", h.star)
	mux.HandleFunc("POST /api/repos/{repoId}/unstar", h.unstar)

	mux.HandleFunc("GET /api/repos/{repoId}/branches", h.listBranches)
	mux.HandleFunc("GET /api/repos/{repoId}/branch-picker", h.branchPicker)
	mux.HandleFunc("POST /api/repos/{repoId}/branches/open", h.openBranch)
	mux.HandleFunc("DELETE /api/repos/{repoId}/branches/{branchId}", h.closeBranch)

	mux.HandleFunc("GET /api/repos/{repoId}/branches/{branchId}/tabs", h.listTabs)
	mux.HandleFunc("POST /api/repos/{repoId}/branches/{branchId}/tabs", h.addTab)
	mux.HandleFunc("DELETE /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}", h.removeTab)
	mux.HandleFunc("PATCH /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}", h.renameTab)

	mux.HandleFunc("GET /api/settings", h.getSettings)
	mux.HandleFunc("PATCH /api/settings", h.patchSettings)

	mux.HandleFunc("GET /api/connections", h.connections)
	mux.HandleFunc("GET /api/orphan-sessions", h.orphanSessions)
}

// handlers groups every handler that needs Store access.
type handlers struct {
	store        *store.Store
	logger       *slog.Logger
	healthDetail map[string]any
}

// helpers ────────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Header already written; just log.
		slog.Error("writeJSON encode", "err", err)
	}
}

// errorResponse encodes an error as { "error": "..." }.
type errorResponse struct {
	Error string `json:"error"`
}

func writeErr(w http.ResponseWriter, err error) {
	status := statusForErr(err)
	writeJSON(w, status, errorResponse{Error: err.Error()})
}

func statusForErr(err error) int {
	switch {
	case errors.Is(err, store.ErrRepoNotFound), errors.Is(err, store.ErrBranchNotFound), errors.Is(err, store.ErrTabNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrTabProtected):
		return http.StatusForbidden
	case errors.Is(err, store.ErrInvalidArg):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// SPA fallback ───────────────────────────────────────────────────────────────

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
		http.Error(w, fmt.Sprintf("index.html not found: %v (run `make build`?)", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
