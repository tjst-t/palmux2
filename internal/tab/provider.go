// Package tab defines the TabProvider abstraction that lets new tab types
// (Claude, Bash, Files, Git, …) be plugged in without touching the Store or
// HTTP layers. Providers are registered in cmd/palmux/main.go; everything
// downstream iterates the Registry generically.
//
// ADR-0012 splits the two roles this package used to conflate:
//
//   - [Provider.Tabs] is a PURE QUERY — the sole provider-side input to the
//     Store's tab-set derivation. It must be side-effect free and idempotent
//     because the Store calls it from every recompute path, including the 5 s
//     sync loop.
//   - [Provider.OnBranchOpen] / [Provider.OnBranchClose] are LIFECYCLE
//     NOTIFICATIONS. They fire when a branch actually opens or closes and may
//     have side effects. Their return value never contributes tabs.
//
// Before ADR-0012 the Store called OnBranchOpen as a query with Resume=false
// and asserted in a comment that this was side-effect free. It was not: the
// sprint provider allocated an inotify handle from that path, under the
// Store's write lock.
package tab

import (
	"context"
	"fmt"
	"net/http"
	"strings"

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

// ProviderResult is what OnBranchOpen returns: the tmux windows the Store
// should create for this branch.
//
// ADR-0012 removed the `Tabs` field. Tabs are declared by [Provider.Tabs],
// never by the lifecycle hook — otherwise the Store has to call a
// side-effecting hook every time it wants to know what tabs exist.
type ProviderResult struct {
	Windows []WindowSpec
}

// TabsParams is what [Provider.Tabs] receives.
type TabsParams struct {
	Branch *domain.Branch

	// Windows holds the tmux window-name suffixes of THIS provider's type
	// that currently exist in the branch's session, in tmux index order
	// (e.g. ["bash", "bash-2", "server"] for the bash provider).
	//
	// Only populated for providers whose NeedsTmuxWindow() is true; always
	// nil otherwise. The Store resolves it — including the S009-fix-1
	// transient-ListWindows-failure fallback and the canonical-instance
	// seeding — so providers stay free of tmux policy and Tabs() stays a
	// pure function of its arguments.
	Windows []string
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

// HeadChangedParams is what [HeadChangedHook.OnBranchHeadChanged] receives
// (S1e8d02). `Branch` already carries the new name; `OldBranch` is provided
// separately so providers that key off branch name can update their lookup
// tables.
type HeadChangedParams struct {
	Branch    *domain.Branch
	OldBranch string
	NewBranch string
}

// HeadChangedHook is an optional capability a [Participant] may also
// implement (S1e8d02). Participants that have per-branch in-memory state
// keyed off the branch name implement this so the store can invoke them
// when an in-place `git checkout` changes the head ref of a workspace whose
// ID and tmux session both stay alive.
//
// This is intentionally NOT part of the core [Provider] interface so the
// large set of existing implementations need no churn. The default — for
// anything that does NOT implement it — is a no-op, which is exactly the
// desired behaviour for the canonical Claude / Bash / Files / Git / Sprint
// providers (all derive their state from `branch.WorktreePath`, not from
// `branch.Name`).
type HeadChangedHook interface {
	OnBranchHeadChanged(ctx context.Context, params HeadChangedParams) error
}

// RuntimeRestartHook is an optional capability a [Participant] may implement
// when it holds a long-lived process bound to the workspace runtime (the
// Claude daemons run claude inside the incus container). When the runtime is
// recreated in place — a container regenerate (S7364e3) or a host↔incus
// switch (S8478ca) — that process is tied to the now-destroyed container and
// cannot recover on its own; the participant must tear its per-branch daemon
// down so the next WS attach respawns it against the NEW runtime. Anything
// that doesn't implement it is a no-op (its tabs are stateless or
// tmux-backed and reconnect on branch.restarted). (S4d8b1c-fix)
type RuntimeRestartHook interface {
	OnBranchRuntimeRestarted(ctx context.Context, params CloseParams) error
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

// Participant is anything that takes part in the branch lifecycle and owns
// HTTP routes, whether or not it contributes tabs.
//
// ADR-0012 introduced this so a pure runtime — the agenttui PTY daemon host
// — can receive lifecycle callbacks and register its WS/stats/resize
// endpoints WITHOUT masquerading as a zero-tab [Provider]. Previously
// agenttui returned Conditional()==true purely so the Store's recompute loop
// would call its OnBranchOpen, which is a visibility flag being used as a
// subscription mechanism.
//
// Every [Provider] is also a Participant.
type Participant interface {
	Type() string
	OnBranchClose(ctx context.Context, params CloseParams) error
	RegisterRoutes(mux *http.ServeMux, prefix string)
}

// Provider is the interface every tab type implements.
type Provider interface {
	Participant

	DisplayName() string // UI label
	Protected() bool     // user cannot delete this tab
	Multiple() bool      // multiple instances allowed (Bash, Claude post-S009)
	NeedsTmuxWindow() bool

	// Limits returns the min/max number of instances allowed on a branch.
	// Singletons return Min=1, Max=1. Multi-instance providers return
	// Min=1 (so the tab type is always present) and Max from settings.
	Limits(view SettingsView) InstanceLimits

	// Tabs reports the tabs this provider currently contributes to the
	// branch. It is the ONLY provider-side input to the Store's tab-set
	// derivation.
	//
	// CONTRACT (ADR-0012) — the Store calls this from every recompute path,
	// including the 5 s sync loop and, transitively, from HTTP handlers:
	//
	//   - MUST be side-effect free. Do not start watchers, spawn daemons,
	//     touch registries, or mutate provider state here. Anything with a
	//     side effect belongs in OnBranchOpen.
	//   - MUST be idempotent: same params ⇒ same result.
	//   - SHOULD be cheap. It is called for every provider on every
	//     recompute; there is no Conditional() short-circuit any more.
	//
	// Returning zero tabs is normal and is how conditional visibility is
	// expressed (the Sprint tab hides itself when docs/ROADMAP.json is
	// absent; Browser/Ports hide on the host runtime). There is no separate
	// Conditional() flag — ADR-0012 removed it because it had drifted into
	// meaning "please call my lifecycle hook".
	Tabs(ctx context.Context, params TabsParams) ([]domain.Tab, error)

	// OnBranchOpen is a lifecycle notification: the branch is actually being
	// opened (or resumed). It returns the tmux windows the Store should
	// create. Side effects are allowed here — this is the hook's purpose.
	// It does NOT contribute tabs; see Tabs.
	OnBranchOpen(ctx context.Context, params OpenParams) (ProviderResult, error)
}

// InstanceDisplayName is the shared convention for labelling one instance of a
// multi-instance tab, given the tmux window-name suffix.
//
//	"bash"    → "Bash"      (the canonical instance shows the provider label)
//	"bash-2"  → "Bash 2"
//	"server"  → "server"     (a user-named window keeps its own name)
//
// It lives here rather than in each provider because it is a cross-provider
// convention. ADR-0012 removed a copy of it that had silently drifted: the
// Store's host-scope path had its own version labelling the canonical tab
// "bash" instead of "Bash", so the Host scope and ordinary workspaces
// disagreed. One definition, one behaviour.
func InstanceDisplayName(displayName, tabType, windowSuffix string) string {
	if windowSuffix == tabType {
		return displayName
	}
	if suffix, ok := strings.CutPrefix(windowSuffix, tabType+"-"); ok {
		return displayName + " " + suffix
	}
	return windowSuffix
}

// Registry is the ordered list of registered Providers plus any non-tab
// Participants. Order matters for Providers: it determines the default tab
// order in the TabBar.
type Registry struct {
	providers []Provider
	services  []Participant
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{} }

// Register adds a Provider after validating it. Registration problems are
// programmer errors and panic at boot rather than producing a subtly wrong
// tab set at runtime (ADR-0012).
func (r *Registry) Register(p Provider) {
	if err := r.validate(p); err != nil {
		panic("tab.Registry: " + err.Error())
	}
	r.providers = append(r.providers, p)
}

// RegisterService adds a non-tab Participant: something that wants lifecycle
// callbacks and HTTP routes but contributes no tabs (ADR-0012). The agenttui
// PTY runtime is the canonical case.
func (r *Registry) RegisterService(s Participant) {
	if s.Type() == "" {
		panic("tab.Registry: service with empty Type()")
	}
	if r.typeTaken(s.Type()) {
		panic("tab.Registry: duplicate participant type " + s.Type())
	}
	r.services = append(r.services, s)
}

func (r *Registry) typeTaken(t string) bool {
	for _, p := range r.providers {
		if p.Type() == t {
			return true
		}
	}
	for _, s := range r.services {
		if s.Type() == t {
			return true
		}
	}
	return false
}

// validate enforces the invariants that used to live only in doc comments.
// Before ADR-0012 an illegal combination registered silently and produced a
// tab set nobody could explain.
func (r *Registry) validate(p Provider) error {
	t := p.Type()
	if t == "" {
		return fmt.Errorf("provider with empty Type()")
	}
	// domain.TabID joins type and name with ":", and domain.ParseWindowName
	// splits "palmux:{type}:{name}" on ":" — a type containing the separator
	// would make tab IDs and window names ambiguous.
	if strings.Contains(t, ":") {
		return fmt.Errorf("provider type %q must not contain ':'", t)
	}
	if r.typeTaken(t) {
		return fmt.Errorf("duplicate provider type %s", t)
	}
	// Limits must agree with Multiple(): the Store enforces Max in AddTab and
	// Min in RemoveTab, so a contradiction here makes one of them unreachable.
	lim := p.Limits(nil)
	if lim.Max > 0 && lim.Min > lim.Max {
		return fmt.Errorf("provider %s: Limits.Min (%d) > Limits.Max (%d)", t, lim.Min, lim.Max)
	}
	if !p.Multiple() && lim.Max > 1 {
		return fmt.Errorf("provider %s: Multiple()==false but Limits.Max==%d (singletons cap at 1)", t, lim.Max)
	}
	if p.Multiple() && lim.Max == 1 {
		return fmt.Errorf("provider %s: Multiple()==true but Limits.Max==1 (AddTab could never succeed)", t)
	}
	return nil
}

// Providers returns the registered Providers in registration order.
func (r *Registry) Providers() []Provider {
	out := make([]Provider, len(r.providers))
	copy(out, r.providers)
	return out
}

// Participants returns every Provider followed by every non-tab service, for
// lifecycle dispatch (OnBranchClose, HeadChangedHook, RuntimeRestartHook)
// and route registration. Providers come first so their ordering semantics
// are unchanged.
func (r *Registry) Participants() []Participant {
	out := make([]Participant, 0, len(r.providers)+len(r.services))
	for _, p := range r.providers {
		out = append(out, p)
	}
	out = append(out, r.services...)
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
