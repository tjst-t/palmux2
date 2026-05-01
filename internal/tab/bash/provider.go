// Package bash provides the Bash tab — a non-protected, multi-instance,
// terminal-backed tab that just opens a default shell in the branch's
// worktree.
package bash

import (
	"context"
	"net/http"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
)

// TabType is the stable provider identifier.
const TabType = "bash"

// Provider implements tab.Provider for Bash.
type Provider struct{}

// New returns a Provider.
func New() *Provider { return &Provider{} }

func (p *Provider) Type() string          { return TabType }
func (p *Provider) DisplayName() string   { return "Bash" }
func (p *Provider) Protected() bool       { return false }
func (p *Provider) Multiple() bool        { return true }
func (p *Provider) NeedsTmuxWindow() bool { return true }

func (p *Provider) OnBranchOpen(_ context.Context, _ tab.OpenParams) (tab.ProviderResult, error) {
	// Initial open creates exactly one Bash tab using the canonical
	// "palmux:bash:bash" name. Additional Bash tabs are added via
	// POST /api/repos/{repoId}/branches/{branchId}/tabs (Phase 1 server).
	windowName := domain.WindowName(TabType, "bash")
	return tab.ProviderResult{
		Tabs: []domain.Tab{{
			ID:         domain.TabID(TabType, "bash"),
			Type:       TabType,
			Name:       p.DisplayName(),
			Protected:  false,
			Multiple:   true,
			WindowName: windowName,
		}},
		Windows: []tab.WindowSpec{{
			Name: windowName,
			// no Command → tmux uses the default shell
		}},
	}, nil
}

func (p *Provider) OnBranchClose(_ context.Context, _ tab.CloseParams) error {
	return nil
}

func (p *Provider) RegisterRoutes(_ *http.ServeMux, _ string) {
	// Bash needs no REST endpoints beyond the generic terminal attach WS.
}
