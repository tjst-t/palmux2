package agent

import (
	"fmt"
	"strings"
	"sync"
)

// sessionIDPlaceholder is the literal token substituted with the resume
// session ID in each ResumeArgs entry at spawn time (design §4.3).
const sessionIDPlaceholder = "{session_id}"

// GenericConfig is the template-driven declaration for a user-defined agent
// (config.toml `[agents.<name>]`, design §4.3). Capabilities are derived
// entirely from which fields are non-empty (AC-Sdec0a7-2-1):
//   - ResumeArgs non-empty  → Capabilities.Resume = true
//   - ContainerCommand set  → Capabilities.InContainer = true
//
// Notify is always [NotifyNone] — a generic command declares no hook
// mechanism palmux can wire into; only adapters that know a specific
// agent's notification protocol (claude today; codex/opencode in later
// Sprints) can offer richer notify levels.
type GenericConfig struct {
	DisplayName      string
	Command          string
	Args             []string
	ResumeArgs       []string // may contain the literal token "{session_id}"
	ContainerCommand string
}

// GenericAdapter is a template-driven [Adapter] for any CLI command declared
// via [GenericConfig] (Sdec0a7-2, design §4.3). It has no notification
// mechanism and does not implement [SessionDiscoverer] — nothing ever
// fsnotify-watches a transcript directory to discover a fresh session ID for
// it.
//
// Resume behavior: when ResumeArgs is declared, Capabilities.Resume is true
// (as AC-Sdec0a7-2-1 requires). Because GenericAdapter implements no
// [SessionDiscoverer], agenttui.Daemon.respawnLoop does NOT block waiting for
// a session id on a crash (that block is gated on the adapter being a
// SessionDiscoverer — Sdec0a7-2 review fix 1). Instead a respawn resumes with
// whatever session id the [agenttui.SessionStore] already holds for the tab
// (from a prior SetSessionID), or does a plain fresh spawn when there is
// none. A real third-party adapter that needs genuine automatic resume
// detection should implement its own [SessionDiscoverer] (as codex/opencode
// Stories C/D will) rather than rely on GenericAdapter.
type GenericAdapter struct {
	kind Kind

	mu               sync.RWMutex
	displayName      string
	bin              string
	args             []string
	resumeArgs       []string
	containerCommand string
}

var (
	_ Adapter      = (*GenericAdapter)(nil)
	_ Configurable = (*GenericAdapter)(nil)
)

// NewGenericAdapter builds a GenericAdapter for kind from cfg.
func NewGenericAdapter(kind Kind, cfg GenericConfig) *GenericAdapter {
	display := cfg.DisplayName
	if display == "" {
		display = string(kind)
	}
	return &GenericAdapter{
		kind:             kind,
		displayName:      display,
		bin:              cfg.Command,
		args:             append([]string(nil), cfg.Args...),
		resumeArgs:       append([]string(nil), cfg.ResumeArgs...),
		containerCommand: cfg.ContainerCommand,
	}
}

// Kind returns the configured kind (the config.toml section name).
func (a *GenericAdapter) Kind() Kind { return a.kind }

// DisplayName returns the configured display_name, or the kind string when
// none was declared.
func (a *GenericAdapter) DisplayName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.displayName
}

// Capabilities derives entirely from which config fields were declared —
// see the GenericConfig doc comment.
func (a *GenericAdapter) Capabilities() Capabilities {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return Capabilities{
		Resume:         len(a.resumeArgs) > 0,
		Notify:         NotifyNone,
		InContainer:    a.containerCommand != "",
		PermissionMode: false,
	}
}

// SetBin hot-swaps the host command used for fresh spawns (Sa53137-3-style
// hot apply, mirrors [ClaudeAdapter.SetBin]). Empty is a no-op.
func (a *GenericAdapter) SetBin(bin string) {
	if bin == "" {
		return
	}
	a.mu.Lock()
	a.bin = bin
	a.mu.Unlock()
}

// SetArgs hot-swaps the extra args passed on every fresh spawn.
func (a *GenericAdapter) SetArgs(args []string) {
	a.mu.Lock()
	a.args = append([]string(nil), args...)
	a.mu.Unlock()
}

func (a *GenericAdapter) snapshot() (bin string, args, resumeArgs []string, containerBin string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.bin, append([]string(nil), a.args...), append([]string(nil), a.resumeArgs...), a.containerCommand
}

// SpawnSpec builds argv for a fresh or resumed spawn:
//
//	fresh:   <bin> <args...>
//	resume:  <bin> <args...> <resumeArgs, with "{session_id}" substituted>
//
// Resume args are only appended when BOTH intent.ResumeSessionID is
// non-empty AND resume_args was declared — otherwise the spawn is
// indistinguishable from fresh (mirrors Capabilities.Resume == false
// semantics used elsewhere: no resume args declared, no resume attempted).
//
// In-container (intent.InContainer): argv[0] becomes ContainerCommand. A
// missing ContainerCommand here returns an explicit error rather than
// silently falling back to the host bin path inside the container (D12).
// In practice this branch should not be reached with an empty
// ContainerCommand — the daemon-level guard in agenttui.Daemon.spawnWithArgs
// already refuses to call SpawnSpec at all when
// Capabilities().InContainer == false and the workspace runtime is incus;
// this is defense-in-depth, not the primary enforcement point.
func (a *GenericAdapter) SpawnSpec(intent SpawnIntent) (SpawnSpec, error) {
	bin, args, resumeArgs, containerBin := a.snapshot()
	if bin == "" {
		return SpawnSpec{}, fmt.Errorf("agent: generic adapter %q has no command configured", a.kind)
	}
	if intent.InContainer {
		if containerBin == "" {
			return SpawnSpec{}, fmt.Errorf("agent: generic adapter %q has no container_command; cannot run in-container", a.kind)
		}
		bin = containerBin
	}

	argv := append([]string{bin}, args...)
	if intent.ResumeSessionID != "" && len(resumeArgs) > 0 {
		argv = append(argv, substituteSessionID(resumeArgs, intent.ResumeSessionID)...)
	}
	return SpawnSpec{Argv: argv}, nil
}

// substituteSessionID replaces every literal "{session_id}" token in each
// arg with id, returning a new slice (args is never mutated in place).
func substituteSessionID(args []string, id string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, sessionIDPlaceholder, id)
	}
	return out
}
