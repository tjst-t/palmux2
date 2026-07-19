// agent-registry-store.ts — S2b5691-2
//
// Boot-loaded registry of enabled agent kinds, backed by GET /api/agents
// (S2b5691-1). This is the FE's single source of truth for "which agent
// kinds exist" — the tab-add picker (tab-bar.tsx, ⌘K) and the agent-tui
// renderer fallback (tab-content.tsx) both key off it instead of hardcoding
// tab-type strings.
//
// State machine (see docs/sprint-logs/S2b5691/gui-spec-S2b5691-2.json
// `data_states`):
//   loading  → GET /api/agents in-flight. `agents` holds the claude-only
//              fallback so callers never see an empty list.
//   loaded   → response applied. `agents.length <= 1` (just claude) means
//              "behave exactly as before this Story"; `> 1` means the
//              agent-picker UI activates.
//   error    → GET /api/agents failed (network / non-2xx / bad JSON). Falls
//              back to the claude-only list — the FE must never crash or
//              lose the ability to create a Claude tab because the registry
//              endpoint is unreachable.

import { create } from 'zustand'

import { agentsApi, type AgentDescriptor } from '../lib/api'

export type AgentRegistryStatus = 'loading' | 'loaded' | 'error'

// The one agent kind that is always available, even before GET /api/agents
// has resolved (or if it never does). Mirrors the backend's built-in claude
// descriptor shape (internal/server/handler_agents.go agentDescriptorFor).
const CLAUDE_FALLBACK: AgentDescriptor = {
  kind: 'claude',
  displayName: 'Claude',
  icon: 'claude',
  capabilities: { resume: true, notify: 'full', inContainer: true, permissionMode: true },
  protected: true,
  modes: ['agent', 'tui'],
}

interface AgentRegistryState {
  status: AgentRegistryStatus
  agents: AgentDescriptor[]
  /** Load (or reload) the registry from GET /api/agents. Safe to call more
   *  than once (e.g. a manual retry) — always resolves, never throws. */
  fetchAgents: () => Promise<void>
}

export const useAgentRegistryStore = create<AgentRegistryState>((set) => ({
  status: 'loading',
  agents: [CLAUDE_FALLBACK],

  fetchAgents: async () => {
    try {
      const agents = await agentsApi.list()
      set({
        status: 'loaded',
        // A malformed/empty response still leaves claude reachable rather
        // than dead-ending tab creation entirely.
        agents: agents.length > 0 ? agents : [CLAUDE_FALLBACK],
      })
    } catch {
      set({ status: 'error', agents: [CLAUDE_FALLBACK] })
    }
  },
}))

/** True when `type` is a known agent kind (claude or a registry-declared
 *  kind such as "codex" / a user-defined generic agent). Reads the current
 *  snapshot — for reactive (re-rendering) use inside a component, prefer
 *  `useAgentRegistryStore((s) => s.agents)` directly. */
export function isAgentTab(type: string): boolean {
  return useAgentRegistryStore.getState().agents.some((a) => a.kind === type)
}

/** The full descriptor for `type`, or undefined if it isn't a known agent
 *  kind (e.g. "bash" / "files" / "git" / an unrecognised type string). */
export function agentFor(type: string): AgentDescriptor | undefined {
  return useAgentRegistryStore.getState().agents.find((a) => a.kind === type)
}

/** Convenience accessor for just the capabilities sub-object. */
export function agentCapabilities(type: string): AgentDescriptor['capabilities'] | undefined {
  return agentFor(type)?.capabilities
}
