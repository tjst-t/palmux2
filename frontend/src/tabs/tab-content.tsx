import type { Tab } from '../lib/api'
import { getRenderer } from '../lib/tab-registry'
import { TerminalView } from './terminal-view'

interface Props {
  tab: Tab
  repoId: string
  branchId: string
}

export function TabContent({ tab, repoId, branchId }: Props) {
  if (tab.windowName) {
    return <TerminalView repoId={repoId} branchId={branchId} tabId={tab.id} />
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
  return <Component repoId={repoId} branchId={branchId} tabId={tab.id} />
}
