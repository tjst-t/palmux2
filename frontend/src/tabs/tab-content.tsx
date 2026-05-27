import { Suspense, useEffect } from 'react'

import type { Tab } from '../lib/api'
import { getRenderer } from '../lib/tab-registry'
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

  useEffect(() => {
    if (tab.type !== 'claude') return
    void fetchTabSettings(repoId, branchId, tab.id)
  }, [repoId, branchId, tab.id, tab.type, fetchTabSettings])

  const effectiveTabType = (() => {
    if (tab.type === 'claude') {
      return tabSettings.claude_mode === 'tui' ? 'claude-tui' : 'claude'
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
      <Component repoId={repoId} branchId={branchId} tabId={tab.id} />
    </Suspense>
  )
}
