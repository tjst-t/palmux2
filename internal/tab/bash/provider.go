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

// Limits — Bash always has at least one tab; the upper bound is
// settings-driven (maxBashTabsPerBranch). A nil view falls through to a
// safe default of 5 so tests / partially-wired setups don't blow up.
func (p *Provider) Limits(view tab.SettingsView) tab.InstanceLimits {
	max := 5
	if view != nil {
		if n := view.MaxBashTabsPerBranch(); n > 0 {
			max = n
		}
	}
	return tab.InstanceLimits{Min: 1, Max: max}
}

// Tabs turns the tmux window suffixes the Store resolved for this provider
// into Bash tabs, one per window, preserving tmux index order so user-added
// tabs keep stable positions.
//
// Pure (ADR-0012): every input arrives via params. The Store owns the tmux
// policy — transient-ListWindows-failure fallback (S009-fix-1) and canonical
// seeding — so this stays a plain mapping.
func (p *Provider) Tabs(_ context.Context, params tab.TabsParams) ([]domain.Tab, error) {
	out := make([]domain.Tab, 0, len(params.Windows))
	for _, n := range params.Windows {
		out = append(out, domain.Tab{
			ID:         domain.TabID(TabType, n),
			Type:       TabType,
			Name:       tab.InstanceDisplayName(p.DisplayName(), TabType, n),
			Protected:  false,
			Multiple:   true,
			WindowName: domain.WindowName(TabType, n),
		})
	}
	return out, nil
}

// OnBranchOpen declares the canonical Bash tmux window. Additional Bash
// windows are created via POST /tabs. Tabs are declared by Tabs (ADR-0012).
func (p *Provider) OnBranchOpen(_ context.Context, _ tab.OpenParams) (tab.ProviderResult, error) {
	return tab.ProviderResult{
		Windows: []tab.WindowSpec{{
			Name: domain.WindowName(TabType, "bash"),
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
