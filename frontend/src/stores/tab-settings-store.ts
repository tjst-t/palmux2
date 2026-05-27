// tab-settings-store.ts — Sadf90e
//
// Per-tab settings store backed by
//   GET  /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/settings
//   PATCH /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/settings
//
// Replaces the branch-scope branch-settings-store. Cached keyed by
// "{repoId}/{branchId}/{tabId}" so multiple tabs on the same branch each
// hold their own mode independently.

import { create } from 'zustand'
import { api, type TabSettings } from '../lib/api'

type TabKey = string // "{repoId}/{branchId}/{tabId}"

interface TabSettingsState {
  /** Loaded settings keyed by "{repoId}/{branchId}/{tabId}". */
  settings: Record<TabKey, TabSettings>

  /** Fetch (or re-fetch) tab settings from the server. */
  fetchSettings: (repoId: string, branchId: string, tabId: string) => Promise<void>

  /** Patch claude_mode (or future fields) and persist to server. */
  patchSettings: (
    repoId: string,
    branchId: string,
    tabId: string,
    update: Partial<TabSettings>,
  ) => Promise<TabSettings>

  /** Return cached settings or a safe default. Synchronous — does NOT fetch. */
  getSettings: (repoId: string, branchId: string, tabId: string) => TabSettings
}

// DEFAULT_TAB_SETTINGS is exported and shared across all consumers (tab-bar,
// tab-content, command-palette, toolbar) so React state-equality checks see
// the same reference and selector subscriptions don't loop. Falling-through
// to agent matches the migration-safe default for tabs that were created
// before claude_mode was a thing.
export const DEFAULT_TAB_SETTINGS: TabSettings = { claude_mode: 'agent' }

export const useTabSettingsStore = create<TabSettingsState>((set, get) => ({
  settings: {},

  fetchSettings: async (repoId, branchId, tabId) => {
    const key: TabKey = `${repoId}/${branchId}/${tabId}`
    try {
      const s = await api.get<TabSettings>(
        `/api/repos/${encodeURIComponent(repoId)}` +
          `/branches/${encodeURIComponent(branchId)}` +
          `/tabs/${encodeURIComponent(tabId)}/settings`,
      )
      set((state) => ({ settings: { ...state.settings, [key]: s } }))
    } catch {
      // Non-fatal: leave existing cached value or default.
    }
  },

  patchSettings: async (repoId, branchId, tabId, update) => {
    const key: TabKey = `${repoId}/${branchId}/${tabId}`
    const result = await api.patch<TabSettings>(
      `/api/repos/${encodeURIComponent(repoId)}` +
        `/branches/${encodeURIComponent(branchId)}` +
        `/tabs/${encodeURIComponent(tabId)}/settings`,
      update,
    )
    set((state) => ({ settings: { ...state.settings, [key]: result } }))
    return result
  },

  getSettings: (repoId, branchId, tabId) => {
    const key: TabKey = `${repoId}/${branchId}/${tabId}`
    return get().settings[key] ?? DEFAULT_TAB_SETTINGS
  },
}))
