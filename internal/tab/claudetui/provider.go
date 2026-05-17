package claudetui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
)

// TabType is the stable provider identifier surfaced over the wire.
const TabType = "claude-tui"

// Provider wraps Manager and implements the tab.Provider interface so that
// the claude-tui tab participates in palmux2's branch lifecycle and HTTP
// routing machinery.
//
// Design decisions:
//   - NeedsTmuxWindow() == false — the daemon owns its own PTY, no tmux window.
//   - Protected() == true — always-present singleton per branch, not user-removable.
//   - Multiple() == false — exactly one claude-tui per branch.
//   - Daemon spawn is LAZY (priority_rule 4): OnBranchOpen registers the entry
//     in the Manager via EnsureDaemon but does NOT call EnsureStarted. The
//     subprocess only starts on the first WebSocket attach.
type Provider struct {
	manager *Manager
}

// New returns a Provider wrapping mgr.
func New(mgr *Manager) *Provider {
	return &Provider{manager: mgr}
}

// Type returns the stable tab-type identifier.
func (p *Provider) Type() string { return TabType }

// DisplayName returns the UI label shown in the TabBar.
func (p *Provider) DisplayName() string { return "Claude (TUI)" }

// Protected reports true — the user cannot delete this tab.
func (p *Provider) Protected() bool { return true }

// Multiple reports false — at most one claude-tui per branch.
func (p *Provider) Multiple() bool { return false }

// NeedsTmuxWindow reports false — the daemon manages its own PTY; no tmux
// window is created by the Store for this tab.
func (p *Provider) NeedsTmuxWindow() bool { return false }

// Conditional reports false — this tab is always present on every branch.
func (p *Provider) Conditional() bool { return false }

// Limits returns Min=1, Max=1 (singleton).
func (p *Provider) Limits(_ tab.SettingsView) tab.InstanceLimits {
	return tab.InstanceLimits{Min: 1, Max: 1}
}

// OnBranchOpen registers the branch with the Manager (creating a Daemon object
// in idle state) without spawning the subprocess.  The subprocess starts lazily
// on the first WebSocket attach (priority_rule 4).
func (p *Provider) OnBranchOpen(ctx context.Context, params tab.OpenParams) (tab.ProviderResult, error) {
	if params.Branch == nil {
		return tab.ProviderResult{}, nil
	}
	b := params.Branch
	// EnsureDaemon creates the Daemon object but does NOT spawn the subprocess.
	if _, err := p.manager.EnsureDaemon(ctx, b.RepoID, b.ID); err != nil {
		return tab.ProviderResult{}, fmt.Errorf("claudetui provider: ensure daemon: %w", err)
	}
	return tab.ProviderResult{
		Tabs: []domain.Tab{{
			ID:        TabType,
			Type:      TabType,
			Name:      p.DisplayName(),
			Protected: true,
			Multiple:  false,
		}},
	}, nil
}

// OnBranchClose shuts down and removes the Daemon for the branch.
func (p *Provider) OnBranchClose(ctx context.Context, params tab.CloseParams) error {
	if params.Branch == nil {
		return nil
	}
	b := params.Branch
	if err := p.manager.CloseDaemon(ctx, b.RepoID, b.ID); err != nil {
		return fmt.Errorf("claudetui provider: close daemon: %w", err)
	}
	return nil
}

// RegisterRoutes attaches the three claude-tui HTTP endpoints to mux.
//
//	WS    GET  /api/repos/{repoId}/branches/{branchId}/tabs/claude-tui/attach
//	JSON  GET  /api/repos/{repoId}/branches/{branchId}/tabs/claude-tui/stats
//	JSON  POST /api/repos/{repoId}/branches/{branchId}/tabs/claude-tui/resize
func (p *Provider) RegisterRoutes(mux *http.ServeMux, _ string) {
	const pfx = "/api/repos/{repoId}/branches/{branchId}/tabs/claude-tui"

	mux.Handle("GET "+pfx+"/attach", http.HandlerFunc(p.handleAttach))
	mux.Handle("GET "+pfx+"/stats", http.HandlerFunc(p.handleStats))
	mux.HandleFunc("POST "+pfx+"/resize", p.handleResize)
}

// handleAttach upgrades to WebSocket and attaches to the per-branch Daemon.
func (p *Provider) handleAttach(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	d, err := p.manager.EnsureDaemon(r.Context(), repoID, branchID)
	if err != nil {
		http.Error(w, "daemon error", http.StatusInternalServerError)
		slog.Error("claudetui: handleAttach EnsureDaemon", "err", err)
		return
	}
	AttachHandler(d).ServeHTTP(w, r)
}

// handleStats serves a JSON Stats snapshot for the per-branch Daemon.
func (p *Provider) handleStats(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	d, err := p.manager.EnsureDaemon(r.Context(), repoID, branchID)
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
// terminal resize to the PTY.  Returns 204 No Content on success.
func (p *Provider) handleResize(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	d := p.manager.Get(repoID, branchID)
	if d == nil {
		http.Error(w, "no daemon for branch", http.StatusNotFound)
		return
	}
	var body resizeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := d.Resize(body.Cols, body.Rows); err != nil {
		http.Error(w, "resize error: "+err.Error(), http.StatusInternalServerError)
		slog.Warn("claudetui: resize", "repoId", repoID, "branchId", branchID, "err", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
