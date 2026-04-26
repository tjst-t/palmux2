import { create } from 'zustand'

import {
  api,
  type AvailableRepoEntry,
  type Branch,
  type BranchPickerEntry,
  type Repository,
  type Tab,
} from '../lib/api'
import type { ToolbarConfig } from '../types/toolbar'

export type ConnectionStatus = 'connected' | 'connecting' | 'disconnected'
export type FocusedPanel = 'left' | 'right'

export interface NotificationItem {
  type: string
  message?: string
  title?: string
  createdAt: string
}

export interface BranchNotificationState {
  unreadCount: number
  lastMessage?: string
  lastType?: string
  lastAt?: string
  notifications?: NotificationItem[]
}

export interface DeviceSettings {
  theme: 'dark' | 'light'
  fontSize: number
  drawerPinned: boolean
  drawerWidth: number
  branchSortOrder: 'name' | 'activity'
  scrollbackLines: number
  splitEnabled: boolean
  splitRatio: number
}

export interface GlobalSettings {
  branchSortOrder?: 'name' | 'activity'
  lastActiveBranch?: string
  imageUploadDir?: string
  toolbar?: Partial<ToolbarConfig>
}

const DEVICE_DEFAULTS: DeviceSettings = {
  theme: 'dark',
  fontSize: 14,
  drawerPinned: true,
  drawerWidth: 280,
  branchSortOrder: 'name',
  scrollbackLines: 5000,
  splitEnabled: false,
  splitRatio: 50,
}

const LS_PREFIX = 'palmux:'

function loadDeviceSettings(): DeviceSettings {
  if (typeof localStorage === 'undefined') return DEVICE_DEFAULTS
  const out: DeviceSettings = { ...DEVICE_DEFAULTS }
  const bag = out as unknown as Record<string, unknown>
  const tryNum = (key: string, target: keyof DeviceSettings) => {
    const v = localStorage.getItem(LS_PREFIX + key)
    if (v == null) return
    const n = Number(v)
    if (!Number.isNaN(n)) bag[target] = n
  }
  const tryStr = (key: string, target: keyof DeviceSettings) => {
    const v = localStorage.getItem(LS_PREFIX + key)
    if (v != null) bag[target] = v
  }
  const tryBool = (key: string, target: keyof DeviceSettings) => {
    const v = localStorage.getItem(LS_PREFIX + key)
    if (v != null) bag[target] = v === 'true'
  }
  tryStr('theme', 'theme')
  tryNum('fontSize', 'fontSize')
  tryBool('drawerPinned', 'drawerPinned')
  tryNum('drawerWidth', 'drawerWidth')
  tryStr('branchSortOrder', 'branchSortOrder')
  tryNum('scrollbackLines', 'scrollbackLines')
  tryBool('splitEnabled', 'splitEnabled')
  tryNum('splitRatio', 'splitRatio')
  // Clamp the persisted ratio to the supported drag range.
  if (out.splitRatio < 20 || out.splitRatio > 80) out.splitRatio = 50
  return out
}

function persistDeviceSetting<K extends keyof DeviceSettings>(key: K, value: DeviceSettings[K]) {
  if (typeof localStorage === 'undefined') return
  localStorage.setItem(LS_PREFIX + key, String(value))
}

export interface RemoteEvent {
  type: string
  repoId?: string
  branchId?: string
  tabId?: string
  payload?: unknown
}

interface PalmuxStoreState {
  bootstrapped: boolean
  loading: boolean
  error: string | null

  repos: Repository[]
  availableRepos: AvailableRepoEntry[]
  branchPicker: { repoId: string; entries: BranchPickerEntry[] } | null

  globalSettings: GlobalSettings
  deviceSettings: DeviceSettings

  connectionStatus: ConnectionStatus

  focusedPanel: FocusedPanel
  mobileDrawerOpen: boolean

  notifications: Record<string, BranchNotificationState>

  // Actions ────────────────────────────────────────────────────────────────
  bootstrap: () => Promise<void>
  reloadRepos: () => Promise<void>
  reloadAvailableRepos: () => Promise<void>
  reloadBranchPicker: (repoId: string) => Promise<void>
  applyEvent: (ev: RemoteEvent) => void
  setConnectionStatus: (status: ConnectionStatus) => void
  setFocusedPanel: (panel: FocusedPanel) => void
  setMobileDrawerOpen: (open: boolean) => void
  clearBranchNotifications: (repoId: string, branchId: string) => Promise<void>

  setDeviceSetting: <K extends keyof DeviceSettings>(key: K, value: DeviceSettings[K]) => void

  openRepo: (repoId: string) => Promise<Repository>
  closeRepo: (repoId: string) => Promise<void>
  starRepo: (repoId: string, starred: boolean) => Promise<void>

  openBranch: (repoId: string, branchName: string) => Promise<Branch>
  closeBranch: (repoId: string, branchId: string) => Promise<void>

  addTab: (repoId: string, branchId: string, type: string, name?: string) => Promise<Tab>
  removeTab: (repoId: string, branchId: string, tabId: string) => Promise<void>
  renameTab: (repoId: string, branchId: string, tabId: string, name: string) => Promise<void>
}

export const usePalmuxStore = create<PalmuxStoreState>()((set, get) => ({
  bootstrapped: false,
  loading: false,
  error: null,
  repos: [],
  availableRepos: [],
  branchPicker: null,
  globalSettings: {},
  deviceSettings: loadDeviceSettings(),
  connectionStatus: 'connecting',
  focusedPanel: 'left',
  mobileDrawerOpen: false,
  notifications: {},

  bootstrap: async () => {
    if (get().bootstrapped || get().loading) return
    set({ loading: true, error: null })
    try {
      const [repos, settings, notifications] = await Promise.all([
        api.get<Repository[]>('/api/repos'),
        api.get<GlobalSettings>('/api/settings'),
        api
          .get<Record<string, BranchNotificationState>>('/api/notifications')
          .catch(() => ({}) as Record<string, BranchNotificationState>),
      ])
      set({
        repos,
        globalSettings: settings,
        notifications,
        bootstrapped: true,
        loading: false,
      })
    } catch (err) {
      set({ error: err instanceof Error ? err.message : String(err), loading: false })
    }
  },

  reloadRepos: async () => {
    try {
      const repos = await api.get<Repository[]>('/api/repos')
      set({ repos })
    } catch (err) {
      set({ error: err instanceof Error ? err.message : String(err) })
    }
  },

  reloadAvailableRepos: async () => {
    const list = await api.get<AvailableRepoEntry[]>('/api/repos/available')
    set({ availableRepos: list })
  },

  reloadBranchPicker: async (repoId) => {
    const entries = await api.get<BranchPickerEntry[]>(
      `/api/repos/${encodeURIComponent(repoId)}/branch-picker`,
    )
    set({ branchPicker: { repoId, entries } })
  },

  applyEvent: (ev) => {
    // Phase 3 takes the simple-but-correct route: any domain event triggers
    // a /api/repos refresh. Phase 10 can swap in fine-grained updates.
    const domainEvents = new Set([
      'repo.opened',
      'repo.closed',
      'repo.starred',
      'repo.unstarred',
      'branch.opened',
      'branch.closed',
      'tab.added',
      'tab.removed',
      'tab.renamed',
    ])
    if (domainEvents.has(ev.type)) {
      void get().reloadRepos()
    }
    if (ev.type === 'settings.updated' && ev.payload) {
      set({ globalSettings: ev.payload as GlobalSettings })
    }
    if (
      (ev.type === 'notification' || ev.type === 'notification.cleared') &&
      ev.repoId &&
      ev.branchId &&
      ev.payload
    ) {
      const key = `${ev.repoId}/${ev.branchId}`
      const state = ev.payload as BranchNotificationState
      set((s) => ({
        notifications: { ...s.notifications, [key]: state },
      }))
      // Browser-level notification on a new message (not a clear).
      if (ev.type === 'notification' && state.lastMessage) {
        maybePostNotification(state.lastMessage)
      }
    }
  },

  setConnectionStatus: (status) => set({ connectionStatus: status }),
  setFocusedPanel: (panel) => set({ focusedPanel: panel }),
  setMobileDrawerOpen: (open) => set({ mobileDrawerOpen: open }),

  clearBranchNotifications: async (repoId, branchId) => {
    const key = `${repoId}/${branchId}`
    const current = get().notifications[key]
    if (!current || current.unreadCount === 0) return
    // Optimistic — server will broadcast the canonical state via the WS.
    set((s) => ({
      notifications: {
        ...s.notifications,
        [key]: { ...current, unreadCount: 0 },
      },
    }))
    try {
      await api.post('/api/notify/clear', { repoId, branchId })
    } catch {
      // ignore — next event-stream message will resync.
    }
  },

  setDeviceSetting: (key, value) => {
    persistDeviceSetting(key, value)
    set((state) => ({ deviceSettings: { ...state.deviceSettings, [key]: value } }))
  },

  openRepo: async (repoId) => {
    const repo = await api.post<Repository>(`/api/repos/${encodeURIComponent(repoId)}/open`)
    set((state) => {
      const others = state.repos.filter((r) => r.id !== repo.id)
      return { repos: [...others, repo] }
    })
    return repo
  },
  closeRepo: async (repoId) => {
    await api.post(`/api/repos/${encodeURIComponent(repoId)}/close`)
    set((state) => ({ repos: state.repos.filter((r) => r.id !== repoId) }))
  },
  starRepo: async (repoId, starred) => {
    await api.post(`/api/repos/${encodeURIComponent(repoId)}/${starred ? 'star' : 'unstar'}`)
    set((state) => ({
      repos: state.repos.map((r) => (r.id === repoId ? { ...r, starred } : r)),
    }))
  },

  openBranch: async (repoId, branchName) => {
    const branch = await api.post<Branch>(
      `/api/repos/${encodeURIComponent(repoId)}/branches/open`,
      { branchName },
    )
    await get().reloadRepos()
    return branch
  },
  closeBranch: async (repoId, branchId) => {
    await api.delete(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}`,
    )
    await get().reloadRepos()
  },

  addTab: async (repoId, branchId, type, name) => {
    const tab = await api.post<Tab>(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/tabs`,
      { type, name },
    )
    await get().reloadRepos()
    return tab
  },
  removeTab: async (repoId, branchId, tabId) => {
    await api.delete(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/tabs/${encodeURIComponent(tabId)}`,
    )
    await get().reloadRepos()
  },
  renameTab: async (repoId, branchId, tabId, name) => {
    await api.patch(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/tabs/${encodeURIComponent(tabId)}`,
      { name },
    )
    await get().reloadRepos()
  },
}))

// Convenience selectors
export const selectRepoById = (id: string) => (s: PalmuxStoreState) =>
  s.repos.find((r) => r.id === id)

export const selectBranchById = (repoId: string, branchId: string) => (s: PalmuxStoreState) =>
  s.repos.find((r) => r.id === repoId)?.openBranches.find((b) => b.id === branchId)

export const selectBranchNotifications =
  (repoId: string, branchId: string) =>
  (s: PalmuxStoreState): BranchNotificationState | undefined =>
    s.notifications[`${repoId}/${branchId}`]

// maybePostNotification asks for permission once, then surfaces a system
// notification on subsequent events. Vibrates if the API is supported. All
// optional — a denied permission silently degrades to badges-only UX.
function maybePostNotification(message: string) {
  if (typeof window === 'undefined' || typeof Notification === 'undefined') return
  const fire = () => {
    try {
      new Notification('Palmux', { body: message })
    } catch {
      // ignore (e.g. Safari restricts in some contexts)
    }
    if ('vibrate' in navigator) {
      try {
        navigator.vibrate?.(100)
      } catch {
        // ignore
      }
    }
  }
  if (Notification.permission === 'granted') {
    fire()
  } else if (Notification.permission === 'default') {
    void Notification.requestPermission().then((p) => {
      if (p === 'granted') fire()
    })
  }
}
