// Package claudeagent implements the Claude tab — a non-tmux,
// WebSocket-only chat UI on top of `claude` CLI's stream-json mode.
//
// The package directory is named `claudeagent` for historical reasons
// (it once stood next to a tmux-backed `claude` tab); the user-facing
// type is just "claude".
package claudeagent

import (
	"context"
	"net/http"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
)

// TabType is the stable identifier surfaced over the wire.
const TabType = "claude"

// Provider is the tab.Provider for claude-agent. It owns no state of its
// own; all per-branch lifecycle is delegated to the Manager.
type Provider struct {
	manager *Manager
}

// NewProvider returns a Provider wrapping mgr.
func NewProvider(mgr *Manager) *Provider {
	return &Provider{manager: mgr}
}

func (p *Provider) Type() string         { return TabType }
func (p *Provider) DisplayName() string  { return "Claude" }
func (p *Provider) Protected() bool      { return true }
func (p *Provider) Multiple() bool       { return false }
func (p *Provider) NeedsTmuxWindow() bool { return false }

// OnBranchOpen is intentionally lazy — we don't spawn a CLI until the user
// actually sends a message.
func (p *Provider) OnBranchOpen(_ context.Context, _ tab.OpenParams) (tab.ProviderResult, error) {
	return tab.ProviderResult{
		Tabs: []domain.Tab{{
			ID:        TabType,
			Type:      TabType,
			Name:      p.DisplayName(),
			Protected: true,
		}},
	}, nil
}

// OnBranchClose terminates the agent for that branch (if any).
func (p *Provider) OnBranchClose(ctx context.Context, params tab.CloseParams) error {
	if params.Branch == nil {
		return nil
	}
	return p.manager.KillBranch(ctx, params.Branch.RepoID, params.Branch.ID)
}

// RegisterRoutes attaches every Claude-tab endpoint. The caller-supplied
// `prefix` doesn't fit our WS path; per spec §5.1 the WS goes under
// `…/tabs/claude/agent` (matching the rest of the per-tab API), so we
// ignore the prefix and register at absolute paths.
//
//	WS     /api/repos/{repoId}/branches/{branchId}/tabs/claude/agent
//	GET    /api/repos/{repoId}/branches/{branchId}/tabs/claude/sessions
//	GET    /api/claude/auth-status
//	GET    /api/claude/modes
//	GET    /api/sessions                          (?repo=&branch=)
//	GET    /api/sessions/{sessionId}
//	PATCH  /api/sessions/{sessionId}
//	DELETE /api/sessions/{sessionId}
func (p *Provider) RegisterRoutes(mux *http.ServeMux, _ string) {
	h := newHTTPHandler(p.manager)

	const branchPrefix = "/api/repos/{repoId}/branches/{branchId}/tabs/claude"
	mux.HandleFunc("GET "+branchPrefix+"/agent", h.handleWS)
	mux.HandleFunc("GET "+branchPrefix+"/sessions", h.handleListBranchSessions)
	// Out-of-band permission answer (used by Activity Inbox so it can
	// resolve a request without opening the Claude WS).
	mux.HandleFunc("POST "+branchPrefix+"/permission/{permissionId}", h.handleAnswerPermission)
	// .claude/settings.json viewer / editor (S002).
	mux.HandleFunc("GET "+branchPrefix+"/settings", h.handleGetSettings)
	mux.HandleFunc("DELETE "+branchPrefix+"/settings/permissions/allow", h.handleDeleteSettingsAllow)

	mux.HandleFunc("GET /api/claude/auth-status", h.handleAuthStatus)
	mux.HandleFunc("GET /api/claude/modes", h.handleModes)
	mux.HandleFunc("GET /api/sessions", h.handleListAllSessions)
	mux.HandleFunc("GET /api/sessions/{sessionId}", h.handleGetSession)
	mux.HandleFunc("DELETE /api/sessions/{sessionId}", h.handleDeleteSession)
	mux.HandleFunc("PATCH /api/sessions/{sessionId}", h.handlePatchSession)
}
