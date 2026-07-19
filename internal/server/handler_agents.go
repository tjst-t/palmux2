package server

import (
	"net/http"

	"github.com/tjst-t/palmux2/internal/agent"
)

// S2b5691 (ported from the maultiagent reference branch's Sdec0a7-2): GET
// /api/agents. Enumerates every agent kind registered at startup (built-in
// claude plus one entry per enabled `[agents.<name>]` config.toml section —
// see agent.BuildRegistry) so the FE can build the agent-picker /
// tab-creation UI and capability badges without hardcoding kind strings.

// AgentCapabilitiesDTO is the wire shape of agent.Capabilities.
type AgentCapabilitiesDTO struct {
	Resume         bool   `json:"resume"`
	Notify         string `json:"notify"`
	InContainer    bool   `json:"inContainer"`
	PermissionMode bool   `json:"permissionMode"`
}

// AgentDescriptor is one entry in the GET /api/agents response.
type AgentDescriptor struct {
	Kind         string               `json:"kind"`
	DisplayName  string               `json:"displayName"`
	Icon         string               `json:"icon"`
	Capabilities AgentCapabilitiesDTO `json:"capabilities"`
	// Protected mirrors the claude tab's Provider.Protected() — true only
	// for "claude" (its provider always keeps at least one tab open and
	// cannot be fully removed). Generic/codex/opencode agent tabs are
	// never protected.
	Protected bool `json:"protected"`
	// Modes lists which tab surfaces this kind supports. claude supports
	// both "agent" (stream-json chat UI) and "tui" (raw PTY); every other
	// kind is "tui"-only (generic/codex/opencode have no stream-json
	// surface — see internal/agent's Adapter doc comments).
	Modes []string `json:"modes"`
}

// getAgents serves the enabled-agent descriptor list.
func (h *handlers) getAgents(w http.ResponseWriter, _ *http.Request) {
	out := []AgentDescriptor{}
	if h.agents != nil {
		for _, k := range h.agents.Kinds() {
			a, ok := h.agents.Get(k)
			if !ok {
				continue
			}
			out = append(out, agentDescriptorFor(a))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func agentDescriptorFor(a agent.Adapter) AgentDescriptor {
	kind := string(a.Kind())
	protected := a.Kind() == agent.KindClaude
	modes := []string{"tui"}
	if protected {
		modes = []string{"agent", "tui"}
	}
	caps := a.Capabilities()
	return AgentDescriptor{
		Kind:        kind,
		DisplayName: a.DisplayName(),
		Icon:        iconForAgentKind(kind),
		Capabilities: AgentCapabilitiesDTO{
			Resume:         caps.Resume,
			Notify:         string(caps.Notify),
			InContainer:    caps.InContainer,
			PermissionMode: caps.PermissionMode,
		},
		Protected: protected,
		Modes:     modes,
	}
}

// iconForAgentKind maps a kind to the FE icon key. The built-in kinds
// ("claude", "codex", "opencode") get a dedicated icon key; any
// user-defined `[agents.<name>]` kind falls back to the generic robot icon.
func iconForAgentKind(kind string) string {
	switch kind {
	case "claude", "codex", "opencode":
		return kind
	default:
		return "generic"
	}
}
