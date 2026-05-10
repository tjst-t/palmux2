/** Scroll restoration + persistence hooks for the Claude tab
 *  conversation list.
 *
 *  These were originally co-located with the virtualised list in
 *  conversation-list.tsx (S017). They're independent of react-window
 *  internals — only `containerRef` and the turn-id data attributes
 *  are needed — so S43cfb1-3 extracted them into their own module to
 *  separate "list virtualisation" concerns from "scroll position
 *  storage" concerns.
 *
 *  Public API:
 *    - scrollStorageKey(repoId, branchId, tabId) → localStorage key
 *    - readPersistedScroll / writePersistedScroll: low-level I/O
 *    - useScrollRestore: re-anchors the view on (re)mount
 *    - usePersistScroll: writes the live position back as the user scrolls
 *    - findTopAnchor: helper exposed for tests
 *    - PersistedScroll, PersistedScrollRecord types
 */
import { useEffect, useLayoutEffect, useRef } from 'react'

/** localStorage key for the per-session scroll offset. */
export function scrollStorageKey(repoId: string, branchId: string, tabId: string): string {
  return `palmux:claudeScroll:${repoId}/${branchId}/${tabId}`
}

/** Persistence record for the conversation scroll position. We do
 *  NOT store an absolute scrollTop — virtualised row heights start
 *  as estimates and converge as ResizeObserver fires, so the same
 *  "place in the conversation" maps to different scrollTop values
 *  across mounts. Instead we anchor to a specific turn (its
 *  human-readable position never changes) plus the pixel offset of
 *  that turn's top edge above the viewport top.
 *
 *  - `atBottom`: the user was at/near the bottom — auto-follow
 *    semantics; on restore we don't touch the position and let the
 *    parent's auto-follow effect park us at the latest content.
 *  - `anchor.turnId`: id of the topmost turn that intersected the
 *    viewport top at save time.
 *  - `anchor.offset`: pixels by which that turn's top edge sat above
 *    the viewport top. 0 = turn's top exactly at viewport top;
 *    positive = the user had scrolled into the turn by `offset` px.
 */
export interface PersistedScroll {
  sessionId: string
  atBottom: boolean
  anchor?: { turnId: string; offset: number }
}

export type PersistedScrollRecord = Omit<PersistedScroll, 'sessionId'>

/** Find the row the user is looking at as the "top of the
 *  viewport" — the row whose top edge is at or just above the
 *  viewport top (rect.top ≤ sRect.top < rect.bottom).
 *
 *  Returns null when no rendered row spans the viewport top. This
 *  happens during transient render states (the user wheels rapidly
 *  and react-window's rendered rows lag below the new scrollTop, or
 *  ResizeObserver is mid-update). Callers should keep their
 *  previous good anchor rather than overwrite with a transient
 *  fallback that would produce a negative offset. */
export function findTopAnchor(
  scroller: HTMLElement,
): { turnId: string; offset: number } | null {
  const sRect = scroller.getBoundingClientRect()
  const rows = scroller.querySelectorAll<HTMLElement>('[data-turn-id]')
  // Among spanning candidates (rect.top ≤ sRect.top < rect.bottom)
  // there's at most one in steady state. We pick the one with the
  // largest top (= closest to sRect.top from above) defensively.
  let spanning: HTMLElement | null = null
  let spanningTop = -Infinity
  for (const row of Array.from(rows)) {
    const r = row.getBoundingClientRect()
    if (r.bottom <= sRect.top) continue
    if (r.top > sRect.top) continue
    if (r.top > spanningTop) {
      spanningTop = r.top
      spanning = row
    }
  }
  if (!spanning) return null
  const id = spanning.dataset.turnId
  if (!id) return null
  const r = spanning.getBoundingClientRect()
  return { turnId: id, offset: sRect.top - r.top }
}

/** Read the persisted record for a tab, returning null when none is
 *  recorded or the recorded sessionId no longer matches (a session
 *  swap means the saved anchor's turnId is meaningless). */
export function readPersistedScroll(
  key: string,
  expectedSessionId: string,
): PersistedScrollRecord | null {
  if (typeof localStorage === 'undefined') return null
  try {
    const raw = localStorage.getItem(key)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<PersistedScroll>
    if (parsed.sessionId !== expectedSessionId) return null
    if (typeof parsed.atBottom !== 'boolean') return null
    let anchor: { turnId: string; offset: number } | undefined
    if (parsed.anchor) {
      if (typeof parsed.anchor.turnId !== 'string') return null
      if (typeof parsed.anchor.offset !== 'number') return null
      anchor = { turnId: parsed.anchor.turnId, offset: parsed.anchor.offset }
    }
    return { atBottom: parsed.atBottom, anchor }
  } catch {
    return null
  }
}

/** Persist the current anchor under `key`. Pinned to a sessionId so
 *  a session swap doesn't accidentally restore the prior
 *  conversation's anchor. */
export function writePersistedScroll(
  key: string,
  sessionId: string,
  rec: PersistedScrollRecord,
): void {
  if (typeof localStorage === 'undefined') return
  if (!sessionId) return
  try {
    const payload: PersistedScroll = {
      sessionId,
      atBottom: rec.atBottom,
      ...(rec.anchor ? { anchor: rec.anchor } : {}),
    }
    localStorage.setItem(key, JSON.stringify(payload))
  } catch {
    // Ignore quota errors — losing scroll restoration on one reload
    // is benign.
  }
}

/** useScrollRestore re-anchors the conversation after a tab switch
 *  or page reload using a turn-id anchor.
 *
 *  Vastly simpler now that ConversationList renders all turns into
 *  the DOM (no virtualisation): the anchor row's element is always
 *  present, so we can find it directly and compute the scrollTop
 *  delta in one shot. No polling, no estimation lag, no retries.
 *
 *    - atBottom=true → no-op. The parent's auto-follow effect parks
 *      us at the latest content.
 *    - atBottom=false + anchor present → find the row by
 *      `[data-turn-id]`, scrollTop = (row.top - container.top) +
 *      offset, done.
 *    - anchor turnId not in current list (rewound, truncated): give
 *      up. The user gets the default (top of list / auto-follow).
 *
 *  User-input abort listeners are kept as defense in depth even
 *  though the restore window is now ~1 frame.
 */
export function useScrollRestore(opts: {
  sessionId: string
  storageKey: string
  containerRef: React.RefObject<HTMLDivElement | null>
  hasTurns: boolean
  /** Called once per (sessionId) when the first useful scroll
   *  position has been applied — or determined to be unnecessary
   *  (no record, atBottom path, anchor missing). The parent uses
   *  this to flip a `visibility: hidden → visible` gate so the user
   *  never sees the conversation paint at scrollTop=0 before
   *  jumping to the saved position. */
  onSettled?: () => void
}) {
  const { sessionId, storageKey, containerRef, hasTurns } = opts
  const restoredFor = useRef<string>('')
  const onSettledRef = useRef(opts.onSettled)
  onSettledRef.current = opts.onSettled

  useLayoutEffect(() => {
    if (!sessionId || !hasTurns) return
    if (restoredFor.current === sessionId) return
    restoredFor.current = sessionId
    const settle = () => {
      const cb = onSettledRef.current
      if (cb) cb()
    }
    const stored = readPersistedScroll(storageKey, sessionId)
    if (stored == null) {
      settle()
      return
    }
    if (stored.atBottom) {
      // Auto-follow path: parent's auto-follow effect handles
      // scroll-to-bottom on first paint. Defer settle by one rAF so
      // the scrollToBottom has fired before we make the conversation
      // visible — otherwise a flash of scrollTop=0 leaks through.
      requestAnimationFrame(settle)
      return
    }
    if (!stored.anchor) {
      settle()
      return
    }
    const { turnId, offset } = stored.anchor

    // One-shot anchor restore. With all turns rendered to the DOM,
    // the anchor element is present immediately; we just measure and
    // assign scrollTop.
    let cancelled = false
    let userAborted = false
    const userAbort = () => { userAborted = true }
    const elInit = containerRef.current
    if (elInit) {
      elInit.addEventListener('wheel', userAbort, { once: true, passive: true })
      elInit.addEventListener('touchmove', userAbort, { once: true, passive: true })
      elInit.addEventListener('keydown', userAbort, { once: true })
      elInit.addEventListener('mousedown', userAbort, { once: true })
    }

    // One rAF lets the browser commit the initial paint so
    // getBoundingClientRect returns the correct positions.
    const raf = requestAnimationFrame(() => {
      if (cancelled || userAborted) {
        settle()
        return
      }
      const el = containerRef.current
      if (!el) {
        settle()
        return
      }
      const escapeId =
        typeof CSS !== 'undefined' && CSS.escape
          ? CSS.escape(turnId)
          : turnId.replace(/"/g, '\\"')
      const row = el.querySelector<HTMLElement>(`[data-turn-id="${escapeId}"]`)
      if (!row) {
        // Anchor row not present (turn was deleted / rewound).
        settle()
        return
      }
      const sRect = el.getBoundingClientRect()
      const rRect = row.getBoundingClientRect()
      // delta = current - desired; scrolling by delta lands the row
      // at the saved offset above the viewport top.
      const delta = (rRect.top - sRect.top) + offset
      if (Math.abs(delta) > 0) {
        el.scrollTop = el.scrollTop + delta
      }
      settle()
    })

    return () => {
      cancelled = true
      cancelAnimationFrame(raf)
      if (elInit) {
        elInit.removeEventListener('wheel', userAbort)
        elInit.removeEventListener('touchmove', userAbort)
        elInit.removeEventListener('keydown', userAbort)
        elInit.removeEventListener('mousedown', userAbort)
      }
    }
  }, [sessionId, hasTurns, storageKey, containerRef])
}

/** usePersistScroll throttles writes of the live anchor + atBottom
 *  flag to localStorage. We don't write on every scroll event (would
 *  burn the main thread on autopilot floods); a 250ms trailing-edge
 *  debounce is plenty for restore-on-reload accuracy.
 *
 *  Two subtleties:
 *  1. We mirror the latest sample into closure-local variables.
 *     That way unmount-cleanup can flush without touching the DOM —
 *     which has already been removed by the time React tears down
 *     the parent's effects, so reading rect off the detached element
 *     would return zeros and persist a corrupted record.
 *  2. The cleanup ALWAYS flushes the latest known sample. Without
 *     this, a user who scrolls and then switches tabs inside the
 *     250ms debounce window loses their position. */
export function usePersistScroll(opts: {
  sessionId: string
  storageKey: string
  containerRef: React.RefObject<HTMLDivElement | null>
}) {
  const { sessionId, storageKey, containerRef } = opts

  useEffect(() => {
    if (!sessionId) return
    let timer: number | undefined
    let installedEl: HTMLDivElement | null = null
    let attached = false
    let sampled = false
    let latestAtBottom = true
    let latestAnchor: { turnId: string; offset: number } | null = null
    const flush = () => {
      if (!sampled) return
      writePersistedScroll(storageKey, sessionId, {
        atBottom: latestAtBottom,
        anchor: latestAnchor ?? undefined,
      })
    }
    const sample = (el: HTMLDivElement) => {
      latestAtBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 32
      if (latestAtBottom) {
        latestAnchor = null
        sampled = true
        return
      }
      // Mid-conversation. findTopAnchor returns null when no row
      // spans the viewport top (transient render state — the user
      // just wheeled and react-window hasn't re-rendered yet).
      // Don't overwrite a previously-good anchor with a transient
      // null; the next sample will set it once the layout settles.
      const a = findTopAnchor(el)
      if (a) {
        latestAnchor = a
        sampled = true
      }
    }
    const onScroll = () => {
      if (!installedEl) return
      sample(installedEl)
      if (timer) window.clearTimeout(timer)
      timer = window.setTimeout(flush, 250)
    }
    // Containers can become available AFTER this effect first runs,
    // so we poll briefly until we find one. Once attached we stop
    // polling and capture the initial position so an unmount before
    // any user scroll still persists the (likely-correct) starting
    // sample.
    const poll = window.setInterval(() => {
      const el = containerRef.current
      if (!el || attached) return
      el.addEventListener('scroll', onScroll)
      installedEl = el
      attached = true
      window.clearInterval(poll)
      sample(el)
    }, 80)
    return () => {
      window.clearInterval(poll)
      if (installedEl) installedEl.removeEventListener('scroll', onScroll)
      if (timer) window.clearTimeout(timer)
      flush()
    }
  }, [containerRef, sessionId, storageKey])
}
