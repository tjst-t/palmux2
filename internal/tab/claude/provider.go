// Package claude provides the Claude Code tab — a singleton, protected,
// terminal-backed tab that auto-starts the `claude` CLI in the branch's
// worktree.
package claude

import (
	"context"
	"net/http"
	"strings"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
)

// TabType is the stable provider identifier.
const TabType = "claude"

// WindowName is the canonical tmux window name. Per spec, the Claude tab is
// always at this exact window name, never indexed.
const WindowName = "palmux:claude:claude"

// Options configures the claude command.
type Options struct {
	Model string // optional --model
}

// Provider implements tab.Provider for Claude.
type Provider struct {
	opts Options
}

// New returns a Provider with the given options.
func New(opts Options) *Provider { return &Provider{opts: opts} }

func (p *Provider) Type() string         { return TabType }
func (p *Provider) DisplayName() string  { return "Claude" }
func (p *Provider) Protected() bool      { return true }
func (p *Provider) Multiple() bool       { return false }
func (p *Provider) NeedsTmuxWindow() bool { return true }

func (p *Provider) OnBranchOpen(_ context.Context, params tab.OpenParams) (tab.ProviderResult, error) {
	cmd := buildCommand(p.opts, params.Resume)
	return tab.ProviderResult{
		Tabs: []domain.Tab{{
			ID:         TabType,
			Type:       TabType,
			Name:       p.DisplayName(),
			Protected:  true,
			Multiple:   false,
			WindowName: WindowName,
		}},
		Windows: []tab.WindowSpec{{
			Name:    WindowName,
			Command: cmd,
		}},
	}, nil
}

func (p *Provider) OnBranchClose(_ context.Context, _ tab.CloseParams) error {
	return nil
}

func (p *Provider) RegisterRoutes(_ *http.ServeMux, _ string) {
	// Restart/Resume endpoints are added in Phase 10 (Polish).
}

// buildCommand assembles the `claude` invocation for tmux. Phase 1 supports
// Resume + Model; Phase 10 will surface Restart with model selection from the
// UI.
func buildCommand(opts Options, resume bool) string {
	parts := []string{"claude"}
	if resume {
		parts = append(parts, "--resume")
	}
	if opts.Model != "" {
		parts = append(parts, "--model", opts.Model)
	}
	return strings.Join(parts, " ")
}
