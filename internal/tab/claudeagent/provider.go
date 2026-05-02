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
	"strings"

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

func (p *Provider) Type() string          { return TabType }
func (p *Provider) DisplayName() string   { return "Claude" }
func (p *Provider) Protected() bool       { return true }
func (p *Provider) Multiple() bool        { return true } // S009
func (p *Provider) NeedsTmuxWindow() bool { return false }
func (p *Provider) Conditional() bool     { return false }

// Limits — at least one Claude tab is always present; the upper bound is
// settings-driven (maxClaudeTabsPerBranch, default 3). Closing the last
// Claude tab is forbidden so a branch always has a default conversation
// surface — same shape as Files/Git, with Min=1=Max=1 there.
func (p *Provider) Limits(view tab.SettingsView) tab.InstanceLimits {
	max := 3
	if view != nil {
		if n := view.MaxClaudeTabsPerBranch(); n > 0 {
			max = n
		}
	}
	return tab.InstanceLimits{Min: 1, Max: max}
}

// OnBranchOpen is intentionally lazy — we don't spawn a CLI until the user
// actually sends a message. The Claude tab is non-tmux so the Store can't
// derive its instances from `tmux ls windows`; instead we ask the Manager
// (which reads the persisted set from sessions.json). On a fresh branch
// the Manager auto-seeds a single canonical tab ID `claude:claude`.
func (p *Provider) OnBranchOpen(_ context.Context, params tab.OpenParams) (tab.ProviderResult, error) {
	if params.Branch == nil {
		return tab.ProviderResult{}, nil
	}
	tabIDs := p.manager.tabsForBranch(params.Branch.RepoID, params.Branch.ID)
	tabs := make([]domain.Tab, 0, len(tabIDs))
	for _, id := range tabIDs {
		tabs = append(tabs, domain.Tab{
			ID:        id,
			Type:      TabType,
			Name:      DisplayNameForTab(id),
			Protected: true,
			Multiple:  true,
		})
	}
	return tab.ProviderResult{Tabs: tabs}, nil
}

// OnBranchClose terminates every Claude agent owned by this branch.
func (p *Provider) OnBranchClose(ctx context.Context, params tab.CloseParams) error {
	if params.Branch == nil {
		return nil
	}
	return p.manager.KillBranch(ctx, params.Branch.RepoID, params.Branch.ID)
}

// DisplayNameForTab maps a Claude tab ID to its UI label. The canonical
// tab id is `claude:claude` (or legacy `claude`) and renders as "Claude".
// Subsequent tabs `claude:claude-2`, `claude:claude-3` render as "Claude
// 2", "Claude 3".
func DisplayNameForTab(tabID string) string {
	suffix := tabID
	if i := strings.IndexByte(tabID, ':'); i >= 0 {
		suffix = tabID[i+1:]
	}
	if suffix == "" || suffix == TabType {
		return "Claude"
	}
	if strings.HasPrefix(suffix, TabType+"-") {
		return "Claude " + strings.TrimPrefix(suffix, TabType+"-")
	}
	return suffix
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

	// Legacy single-tab prefix — kept as backward-compat for clients that
	// haven't migrated to the explicit `claude:claude` tab id. The handlers
	// resolve a missing/legacy `tabId` PathValue to the canonical
	// "claude:claude" tab.
	const legacyPrefix = "/api/repos/{repoId}/branches/{branchId}/tabs/claude"
	mux.HandleFunc("GET "+legacyPrefix+"/agent", h.handleWS)
	mux.HandleFunc("GET "+legacyPrefix+"/sessions", h.handleListBranchSessions)
	mux.HandleFunc("POST "+legacyPrefix+"/permission/{permissionId}", h.handleAnswerPermission)
	mux.HandleFunc("GET "+legacyPrefix+"/settings", h.handleGetSettings)
	mux.HandleFunc("DELETE "+legacyPrefix+"/settings/permissions/allow", h.handleDeleteSettingsAllow)
	mux.HandleFunc("GET "+legacyPrefix+"/prefs", h.handleGetBranchPrefs)
	mux.HandleFunc("PATCH "+legacyPrefix+"/prefs", h.handlePatchBranchPrefs)
	// S019: rewind endpoint. The body carries turnId + newMessage; the
	// server archives the prior version + truncates the live conversation
	// at the rewind boundary, then SendUserMessages newMessage to the CLI.
	mux.HandleFunc("POST "+legacyPrefix+"/sessions/rewind", h.handleRewindSession)

	// S009 multi-tab routes — `tabId` is e.g. "claude:claude-2". The
	// Go ServeMux accepts colons in path segments; the handlers extract
	// `tabId` via PathValue and route to the right Agent. We register the
	// suffixes that don't collide with the generic terminal `/attach`
	// route (which is owned by the server-level WS handler for Bash etc).
	const tabPrefix = "/api/repos/{repoId}/branches/{branchId}/tabs/{tabId}"
	mux.HandleFunc("GET "+tabPrefix+"/agent", h.handleWS)
	mux.HandleFunc("GET "+tabPrefix+"/claude/sessions", h.handleListBranchSessions)
	mux.HandleFunc("POST "+tabPrefix+"/claude/permission/{permissionId}", h.handleAnswerPermission)
	mux.HandleFunc("GET "+tabPrefix+"/claude/settings", h.handleGetSettings)
	mux.HandleFunc("DELETE "+tabPrefix+"/claude/settings/permissions/allow", h.handleDeleteSettingsAllow)
	mux.HandleFunc("GET "+tabPrefix+"/claude/prefs", h.handleGetBranchPrefs)
	mux.HandleFunc("PATCH "+tabPrefix+"/claude/prefs", h.handlePatchBranchPrefs)
	mux.HandleFunc("POST "+tabPrefix+"/claude/sessions/rewind", h.handleRewindSession)

	mux.HandleFunc("GET /api/claude/auth-status", h.handleAuthStatus)
	mux.HandleFunc("GET /api/claude/modes", h.handleModes)
	mux.HandleFunc("GET /api/sessions", h.handleListAllSessions)
	mux.HandleFunc("GET /api/sessions/{sessionId}", h.handleGetSession)
	mux.HandleFunc("DELETE /api/sessions/{sessionId}", h.handleDeleteSession)
	mux.HandleFunc("PATCH /api/sessions/{sessionId}", h.handlePatchSession)
}
