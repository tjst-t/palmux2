/** Scroll save / restore for the Claude tab conversation list.
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

/** Persistence record for the conversation scroll position. We store
 *  a turn-id anchor (turn-id + offset of the turn's top edge above
 *  the viewport top) rather than absolute scrollTop so the saved
 *  position survives content changes between sessions (a new image
 *  loading earlier in the conversation, a remeasurement, etc.).
 *
 *  - `atBottom`: the user was at/near the bottom — auto-follow
 *    semantics; on restore we don't compute an anchor, the parent's
 *    auto-follow effect parks us at the latest content.
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

/** Find the row at the top of the viewport — the one whose top edge
 *  is at or just above the scroller's top edge
 *  (rect.top ≤ sRect.top < rect.bottom).
 *
 *  All turns are in the DOM (no virtualisation), so in steady state
 *  we always find a spanning row unless the user is at the very top
 *  with all turns scrolled into view (rect.top > sRect.top for every
 *  turn). In that edge case the caller keeps its previous anchor —
 *  there's no meaningful one to record. */
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
 *    - atBottom=true → no-op. The parent's auto-follow effect parks
 *      us at the latest content.
 *    - atBottom=false + anchor present → find the row by
 *      `[data-turn-id]`, scrollTop = (row.top - container.top) +
 *      offset, done.
 *    - anchor turnId not in current list (rewound, truncated): give
 *      up. The user gets the default (top of list / auto-follow).
 *
 *  User-input abort listeners are kept as defense in depth even
 *  though the restore window is now ~1 frame (one rAF for the
 *  initial paint commit).
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
  // eslint-disable-next-line react-hooks/refs -- pre-React-19 latest-closure ref pattern (no useEffectEvent yet)
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
      // scroll-to-bottom in its own useLayoutEffect (also commit
      // phase). Defer settle by one rAF so visibility flips after
      // paint, by which time the bottom-pin has already happened.
      requestAnimationFrame(settle)
      return
    }
    if (!stored.anchor) {
      settle()
      return
    }
    const { turnId, offset } = stored.anchor

    // One-shot anchor restore. With all turns in the DOM the anchor
    // element is present immediately; we just measure and assign
    // scrollTop. One rAF lets the browser commit the initial paint
    // so getBoundingClientRect returns correct positions.
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
 *  flag to localStorage on every scroll event (250 ms trailing-edge
 *  debounce). Cleanup ALWAYS flushes the latest known sample so a
 *  user who scrolls and then closes / switches inside the debounce
 *  window doesn't lose their position.
 *
 *  Two subtleties:
 *    1. We mirror the latest sample into closure-local variables
 *       (latestAtBottom / latestAnchor). Unmount-cleanup flushes
 *       from these vars without touching the DOM, which would
 *       return zeros on a detached element and persist garbage.
 *    2. The effect re-runs when `hasTurns` flips false → true. On
 *       the very first mount of the Claude tab, ConversationList
 *       isn't in the DOM yet (state.turns is []), so containerRef
 *       is null and there's nothing to listen on. When the WS init
 *       populates turns, ConversationList mounts, the parent's
 *       useLayoutEffect updates containerRef, and this effect
 *       re-runs to attach its scroll listener.
 */
export function usePersistScroll(opts: {
  sessionId: string
  storageKey: string
  containerRef: React.RefObject<HTMLDivElement | null>
  hasTurns: boolean
}) {
  const { sessionId, storageKey, containerRef, hasTurns } = opts

  useEffect(() => {
    if (!sessionId || !hasTurns) return
    const el = containerRef.current
    if (!el) return

    let timer: number | undefined
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
    const sample = (target: HTMLDivElement) => {
      latestAtBottom = target.scrollHeight - target.scrollTop - target.clientHeight < 32
      if (latestAtBottom) {
        latestAnchor = null
        sampled = true
        return
      }
      // Mid-conversation. findTopAnchor returns null only at the
      // very top of the list (no row spans the viewport top).
      // Don't overwrite a previous good anchor with null.
      const a = findTopAnchor(target)
      if (a) {
        latestAnchor = a
        sampled = true
      }
    }
    const onScroll = () => {
      sample(el)
      if (timer) window.clearTimeout(timer)
      timer = window.setTimeout(flush, 250)
    }
    el.addEventListener('scroll', onScroll)
    // Capture the initial position so an unmount before any user
    // scroll still persists the (likely-correct) starting sample.
    sample(el)

    return () => {
      el.removeEventListener('scroll', onScroll)
      if (timer) window.clearTimeout(timer)
      flush()
    }
  }, [sessionId, hasTurns, storageKey, containerRef])
}
