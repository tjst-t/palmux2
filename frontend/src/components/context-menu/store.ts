// Generic context-menu state. Components call `useContextMenu().open(items, x, y)`
// from an onContextMenu handler; the renderer mounted in App handles the rest.

import { create } from 'zustand'

export type ContextMenuItem = ContextMenuAction | ContextMenuSeparator | ContextMenuHeading

export interface ContextMenuAction {
  type?: 'item'
  label: string
  onClick: () => void | Promise<void>
  danger?: boolean
  disabled?: boolean
  shortcut?: string
}

export interface ContextMenuSeparator {
  type: 'separator'
}

export interface ContextMenuHeading {
  type: 'heading'
  label: string
}

interface ContextMenuState {
  open: boolean
  x: number
  y: number
  items: ContextMenuItem[]
  show: (items: ContextMenuItem[], x: number, y: number) => void
  hide: () => void
}

export const useContextMenuStore = create<ContextMenuState>((set) => ({
  open: false,
  x: 0,
  y: 0,
  items: [],
  show: (items, x, y) => set({ open: true, items, x, y }),
  hide: () => set({ open: false, items: [] }),
}))

// Convenience hook for components that just want to dispatch.
export function useContextMenu() {
  return useContextMenuStore((s) => s.show)
}
