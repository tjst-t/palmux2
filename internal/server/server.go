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
	"github.com/tjst-t/palmux2/internal/commands"
	"github.com/tjst-t/palmux2/internal/notify"
	"github.com/tjst-t/palmux2/internal/portman"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// Deps bundles dependencies for NewMux.
type Deps struct {
	Store        *store.Store
	Auth         *auth.Authenticator
	Tmux         tmux.Client
	Commands     *commands.Detector
	Notify       *notify.Hub
	Portman      *portman.Client
	FrontendFS   fs.FS // embedded SPA bundle
	StaticFS     fs.FS // embedded third-party assets (e.g. drawio webapp); served at /static/* (S010)
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
	// S010: serve bundled OSS assets (currently the drawio webapp used by
	// the Files-tab `.drawio` viewer) under `/static/`. No auth gate — the
	// content is public and the SPA needs to reference it from an iframe
	// before any session cookie negotiation. Strip the `/static/` prefix
	// so the embed FS sees `internal/static/<rest>`.
	if deps.StaticFS != nil {
		root.Handle("/static/", staticHandler(deps.StaticFS))
	}
	root.Handle("/", spaHandler(deps.FrontendFS, deps.BasePath))
	return root
}

// registerRoutes attaches every Phase 1 API endpoint to the mux. Phase 2+
// add their handlers in their own files but use the same mux.
func registerRoutes(mux *http.ServeMux, deps Deps) {
	h := &handlers{
		store:        deps.Store,
		logger:       deps.Logger,
		healthDetail: deps.HealthDetail,
		commands:     deps.Commands,
		notify:       deps.Notify,
		portman:      deps.Portman,
	}

	mux.HandleFunc("GET /api/health", h.health)

	mux.HandleFunc("GET /api/repos", h.listRepos)
	mux.HandleFunc("GET /api/repos/available", h.availableRepos)
	// S030: clone must be registered BEFORE {repoId}/open to avoid ambiguity.
	mux.HandleFunc("POST /api/repos/clone", h.cloneRepo)
	mux.HandleFunc("POST /api/repos/{repoId}/open", h.openRepo)
	mux.HandleFunc("POST /api/repos/{repoId}/close", h.closeRepo)
	mux.HandleFunc("POST /api/repos/{repoId}/star", h.star)
	mux.HandleFunc("POST /api/repos/{repoId}/unstar", h.unstar)
	// S030: delete-preview and permanent delete.
	mux.HandleFunc("GET /api/repos/{repoId}/delete-preview", h.deletePreview)
	mux.HandleFunc("DELETE /api/repos/{repoId}", h.deleteRepo)

	mux.HandleFunc("GET /api/repos/{repoId}/branches", h.listBranches)
	mux.HandleFunc("GET /api/repos/{repoId}/branch-picker", h.branchPicker)
	mux.HandleFunc("POST /api/repos/{repoId}/branches/open", h.openBranch)
	mux.HandleFunc("DELETE /api/repos/{repoId}/branches/{branchId}", h.closeBranch)
	// S015: promote / demote a branch into / out of `userOpenedBranches`.
	// Promote is the `+ Add to my worktrees` button in the Drawer; demote
	// is the symmetric undo (used by the context menu and reserved for
	// the future subagent → unmanaged demotion flow).
	mux.HandleFunc("POST /api/repos/{repoId}/branches/{branchId}/promote", h.promoteBranch)
	mux.HandleFunc("DELETE /api/repos/{repoId}/branches/{branchId}/promote", h.demoteBranch)
	// S023: per-repo last-active-branch persistence. Body `{branch: "<name>"}`
	// (empty string clears). Idempotent; emits `branch.lastActiveChanged`
	// on actual change.
	mux.HandleFunc("PATCH /api/repos/{repoId}/last-active-branch", h.setLastActiveBranch)
	// S021: subagent lifecycle. cleanup-subagent operates on a repo
	// (since it spans every branch); promote-subagent is per-branch.
	mux.HandleFunc("POST /api/repos/{repoId}/worktrees/cleanup-subagent", h.cleanupSubagentWorktrees)
	mux.HandleFunc("POST /api/repos/{repoId}/branches/{branchId}/promote-subagent", h.promoteSubagentBranch)

	mux.HandleFunc("GET /api/repos/{repoId}/branches/{branchId}/commands", h.listCommands)
	mux.HandleFunc("GET /api/repos/{repoId}/branches/{branchId}/portman", h.listRepoPortman)

	mux.HandleFunc("GET /api/repos/{repoId}/branches/{branchId}/tabs", h.listTabs)
	mux.HandleFunc("POST /api/repos/{repoId}/branches/{branchId}/tabs", h.addTab)
	// S020: order PUT must be registered BEFORE the {tabId} pattern below;
	// Go's http.ServeMux uses path-pattern specificity but registering
	// in this order makes the intent obvious.
	mux.HandleFunc("PUT /api/repos/{repoId}/branches/{branchId}/tabs/order", h.reorderTabs)
	mux.HandleFunc("DELETE /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}", h.removeTab)
	mux.HandleFunc("PATCH /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}", h.renameTab)

	mux.HandleFunc("GET /api/settings", h.getSettings)
	mux.HandleFunc("PATCH /api/settings", h.patchSettings)

	mux.HandleFunc("GET /api/connections", h.connections)
	mux.HandleFunc("GET /api/orphan-sessions", h.orphanSessions)

	mux.HandleFunc("GET /api/notifications", h.listNotifications)
	mux.HandleFunc("POST /api/notify", h.ingestNotification)
	mux.HandleFunc("POST /api/notify/clear", h.clearNotifications)

	// Per-branch attachment upload (S008). Files land in
	// `<attachmentUploadDir>/<repoId>/<branchId>/`. The fetch route is
	// per-branch too; the legacy `/api/upload/{name}` GET is retained
	// for embeds that only carry the basename (older `[image: ...]`
	// references in transcripts).
	mux.HandleFunc("POST /api/repos/{repoId}/branches/{branchId}/upload", h.uploadAttachment)
	mux.HandleFunc("GET /api/repos/{repoId}/branches/{branchId}/upload/{name}", h.fetchBranchUpload)
	mux.HandleFunc("POST /api/upload", h.uploadGlobal)
	mux.HandleFunc("GET /api/upload/{name}", h.fetchUpload)
}

// handlers groups every handler that needs Store access.
type handlers struct {
	store        *store.Store
	logger       *slog.Logger
	healthDetail map[string]any
	commands     *commands.Detector
	notify       *notify.Hub
	portman      *portman.Client
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
	// S009: AddTab over the cap and RemoveTab below the floor return
	// 409 Conflict so the FE can disable the `+` button / suppress the
	// Close menu item with a precise reason.
	case errors.Is(err, store.ErrTabLimit):
		return http.StatusConflict
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
		// Hashed assets get long-lived cache headers; everything else stays
		// fresh because the SPA may need to re-fetch.
		if strings.HasPrefix(path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
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

// staticHandler serves /static/* from the embedded `internal/static` tree.
// Used by S010 to ship the drawio webapp bundle (~21 MB) without an external
// CDN. The `internal/static` prefix is preserved inside the embed but
// stripped from the request path so an `index.html` lookup at `/static/drawio/`
// hits `internal/static/drawio/index.html`.
func staticHandler(staticFS fs.FS) http.Handler {
	// fs.Sub strips the `internal/static/` prefix the embed left in the
	// FS so request paths line up with the on-disk layout. Errors here
	// only happen when the directive is wrong; default to a 404 handler
	// so the server still boots.
	sub, err := fs.Sub(staticFS, "internal/static")
	if err != nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Aggressive caching is fine — the assets are pinned to a known
		// upstream commit (see internal/static/drawio/README.md).
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		// Allow the SPA to load this in an iframe under the same origin.
		// The drawio webapp itself sets X-Frame-Options at runtime via
		// JS; nothing here needs to be done.
		r2 := r.Clone(r.Context())
		r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/static")
		if r2.URL.Path == "" {
			r2.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r2)
	})
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
