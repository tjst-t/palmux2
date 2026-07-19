import type { ComponentType } from 'react'

export interface TabViewProps {
  repoId: string
  branchId: string
  tabId: string
  /** S2b5691-2: the agent kind driving this tab's PTY endpoint, when the
   *  renderer needs it to build a kind-scoped URL (agent-tui). Omitted (or
   *  "claude") means "use claude's bare /tabs/{tabId}/tui/* path" — every
   *  other renderer ignores this prop. */
  kind?: string
}

export interface TabRenderer {
  type: string
  component: ComponentType<TabViewProps>
}

const registry = new Map<string, TabRenderer>()

export function registerTab(renderer: TabRenderer): void {
  registry.set(renderer.type, renderer)
}

export function getRenderer(type: string): TabRenderer | undefined {
  return registry.get(type)
}

export function listRenderers(): TabRenderer[] {
  return [...registry.values()]
}
