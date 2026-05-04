import { Suspense } from 'react'

import type { Tab } from '../lib/api'
import { getRenderer } from '../lib/tab-registry'
import { TerminalView } from './terminal-view'

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
  if (tab.windowName) {
    // S032: pass tabType so TerminalView can update the MRU Bash tab cache
    // (updateMruBashTab) on user pty input when type === 'bash'.
    return <TerminalView repoId={repoId} branchId={branchId} tabId={tab.id} tabType={tab.type} />
  }
  const renderer = getRenderer(tab.type)
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
