// Package ports implements the "Ports" tab module (See8bd4-3).
//
// The Ports tab is **conditional**: it appears in a workspace's TabBar only
// when the workspace runtime is incus-container (host workspaces publish ports
// via portman, not palmux). It lets the user see the container's listening
// ports and publish/unpublish each one as an HTTPS subdomain
// (<port>--<workspace>--<repo>.<base>) via the Caddy admin API.
//
// REST endpoints (under /api/repos/{repoId}/branches/{branchId}/ports):
//
//	GET    .../ports                  → {runtimeKind, ports:[PortView]}
//	POST   .../ports/{port}/expose    body {public} → {port, public, publicUrl}
//	DELETE .../ports/{port}/expose    → 204
//
// Visibility is recomputed when the workspace runtime switches host↔incus: the
// runtime PATCH handler calls store.RecomputeBranchTabs, which re-runs
// OnBranchOpen and emits tab.added / tab.removed.
package ports

import (
	"context"
	"net/http"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tab"
)

// TabType is the stable provider identifier.
const TabType = "ports"

// Provider implements tab.Provider for the Ports tab.
type Provider struct {
	st *store.Store
}

// New returns a Provider with a Store reference for runtime resolution and port
// publishing.
func New(s *store.Store) *Provider { return &Provider{st: s} }

func (p *Provider) Type() string          { return TabType }
func (p *Provider) DisplayName() string   { return "Ports" }
func (p *Provider) Protected() bool       { return false }
func (p *Provider) Multiple() bool        { return false }
func (p *Provider) NeedsTmuxWindow() bool { return false }

// Conditional — the Ports tab hides itself on host-runtime workspaces.
func (p *Provider) Conditional() bool { return true }

// Limits — singleton when present.
func (p *Provider) Limits(_ tab.SettingsView) tab.InstanceLimits {
	return tab.InstanceLimits{Min: 1, Max: 1}
}

// OnBranchOpen returns a single Ports tab iff the workspace runtime is
// incus-container; otherwise no tabs.
func (p *Provider) OnBranchOpen(_ context.Context, params tab.OpenParams) (tab.ProviderResult, error) {
	if params.Branch == nil {
		return tab.ProviderResult{}, nil
	}
	if !p.isIncus(params.Branch.RepoID, params.Branch.ID) {
		return tab.ProviderResult{}, nil
	}
	return tab.ProviderResult{
		Tabs: []domain.Tab{{
			ID:        TabType,
			Type:      TabType,
			Name:      p.DisplayName(),
			Protected: false,
			Multiple:  false,
		}},
	}, nil
}

// OnBranchClose is a no-op — the Ports tab holds no per-branch resources.
func (p *Provider) OnBranchClose(_ context.Context, _ tab.CloseParams) error { return nil }

// isIncus reports whether the workspace's resolved runtime is incus-container.
func (p *Provider) isIncus(repoID, branchID string) bool {
	reg := p.st.RuntimeRegistry()
	if reg == nil {
		return false
	}
	rt := reg.Get(repoID, branchID)
	return rt != nil && rt.Kind() == runtime.KindIncusContainer
}

// RegisterRoutes wires the list + expose/unexpose endpoints.
func (p *Provider) RegisterRoutes(mux *http.ServeMux, prefix string) {
	h := newHandler(p.st)
	mux.HandleFunc("GET "+prefix, h.list)
	mux.HandleFunc("POST "+prefix+"/{port}/expose", h.expose)
	mux.HandleFunc("DELETE "+prefix+"/{port}/expose", h.unexpose)
}
