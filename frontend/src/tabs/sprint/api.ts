// API helpers for the Sprint Dashboard. Wraps the global `fetch` with
// ETag-aware GET so window.focus revalidation can short-circuit on 304.
//
// We bypass the central `lib/api.ts` wrapper for these GETs because we
// need access to response *headers* (ETag) — the wrapper currently
// returns just the body. The bodies are read with the same
// credentials/error semantics, so behaviour stays consistent.

import type {
  DecisionsResponse,
  DependencyGraphResponse,
  OverviewResponse,
  RefineResponse,
  SprintDetailResponse,
} from './types'

interface CachedFetchResult<T> {
  status: number
  etag: string | null
  body: T | null
}

async function cachedJSON<T>(url: string, prevETag: string | null): Promise<CachedFetchResult<T>> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (prevETag) headers['If-None-Match'] = prevETag
  const res = await fetch(url, { credentials: 'include', headers })
  if (res.status === 304) {
    return { status: 304, etag: prevETag, body: null }
  }
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`
    try {
      const data = await res.json()
      if (data?.error) msg = data.error
    } catch {
      // ignore
    }
    throw new Error(msg)
  }
  const body = (await res.json()) as T
  return { status: res.status, etag: res.headers.get('ETag'), body }
}

const base = (repoId: string, branchId: string) =>
  `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/sprint`

export const sprintApi = {
  overview: (repoId: string, branchId: string, prevETag: string | null) =>
    cachedJSON<OverviewResponse>(`${base(repoId, branchId)}/overview`, prevETag),
  sprintDetail: (
    repoId: string,
    branchId: string,
    sprintId: string,
    prevETag: string | null,
  ) =>
    cachedJSON<SprintDetailResponse>(
      `${base(repoId, branchId)}/sprints/${encodeURIComponent(sprintId)}`,
      prevETag,
    ),
  dependencies: (repoId: string, branchId: string, prevETag: string | null) =>
    cachedJSON<DependencyGraphResponse>(`${base(repoId, branchId)}/dependencies`, prevETag),
  decisions: (
    repoId: string,
    branchId: string,
    filter: string | null,
    prevETag: string | null,
  ) =>
    cachedJSON<DecisionsResponse>(
      `${base(repoId, branchId)}/decisions${filter ? `?filter=${encodeURIComponent(filter)}` : ''}`,
      prevETag,
    ),
  refine: (repoId: string, branchId: string, prevETag: string | null) =>
    cachedJSON<RefineResponse>(`${base(repoId, branchId)}/refine`, prevETag),
}
