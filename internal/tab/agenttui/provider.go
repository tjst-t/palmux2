package agenttui

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/tjst-t/palmux2/internal/tab"
)

// TabType was the historical Type identifier surfaced over the wire as a
// distinct "claude-tui" tab. Sadf90e collapses this: there is only one
// "claude" tab type and the mode (agent vs tui) is a per-tab setting.
// The constant is kept (= "claude-tui") so existing log breadcrumbs / WS
// path fragments remain identifiable, but it is NOT the Provider's Type().
const TabType = "claude-tui"

// Provider is a SERVICE PARTICIPANT — it contributes no tabs. It takes part
// in the branch lifecycle solely to (a) tear down PTY daemons when a branch
// closes or its runtime is recreated and (b) host the per-tab WS / stats /
// resize HTTP endpoints. The visible Claude tab is owned by the claudeagent
// Provider; when its mode is "tui" the FE opens a WebSocket against the
// routes registered here.
//
// ADR-0012: this used to be registered as a tab.Provider with
// Conditional()==true and an OnBranchOpen that returned zero tabs — not
// because it had conditional visibility, but purely so the Store's recompute
// loop would call its lifecycle hook. That made a visibility flag double as a
// subscription mechanism. It is now registered via Registry.RegisterService
// as a plain tab.Participant, so it never appears in the tab-derivation path
// at all.
//
// Design decisions:
//   - Type() == "claude-tui" — retained for legacy log clarity; the registry
//     uses this only as a unique key, never as a user-visible tab type.
//   - Daemon spawn is LAZY (priority_rule 4): the first WS attach to a tab
//     creates the daemon; nothing happens at branch-open time.
type Provider struct {
	manager  *Manager
	resolver WorktreeResolver // set once at boot via SetWorktreeResolver
}

// New returns a Provider wrapping mgr.
func New(mgr *Manager) *Provider { return &Provider{manager: mgr} }

var (
	_ tab.Participant        = (*Provider)(nil) // ADR-0012: service, not a tab provider
	_ tab.RuntimeRestartHook = (*Provider)(nil) // S4d8b1c-fix
)

// Type returns the stable registry key. Not user-visible.
func (p *Provider) Type() string { return TabType }

// OnBranchClose tears down every Daemon that belongs to this branch.
// Idempotent — no-op when the Manager holds no entries for the branch.
func (p *Provider) OnBranchClose(ctx context.Context, params tab.CloseParams) error {
	if params.Branch == nil {
		return nil
	}
	p.manager.CloseBranchDaemons(ctx, params.Branch.RepoID, params.Branch.ID)
	return nil
}

// OnBranchRuntimeRestarted tears the claude-tui daemons down when the workspace
// runtime is recreated (container regenerate / host↔incus switch). The in-
// container claude is bound to the now-destroyed container; closing the daemon
// makes the next WS attach respawn it against the new runtime. Without this the
// daemon stays stuck on the dead container — a garbled screen with no input.
// (S4d8b1c-fix)
func (p *Provider) OnBranchRuntimeRestarted(ctx context.Context, params tab.CloseParams) error {
	if params.Branch == nil {
		return nil
	}
	p.manager.CloseBranchDaemons(ctx, params.Branch.RepoID, params.Branch.ID)
	return nil
}

// RegisterRoutes attaches the claude-tui HTTP endpoints to mux. All paths are
// keyed by {tabId} so multiple Claude(tui) tabs on the same branch get
// independent WS / stats / resize endpoints.
//
//	WS    GET  /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/tui/attach
//	JSON  GET  /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/tui/stats
//	JSON  POST /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/tui/resize
func (p *Provider) RegisterRoutes(mux *http.ServeMux, _ string) {
	const pfx = "/api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/tui"

	mux.Handle("GET "+pfx+"/attach", http.HandlerFunc(p.handleAttach))
	mux.Handle("GET "+pfx+"/stats", http.HandlerFunc(p.handleStats))
	mux.HandleFunc("POST "+pfx+"/resize", p.handleResize)
}

// handleAttach upgrades to WebSocket and attaches to the per-tab Daemon.
//
// The daemon is created lazily on the first attach. We resolve the worktree
// path via the storeQuery callback the Provider was wired with — for now we
// pass an empty worktree which means the daemon inherits the server cwd.
// (Provider takes the storeQuery in a follow-up patch; for now the daemon
// is created without session-watcher support and resume relies entirely on
// claude's own --resume bookkeeping.)
func (p *Provider) handleAttach(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	tabID := r.PathValue("tabId")
	worktree := p.resolveWorktree(repoID, branchID)

	d, err := p.manager.EnsureDaemon(r.Context(), repoID, branchID, tabID, worktree)
	if err != nil {
		http.Error(w, "daemon error", http.StatusInternalServerError)
		slog.Error("claudetui: handleAttach EnsureDaemon", "err", err)
		return
	}
	AttachHandler(d).ServeHTTP(w, r)
}

// handleStats serves a JSON Stats snapshot for the per-tab Daemon.
func (p *Provider) handleStats(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	tabID := r.PathValue("tabId")
	worktree := p.resolveWorktree(repoID, branchID)

	d, err := p.manager.EnsureDaemon(r.Context(), repoID, branchID, tabID, worktree)
	if err != nil {
		http.Error(w, "daemon error", http.StatusInternalServerError)
		slog.Error("claudetui: handleStats EnsureDaemon", "err", err)
		return
	}
	StatsHandler(d).ServeHTTP(w, r)
}

// resizeBody is the JSON body for POST …/resize.
type resizeBody struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// handleResize decodes {"cols":<uint16>,"rows":<uint16>} and propagates a
// terminal resize to the PTY. Returns 204 No Content on success.
func (p *Provider) handleResize(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	tabID := r.PathValue("tabId")
	d := p.manager.Get(repoID, branchID, tabID)
	if d == nil {
		http.Error(w, "no daemon for tab", http.StatusNotFound)
		return
	}
	var body resizeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := d.Resize(body.Cols, body.Rows); err != nil {
		http.Error(w, "resize error: "+err.Error(), http.StatusInternalServerError)
		slog.Warn("claudetui: resize",
			"repoId", repoID, "branchId", branchID, "tabId", tabID, "err", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// WorktreeResolver is wired by cmd/palmux/main.go so the Provider can ask the
// Store for a branch's worktree path at attach-time. Decoupled from the Store
// type via this interface so the package keeps an empty internal-import set.
type WorktreeResolver interface {
	BranchWorktreePath(repoID, branchID string) string
}

// SetWorktreeResolver installs the lookup used by handleAttach / handleStats.
// Called once at server boot from main.go.
func (p *Provider) SetWorktreeResolver(r WorktreeResolver) { p.resolver = r }

// resolveWorktree is a small helper around the optional resolver: empty
// string is acceptable (daemon falls back to inheriting the server cwd).
func (p *Provider) resolveWorktree(repoID, branchID string) string {
	if p.resolver == nil {
		return ""
	}
	return p.resolver.BranchWorktreePath(repoID, branchID)
}
