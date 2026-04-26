// Command palette open/close + query state.
//
// Open via Cmd+K / Ctrl+K (registered globally in CommandPalette). The
// palette searches across four sources, narrowed by a prefix:
//   (none) — all sources mixed
//   @      — workspaces (open repos / branches / tabs)
//   /      — slash commands sent to a Claude tab
//   >      — Makefile / package.json commands for the active branch
//   :      — files in the active branch

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
