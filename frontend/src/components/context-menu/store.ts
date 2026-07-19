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
  /** S2b5691-2: optional data-testid for the rendered <button>. Used by
   *  callers that need a stable per-item selector (e.g. the agent-picker
   *  menu's `agent-picker-item-<kind>` rows). Existing callers that don't
   *  set this are unaffected. */
  testId?: string
}

export interface ContextMenuSeparator {
  type: 'separator'
}

export interface ContextMenuHeading {
  type: 'heading'
  label: string
}

interface ShowOptions {
  /** S2b5691-2: data-testid stamped on the menu's root container. Lets a
   *  specific menu instance (e.g. the agent-picker) be selected without
   *  matching every context menu in the app. Omitted for ordinary
   *  right-click menus (unchanged behavior). */
  containerTestId?: string
}

interface ContextMenuState {
  open: boolean
  x: number
  y: number
  items: ContextMenuItem[]
  containerTestId?: string
  show: (items: ContextMenuItem[], x: number, y: number, opts?: ShowOptions) => void
  hide: () => void
}

export const useContextMenuStore = create<ContextMenuState>((set) => ({
  open: false,
  x: 0,
  y: 0,
  items: [],
  containerTestId: undefined,
  show: (items, x, y, opts) =>
    set({ open: true, items, x, y, containerTestId: opts?.containerTestId }),
  hide: () => set({ open: false, items: [], containerTestId: undefined }),
}))

// Convenience hook for components that just want to dispatch.
export function useContextMenu() {
  return useContextMenuStore((s) => s.show)
}
