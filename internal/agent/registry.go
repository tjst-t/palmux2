package agent

import (
	"fmt"
	"sort"
)

// reservedKinds are tab-type identifiers already owned by built-in,
// compile-time providers (Sdec0a7-2 review fix 2). A user-defined
// `[agents.<name>]` section whose name is one of these would, at
// tab.Registry.Register time, PANIC on a duplicate provider Type() and crash
// palmux2 entirely — so BuildRegistry rejects them up front with a clear
// error instead. "claude" is deliberately NOT here: `[agents.claude]` is a
// legitimate bin/args OVERRIDE of the built-in claude adapter (handled in
// the config layer + skipped by BuildRegistry's loop), not a second provider.
//
// Kept as a static set (rather than reaching into internal/tab) so this
// package stays free of a dependency on the tab layer; the values mirror the
// providers registered in cmd/palmux/main.go's run(): files / git / sprint /
// ports / browser / bash, plus agenttui's service-provider Type()
// "claude-tui".
var reservedKinds = map[Kind]bool{
	"files":      true,
	"git":        true,
	"sprint":     true,
	"ports":      true,
	"browser":    true,
	"bash":       true,
	"claude-tui": true,
}

// IsReservedKind reports whether name collides with a built-in tab type and
// therefore cannot be used as a user-defined agent kind.
func IsReservedKind(name string) bool {
	return reservedKinds[Kind(name)]
}

// Registry maps a [Kind] to its [Adapter]. It is built once at server
// startup (cmd/palmux/main.go) from the built-in claude adapter plus one
// [GenericAdapter] per enabled `[agents.<name>]` config.toml section
// (Sdec0a7-2, design §4.3-§4.4), and is then read by:
//   - the per-kind agenttab.Provider / agenttui.Manager wiring loop, and
//   - GET /api/agents (internal/server/handler_agents.go).
type Registry struct {
	adapters map[Kind]Adapter
	order    []Kind // registration order — stable GET /api/agents listing
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[Kind]Adapter)}
}

// Register adds (or replaces) the Adapter for its own Kind(). Re-registering
// an existing Kind keeps its position in Kinds()/All() order.
func (r *Registry) Register(a Adapter) {
	k := a.Kind()
	if _, exists := r.adapters[k]; !exists {
		r.order = append(r.order, k)
	}
	r.adapters[k] = a
}

// Get returns the Adapter for k, or (nil, false) if none is registered.
func (r *Registry) Get(k Kind) (Adapter, bool) {
	a, ok := r.adapters[k]
	return a, ok
}

// Kinds returns every registered Kind in registration order.
func (r *Registry) Kinds() []Kind {
	out := make([]Kind, len(r.order))
	copy(out, r.order)
	return out
}

// All returns every registered Adapter in registration order.
func (r *Registry) All() []Adapter {
	out := make([]Adapter, 0, len(r.order))
	for _, k := range r.order {
		out = append(out, r.adapters[k])
	}
	return out
}

// SharedContainerPaths (S339021) aggregates every registered adapter's own
// in-container binary + auth/config share (see [InContainerProvider]) for
// adapters that resolve a usable binary on THIS host. Used to extend the
// host-wide incus palmux-shared profile
// (internal/runtime/incus.SharedProfileManager.SetAgentSharedPaths) so
// codex/opencode (and any future built-in implementing
// InContainerProvider) get their binary + credentials bind-mounted into
// every workspace container — mirroring how claude's own binary + ~/.claude
// already ride the static ~/.local/bin / dot-claude shared devices.
// Adapters that don't implement InContainerProvider (GenericAdapter today —
// its container_command is a plain user-declared config path, not a host
// path to auto-share) contribute nothing.
func (r *Registry) SharedContainerPaths() []string {
	var out []string
	for _, a := range r.All() {
		icp, ok := a.(InContainerProvider)
		if !ok {
			continue
		}
		if _, ok := icp.ContainerBinary(); !ok {
			continue // no usable binary on this host — nothing to share
		}
		out = append(out, icp.SharedContainerPaths()...)
	}
	return out
}

// AgentConfigEntry is main.go's plain translation of one `[agents.<name>]`
// config.toml section (internal/config.AgentSection) into the shape
// [BuildRegistry] needs. Defined here (rather than importing internal/config)
// so this package stays free of a dependency on the config-file format —
// main.go, the only place that reads config.toml, does the translation.
type AgentConfigEntry struct {
	// Enabled gates whether this entry is offered as a tab kind at all
	// (AC-Sdec0a7-2-1). Ignored for the reserved "claude" name — the
	// built-in claude adapter is always registered.
	Enabled bool
	// DisplayName is the human-readable label; empty falls back to the
	// section name.
	DisplayName string
	// Command is the binary/command to spawn. An entry with an empty
	// Command is skipped entirely (nothing to run).
	Command string
	Args    []string
	// ResumeArgs declares the adapter's Resume capability when non-empty.
	ResumeArgs []string
	// ContainerCommand declares the adapter's InContainer capability when
	// non-empty.
	ContainerCommand string
}

// BuildRegistry constructs the full agent Registry: the built-in claude
// adapter (claudeBin/claudeArgs — already alias-resolved from
// server.claude_bin/args + any [agents.claude] override by the config
// layer, D8) plus one [GenericAdapter] per enabled, non-"claude" entry in
// agents whose Command is non-empty.
//
// Entries named "claude" are ignored here — claude's bin/args override is
// resolved earlier, at config-layer time (see internal/config), not by
// registering a second claude adapter.
//
// Returns an error (rather than panicking later at tab.Registry.Register
// time) when a user-defined section reuses a reserved built-in tab type
// name (Sdec0a7-2 review fix 2) — see reservedKinds. The check runs before
// the enabled/command filters so even a disabled `[agents.files]` is flagged
// as the configuration mistake it is.
func BuildRegistry(claudeBin string, claudeArgs []string, agents map[string]AgentConfigEntry) (*Registry, error) {
	r := NewRegistry()
	r.Register(NewClaudeAdapter(claudeBin, claudeArgs))

	// Sort names for deterministic registration order (Go map iteration is
	// randomised; GET /api/agents and tests both want a stable listing).
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		entry := agents[name]
		if Kind(name) == KindClaude {
			continue
		}
		if IsReservedKind(name) {
			return nil, fmt.Errorf("agents.%s: %q is a reserved tab type and cannot be used as an agent name", name, name)
		}
		if !entry.Enabled || entry.Command == "" {
			continue
		}
		// S339021-1: "codex" is a second built-in, like "claude" above —
		// [agents.codex] declares bin/args for the real CodexAdapter (real
		// resume + turn-end notify hooks) rather than a template-driven
		// GenericAdapter. DisplayName is intentionally NOT threaded through
		// here, mirroring how the built-in claude registration above ignores
		// a custom [agents.claude].display_name too.
		if Kind(name) == KindCodex {
			r.Register(NewCodexAdapter(entry.Command, entry.Args))
			continue
		}
		// S339021-2: "opencode" is a third built-in, like "codex" above —
		// [agents.opencode] declares bin/args for the real OpencodeAdapter
		// (real cwd-matched resume + full turn-end/permission-wait notify)
		// rather than a template-driven GenericAdapter. DisplayName is
		// intentionally NOT threaded through here, same as claude/codex.
		if Kind(name) == KindOpencode {
			r.Register(NewOpencodeAdapter(entry.Command, entry.Args))
			continue
		}
		display := entry.DisplayName
		if display == "" {
			display = name
		}
		r.Register(NewGenericAdapter(Kind(name), GenericConfig{
			DisplayName:      display,
			Command:          entry.Command,
			Args:             entry.Args,
			ResumeArgs:       entry.ResumeArgs,
			ContainerCommand: entry.ContainerCommand,
		}))
	}
	return r, nil
}
