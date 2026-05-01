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
}

export interface Branch {
  id: string
  name: string
  worktreePath: string
  repoId: string
  isPrimary: boolean
  tabSet: TabSet
  lastActivity: string
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

export interface PortmanLease {
  name: string
  project: string
  worktree: string
  port: number
  hostname: string
  expose: boolean
  status: string
  url: string
}
