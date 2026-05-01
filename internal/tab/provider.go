// Package tab defines the TabProvider abstraction that lets new tab types
// (Claude, Bash, Files, Git, …) be plugged in without touching the Store or
// HTTP layers. Providers are registered in cmd/palmux/main.go; everything
// downstream iterates the Registry generically.
package tab

import (
	"context"
	"net/http"

	"github.com/tjst-t/palmux2/internal/domain"
)

// WindowSpec describes a tmux window that a Provider wants the Store to
// create when a branch is Open'd. Terminal-backed providers populate this;
// REST-only providers leave it empty.
type WindowSpec struct {
	Name    string   // tmux window name, e.g. "palmux:claude:claude"
	Command string   // optional command to run; empty = default shell
	Cwd     string   // optional cwd; empty = branch.WorktreePath
	Env     []string // optional env additions
}

// ProviderResult is what OnBranchOpen returns: the tabs to add to the
// branch's TabSet, plus any tmux windows the Store should create.
type ProviderResult struct {
	Tabs    []domain.Tab
	Windows []WindowSpec
}

// OpenParams is what OnBranchOpen receives.
type OpenParams struct {
	Branch *domain.Branch

	// Resume indicates the branch is being restored (e.g. tmux session was
	// killed externally, or Palmux restarted). Providers may use this to
	// alter their startup command — most notably the Claude provider runs
	// `claude --resume` instead of plain `claude`.
	Resume bool
}

// CloseParams is what OnBranchClose receives.
type CloseParams struct {
	Branch *domain.Branch
}

// InstanceLimits captures min/max constraints on how many tabs of a given
// provider may exist on a single branch. The Settings dependency lets the
// upper bound vary by user config (e.g. maxClaudeTabsPerBranch).
//
// Defined as a separate struct rather than two extra interface methods so a
// future provider can carry richer policy (e.g. "max scales with hardware
// concurrency") without churning every implementation.
type InstanceLimits struct {
	Min int // 1 for protected singletons and Min=1 multi-instance tabs (Claude/Bash)
	Max int // 1 for singletons; settings-driven for multi-instance tabs
}

// SettingsView is the read-only slice of global settings that providers need
// at request time. Exposed as an interface so the tab package stays free of
// cycle-prone imports of internal/config.
type SettingsView interface {
	MaxClaudeTabsPerBranch() int
	MaxBashTabsPerBranch() int
}

// Provider is the interface every tab type implements.
type Provider interface {
	Type() string        // stable identifier ("claude", "bash", "files", "git", ...)
	DisplayName() string // UI label
	Protected() bool     // user cannot delete this tab
	Multiple() bool      // multiple instances allowed (Bash, Claude post-S009)
	NeedsTmuxWindow() bool

	// Limits returns the min/max number of instances allowed on a branch.
	// Singletons return Min=1, Max=1. Multi-instance providers return
	// Min=1 (so the tab type is always present) and Max from settings.
	Limits(view SettingsView) InstanceLimits

	OnBranchOpen(ctx context.Context, params OpenParams) (ProviderResult, error)
	OnBranchClose(ctx context.Context, params CloseParams) error

	// RegisterRoutes lets REST-backed providers attach their handlers under
	// /api/repos/{repoId}/branches/{branchId}/{type}.
	RegisterRoutes(mux *http.ServeMux, prefix string)
}

// Registry is the ordered list of registered Providers. Order matters: it
// determines the default tab order in the TabBar.
type Registry struct {
	providers []Provider
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{} }

// Register adds a Provider. Subsequent registrations of the same Type panic
// (programmer error — providers must have unique types).
func (r *Registry) Register(p Provider) {
	for _, existing := range r.providers {
		if existing.Type() == p.Type() {
			panic("tab.Registry: duplicate provider type " + p.Type())
		}
	}
	r.providers = append(r.providers, p)
}

// Providers returns the registered Providers in registration order.
func (r *Registry) Providers() []Provider {
	out := make([]Provider, len(r.providers))
	copy(out, r.providers)
	return out
}

// Get returns the Provider with the given type, or nil.
func (r *Registry) Get(t string) Provider {
	for _, p := range r.providers {
		if p.Type() == t {
			return p
		}
	}
	return nil
}
