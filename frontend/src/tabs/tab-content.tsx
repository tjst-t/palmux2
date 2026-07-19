import { Suspense, useEffect } from 'react'

import type { Tab } from '../lib/api'
import { getRenderer } from '../lib/tab-registry'
import { useAgentRegistryStore } from '../stores/agent-registry-store'
import { DEFAULT_TAB_SETTINGS, useTabSettingsStore } from '../stores/tab-settings-store'
import { TerminalView } from './terminal-view'

interface Props {
  tab: Tab
  repoId: string
  branchId: string
}

function TabFallback() {
  return (
    <div
      style={{
        padding: 24,
        color: 'var(--color-fg-muted)',
        fontSize: 13,
      }}
      data-testid="tab-loading"
    >
      Loading…
    </div>
  )
}

export function TabContent({ tab, repoId, branchId }: Props) {
  // Sadf90e: for Claude tabs, the rendered component depends on this tab's
  // claude_mode setting (tab-scoped, not branch-scoped). Two Claude tabs on
  // the same branch can render different components — one agent, one tui.
  const tabSettings = useTabSettingsStore((s) => {
    const key = `${repoId}/${branchId}/${tab.id}`
    return s.settings[key] ?? DEFAULT_TAB_SETTINGS
  })
  const fetchTabSettings = useTabSettingsStore((s) => s.fetchSettings)

  // S2b5691-2: subscribe to the agent registry so a tab type that resolves
  // to "unknown → agent-tui fallback" re-renders once GET /api/agents lands
  // (it may still be 'loading' on first paint).
  const registryAgents = useAgentRegistryStore((s) => s.agents)

  useEffect(() => {
    if (tab.type !== 'claude') return
    void fetchTabSettings(repoId, branchId, tab.id)
  }, [repoId, branchId, tab.id, tab.type, fetchTabSettings])

  // claude's claude_mode branch is UNCHANGED (verbatim) — this is the one
  // renderer resolution path that predates S2b5691 and must keep working
  // exactly as before.
  const effectiveTabType = (() => {
    if (tab.type === 'claude') {
      return tabSettings.claude_mode === 'tui' ? 'claude-tui' : 'claude'
    }
    // S2b5691-2: any other tab type that (a) has no dedicated renderer
    // registered AND (b) is a known agent kind (per the registry) falls
    // back to the shared agent-tui PTY renderer. Types that are neither a
    // dedicated renderer nor a registry agent kind (e.g. a stale/unknown
    // type) fall through unchanged to the "Unknown tab type" message below.
    if (!getRenderer(tab.type) && registryAgents.some((a) => a.kind === tab.type)) {
      return 'agent-tui'
    }
    return tab.type
  })()

  if (tab.windowName) {
    // S032: pass tabType so TerminalView can update the MRU Bash tab cache
    // (updateMruBashTab) on user pty input when type === 'bash'.
    return <TerminalView repoId={repoId} branchId={branchId} tabId={tab.id} tabType={tab.type} />
  }
  const renderer = getRenderer(effectiveTabType)
  if (!renderer) {
    return (
      <div style={{ padding: 24, color: 'var(--color-fg-muted)' }}>
        Unknown tab type: <code>{tab.type}</code>
      </div>
    )
  }
  const Component = renderer.component
  return (
    <Suspense fallback={<TabFallback />}>
      <Component repoId={repoId} branchId={branchId} tabId={tab.id} kind={tab.type} />
    </Suspense>
  )
}
