// branch-settings-store.ts — S1f75ec-2
//
// Per-branch settings store backed by
//   GET  /api/repos/{repoId}/branches/{branchId}/settings
//   PATCH /api/repos/{repoId}/branches/{branchId}/settings
//
// Uses a simple record keyed by "{repoId}/{branchId}" so multiple branches
// can be cached without full re-bootstrapping.

import { create } from 'zustand'
import { api, type BranchSettings } from '../lib/api'

type BranchKey = string // "{repoId}/{branchId}"

interface BranchSettingsState {
  /** Loaded settings keyed by "{repoId}/{branchId}". */
  settings: Record<BranchKey, BranchSettings>

  /** Fetch (or re-fetch) branch settings from the server. */
  fetchSettings: (repoId: string, branchId: string) => Promise<void>

  /** Patch a single field and persist to server. */
  patchSettings: (
    repoId: string,
    branchId: string,
    update: Partial<BranchSettings>,
  ) => Promise<BranchSettings>

  /** Return cached settings or a safe default. */
  getSettings: (repoId: string, branchId: string) => BranchSettings
}

const DEFAULT_SETTINGS: BranchSettings = { claude_mode: 'agent' }

export const useBranchSettingsStore = create<BranchSettingsState>((set, get) => ({
  settings: {},

  fetchSettings: async (repoId, branchId) => {
    const key: BranchKey = `${repoId}/${branchId}`
    try {
      const s = await api.get<BranchSettings>(
        `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/settings`,
      )
      set((state) => ({ settings: { ...state.settings, [key]: s } }))
    } catch {
      // Non-fatal: leave existing cached value or default.
    }
  },

  patchSettings: async (repoId, branchId, update) => {
    const key: BranchKey = `${repoId}/${branchId}`
    const result = await api.patch<BranchSettings>(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/settings`,
      update,
    )
    set((state) => ({ settings: { ...state.settings, [key]: result } }))
    return result
  },

  getSettings: (repoId, branchId) => {
    const key: BranchKey = `${repoId}/${branchId}`
    return get().settings[key] ?? DEFAULT_SETTINGS
  },
}))
