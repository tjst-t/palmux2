// Package browser — tab.Provider for the Browser tab (S62374c-1).
//
// The Browser tab is conditional: it appears only in incus-container workspaces.
// Host-runtime workspaces do not show it (portman is the host-side tool; Caddy
// port publishing is the incus-side tool; a Browser tab would have nowhere
// useful to point on host).
//
// REST routes (under /api/repos/{repoId}/branches/{branchId}/tabs/browser):
//
//	GET  .../browser/state  → StateView
//	POST .../browser/start  → StartResponse
//	POST .../browser/stop   → StopResponse
//
// Story 2 (CDP screencast / navigate via WS) — TODO: implement in the next
// story once the screencast sprint is scheduled.
package browser

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/runtime"
	incuspkg "github.com/tjst-t/palmux2/internal/runtime/incus"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tab"
)

// TabType is the stable provider identifier.
const TabType = "browser"

// Provider implements tab.Provider for the Browser tab.
type Provider struct {
	st  *store.Store
	log *slog.Logger

	// managers maps "repoID/branchID" → *Manager (created lazily on OnBranchOpen).
	manMu    sync.Mutex
	managers map[string]*Manager
}

// New returns a Provider.
func New(s *store.Store) *Provider {
	return &Provider{
		st:       s,
		log:      slog.Default(),
		managers: map[string]*Manager{},
	}
}

func (p *Provider) Type() string          { return TabType }
func (p *Provider) DisplayName() string   { return "Browser" }
func (p *Provider) Protected() bool       { return false }
func (p *Provider) Multiple() bool        { return false }
func (p *Provider) NeedsTmuxWindow() bool { return false }

// Conditional — the Browser tab hides itself on host-runtime workspaces.
func (p *Provider) Conditional() bool { return true }

// Limits — singleton when present.
func (p *Provider) Limits(_ tab.SettingsView) tab.InstanceLimits {
	return tab.InstanceLimits{Min: 1, Max: 1}
}

// OnBranchOpen returns a single Browser tab iff the workspace runtime is
// incus-container; otherwise no tabs.
func (p *Provider) OnBranchOpen(_ context.Context, params tab.OpenParams) (tab.ProviderResult, error) {
	if params.Branch == nil {
		return tab.ProviderResult{}, nil
	}
	if !p.isIncus(params.Branch.RepoID, params.Branch.ID) {
		return tab.ProviderResult{}, nil
	}

	// Ensure a Manager exists for this workspace.
	p.getOrCreateManager(params.Branch.RepoID, params.Branch.ID)

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

// OnBranchClose stops the browser (if running) and removes the Manager.
func (p *Provider) OnBranchClose(ctx context.Context, params tab.CloseParams) error {
	if params.Branch == nil {
		return nil
	}
	key := managerKey(params.Branch.RepoID, params.Branch.ID)
	p.manMu.Lock()
	mgr, ok := p.managers[key]
	if ok {
		delete(p.managers, key)
	}
	p.manMu.Unlock()
	if ok {
		if _, err := mgr.Stop(ctx); err != nil {
			p.log.Warn("browser: stop on branch close failed", "err", err)
		}
	}
	return nil
}

// RegisterRoutes wires the state / start / stop endpoints.
func (p *Provider) RegisterRoutes(mux *http.ServeMux, prefix string) {
	h := &handler{p: p}
	mux.HandleFunc("GET "+prefix+"/state", h.state)
	mux.HandleFunc("POST "+prefix+"/start", h.start)
	mux.HandleFunc("POST "+prefix+"/stop", h.stop)
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// isIncus reports whether the workspace's resolved runtime is incus-container.
func (p *Provider) isIncus(repoID, branchID string) bool {
	reg := p.st.RuntimeRegistry()
	if reg == nil {
		return false
	}
	rt := reg.Get(repoID, branchID)
	return rt != nil && rt.Kind() == runtime.KindIncusContainer
}

// managerKey returns the lookup key for the managers map.
func managerKey(repoID, branchID string) string {
	return fmt.Sprintf("%s/%s", repoID, branchID)
}

// getOrCreateManager returns the existing Manager or creates a new one.
func (p *Provider) getOrCreateManager(repoID, branchID string) *Manager {
	key := managerKey(repoID, branchID)
	p.manMu.Lock()
	defer p.manMu.Unlock()
	if m, ok := p.managers[key]; ok {
		return m
	}

	// Resolve runtime — we already know it's incus-container here.
	reg := p.st.RuntimeRegistry()
	rt := reg.Get(repoID, branchID)
	// Derive the incus instance name to use for bind-mount device naming.
	inst := incuspkg.InstanceName(repoID, branchID)

	mgr := NewManager(rt, inst, nil, p.log)
	p.managers[key] = mgr
	return mgr
}

// managerFor returns the Manager for a given workspace, or nil if absent.
func (p *Provider) managerFor(repoID, branchID string) *Manager {
	p.manMu.Lock()
	defer p.manMu.Unlock()
	return p.managers[managerKey(repoID, branchID)]
}
