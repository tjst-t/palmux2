package git

import (
	"context"
	"net/http"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tab"
)

// TabType is the stable provider identifier.
const TabType = "git"

// Provider implements tab.Provider for the Git tab.
type Provider struct {
	store *store.Store
}

// New returns a Provider with a Store reference for path resolution.
func New(s *store.Store) *Provider { return &Provider{store: s} }

func (p *Provider) Type() string         { return TabType }
func (p *Provider) DisplayName() string  { return "Git" }
func (p *Provider) Protected() bool      { return true }
func (p *Provider) Multiple() bool       { return false }
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
	mux.HandleFunc("GET "+prefix+"/status", h.status)
	mux.HandleFunc("GET "+prefix+"/log", h.log)
	mux.HandleFunc("GET "+prefix+"/diff", h.diff)
	mux.HandleFunc("GET "+prefix+"/branches", h.branches)
	mux.HandleFunc("POST "+prefix+"/stage", h.stage)
	mux.HandleFunc("POST "+prefix+"/unstage", h.unstage)
	mux.HandleFunc("POST "+prefix+"/discard", h.discard)
	mux.HandleFunc("POST "+prefix+"/stage-hunk", h.stageHunk)
	mux.HandleFunc("POST "+prefix+"/unstage-hunk", h.unstageHunk)
	mux.HandleFunc("POST "+prefix+"/discard-hunk", h.discardHunk)
}
