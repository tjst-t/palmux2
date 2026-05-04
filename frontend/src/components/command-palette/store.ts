// Command palette open/close + query state.
//
// Open via Cmd+K / Ctrl+K (registered globally in CommandPalette). The
// palette searches across sources, narrowed by a prefix:
//   (none) — recents (empty query) or all sources mixed (with query)
//   @      — workspaces (open repos / branches / tabs)
//   #      — tabs of the active branch
//   >      — Makefile / npm commands + builtin tab/theme/font actions
//   :      — files in the active branch (path search)
//   ?      — content grep in the active branch (S031-5)
//
// S031-1: '/' slash mode removed. '/compact' etc. are in Claude tab composer.

import { create } from 'zustand'

interface State {
  open: boolean
  initialQuery: string
  show: (query?: string) => void
  hide: () => void
  toggle: () => void
}

export const useCommandPaletteStore = create<State>((set, get) => ({
  open: false,
  initialQuery: '',
  show: (query = '') => set({ open: true, initialQuery: query }),
  hide: () => set({ open: false }),
  toggle: () => (get().open ? set({ open: false }) : set({ open: true, initialQuery: '' })),
}))
