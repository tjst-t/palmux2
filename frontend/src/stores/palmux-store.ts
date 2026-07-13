import { create } from 'zustand'

import {
  api,
  type AvailableRepoEntry,
  type Branch,
  type BranchPickerEntry,
  type HostScope,
  type IncusGroupStatus,
  incusGroupApi,
  type OrphanSession,
  type Repository,
  type RuntimeCaps,
  type RuntimeView,
  type SelfUpdateSnapshot,
  selfUpdateApi,
  type TabSet,
  type SubagentCleanupCandidate,
  type SubagentCleanupResult,
  type Tab,
  type WorkspacePorts,
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
  /** S017: number of leading lines of a Read tool result rendered
   *  before the "Show all (X lines)" toggle is offered (default 50). */
  readPreviewLineCount?: number
  /** S021: age threshold (days) for the subagent-cleanup Drawer action. */
  subagentStaleAfterDays?: number
  toolbar?: Partial<ToolbarConfig>
  /** S032: palette-specific settings (user-defined commands etc.) */
  palette?: {
    userCommands?: UserCommand[]
  }
  /** S1f75ec-2: global claude tab settings (default mode for new branches). */
  claude?: {
    default_mode?: 'agent' | 'tui'
    /** claude --permission-mode for launched sessions. Default "auto". */
    permission_mode?: 'default' | 'auto' | 'plan' | 'acceptEdits' | 'bypassPermissions'
  }
  /** S8478ca-3: global default runtime applied when no per-WS/per-repo override. */
  defaultRuntime?: {
    kind?: 'host' | 'incus-container'
  }
}

/** S032: A user-defined command entry in palette.userCommands. */
export interface UserCommand {
  name: string
  target: 'bash' | 'url' | 'files'
  command?: string
  url?: string
  path?: string
  notes?: string
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
  /** S0c6a1b: reserved, repository-independent host scope descriptor. */
  host: HostScope | null
  /** S0c6a1b: synthetic Repository (one branch) for the host scope. Kept out
   *  of `repos` so it never appears in the Repositories list; selectors fall
   *  back to it so /host--0000/host/<tab> routes resolve. */
  hostRepo: Repository | null

  /** S8478ca-5: runtime capability probe (GET /api/runtimes). Null until loaded. */
  runtimeCaps: RuntimeCaps | null

  globalSettings: GlobalSettings
  deviceSettings: DeviceSettings

  connectionStatus: ConnectionStatus

  focusedPanel: FocusedPanel
  mobileDrawerOpen: boolean
  /** Non-null requests the settings panel open on this tab; command-palette
   * consumes it and resets to null. Set by the header gear button. */
  settingsRequestTab: string | null
  /** Per-branch counter bumped on branch.restarted (runtime switch / container
   * regenerate). claude-tui tabs watch their key and reconnect their own WS —
   * terminalManager only covers xterm (bash/agent) tabs, not the tui daemon WS. */
  branchRestartSignal: Record<string, number>

  notifications: Record<string, BranchNotificationState>
  /** Per-branch ("{repoId}/{branchId}") Claude-tab state. */
  agents: Record<string, AgentBranchState>

  /** See8bd4-3: Per-branch ports payload, keyed by "{repoId}/{branchId}".
   *  Updated by the `branch.portsChanged` WS event so the Ports tab
   *  refreshes without a full REST round-trip. */
  branchPorts: Record<string, WorkspacePorts>

  /** See8bd4-3: optimistically patch a single port in branchPorts after a
   *  successful expose/unexpose so the UI updates immediately even if the
   *  branch.portsChanged WS event is briefly delayed (no snap-back). No-op if
   *  no WS snapshot exists yet (the component's local REST state covers that). */
  applyBranchPortPatch: (
    repoId: string,
    branchId: string,
    port: number,
    patch: Partial<import('../lib/api').PortView>,
  ) => void

  // Actions ────────────────────────────────────────────────────────────────
  bootstrap: () => Promise<void>
  reloadRepos: () => Promise<void>
  reloadAvailableRepos: () => Promise<void>
  reloadBranchPicker: (repoId: string) => Promise<void>
  reloadOrphanSessions: () => Promise<void>
  /** S0c6a1b: (re)load the host scope descriptor + its bash tab list. */
  refreshHost: () => Promise<void>
  applyEvent: (ev: RemoteEvent) => void
  setConnectionStatus: (status: ConnectionStatus) => void
  setFocusedPanel: (panel: FocusedPanel) => void
  setMobileDrawerOpen: (open: boolean) => void
  requestSettings: (tab?: string) => void
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
  /** S020: reorder a Multiple()=true group within one branch.
   *  `order` MUST be a contiguous slice of tab IDs that all share one
   *  Multiple()=true type — the server rejects cross-group payloads
   *  with 400. */
  reorderTabs: (repoId: string, branchId: string, order: string[]) => Promise<void>

  /** S015: move a branch into `my` by appending to
   *  repos.json#userOpenedBranches. Optimistic — the local Branch's
   *  `category` flips to "user" immediately; on API failure we revert
   *  and surface the error. */
  promoteBranch: (repoId: string, branchId: string) => Promise<void>
  /** S015: opposite of promoteBranch. */
  demoteBranch: (repoId: string, branchId: string) => Promise<void>

  /** S023: persist the most-recently-navigated branch for a repo. The FE
   *  fires this in the background on every successful branch nav so a
   *  collapsed repo can be re-entered with one click. Idempotent and
   *  silent on failure (the hook discards rejections). */
  setLastActiveBranch: (repoId: string, branchName: string) => Promise<void>

  /** S021: list stale subagent worktrees for a repo (dry-run). */
  listStaleSubagentWorktrees: (
    repoId: string,
  ) => Promise<{ thresholdDays: number; candidates: SubagentCleanupCandidate[] }>

  /** S021: bulk-remove the selected stale subagent worktrees. Returns the
   *  per-worktree outcome so the caller can update its dialog. */
  cleanupSubagentWorktrees: (
    repoId: string,
    branchNames?: string[],
  ) => Promise<SubagentCleanupResult>

  /** S021: move a subagent worktree to gwq's standard path AND record
   *  it as user-opened. */
  promoteSubagentBranch: (
    repoId: string,
    branchId: string,
  ) => Promise<{ branch: Branch; destination: string }>

  /** S8478ca-5: fetch (or refresh) GET /api/runtimes capability probe. */
  loadRuntimeCaps: () => Promise<RuntimeCaps>

  /** S8478ca-5: refresh a single repo's data (incl. runtime views) from GET /api/repos/{repoId}. */
  reloadRepo: (repoId: string) => Promise<void>

  /** S8478ca-5: PATCH per-workspace runtime kind. */
  patchWorkspaceRuntime: (repoId: string, branchId: string, kind: string) => Promise<void>

  /** S7364e3: POST regenerate an incus container from the current image. */
  regenerateContainer: (repoId: string, branchId: string) => Promise<void>

  // S6ab0ed: self-update ───────────────────────────────────────────────────
  /** Latest detection snapshot (null until loaded). */
  selfUpdate: SelfUpdateSnapshot | null
  /** True while a GUI-triggered "Update all" is in flight (drives the progress
   *  badge/panel + reconnect handshake). */
  updateInProgress: boolean
  /** Version recorded when the update was triggered, compared against /health
   *  after reconnect to detect success. */
  updateBaselineVersion: string | null
  /** One-shot completion notice ("vX に更新しました") shown as a toast. Cleared
   *  by the component after display. `message`, when set, replaces the default
   *  "<version> に更新しました" text (e.g. the incus-admin recover completion). */
  updateToast: { version: string; message?: string } | null
  /** Set when the post-restart reconnect handshake decided the update failed
   *  (version unchanged after timeout). */
  updateFailed: boolean
  /** Fetch (or refresh) the self-update snapshot. */
  loadSelfUpdate: () => Promise<void>
  refreshSelfUpdate: () => Promise<void>
  /** Trigger the one-click "Update all". Throws (ApiError) on 409 (Nix-unmanaged)
   *  so the caller can show manual-update guidance. */
  runSelfUpdate: () => Promise<void>
  /** S673a42-2: kick the appliance HOST update (nix flake update palmux +
   *  nixos-rebuild switch) on a NixOS appliance. Reuses the same updateInProgress
   *  reconnect handshake as runSelfUpdate (the switch restarts palmux2 onto the new
   *  version) and additionally polls the rebuild-unit state to catch a pre-restart
   *  failure. Throws on 409 (not a NixOS host). */
  runNixosRebuildUpdate: () => Promise<void>
  /** S673a42-3: true while a GUI-kicked palmux-ws image fetch is running. */
  imageInstallInProgress: boolean
  /** S673a42-3: last image-fetch error (null = none). */
  imageInstallError: string | null
  /** S673a42-3: kick a palmux-ws image fetch (POST /api/selfupdate/image-install)
   *  and poll until done; on success reload the snapshot (badge clears) and repos
   *  (so the per-branch "Update container" drift affordance refreshes). */
  runImageInstall: () => Promise<void>
  /** Clear the completion toast after it's been shown. */
  clearUpdateToast: () => void

  // Sfef725: incus-admin stale-group detection + GUI click-recover ──────────
  /** Latest incus-admin group detection (null until loaded). */
  incusGroup: IncusGroupStatus | null
  /** True while a click-recover (user-manager restart) is in flight (drives the
   *  same WS-drop → /health reconnect handshake as the self-update). */
  incusGroupFixInProgress: boolean
  /** Fetch (or refresh) the incus-admin group status. Best-effort. */
  loadIncusGroup: () => Promise<void>
  /** Trigger the click-recover (POST /api/incus-group/fix). Throws on 409
   *  (no privileged verb) so the caller shows the manual command instead. */
  fixIncusGroup: () => Promise<void>
}

export const usePalmuxStore = create<PalmuxStoreState>()((set, get) => ({
  bootstrapped: false,
  loading: false,
  error: null,
  repos: [],
  availableRepos: [],
  branchPicker: null,
  host: null,
  hostRepo: null,
  orphanSessions: [],
  serverInfo: {},
  globalSettings: {},
  deviceSettings: loadDeviceSettings(),
  connectionStatus: 'connecting',
  focusedPanel: 'left',
  mobileDrawerOpen: false,
  settingsRequestTab: null,
  branchRestartSignal: {},
  notifications: {},
  agents: {},
  runtimeCaps: null,
  branchPorts: {},

  selfUpdate: null,
  updateInProgress: false,
  updateBaselineVersion: null,
  updateToast: null,
  updateFailed: false,
  imageInstallInProgress: false,
  imageInstallError: null,
  incusGroup: null,
  incusGroupFixInProgress: false,

  applyBranchPortPatch: (repoId, branchId, port, patch) =>
    set((s) => {
      const key = `${repoId}/${branchId}`
      const cur = s.branchPorts[key]
      if (!cur) return {}
      return {
        branchPorts: {
          ...s.branchPorts,
          [key]: {
            ...cur,
            ports: cur.ports.map((p) => (p.port === port ? { ...p, ...patch } : p)),
          },
        },
      }
    }),

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
        loading: false,
      })
      // S6ab0ed: load the self-update snapshot in the background (best-effort —
      // a failure must not block bootstrap; the WS event refreshes it later).
      void get().loadSelfUpdate()
      // Sfef725: detect the incus-admin stale-group condition on cold load so
      // the recover surface appears proactively (best-effort).
      void get().loadIncusGroup()
      // S0c6a1b: load the reserved host scope BEFORE flipping `bootstrapped`
      // so the /host--0000/host/* route resolves on a cold load / reload —
      // otherwise MainLayout's "branch not found → bounce to /" effect fires
      // in the window between bootstrapped=true and hostRepo being set.
      // Best-effort: a failure must not block bootstrap.
      await get().refreshHost().catch(() => {})
      set({ bootstrapped: true })
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

  refreshHost: async () => {
    try {
      const desc = get().host ?? (await api.get<HostScope>('/api/host'))
      const tabSet = await api.get<TabSet>(
        `/api/repos/${encodeURIComponent(desc.repoId)}/branches/${encodeURIComponent(desc.branchId)}/tabs`,
      )
      const branch: Branch = {
        id: desc.branchId,
        name: desc.displayName,
        worktreePath: '',
        repoId: desc.repoId,
        isPrimary: false,
        tabSet,
        lastActivity: new Date().toISOString(),
        category: 'user',
      }
      const hostRepo: Repository = {
        id: desc.repoId,
        ghqPath: '',
        fullPath: '',
        starred: false,
        openBranches: [branch],
      }
      set({ host: desc, hostRepo })
    } catch {
      // best-effort — leave any previously-loaded host scope intact
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
      'tab.reordered',
    ])
    if (domainEvents.has(ev.type)) {
      // S0c6a1b: host-scoped tab events refresh the host scope (not in
      // /api/repos); everything else refreshes the repo list.
      if (ev.repoId === HOST_REPO_ID) void get().refreshHost()
      else void get().reloadRepos()
    }
    // S1e8d02: in-place `git checkout` rewrites only the branch's
    // display name. The BranchID, tmux session, Claude agent process,
    // tab list, and Drawer position are all preserved. Update the
    // `name` field in place — no reload, no remove+add.
    if (
      ev.type === 'branch.head_changed' &&
      ev.repoId &&
      ev.branchId &&
      ev.payload
    ) {
      const payload = ev.payload as {
        oldBranch?: string
        newBranch?: string
        worktreePath?: string
      }
      const next = payload.newBranch ?? ''
      if (next) {
        set((state) => ({
          repos: state.repos.map((r) =>
            r.id !== ev.repoId
              ? r
              : {
                  ...r,
                  openBranches: r.openBranches.map((b) =>
                    b.id === ev.branchId ? { ...b, name: next } : b,
                  ),
                },
          ),
        }))
      }
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
    // S023: cross-client last-active sync. Cheap local update — no
    // reload needed because only the persisted shortcut changed.
    if (ev.type === 'branch.lastActiveChanged' && ev.repoId) {
      const payload = (ev.payload ?? {}) as { branch?: string }
      const next = payload.branch ?? ''
      set((state) => ({
        repos: state.repos.map((r) =>
          r.id !== ev.repoId ? r : { ...r, lastActiveBranch: next },
        ),
      }))
    }
    // S021: cleanup completed elsewhere — drop the removed branches
    // from the local snapshot. (CloseBranch on the server already
    // emits `branch.closed` per-branch but those arrive piecemeal;
    // the cleaned event is the consolidated signal.)
    if (ev.type === 'worktree.cleaned' && ev.repoId && ev.payload) {
      const payload = ev.payload as {
        removed?: { branchId: string }[]
      }
      const removedIds = new Set((payload.removed ?? []).map((r) => r.branchId))
      if (removedIds.size > 0) {
        set((state) => ({
          repos: state.repos.map((r) =>
            r.id !== ev.repoId
              ? r
              : {
                  ...r,
                  openBranches: r.openBranches.filter(
                    (b) => !removedIds.has(b.id),
                  ),
                },
          ),
        }))
      }
    }
    // S8478ca-5: runtime view changed — update the branch in-place so the
    // header chip and drawer badge refresh without a full repo reload.
    if (ev.type === 'branch.runtimeChanged' && ev.repoId && ev.branchId && ev.payload) {
      const rtView = ev.payload as RuntimeView
      set((state) => ({
        repos: state.repos.map((r) =>
          r.id !== ev.repoId
            ? r
            : {
                ...r,
                openBranches: r.openBranches.map((b) =>
                  b.id === ev.branchId ? { ...b, runtime: rtView } : b,
                ),
              },
        ),
      }))
    }
    // S7364e3: image-drift status changed for an incus workspace. Update the
    // branch's runtime.stale in-place so the "update available" badge on the
    // header chip + drawer entry appears/clears live.
    if (ev.type === 'branch.runtimeDrift' && ev.repoId && ev.branchId && ev.payload) {
      const stale = !!(ev.payload as { stale?: boolean }).stale
      set((state) => ({
        repos: state.repos.map((r) =>
          r.id !== ev.repoId
            ? r
            : {
                ...r,
                openBranches: r.openBranches.map((b) =>
                  b.id === ev.branchId && b.runtime
                    ? { ...b, runtime: { ...b.runtime, stale } }
                    : b,
                ),
              },
        ),
      }))
    }
    // S8478ca-refine: workspace was restarted in a new runtime. Force all
    // terminal tabs to reconnect — the old tmux session is gone.
    if (ev.type === 'branch.restarted' && ev.repoId && ev.branchId) {
      _triggerBranchTerminalReconnect(ev.repoId, ev.branchId, get().repos)
      // claude-tui tabs own their WS (not terminalManager); bump a per-branch
      // signal they subscribe to so they reconnect to the new runtime without a
      // manual page reload.
      const rk = `${ev.repoId}/${ev.branchId}`
      set((s) => ({ branchRestartSignal: { ...s.branchRestartSignal, [rk]: (s.branchRestartSignal[rk] ?? 0) + 1 } }))
    }

    // S6ab0ed: the self-update poller detected a change in the set of
    // update-available managed components. Payload is the full Snapshot —
    // replace the cache so the top-right "更新あり" badge + panel refresh live.
    if (ev.type === 'app.updateAvailable' && ev.payload) {
      set({ selfUpdate: ev.payload as SelfUpdateSnapshot })
    }

    // See8bd4-3: ports scan detected a change — update the Ports tab cache
    // so active Ports views refresh without issuing a fresh GET .../ports.
    if (ev.type === 'branch.portsChanged' && ev.repoId && ev.branchId && ev.payload) {
      const key = `${ev.repoId}/${ev.branchId}`
      const payload = ev.payload as WorkspacePorts
      set((s) => ({ branchPorts: { ...s.branchPorts, [key]: payload } }))
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
  requestSettings: (tab = 'app') => set({ settingsRequestTab: tab }),

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
    if (repoId === HOST_REPO_ID) await get().refreshHost()
    else await get().reloadRepos()
    return tab
  },
  removeTab: async (repoId, branchId, tabId) => {
    await api.delete(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/tabs/${encodeURIComponent(tabId)}`,
    )
    if (repoId === HOST_REPO_ID) await get().refreshHost()
    else await get().reloadRepos()
  },
  renameTab: async (repoId, branchId, tabId, name) => {
    await api.patch(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/tabs/${encodeURIComponent(tabId)}`,
      { name },
    )
    if (repoId === HOST_REPO_ID) await get().refreshHost()
    else await get().reloadRepos()
  },
  reorderTabs: async (repoId, branchId, order) => {
    // Optimistic: shuffle the local cache so the TabBar reflects the drop
    // immediately. On failure we revert + reload from the server.
    const prev = get().repos
    set((state) => ({
      repos: state.repos.map((r) =>
        r.id !== repoId
          ? r
          : {
              ...r,
              openBranches: r.openBranches.map((b) =>
                b.id !== branchId ? b : { ...b, tabSet: { ...b.tabSet, tabs: applyLocalOrder(b.tabSet.tabs, order) } },
              ),
            },
      ),
    }))
    try {
      await api.put(
        `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/tabs/order`,
        { order },
      )
    } catch (err) {
      set({ repos: prev })
      throw err
    }
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

  // S023: per-repo last-active-branch memory. Optimistic local update
  // + fire-and-forget PATCH. The server emits
  // `branch.lastActiveChanged` which the applyEvent path picks up for
  // cross-client sync; we only revert local state if the request 404s
  // (repo gone), otherwise transient errors are swallowed because the
  // value is purely a UX shortcut.
  setLastActiveBranch: async (repoId, branchName) => {
    set((state) => ({
      repos: state.repos.map((r) =>
        r.id !== repoId ? r : { ...r, lastActiveBranch: branchName },
      ),
    }))
    try {
      await api.patch(
        `/api/repos/${encodeURIComponent(repoId)}/last-active-branch`,
        { branch: branchName },
      )
    } catch (err) {
      // Don't revert — a transient failure here should not undo the
      // optimistic UI; the next navigation will retry. We just log so
      // bugs are visible during dev.
      console.warn('setLastActiveBranch failed', err)
    }
  },

  // S021: subagent worktree cleanup. Server endpoint accepts both
  // `dryRun` and `branchNames` body params. We split the call into a
  // listing helper (used to populate the dialog) and a confirm helper
  // (used to actually issue removals). Failure surface is per-row in
  // the response, not the HTTP status.
  listStaleSubagentWorktrees: async (repoId) => {
    const res = await api.post<{
      thresholdDays: number
      candidates: SubagentCleanupCandidate[]
    }>(
      `/api/repos/${encodeURIComponent(repoId)}/worktrees/cleanup-subagent`,
      { dryRun: true },
    )
    return res
  },

  cleanupSubagentWorktrees: async (repoId, branchNames) => {
    const body: Record<string, unknown> = { dryRun: false }
    if (branchNames && branchNames.length > 0) body.branchNames = branchNames
    const res = await api.post<SubagentCleanupResult>(
      `/api/repos/${encodeURIComponent(repoId)}/worktrees/cleanup-subagent`,
      body,
    )
    // Drop removed branches from the local snapshot immediately so the
    // Drawer reflects the change before /api/repos roundtrips. The WS
    // event will trigger a reloadRepos shortly anyway.
    if (res.removed && res.removed.length > 0) {
      const removedIds = new Set(res.removed.map((r) => r.branchId))
      set((state) => ({
        repos: state.repos.map((r) =>
          r.id !== repoId
            ? r
            : {
                ...r,
                openBranches: r.openBranches.filter(
                  (b) => !removedIds.has(b.id),
                ),
              },
        ),
      }))
    }
    return res
  },

  promoteSubagentBranch: async (repoId, branchId) => {
    const res = await api.post<{ branch: Branch; destination: string }>(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/promote-subagent`,
    )
    // Apply the returned branch snapshot directly so the Drawer flips
    // to `my` without waiting for `branch.categoryChanged`.
    set((state) => ({
      repos: state.repos.map((r) =>
        r.id !== repoId
          ? r
          : {
              ...r,
              openBranches: r.openBranches.map((b) =>
                b.id === branchId
                  ? {
                      ...b,
                      category: res.branch.category,
                      worktreePath: res.branch.worktreePath,
                    }
                  : b,
              ),
            },
      ),
    }))
    return res
  },

  // S8478ca-5: runtime capabilities.
  loadRuntimeCaps: async () => {
    const caps = await api.get<RuntimeCaps>('/api/runtimes')
    set({ runtimeCaps: caps })
    return caps
  },

  // S8478ca-5: refresh a single repo's data (incl. runtime views on branches).
  reloadRepo: async (repoId) => {
    const updated = await api.get<Repository>(`/api/repos/${encodeURIComponent(repoId)}`)
    set((state) => {
      const existing = state.repos.find((r) => r.id === repoId)
      if (existing) {
        // Merge: keep all existing fields, update runtime views on branches.
        return {
          repos: state.repos.map((r) =>
            r.id !== repoId
              ? r
              : {
                  ...r,
                  openBranches: r.openBranches.map((b) => {
                    const fresh = updated.openBranches.find((fb) => fb.id === b.id)
                    if (!fresh) return b
                    return { ...b, runtime: fresh.runtime }
                  }),
                },
          ),
        }
      }
      // Repo not yet in store: add it with safe defaults for missing fields.
      // Ensure every branch has a valid tabSet so downstream code never
      // crashes on `branch.tabSet.tabs`.
      const safeBranches = (updated.openBranches ?? []).map((b): Branch => {
        return {
          id: b.id ?? '',
          name: b.name ?? '',
          worktreePath: b.worktreePath ?? '',
          repoId: b.repoId ?? repoId,
          isPrimary: b.isPrimary ?? false,
          lastActivity: b.lastActivity ?? new Date().toISOString(),
          category: b.category,
          runtime: b.runtime,
          tabSet: b.tabSet ?? { tmuxSession: '', tabs: [] },
        }
      })
      const stub: Repository = {
        id: updated.id ?? repoId,
        ghqPath: updated.ghqPath ?? repoId,
        fullPath: updated.fullPath ?? '',
        starred: updated.starred ?? false,
        openBranches: safeBranches,
        lastActiveBranch: updated.lastActiveBranch,
      }
      return { repos: [...state.repos, stub] }
    })
  },

  patchWorkspaceRuntime: async (repoId, branchId, kind) => {
    const resp = await api.patch<{
      ok: boolean
      restarted: boolean
      restartError?: string
      runtime?: RuntimeView
    }>(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/runtime`,
      { kind },
    )
    // Update the local runtime view from the server response (contains the
    // freshly-resolved RuntimeView after restart, or the updated kind for
    // no-op cases). The WS branch.runtimeChanged event will arrive soon
    // as well; this optimistic update reduces visible flicker.
    const freshRuntime: RuntimeView = resp.runtime ?? { kind: kind as RuntimeView['kind'], state: 'stopped' }
    set((state) => ({
      repos: state.repos.map((r) =>
        r.id !== repoId
          ? r
          : {
              ...r,
              openBranches: r.openBranches.map((b) =>
                b.id === branchId ? { ...b, runtime: freshRuntime } : b,
              ),
            },
      ),
    }))
    // S8478ca-fix: the server returns HTTP 200 even when the in-place restart
    // FAILED and rolled the workspace back to its previous runtime (config
    // persisted, but the live switch could not be applied — e.g. the palmux
    // process lacks incus-admin group permission to talk to the incus daemon).
    // `resp.runtime` above already reflects the rolled-back (true) state, so the
    // badge is correct; but we must NOT let this look like success. Throw so the
    // caller (header runtime chip) surfaces it as a prominent error instead of
    // silently appearing to switch. Note: a plain `!restarted` is the legitimate
    // no-op case (branch not open / same kind) — only restartError means failure.
    if (resp.restartError) {
      throw new Error(
        `Runtime switch failed — the workspace was kept on its previous runtime. ${resp.restartError}`,
      )
    }
    // S8478ca-refine: when the server performed an in-place restart, the
    // tmux session was recreated in a new runtime. All active terminal tabs
    // for this workspace need to reconnect.
    if (resp.restarted) {
      _triggerBranchTerminalReconnect(repoId, branchId, get().repos)
    }
  },

  // S7364e3: recreate an incus container from the current palmux-ws image
  // (image update). Transactional server-side — a failed update keeps the
  // existing container and returns updateError (thrown here so the UI shows it).
  regenerateContainer: async (repoId, branchId) => {
    const resp = await api.post<{
      ok: boolean
      regenerated: boolean
      updateError?: string
      runtime?: RuntimeView
    }>(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/runtime/regenerate`,
    )
    if (resp.runtime) {
      const fresh = resp.runtime
      set((state) => ({
        repos: state.repos.map((r) =>
          r.id !== repoId
            ? r
            : {
                ...r,
                openBranches: r.openBranches.map((b) =>
                  b.id === branchId ? { ...b, runtime: fresh } : b,
                ),
              },
        ),
      }))
    }
    if (resp.updateError) {
      throw new Error(
        `Container update failed — the existing container was kept. ${resp.updateError}`,
      )
    }
    if (resp.regenerated) {
      // The tmux session was recreated in the fresh container; reconnect terminals.
      _triggerBranchTerminalReconnect(repoId, branchId, get().repos)
    }
  },

  // S6ab0ed: self-update actions ─────────────────────────────────────────────
  loadSelfUpdate: async () => {
    try {
      const snap = await selfUpdateApi.get()
      set({ selfUpdate: snap })
    } catch {
      // best-effort; the WS event will refresh it later.
    }
  },

  refreshSelfUpdate: async () => {
    // Force a server-side detection cycle NOW (bypasses the 6h poll) so the badge
    // reflects the latest release without restarting palmux2. Throws on failure so
    // the caller can surface it.
    const snap = await selfUpdateApi.refresh()
    set({ selfUpdate: snap })
  },

  runSelfUpdate: async () => {
    // Guard: self-update and the incus-admin recover share the updateInProgress
    // reconnect handshake. Refuse to start one while the other is in flight so
    // the handshake isn't mis-routed (the recover polls /api/incus-group, the
    // self-update polls /api/health for a version change).
    if (get().updateInProgress) {
      throw new Error('別の更新/復旧が実行中です。完了を待ってください。')
    }
    // Record the version we're updating FROM so the post-restart reconnect
    // handshake can detect when the new version is live (decisions PD-6/PD-7).
    const baseline = get().serverInfo.version ?? null
    set({ updateInProgress: true, updateBaselineVersion: baseline, updateFailed: false })
    try {
      const resp = await selfUpdateApi.run()
      if (!resp.ok) {
        set({ updateInProgress: false })
        throw new Error(resp.error ?? '更新を開始できませんでした。')
      }
      // Success path is observed asynchronously via the reconnect handshake in
      // use-event-stream.ts (WS drops when palmux restarts → poll /health).
    } catch (err) {
      // 409 (Nix-unmanaged) or any failure to START the update — surface it.
      set({ updateInProgress: false })
      throw err
    }
    // Sfeed64: poll palmux-update.service's live systemd state alongside the
    // WS-drop → /health reconnect handshake above. That handshake alone can
    // only INFER a failure from a fixed timeout — and a real update on this
    // host (nix evaluation, a caddy-cloudflare rebuild, a ~1GB palmux-ws image
    // download, a mid-script Caddy restart, then finally the palmux2 restart
    // itself) was observed legitimately taking several minutes, well past a
    // short guess, producing a false "更新に失敗しました…ロールバックされ"
    // report on an update that went on to complete successfully seconds later
    // (ndev.tjstkm.net, 2026-07-13 — three separate runs, all finished with
    // systemd result=success, yet the GUI had already reported a rollback on
    // the first). This poll instead asks systemd DIRECTLY whether the unit
    // ended without success, so a genuine failure surfaces immediately while a
    // still-running unit is never mistaken for one no matter how long it
    // legitimately takes. It never claims SUCCESS itself — only the version
    // handshake proves the new binary is actually serving — it only ever sets
    // updateFailed early on a real failure signal.
    const started = Date.now()
    const poll = async (): Promise<void> => {
      if (!get().updateInProgress) return // handshake already completed/cleared
      if (Date.now() - started > 15 * 60 * 1000) {
        // Backstop only: the WS-handshake timeout (use-event-stream.ts) is the
        // same length and covers this same deadline independently; this just
        // avoids polling forever if the unit query itself never resolves
        // (e.g. legacy install predating palmux-update.service).
        return
      }
      try {
        const st = await selfUpdateApi.updateStatus()
        if (st.active === 'failed' || (st.result && st.result !== 'success' && !st.running)) {
          set({ updateInProgress: false, updateFailed: true })
          return
        }
      } catch {
        // Unit not found (legacy install) or transient — the WS handshake
        // still covers both the success and eventual-timeout paths.
      }
      setTimeout(() => void poll(), 3000)
    }
    setTimeout(() => void poll(), 3000)
  },

  // S673a42-2: appliance host update (nixos-rebuild). Mirrors runSelfUpdate's
  // updateInProgress handshake so the SAME progress panel + WS-drop → /health
  // reconnect toast machinery (use-event-stream) applies unchanged. Adds a
  // rebuild-unit poll to detect a failure that never restarts palmux2 (a flake
  // update / eval error aborts before the switch → no WS drop → the version
  // handshake would only time out after ~60s; the poll surfaces it fast).
  runNixosRebuildUpdate: async () => {
    if (get().updateInProgress) {
      throw new Error('別の更新/復旧が実行中です。完了を待ってください。')
    }
    const baseline = get().serverInfo.version ?? null
    set({ updateInProgress: true, updateBaselineVersion: baseline, updateFailed: false })
    try {
      const resp = await selfUpdateApi.rebuildKick()
      if (!resp.ok) {
        set({ updateInProgress: false })
        throw new Error('更新を開始できませんでした。')
      }
    } catch (err) {
      set({ updateInProgress: false })
      throw err
    }
    // Failure-detection poll: if the update unit reports failed BEFORE palmux2
    // restarts, mark it failed now (old generation kept — atomic). Success is
    // observed by the reconnect handshake, which clears updateInProgress; the poll
    // stops as soon as it sees updateInProgress cleared, so the two never conflict.
    const started = Date.now()
    const poll = async (): Promise<void> => {
      if (!get().updateInProgress) return // handshake already completed/cleared
      if (Date.now() - started > 20 * 60 * 1000) {
        set({ updateInProgress: false, updateFailed: true })
        return
      }
      try {
        const st = await selfUpdateApi.rebuildStatus()
        if (st.active === 'failed' || (st.result && st.result !== 'success' && !st.running)) {
          set({ updateInProgress: false, updateFailed: true })
          return
        }
      } catch {
        // transient — palmux2 likely restarting from the switch; the reconnect
        // handshake will take over. Keep polling in case it comes back failed.
      }
      setTimeout(() => void poll(), 3000)
    }
    setTimeout(() => void poll(), 3000)
  },

  // S673a42-3: palmux-ws image fetch. Does NOT restart palmux2, so it uses its own
  // in-progress flag (not the updateInProgress reconnect handshake).
  // runImageInstall kicks the palmux-ws image fetch and RESOLVES when it finishes
  // (success or failure), so callers can `await` it — e.g. the host-update button
  // fetches the image first, then kicks the rebuild. On failure it sets
  // imageInstallError; the promise still resolves (never rejects) so a chained
  // host update is not blocked by an image hiccup.
  runImageInstall: async () => {
    if (get().imageInstallInProgress) return
    set({ imageInstallInProgress: true, imageInstallError: null })
    try {
      const resp = await selfUpdateApi.imageInstall()
      if (!resp.ok) {
        set({ imageInstallInProgress: false, imageInstallError: '取得を開始できませんでした。' })
        return
      }
    } catch (err) {
      set({
        imageInstallInProgress: false,
        imageInstallError: err instanceof Error ? err.message : String(err),
      })
      return
    }
    const started = Date.now()
    await new Promise<void>((resolve) => {
      const poll = async (): Promise<void> => {
        if (Date.now() - started > 30 * 60 * 1000) {
          set({ imageInstallInProgress: false, imageInstallError: 'image 取得がタイムアウトしました。' })
          resolve()
          return
        }
        try {
          const st = await selfUpdateApi.imageInstallStatus()
          if (!st.running) {
            if (st.error) {
              set({ imageInstallInProgress: false, imageInstallError: st.error })
            } else {
              set({ imageInstallInProgress: false, imageInstallError: null })
              // Badge clears + per-branch drift/regenerate refreshes.
              void get().loadSelfUpdate()
              void get().reloadRepos()
            }
            resolve()
            return
          }
        } catch {
          // transient — keep polling.
        }
        setTimeout(() => void poll(), 3000)
      }
      setTimeout(() => void poll(), 3000)
    })
  },

  clearUpdateToast: () => set({ updateToast: null }),

  // Sfef725: incus-admin group actions ────────────────────────────────────────
  loadIncusGroup: async () => {
    try {
      const st = await incusGroupApi.get()
      set({ incusGroup: st })
    } catch {
      // best-effort; surfaced on demand (runtime-switch failure / self-update).
    }
  },

  fixIncusGroup: async () => {
    // Guard against colliding with a self-update (both share updateInProgress +
    // the reconnect handshake).
    if (get().updateInProgress) {
      throw new Error('別の更新/復旧が実行中です。完了を待ってください。')
    }
    // Record the baseline version so the post-restart reconnect handshake can
    // detect the server came back (reusing the S6ab0ed updateInProgress path).
    const baseline = get().serverInfo.version ?? null
    set({ incusGroupFixInProgress: true, updateInProgress: true, updateBaselineVersion: baseline, updateFailed: false })
    try {
      const resp = await incusGroupApi.fix()
      if (!resp.ok) {
        set({ incusGroupFixInProgress: false, updateInProgress: false })
        // 409 (no privileged verb) or other failure — surface the manual command.
        throw new Error(resp.error ?? 'incus-admin の適用を開始できませんでした。')
      }
      // Success path observed asynchronously by the reconnect handshake in
      // use-event-stream.ts (WS drops when the user manager restarts palmux).
    } catch (err) {
      set({ incusGroupFixInProgress: false, updateInProgress: false })
      throw err
    }
  },
}))

// S8478ca-refine: force terminal re-attachment for all terminal-backed tabs
// (claude, bash) on a given workspace after an in-place runtime restart.
// The tmux session was killed and recreated in the new runtime, so any live
// WebSocket terminal connections are now stale. We evict the cached
// ManagedTerminal entries from terminalManager (closing the old WS and
// disposing the xterm.js terminal). The TerminalView component will detect
// the removal on its next render cycle and re-mount, reconnecting the WS
// against the freshly-started session.
function _triggerBranchTerminalReconnect(repoId: string, branchId: string, repos: Repository[]): void {
  // Import terminalManager lazily to avoid module init order issues.
  import('../lib/terminal-manager').then(({ terminalManager }) => {
    const branch = repos.find((r) => r.id === repoId)?.openBranches.find((b) => b.id === branchId)
    if (!branch) return
    for (const tab of branch.tabSet.tabs) {
      if (tab.type === 'claude' || tab.type === 'bash') {
        const key = `${repoId}/${branchId}/${tab.id}`
        terminalManager.remove(key)
      }
    }
  }).catch(() => {})
}

// S020: applyLocalOrder rebuilds the tabs slice with the user-requested
// order applied within the contiguous Multiple()=true group whose IDs are
// in `order`. Singletons and other groups keep their original positions.
function applyLocalOrder(tabs: Tab[], order: string[]): Tab[] {
  if (order.length === 0) return tabs
  const orderSet = new Set(order)
  const groupType = tabs.find((t) => orderSet.has(t.id))?.type
  if (!groupType) return tabs
  const byId = new Map(tabs.map((t) => [t.id, t]))
  const out: Tab[] = []
  const emitted = new Set<string>()
  let i = 0
  while (i < tabs.length) {
    const t = tabs[i]
    if (t.type !== groupType || !t.multiple) {
      out.push(t)
      i++
      continue
    }
    // Walk the contiguous same-type Multiple group.
    let j = i
    while (j < tabs.length && tabs[j].type === groupType && tabs[j].multiple) j++
    // Emit user-ordered IDs first (only those that exist in this group's
    // payload), then any remaining members in their original relative
    // order so a partial drop-payload (shouldn't happen, but be safe)
    // doesn't lose tabs.
    const present = new Set(tabs.slice(i, j).map((x) => x.id))
    for (const id of order) {
      if (!present.has(id)) continue
      const t2 = byId.get(id)
      if (!t2 || emitted.has(id)) continue
      out.push(t2)
      emitted.add(id)
    }
    for (let k = i; k < j; k++) {
      if (emitted.has(tabs[k].id)) continue
      out.push(tabs[k])
      emitted.add(tabs[k].id)
    }
    i = j
  }
  return out
}

// S0c6a1b: reserved host scope repo id (mirrors store.HostRepoID on the BE).
export const HOST_REPO_ID = 'host--0000'

// Convenience selectors. Both fall back to the synthetic host repo so that
// /host--0000/host/<tab> routes resolve even though the host scope is kept
// out of `repos` (and thus out of the Repositories list).
export const selectRepoById = (id: string) => (s: PalmuxStoreState) =>
  s.repos.find((r) => r.id === id) ??
  (s.hostRepo && s.hostRepo.id === id ? s.hostRepo : undefined)

export const selectBranchById = (repoId: string, branchId: string) => (s: PalmuxStoreState) =>
  s.repos.find((r) => r.id === repoId)?.openBranches.find((b) => b.id === branchId) ??
  (s.hostRepo && s.hostRepo.id === repoId
    ? s.hostRepo.openBranches.find((b) => b.id === branchId)
    : undefined)

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
