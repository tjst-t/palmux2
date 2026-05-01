// useSprintData — generic ETag-aware data hook for one Sprint Dashboard
// view. Implements the four refresh layers from the S016 spec:
//
//   1. tab open / view mount → initial REST GET
//   2. WS `sprint.changed` event → mark stale + refetch (debounced 300ms)
//   3. window.focus → ETag check (304 short-circuits when nothing moved)
//   4. manual Refresh button → forced refetch ignoring cached ETag
//
// Connection status comes from the global Zustand store (set by
// useEventStream) so we expose `offline` true when the WS is not open.
//
// Generic over the response payload `T`; concrete views call with their
// specific payload type.

import { useCallback, useEffect, useRef, useState } from 'react'

import { usePalmuxStore } from '../../stores/palmux-store'

import { PALMUX_EVENT } from '../../hooks/use-git-status-events'
import type { RemoteEvent } from '../../stores/palmux-store'

import type { SprintChangedScope } from './types'

interface FetchResult<T> {
  status: number
  etag: string | null
  body: T | null
}

interface UseSprintDataOptions<T> {
  repoId: string
  branchId: string
  // Which scope this view watches; sprint.changed events outside this
  // scope are ignored to avoid spurious refetches.
  scope: SprintChangedScope
  // The fetch function: receives the previous ETag and returns a cached
  // result (304 / 200 / new body).
  fetcher: (prevETag: string | null) => Promise<FetchResult<T>>
  // Re-run on this dep — for SprintDetail we pass the sprintId so that
  // navigating between sprints triggers a fresh fetch.
  key?: string
}

interface UseSprintDataResult<T> {
  data: T | null
  loading: boolean
  error: string | null
  offline: boolean
  refresh: () => void
}

export function useSprintData<T>({
  repoId,
  branchId,
  scope,
  fetcher,
  key,
}: UseSprintDataOptions<T>): UseSprintDataResult<T> {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const etagRef = useRef<string | null>(null)
  const inflightRef = useRef<AbortController | null>(null)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const connectionStatus = usePalmuxStore((s) => s.connectionStatus)
  const offline = connectionStatus !== 'connected'

  const doFetch = useCallback(
    async (force: boolean) => {
      if (inflightRef.current) inflightRef.current.abort()
      const ac = new AbortController()
      inflightRef.current = ac
      setLoading(true)
      setError(null)
      try {
        const prev = force ? null : etagRef.current
        const res = await fetcher(prev)
        if (ac.signal.aborted) return
        if (res.status !== 304 && res.body !== null) {
          setData(res.body)
        }
        etagRef.current = res.etag
      } catch (e) {
        if (!ac.signal.aborted) {
          setError(e instanceof Error ? e.message : String(e))
        }
      } finally {
        if (!ac.signal.aborted) {
          setLoading(false)
        }
      }
    },
    [fetcher],
  )

  // Layer 1: mount-time fetch + on key change. doFetch internally calls
  // setLoading/setData/setError — those calls are by design (this is the
  // "subscribe to an external system" branch of the React docs guidance,
  // where the fetch IS the external system).
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void doFetch(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [repoId, branchId, key])

  // Layer 2: WS sprint.changed → debounced refetch.
  useEffect(() => {
    const handler = (e: Event) => {
      const ev = (e as CustomEvent<RemoteEvent>).detail
      if (!ev) return
      if (ev.type !== 'sprint.changed') return
      if (ev.repoId !== repoId || ev.branchId !== branchId) return
      const payload = ev.payload as { scopes?: SprintChangedScope[] } | undefined
      // No scopes means "everything moved" — refetch everything to be
      // safe. Otherwise only refetch when our scope is named.
      if (payload?.scopes && payload.scopes.length > 0 && !payload.scopes.includes(scope)) {
        return
      }
      if (debounceRef.current) clearTimeout(debounceRef.current)
      debounceRef.current = setTimeout(() => {
        void doFetch(false)
      }, 300)
    }
    window.addEventListener(PALMUX_EVENT, handler)
    return () => {
      window.removeEventListener(PALMUX_EVENT, handler)
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [doFetch, repoId, branchId, scope])

  // Layer 3: window.focus → ETag check.
  useEffect(() => {
    const handler = () => {
      void doFetch(false)
    }
    window.addEventListener('focus', handler)
    return () => window.removeEventListener('focus', handler)
  }, [doFetch])

  // Layer 3b: when WS reconnects (offline → online), force a refetch so
  // we never miss a `sprint.changed` event lost during the disconnect.
  useEffect(() => {
    if (!offline) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      void doFetch(false)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [offline])

  // Layer 4: manual refresh — bypasses ETag.
  const refresh = useCallback(() => {
    void doFetch(true)
  }, [doFetch])

  return { data, loading, error, offline, refresh }
}
