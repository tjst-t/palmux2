import type { ComponentType } from 'react'

export interface TabViewProps {
  repoId: string
  branchId: string
  tabId: string
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
