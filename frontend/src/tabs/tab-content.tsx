import { Suspense } from 'react'

import type { Tab } from '../lib/api'
import { getRenderer } from '../lib/tab-registry'
import { useBranchSettingsStore } from '../stores/branch-settings-store'
import { TerminalView } from './terminal-view'

// S1f75ec-2: stable fallback — module-level constant prevents Zustand from
// seeing a new object reference on every render (which would cause an infinite
// re-render loop when the branch settings haven't been fetched yet).
const DEFAULT_BRANCH_SETTINGS = { claude_mode: 'agent' as const }

interface Props {
  tab: Tab
  repoId: string
  branchId: string
}

// S022 — Tab modules (Files / Git / Sprint) are now lazy-loaded via
// React.lazy to keep the initial bundle small. Terminal-backed tabs
// (Claude, Bash) are still synchronous because they are the most common
// landing surface and need to mount immediately.
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
  // S1f75ec-2: for the canonical `claude` tab, the actual rendered component
  // depends on `claude_mode`. When mode is "tui" we render the claude-tui
  // component instead. The `claude-tui` tab itself is hidden from the tab bar
  // so there is always exactly one "Claude" entry.
  // Subscribe directly to the settings slice so Zustand triggers a re-render
  // when patchSettings writes a new value.
  const branchSettings = useBranchSettingsStore((s) => {
    const key = `${repoId}/${branchId}`
    return s.settings[key] ?? DEFAULT_BRANCH_SETTINGS
  })
  const effectiveTabType = (() => {
    if (tab.type === 'claude') {
      return branchSettings.claude_mode === 'tui' ? 'claude-tui' : 'claude'
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
