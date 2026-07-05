// Thin fetch wrapper. The session cookie is HttpOnly + same-origin so we just
// pass `credentials: 'include'` and let the browser handle it.

export class ApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

interface Options extends Omit<RequestInit, 'body'> {
  body?: unknown
}

async function request<T>(path: string, options: Options = {}): Promise<T> {
  const init: RequestInit = {
    credentials: 'include',
    ...options,
    headers: {
      Accept: 'application/json',
      ...(options.body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...(options.headers ?? {}),
    },
    body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
  }
  const res = await fetch(path, init)
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`
    try {
      const data = await res.json()
      if (data?.error) msg = data.error
    } catch {
      // body wasn't JSON; fall back to status text
    }
    throw new ApiError(res.status, msg)
  }
  if (res.status === 204) return undefined as T
  const ct = res.headers.get('Content-Type') ?? ''
  if (ct.includes('application/json')) return (await res.json()) as T
  return (await res.text()) as unknown as T
}

export const api = {
  get: <T>(path: string) => request<T>(path, { method: 'GET' }),
  post: <T>(path: string, body?: unknown) => request<T>(path, { method: 'POST', body }),
  put: <T>(path: string, body?: unknown) => request<T>(path, { method: 'PUT', body }),
  patch: <T>(path: string, body?: unknown) => request<T>(path, { method: 'PATCH', body }),
  delete: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
}

// === Domain types (mirror the Go structs at internal/domain) ================

export interface Repository {
  id: string
  ghqPath: string
  fullPath: string
  starred: boolean
  openBranches: Branch[]
  /** S023: per-repo memory of the most recent branch the user navigated
   *  to. Used by the Drawer to navigate-and-expand a collapsed repo on
   *  one click. Empty / undefined means "no remembered branch" (first
   *  open, or the previous branch was reconciled away). */
  lastActiveBranch?: string
}

export type BranchCategory = 'user' | 'unmanaged' | 'subagent'

/** S8478ca-5: point-in-time snapshot of a workspace's runtime state. */
export interface RuntimeView {
  kind: 'host' | 'incus-container'
  state: 'ready' | 'starting' | 'stopped' | 'error'
  address?: string
  error?: string
  /** S7364e3: incus container is older than the current palmux-ws image
   * (an update is available). Always false/absent for host. */
  stale?: boolean
}

/** S8478ca-5: one entry in GET /api/runtimes response. */
export interface RuntimeKindEntry {
  kind: 'host' | 'incus-container'
  available: boolean
  reason?: string
}

/** S8478ca-5: response shape for GET /api/runtimes. */
export interface RuntimeCaps {
  kinds: RuntimeKindEntry[]
}

export interface Branch {
  id: string
  name: string
  worktreePath: string
  repoId: string
  isPrimary: boolean
  tabSet: TabSet
  lastActivity: string
  /** S015: drawer category. `user` = recorded in
   *  repos.json#userOpenedBranches. `subagent` = path matches
   *  `autoWorktreePathPatterns`. `unmanaged` = otherwise. The FE
   *  remaps `user → my` for section titles. */
  category?: BranchCategory
  /** S8478ca-5: live runtime view. Absent for the Host login scope. */
  runtime?: RuntimeView
}

// TabSettings is the per-tab settings shape returned by
// GET /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/settings (Sadf90e).
// Replaces the S1f75ec-2 branch-level BranchSettings: claude_mode is now a
// property of the individual Claude tab so two tabs in the same workspace
// can hold independent modes.
export interface TabSettings {
  claude_mode: 'agent' | 'tui'
}

export interface TabSet {
  tmuxSession: string
  tabs: Tab[]
}

export interface Tab {
  id: string
  type: string
  name: string
  protected: boolean
  multiple: boolean
  windowName?: string
}

// HostScope (S0c6a1b) is the reserved, repository-independent terminal scope
// returned by GET /api/host. The Drawer renders it as a dedicated section and
// the empty-state CTA links to it. Reserved IDs are server-authoritative.
export interface HostScope {
  repoId: string
  branchId: string
  displayName: string
}

export interface AvailableRepoEntry {
  id: string
  ghqPath: string
  fullPath: string
  open: boolean
  starred: boolean
}

export interface BranchPickerEntry {
  name: string
  state: 'open' | 'local' | 'remote'
  branchId?: string
}

export interface OrphanWindow {
  index: number
  name: string
}

export interface OrphanSession {
  name: string
  attached: boolean
  createdAt?: number
  windows: OrphanWindow[]
}

// S021: subagent cleanup result shape mirrors Go's
// `store.SubagentCleanupResult`. Optional `removed`/`failed` arrays appear
// only on the non-dry-run response (or via the `worktree.cleaned` event).
export interface SubagentCleanupCandidate {
  branchId: string
  branchName: string
  worktreePath: string
  lastCommitIso?: string
  ageDays: number
  hasLock: boolean
  isPrimary: boolean
  reason: string
}

export interface SubagentCleanupRemoval {
  branchId: string
  branchName: string
  worktreePath: string
  error?: string
}

export interface SubagentCleanupResult {
  thresholdDays: number
  candidates: SubagentCleanupCandidate[]
  removed?: SubagentCleanupRemoval[]
  failed?: SubagentCleanupRemoval[]
}

// See8bd4-3: Ports tab types ─────────────────────────────────────────────────

/** One listening port in a container workspace, with its exposure state. */
export interface PortView {
  port: number
  proto: string
  bindAddr: string
  process: string
  /** "user" | "system" | "palmux" — the UI shows user by default and reveals
   *  system/palmux (OS infra / palmux's browser stack) behind a toggle. */
  category?: string
  /** True when bound to 127.0.0.1 only — reachable via in-container relay. */
  localhostOnly: boolean
  /** True when the port is exposed without edge basic_auth. */
  public: boolean
  /** True when a public Caddy route exists for this port. */
  exposed: boolean
  /** https://<port>--<ws>--<repo>.<base> when exposed, else "". */
  publicUrl: string
  /** S4c591a host-port mode: true when an incus proxy device exposes this port
   *  on the host (UNAUTHENTICATED). */
  hostPublished: boolean
  /** Host-side port the proxy device listens on (may differ from `port` when
   *  auto-reassigned to avoid collision). 0 when not host-published. */
  hostPort: number
  /** http://<hostIP>:<hostPort> when host-published, else "". */
  hostUrl: string
}

/** Response shape for GET .../ports */
export interface WorkspacePorts {
  runtimeKind: 'host' | 'incus-container'
  ports: PortView[]
  /** S4c591a: false when no wildcard-DNS public domain is configured — the FE
   *  then shows host-port mode instead of subdomain publishing. */
  publicDomainConfigured: boolean
  /** Host's primary IP, for rendering http://<hostIP>:<port> URLs in host-port
   *  mode. Empty in subdomain mode. */
  hostIP?: string
}

/** Response from POST .../ports/{port}/expose */
export interface ExposePortResponse {
  port: number
  public: boolean
  publicUrl: string
  /** S4c591a host-port mode fields. */
  hostPublished?: boolean
  hostPort?: number
  hostUrl?: string
}

export const portsApi = {
  list: (repoId: string, branchId: string) =>
    api.get<WorkspacePorts>(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/ports`,
    ),
  expose: (repoId: string, branchId: string, port: number, pub: boolean) =>
    api.post<ExposePortResponse>(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/ports/${port}/expose`,
      { public: pub },
    ),
  unexpose: (repoId: string, branchId: string, port: number) =>
    api.delete<void>(
      `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/ports/${port}/expose`,
    ),
}

// Sa53137: Deploy config types ─────────────────────────────────────────────────

/** Response shape for GET /api/deploy */
export interface DeployView {
  server: {
    addr: string
    base_path: string
    max_connections: number
    tmux_prefix: string
    caddy_admin: string
    claude_bin: string
    claude_args: string
  }
  public: {
    domain: string
    basic_auth_user: string
  }
  /** Sd44947: [workspace] shared_dirs as absolute host paths (~ expanded) + the
   * host $HOME so the GUI can validate new entries inline. */
  workspace: {
    sharedDirs: string[]
    home: string
  }
  secrets: {
    hasSsoSecret: boolean
    hasBasicAuthHash: boolean
    hasToken: boolean
    hasCloudflareToken: boolean
  }
  configured: boolean
  /** Running on a NixOS appliance → the privileged "apply domain/TLS" step is a
   * GUI-kicked `nixos-rebuild switch` (POST /api/deploy/rebuild), not
   * `sudo palmux reconcile-system`. */
  nixOSHost?: boolean
}

/** Response shape for GET /api/deploy/rebuild (NixOS appliance only). */
export interface RebuildStatus {
  active: string // systemd ActiveState: inactive|activating|active|failed
  result: string // systemd Result: success|exit-code|...
  running: boolean
}

/** Response shape for POST /api/deploy/apply */
export interface ApplyResult {
  changes: Array<{ field: string; class: 'hot' | 'restart' | 'root' | 'workspace' }>
  hotApplied: boolean
  needRestart: boolean
  needPrivilege: boolean
  /** Sd44947: a workspace-class change (shared folders) was live-propagated. */
  workspaceApplied?: boolean
  message: string
}

// S6ab0ed: self-update detection snapshot + one-click "Update all".
export interface SelfUpdateComponent {
  name: string
  display: string
  source: string
  kind: string
  installed: string
  latest: string
  available: boolean
  // Sa8e7d0-2-2: false when this component's latest version could not be
  // resolved (no releases / GitHub unreachable). Rendered as "取得不可" instead
  // of "最新"; never counts toward the "update available" badge.
  fetchable: boolean
}

export interface SelfUpdateSnapshot {
  components: SelfUpdateComponent[]
  available: boolean
  nixManaged: boolean
  /** Running on a NixOS host (palmuxOS appliance) → updates are operator-driven
   * `nixos-rebuild switch`, not the in-app one-click. */
  nixOSHost?: boolean
  checkedAt: string
  degraded: boolean
  /** This "update available" was synthesized by the env-gated force-update test
   * affordance (same real version, not a real release). Always false in
   * production. The panel marks it so a test run is never mistaken for a real
   * update. */
  forced?: boolean
}

export interface SelfUpdateRunResult {
  ok: boolean
  nixManaged?: boolean
  message?: string
  error?: string
}

export const selfUpdateApi = {
  get: (): Promise<SelfUpdateSnapshot> => api.get<SelfUpdateSnapshot>('/api/selfupdate'),
  run: (): Promise<SelfUpdateRunResult> => api.post<SelfUpdateRunResult>('/api/selfupdate/run'),
  health: (): Promise<{ version?: string }> => api.get<{ version?: string }>('/api/health'),
}

// Sfef725: incus-admin stale-group detection + GUI click-recover.
export type IncusGroupState = 'ok' | 'stale' | 'not-member' | 'n/a'

export interface IncusGroupStatus {
  state: IncusGroupState
  gid: number
  user?: string
  remedy: string
  detail: string
  // fixAvailable: the privileged `sudo palmux fix-incus-group` verb is wired
  // (sudoers drop-in present) → show the recover button; else show the manual
  // command. Only set for the stale state.
  fixAvailable?: boolean
  // restartCommand: the exact manual `sudo systemctl restart user@<uid>` command.
  restartCommand?: string
}

export interface IncusGroupFixResult {
  ok: boolean
  message?: string
  error?: string
  fixAvailable?: boolean
  restartCommand?: string
}

export const incusGroupApi = {
  get: (): Promise<IncusGroupStatus> => api.get<IncusGroupStatus>('/api/incus-group'),
  fix: (): Promise<IncusGroupFixResult> => api.post<IncusGroupFixResult>('/api/incus-group/fix'),
}

export const deployApi = {
  get: (): Promise<DeployView> => api.get<DeployView>('/api/deploy'),
  apply: (body: {
    server?: Partial<DeployView['server']>
    public?: Partial<DeployView['public']>
    workspace?: { sharedDirs: string[] }
    dryRun?: boolean
  }): Promise<ApplyResult> => api.post<ApplyResult>('/api/deploy/apply', body),
  rotateSecrets: (body: {
    ssoSecret?: string
    password?: string
    token?: string
    cloudflareToken?: string
  }): Promise<{ ok: boolean; needRestart: boolean; message: string }> =>
    api.post('/api/deploy/secrets', body),
  // Sb14caa: kick `nixos-rebuild switch` on the appliance to apply privileged
  // (domain/TLS) changes. NixOS-only; 409 elsewhere.
  rebuild: (): Promise<{ ok: boolean; status: string; message: string }> =>
    api.post('/api/deploy/rebuild', {}),
  rebuildStatus: (): Promise<RebuildStatus> => api.get<RebuildStatus>('/api/deploy/rebuild'),
}
