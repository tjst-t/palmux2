// Package files implements the Files tab — a singleton, protected,
// REST-backed tab that exposes the branch's worktree as a filesystem
// browser (list, read, filename search, grep).
package files

import (
	"context"
	"net/http"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tab"
)

// TabType is the stable provider identifier.
const TabType = "files"

// Provider implements tab.Provider for the Files tab.
type Provider struct {
	store *store.Store
}

// New returns a Provider. The Store reference is needed at request time so
// handlers can resolve {repoId, branchId} → worktree path.
func New(s *store.Store) *Provider { return &Provider{store: s} }

func (p *Provider) Type() string          { return TabType }
func (p *Provider) DisplayName() string   { return "Files" }
func (p *Provider) Protected() bool       { return true }
func (p *Provider) Multiple() bool        { return false }
func (p *Provider) NeedsTmuxWindow() bool { return false }

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

func (p *Provider) OnBranchClose(_ context.Context, _ tab.CloseParams) error { return nil }

func (p *Provider) RegisterRoutes(mux *http.ServeMux, prefix string) {
	h := &handler{store: p.store}
	mux.HandleFunc("GET "+prefix, h.listDir)
	mux.HandleFunc("GET "+prefix+"/raw", h.readFile)
	mux.HandleFunc("GET "+prefix+"/search", h.search)
	mux.HandleFunc("GET "+prefix+"/grep", h.grep)
}
