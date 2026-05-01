import { create } from 'zustand'

import {
  api,
  type AvailableRepoEntry,
  type Branch,
  type BranchPickerEntry,
  type OrphanSession,
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
  detail?: string
  createdAt: string
  /** Stable id for in-process notifications (e.g. claude permission requests). */
  requestId?: string
  /** Inline action buttons. The string `action` is interpreted by the UI. */
  actions?: NotificationAction[]
  /** True once the underlying request has been answered/cancelled. */
  resolved?: boolean
  /** S009: originating Claude tab id (e.g. "claude:claude-2") so the
   *  Activity Inbox can address per-tab REST endpoints when the user
   *  answers a permission from the inbox without opening the WS. */
  tabId?: string
  /** S009: Display name of the originating tab (e.g. "Claude", "Claude 2"). */
  tabName?: string
}

export interface NotificationAction {
  label: string
  action: string
}

export interface BranchNotificationState {
  unreadCount: number
  lastMessage?: string
  lastType?: string
  lastAt?: string
  notifications?: NotificationItem[]
}

// AgentBranchState mirrors the per-branch claude-tab state surfaced via the
// global event bus. Drawer pip / Activity Inbox / @-workspace switcher all
// read from this slice so they don't need their own WS connection.
export type AgentStatus =
  | 'idle'
  | 'starting'
  | 'thinking'
  | 'tool_running'
  | 'awaiting_permission'
  | 'error'

export interface PendingPermission {
  permissionId: string
  toolName: string
  input?: unknown
}

export interface AgentBranchState {
  status: AgentStatus
  totalCostUsd: number
  lastTurnEndAt?: string
  /** Last unresolved permission, if any. Cleared on resolve / clear. */
  pendingPermission?: PendingPermission
  /** Last error message (e.g. CLI exited unexpectedly). */
  lastError?: string
}

export type ImeMode = 'none' | 'direct' | 'ime'

export interface DeviceSettings {
  theme: 'dark' | 'light'
  fontSize: number
  drawerPinned: boolean
  drawerWidth: number
  branchSortOrder: 'name' | 'activity'
  scrollbackLines: number
  splitEnabled: boolean
  splitRatio: number
  filesListRatio: number
  imeMode: ImeMode
}

export interface GlobalSettings {
  branchSortOrder?: 'name' | 'activity'
  lastActiveBranch?: string
  /** S008 renamed `imageUploadDir` → `attachmentUploadDir`. The
   *  legacy key is still tolerated by the server (loader migrates).
   *  Kept on the type for one release so existing patches still
   *  compile. */
  attachmentUploadDir?: string
  attachmentTtlDays?: number
  imageUploadDir?: string
  /** S009: cap on parallel Claude tabs per branch (default 3). */
  maxClaudeTabsPerBranch?: number
  /** S009: cap on Bash tabs per branch (default 5). */
  maxBashTabsPerBranch?: number
  /** S010: max bytes shipped to the Files-tab preview (default 10 MiB).
   *  Above this we render a "too large to preview" placeholder and skip
   *  fetching the body. */
  previewMaxBytes?: number
  /** S015: glob patterns marking auto-generated worktrees (subagent /
   *  autopilot output). Default `[".claude/worktrees/*"]`. */
  autoWorktreePathPatterns?: string[]
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
  filesListRatio: 35,
  imeMode: 'none',
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
  tryNum('filesListRatio', 'filesListRatio')
  tryStr('imeMode', 'imeMode')
  if (out.imeMode !== 'none' && out.imeMode !== 'direct' && out.imeMode !== 'ime') {
    out.imeMode = 'none'
  }
  // Clamp persisted ratios to the supported drag range.
  if (out.splitRatio < 20 || out.splitRatio > 80) out.splitRatio = 50
  if (out.filesListRatio < 15 || out.filesListRatio > 75) out.filesListRatio = 35
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

export interface ServerInfo {
  version?: string
  open?: boolean
  portmanURL?: string
}

interface PalmuxStoreState {
  bootstrapped: boolean
  loading: boolean
  error: string | null

  repos: Repository[]
  availableRepos: AvailableRepoEntry[]
  branchPicker: { repoId: string; entries: BranchPickerEntry[] } | null
  orphanSessions: OrphanSession[]
  serverInfo: ServerInfo

  globalSettings: GlobalSettings
  deviceSettings: DeviceSettings

  connectionStatus: ConnectionStatus

  focusedPanel: FocusedPanel
  mobileDrawerOpen: boolean

  notifications: Record<string, BranchNotificationState>
  /** Per-branch ("{repoId}/{branchId}") Claude-tab state. */
  agents: Record<string, AgentBranchState>

  // Actions ────────────────────────────────────────────────────────────────
  bootstrap: () => Promise<void>
  reloadRepos: () => Promise<void>
  reloadAvailableRepos: () => Promise<void>
  reloadBranchPicker: (repoId: string) => Promise<void>
  reloadOrphanSessions: () => Promise<void>
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

  /** S015: move a branch into `my` by appending to
   *  repos.json#userOpenedBranches. Optimistic — the local Branch's
   *  `category` flips to "user" immediately; on API failure we revert
   *  and surface the error. */
  promoteBranch: (repoId: string, branchId: string) => Promise<void>
  /** S015: opposite of promoteBranch. */
  demoteBranch: (repoId: string, branchId: string) => Promise<void>
}

export const usePalmuxStore = create<PalmuxStoreState>()((set, get) => ({
  bootstrapped: false,
  loading: false,
  error: null,
  repos: [],
  availableRepos: [],
  branchPicker: null,
  orphanSessions: [],
  serverInfo: {},
  globalSettings: {},
  deviceSettings: loadDeviceSettings(),
  connectionStatus: 'connecting',
  focusedPanel: 'left',
  mobileDrawerOpen: false,
  notifications: {},
  agents: {},

  bootstrap: async () => {
    if (get().bootstrapped || get().loading) return
    set({ loading: true, error: null })
    try {
      const [repos, settings, notifications, orphans, info] = await Promise.all([
        api.get<Repository[]>('/api/repos'),
        api.get<GlobalSettings>('/api/settings'),
        api
          .get<Record<string, BranchNotificationState>>('/api/notifications')
          .catch(() => ({}) as Record<string, BranchNotificationState>),
        api.get<OrphanSession[]>('/api/orphan-sessions').catch(() => [] as OrphanSession[]),
        api.get<ServerInfo>('/api/health').catch(() => ({}) as ServerInfo),
      ])
      set({
        repos,
        globalSettings: settings,
        notifications,
        orphanSessions: orphans ?? [],
        serverInfo: info ?? {},
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

  reloadOrphanSessions: async () => {
    try {
      const list = await api.get<OrphanSession[]>('/api/orphan-sessions')
      set({ orphanSessions: list ?? [] })
    } catch {
      // ignore — best-effort
    }
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
    // S015: cross-client promote/demote. Apply locally (cheap) — a
    // background reloadRepos is unnecessary because category is the
    // only field that changed and the payload carries the new value.
    if (ev.type === 'branch.categoryChanged' && ev.repoId && ev.branchId && ev.payload) {
      const payload = ev.payload as { category?: string }
      const cat = payload.category as Branch['category']
      if (cat === 'user' || cat === 'unmanaged' || cat === 'subagent') {
        set((state) => ({
          repos: state.repos.map((r) =>
            r.id !== ev.repoId
              ? r
              : {
                  ...r,
                  openBranches: r.openBranches.map((b) =>
                    b.id === ev.branchId ? { ...b, category: cat } : b,
                  ),
                },
          ),
        }))
      }
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
      // Detect "actually-new message" by comparing the timestamp against
      // what we last saw for this branch. ClearByRequestID also publishes
      // a notification frame (just to flip Resolved on the matched entry)
      // and we don't want that to re-fire the OS banner.
      const prev = get().notifications[key]
      const isNewMessage =
        ev.type === 'notification' &&
        !!state.lastMessage &&
        (!prev?.lastAt || state.lastAt !== prev.lastAt)
      set((s) => ({
        notifications: { ...s.notifications, [key]: state },
      }))
      if (isNewMessage) {
        maybePostNotification(state.lastMessage!)
      }
    }

    // Claude tab cross-tab events. Each branch maintains a tiny state
    // record so the Drawer pip / Inbox / @-workspace switcher can show
    // status without opening their own WS.
    if (ev.type.startsWith('claude.') && ev.repoId && ev.branchId) {
      const key = `${ev.repoId}/${ev.branchId}`
      const payload = (ev.payload ?? {}) as Record<string, unknown>
      set((s) => {
        const cur: AgentBranchState = s.agents[key] ?? {
          status: 'idle',
          totalCostUsd: 0,
        }
        const next: AgentBranchState = { ...cur }
        switch (ev.type) {
          case 'claude.status': {
            const status = payload.status as AgentStatus | undefined
            if (status) next.status = status
            break
          }
          case 'claude.permission_request': {
            next.pendingPermission = {
              permissionId: String(payload.permissionId ?? ''),
              toolName: String(payload.toolName ?? 'tool'),
              input: payload.input,
            }
            next.status = 'awaiting_permission'
            break
          }
          case 'claude.permission_resolved': {
            next.pendingPermission = undefined
            break
          }
          case 'claude.turn_end': {
            const cost = Number(payload.totalCostUsd ?? 0)
            if (Number.isFinite(cost) && cost > 0) {
              next.totalCostUsd = cur.totalCostUsd + cost
            }
            next.lastTurnEndAt = new Date().toISOString()
            next.lastError = undefined
            // turn_end strongly implies the agent is back to idle — even if
            // a stale 'thinking' status.change got dropped on the wire,
            // this guarantees the Drawer pip clears.
            if (cur.status !== 'awaiting_permission') {
              next.status = 'idle'
            }
            break
          }
          case 'claude.error': {
            next.lastError = String(payload.message ?? 'error')
            next.status = 'error'
            break
          }
          case 'claude.session_replaced': {
            next.totalCostUsd = 0
            next.pendingPermission = undefined
            next.lastError = undefined
            next.status = 'idle'
            break
          }
        }
        return { agents: { ...s.agents, [key]: next } }
      })
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

  // S015 promote/demote: optimistic local update + REST. The server
  // also broadcasts `branch.categoryChanged` which the applyEvent path
  // catches, so multiple browsers stay in sync. We don't need to
  // re-fetch ourselves on success.
  promoteBranch: async (repoId, branchId) => {
    const prev = get().repos
    set((state) => ({
      repos: state.repos.map((r) =>
        r.id !== repoId
          ? r
          : {
              ...r,
              openBranches: r.openBranches.map((b) =>
                b.id === branchId ? { ...b, category: 'user' as const } : b,
              ),
            },
      ),
    }))
    try {
      await api.post(
        `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/promote`,
      )
    } catch (err) {
      // Revert on failure.
      set({ repos: prev })
      throw err
    }
  },
  demoteBranch: async (repoId, branchId) => {
    const prev = get().repos
    // Compute likely target category from the existing record (path
    // pattern matching is server-authoritative, so we tentatively pick
    // "unmanaged" — the WS event will correct us if it's actually
    // "subagent").
    set((state) => ({
      repos: state.repos.map((r) =>
        r.id !== repoId
          ? r
          : {
              ...r,
              openBranches: r.openBranches.map((b) =>
                b.id === branchId ? { ...b, category: 'unmanaged' as const } : b,
              ),
            },
      ),
    }))
    try {
      await api.delete(
        `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/promote`,
      )
    } catch (err) {
      set({ repos: prev })
      throw err
    }
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

export const selectAgentState =
  (repoId: string, branchId: string) =>
  (s: PalmuxStoreState): AgentBranchState | undefined =>
    s.agents[`${repoId}/${branchId}`]

// maybePostNotification asks for permission once, then surfaces a system
// notification on subsequent events. Vibrates if the API is supported. All
// optional — a denied permission silently degrades to badges-only UX.
//
// Skipped when the palmux tab is currently focused: the user is here, the
// in-app Inbox badge is enough; surfacing an OS banner on top would be
// noise. (Most browsers also auto-suppress notifications for the focused
// document, but we double-up the check so headless / unfocused-window
// cases work consistently.)
function maybePostNotification(message: string) {
  if (typeof window === 'undefined' || typeof Notification === 'undefined') return
  if (typeof document !== 'undefined' && document.hasFocus?.()) return
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
