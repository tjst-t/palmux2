// S017: virtualised conversation list.
//
// react-window v2 (`List`) renders only the rows currently in the
// viewport, which keeps the DOM size O(visible turns) regardless of
// how long the session is. Row heights are dynamic — each turn's
// height depends on how much prose / how many code blocks / whether
// individual blocks are collapsed — so we use the v2
// `useDynamicRowHeight` hook. Under the hood it installs a
// `ResizeObserver` on each rendered row element, caches the measured
// height, and tells `List` to relayout when a row's height changes.
// That means collapse / expand toggles "just work" — we don't need
// the v1-era manual `resetAfterIndex` dance.
//
// Scroll position is restored on session reload from `localStorage`
// keyed by sessionId, so the user lands back where they were even
// after an F5 / reconnect.

import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
} from 'react'
import {
  List,
  type ListImperativeAPI,
  type RowComponentProps,
  useDynamicRowHeight,
} from 'react-window'

import type { Turn } from './types'

interface ConversationListHandle {
  /** Scroll the conversation so the row with `index` is fully
   *  visible at the bottom. Called when the user is in auto-follow
   *  mode and a new turn arrives. */
  scrollToBottom: (behavior?: 'auto' | 'instant' | 'smooth') => void
  /** Returns the wrapping HTMLDivElement (the scroll container).
   *  ConversationView needs this to install scroll listeners and to
   *  read/restore scroll position from localStorage. */
  element(): HTMLDivElement | null
}

/** Props passed to each virtual row. List re-renders rows when any
 *  of these values change reference, so we use a stable identity
 *  (memoised in the parent) to avoid sweeping re-renders. */
interface RowProps {
  turns: Turn[]
  /** Renders the original TurnView for the turn at `index`. We hand
   *  this in instead of importing TurnView directly to avoid a circular
   *  module dependency between claude-agent-view (owner of permission /
   *  plan / ask handlers) and this file. */
  renderTurn: (turn: Turn, index: number) => React.ReactNode
}

/** Row component: must be defined at module scope (or memoised) so
 *  React-Window's identity check doesn't trigger a row remount on
 *  every parent render. Receives `index`, `style`, `ariaAttributes`
 *  injected by List, plus our `rowProps`. */
function Row({
  index,
  style,
  ariaAttributes,
  turns,
  renderTurn,
}: RowComponentProps<RowProps>) {
  const turn = turns[index]
  if (!turn) return null
  return (
    <div style={style} {...ariaAttributes}>
      {/* Inner wrapper is what the ResizeObserver measures. We add a
          tiny bottom gap so consecutive turns don't visually fuse. */}
      <div style={{ paddingBottom: 16 }}>{renderTurn(turn, index)}</div>
    </div>
  )
}

interface ConversationListProps {
  turns: Turn[]
  renderTurn: (turn: Turn, index: number) => React.ReactNode
  /** Stable session identity. Used as the React key for the inner
   *  List so a session swap (resume, fork, /clear) drops the cached
   *  row heights — measurements from a different conversation are
   *  meaningless. Pass `''` until the first system/init lands. */
  sessionKey: string
  /** Notified when the user scrolls. The parent uses this to maintain
   *  the auto-follow flag (true = at-bottom, paint new turns;
   *  false = leave alone). */
  onScroll?: (scrollTop: number, scrollHeight: number, clientHeight: number) => void
}

export const ConversationList = forwardRef<ConversationListHandle, ConversationListProps>(
  function ConversationList({ turns, renderTurn, sessionKey, onScroll }, ref) {
    const listRef = useRef<ListImperativeAPI>(null)
    // 200px is a reasonable initial guess for an unmeasured turn
    // (one short user message + a tool block). Real heights replace
    // it as soon as ResizeObserver fires.
    const dynamicHeight = useDynamicRowHeight({
      defaultRowHeight: 200,
      // The session key forces a fresh measurement cache when the
      // CLI rotates sessions — heights from session A are wrong for
      // session B even though the row index range overlaps.
      key: sessionKey,
    })

    const rowProps: RowProps = useMemo(
      () => ({ turns, renderTurn }),
      [turns, renderTurn],
    )

    useImperativeHandle(
      ref,
      () => ({
        scrollToBottom: (behavior = 'instant') => {
          const api = listRef.current
          if (!api) return
          const lastIndex = turns.length - 1
          if (lastIndex < 0) return
          api.scrollToRow({ index: lastIndex, align: 'end', behavior })
        },
        element() {
          return listRef.current?.element ?? null
        },
      }),
      [turns.length],
    )

    // Wire scroll events from the inner scroll container up to the
    // parent so it can update auto-follow without us having to
    // duplicate that state here.
    useEffect(() => {
      const el = listRef.current?.element ?? null
      if (!el || !onScroll) return
      const handler = () => {
        onScroll(el.scrollTop, el.scrollHeight, el.clientHeight)
      }
      el.addEventListener('scroll', handler)
      return () => el.removeEventListener('scroll', handler)
    }, [onScroll])

    return (
      <List
        listRef={listRef}
        rowComponent={Row}
        rowCount={turns.length}
        rowHeight={dynamicHeight}
        rowProps={rowProps}
        // Render a few extra rows above/below the viewport so the user
        // can flick-scroll without seeing blank tiles paint in.
        overscanCount={4}
        style={{ height: '100%', width: '100%' }}
      />
    )
  },
)

export type { ConversationListHandle }

/** localStorage key for the per-session scroll offset. The scroll
 *  bar restoration logic lives in ConversationView (claude-agent-view.tsx)
 *  so we just expose the key shape here. */
export function scrollStorageKey(repoId: string, branchId: string, tabId: string): string {
  return `palmux:claudeScroll:${repoId}/${branchId}/${tabId}`
}

interface PersistedScroll {
  sessionId: string
  top: number
}

/** Read the persisted scroll offset for a tab, returning null when
 *  none is recorded or the recorded sessionId no longer matches the
 *  active one (the conversation underneath has changed, so the offset
 *  is meaningless). */
export function readPersistedScroll(
  key: string,
  expectedSessionId: string,
): number | null {
  if (typeof localStorage === 'undefined') return null
  try {
    const raw = localStorage.getItem(key)
    if (!raw) return null
    const parsed = JSON.parse(raw) as PersistedScroll
    if (parsed.sessionId !== expectedSessionId) return null
    if (typeof parsed.top !== 'number' || parsed.top < 0) return null
    return parsed.top
  } catch {
    return null
  }
}

/** Persist the current scroll offset under `key`. Pinned to a
 *  sessionId so a later session swap doesn't accidentally restore
 *  the prior conversation's offset. */
export function writePersistedScroll(
  key: string,
  sessionId: string,
  top: number,
): void {
  if (typeof localStorage === 'undefined') return
  if (!sessionId) return
  try {
    const payload: PersistedScroll = { sessionId, top }
    localStorage.setItem(key, JSON.stringify(payload))
  } catch {
    // Ignore quota errors — losing scroll restoration on one reload
    // is benign.
  }
}

/** useScrollRestore re-anchors the conversation scrollTop after a
 *  reload / session swap. Called once per (sessionId, ref) pair: on
 *  the first render where `turns.length > 0` after the sessionId
 *  has settled, we look up the stored offset, wait one rAF tick so
 *  the dynamic height cache has measured at least the visible rows,
 *  then assign scrollTop. Returns nothing — fire-and-forget. */
export function useScrollRestore(opts: {
  sessionId: string
  storageKey: string
  containerRef: React.RefObject<HTMLDivElement | null>
  hasTurns: boolean
}) {
  const { sessionId, storageKey, containerRef, hasTurns } = opts
  const restoredFor = useRef<string>('')

  useLayoutEffect(() => {
    if (!sessionId || !hasTurns) return
    if (restoredFor.current === sessionId) return
    const target = readPersistedScroll(storageKey, sessionId)
    if (target == null) {
      restoredFor.current = sessionId
      return
    }
    // The persisted offset matters but the underlying scroll
    // container may not be wired up yet (the List installs its DOM
    // asynchronously). We retry on a short interval (capped at ~1.2s)
    // until either the element appears AND its scrollHeight has
    // grown enough to honour the saved offset, or we give up.
    let cancelled = false
    let attempts = 0
    const tryRestore = () => {
      if (cancelled) return
      attempts++
      const el = containerRef.current
      if (el && el.scrollHeight > el.clientHeight) {
        const max = Math.max(0, el.scrollHeight - el.clientHeight)
        el.scrollTop = Math.min(target, max)
        // Verify it stuck — measurement cache might still be
        // converging, so leave restoredFor only set once we landed
        // close. If we didn't, schedule another retry.
        if (Math.abs(el.scrollTop - Math.min(target, max)) < 4 || attempts >= 12) {
          restoredFor.current = sessionId
          return
        }
      }
      if (attempts >= 12) {
        // Give up — the cache hasn't populated. Leaving restoredFor
        // unset would cause us to thrash; mark done.
        restoredFor.current = sessionId
        return
      }
      window.setTimeout(tryRestore, 100)
    }
    // Two rAF ticks before the first attempt to give List time to
    // mount its DOM.
    let raf2 = 0
    const raf1 = requestAnimationFrame(() => {
      raf2 = requestAnimationFrame(tryRestore)
    })
    return () => {
      cancelled = true
      cancelAnimationFrame(raf1)
      if (raf2) cancelAnimationFrame(raf2)
    }
  }, [sessionId, hasTurns, storageKey, containerRef])
}

/** usePersistScroll throttles writes of the live scrollTop to
 *  localStorage. We don't write on every scroll event (would burn
 *  the main thread on autopilot floods); a 250ms trailing-edge
 *  debounce is plenty for restore-on-reload accuracy. */
export function usePersistScroll(opts: {
  sessionId: string
  storageKey: string
  containerRef: React.RefObject<HTMLDivElement | null>
}) {
  const { sessionId, storageKey, containerRef } = opts
  // Stable callback so we don't re-install listeners on every render.
  const persist = useCallback(() => {
    const el = containerRef.current
    if (!el || !sessionId) return
    writePersistedScroll(storageKey, sessionId, el.scrollTop)
  }, [containerRef, sessionId, storageKey])

  useEffect(() => {
    if (!sessionId) return
    let timer: number | undefined
    let installedEl: HTMLDivElement | null = null
    let attached = false
    const onScroll = () => {
      if (timer) window.clearTimeout(timer)
      timer = window.setTimeout(persist, 250)
    }
    // Containers can become available AFTER this effect first runs,
    // so we poll briefly for ~2s until we find one. Once attached we
    // stop polling.
    const poll = window.setInterval(() => {
      const el = containerRef.current
      if (!el || attached) return
      el.addEventListener('scroll', onScroll)
      installedEl = el
      attached = true
      window.clearInterval(poll)
    }, 80)
    return () => {
      window.clearInterval(poll)
      if (installedEl) installedEl.removeEventListener('scroll', onScroll)
      if (timer) window.clearTimeout(timer)
    }
  }, [persist, containerRef, sessionId])
}
