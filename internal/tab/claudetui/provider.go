package claudetui

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

// Provider is a "service" provider — it does NOT create tabs of its own. It
// participates in the tab.Provider lifecycle solely to (a) tear down PTY
// daemons when a branch closes and (b) host the per-tab WS / stats / resize
// HTTP endpoints. The visible Claude tab is owned by the claudeagent
// Provider (Multiple=true); when its mode is "tui" the FE opens a
// WebSocket against the routes registered here.
//
// Design decisions (Sadf90e):
//   - Type() == "claude-tui" — retained for legacy log clarity; the registry
//     uses this only as a unique key, never as a user-visible tab type.
//   - Multiple() == false — irrelevant (returns 0 tabs from OnBranchOpen).
//   - Conditional() == true with OnBranchOpen returning 0 tabs — keeps the
//     Provider invisible in the TabBar while still participating in the
//     lifecycle hooks.
//   - NeedsTmuxWindow() == false — the daemon owns its own PTY.
//   - Daemon spawn is LAZY (priority_rule 4): the first WS attach to a tab
//     creates the daemon; nothing happens at branch-open time.
type Provider struct {
	manager  *Manager
	resolver WorktreeResolver // set once at boot via SetWorktreeResolver
}

// New returns a Provider wrapping mgr.
func New(mgr *Manager) *Provider {
	return &Provider{manager: mgr}
}

// Type returns the stable registry key. Not user-visible since Sadf90e.
func (p *Provider) Type() string { return TabType }

// DisplayName is unused (Provider returns 0 tabs) but kept for diagnostics.
func (p *Provider) DisplayName() string { return "Claude (TUI runtime)" }

// Protected is unused (no tabs surfaced).
func (p *Provider) Protected() bool { return true }

// Multiple is unused (no tabs surfaced).
func (p *Provider) Multiple() bool { return false }

// NeedsTmuxWindow reports false — the daemon manages its own PTY.
func (p *Provider) NeedsTmuxWindow() bool { return false }

// Conditional reports true so the registry calls OnBranchOpen during
// store.recomputeTabs (the short-circuit branch for non-tmux singletons
// would skip non-Conditional providers entirely). We return 0 tabs from
// OnBranchOpen so the user-visible tab list does not gain a "Claude (TUI)"
// entry, but the lifecycle hook still gets the chance to run.
func (p *Provider) Conditional() bool { return true }

// Limits returns Min=0, Max=0 — this Provider never creates tabs of its own.
func (p *Provider) Limits(_ tab.SettingsView) tab.InstanceLimits {
	return tab.InstanceLimits{Min: 0, Max: 0}
}

// OnBranchOpen returns no tabs. The Provider exists only as a runtime for
// the WS / stats / resize endpoints; the visible Claude tab is owned by the
// claudeagent Provider.
func (p *Provider) OnBranchOpen(_ context.Context, _ tab.OpenParams) (tab.ProviderResult, error) {
	return tab.ProviderResult{}, nil
}

// OnBranchClose tears down every Daemon that belongs to this branch.
// Idempotent — no-op when the Manager holds no entries for the branch.
func (p *Provider) OnBranchClose(ctx context.Context, params tab.CloseParams) error {
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
