/** useScrollAutoFollow — auto-follow + scroll-restore wiring for the
 *  Claude tab conversation list.
 *
 *  ## Mental model
 *
 *  Auto-follow is the composition of TWO orthogonal facts:
 *
 *    (1) "User wants to follow" ≡ "User is currently at the bottom of
 *        the conversation". Tracked as `autoFollow` (state + ref mirror).
 *        Updated EXCLUSIVELY by user-driven scrolls (wheel / touch /
 *        keyboard / mousedown). Programmatic scrolls (our own pin,
 *        scroll-restore on tab switch, react-window's row-measurement
 *        layout adjustments) NEVER toggle it. The "↓ Scroll to latest"
 *        button explicitly sets it true.
 *
 *    (2) "AI / user just produced new content" ≡ `state.contentSeq`
 *        was bumped. The reducer bumps this counter ONLY when an
 *        event of a content-arrival type is applied (turn.start /
 *        block.{start,delta,end} / tool.result / permission.request /
 *        ask.question / plan.question / user.message / rewind.apply /
 *        session.init). Status flips, mcp.update, init.info, etc. do
 *        NOT bump it. Idle ref churn does NOT bump it. The hook
 *        therefore receives a clean direct signal — no "infer from
 *        status" or "diff turns.length" or "watch scrollHeight" tricks.
 *
 *  Auto-follow trigger = (1) AND (2) AND no recent user input (the
 *  250ms guard exists only to defuse a same-tick race where the user
 *  wheels right as a chunk arrives — by the time the effect runs,
 *  the wheel's `scroll` event hasn't fired yet, so autoFollow ref is
 *  still stale-true; the timestamp guard catches that single edge).
 *
 *  ## What this hook does NOT do
 *
 *  - It does NOT inspect `status` — agent state churn doesn't matter.
 *  - It does NOT diff `turns.length` — content arrival is signalled
 *    explicitly by contentSeq.
 *  - It does NOT use ResizeObserver — the imperative scrollToBottom in
 *    ConversationList handles late row-measurement settling for each
 *    pin call.
 *
 *  ## Other responsibilities
 *
 *  - useScrollRestore + usePersistScroll: turn-id anchored save/load
 *    of scroll position across tab switches.
 *  - restoreVisible visibility gate: prevents the user from seeing
 *    a scrollTop=0 flash before the saved anchor lands.
 *  - containerRef polling: react-window installs the scroll element
 *    asynchronously after the imperative-API callback fires.
 */
import { useCallback, useEffect, useRef, useState } from 'react'

import {
  type PersistedScrollRecord,
  readPersistedScroll,
  scrollStorageKey,
  usePersistScroll,
  useScrollRestore,
} from '../scroll-hooks'

import type { ConversationListHandle } from '../conversation-list'

interface UseScrollAutoFollowArgs {
  repoId: string
  branchId: string
  tabId: string
  sessionId: string
  /** Monotonic counter from agent-state. Bumped by the reducer ONLY
   *  on content-arrival events (turn.start / block.* / tool.result /
   *  permission.request / ask.question / plan.question / user.message
   *  / rewind / session.init). The pin effect runs once per change. */
  contentSeq: number
  hasTurns: boolean
  turnIds: readonly string[]
  /** Stable ref to ConversationList's imperative API. */
  listHandleRef: React.RefObject<ConversationListHandle | null>
}

interface UseScrollAutoFollowResult {
  /** True iff the user is currently at the bottom of the conversation
   *  (= they want auto-follow). Drive the "↓ to latest" button
   *  visibility off `!autoFollow`. */
  autoFollow: boolean
  /** Splat into <ConversationList onScroll={onListScroll} />. */
  onListScroll: (
    scrollTop: number,
    scrollHeight: number,
    clientHeight: number,
    isUserDriven: boolean,
  ) => void
  /** Splat into <ConversationList onUserInput={onUserInput} />. */
  onUserInput: () => void
  /** Whether the conversation should be visible yet — false until the
   *  saved scroll anchor has landed. Wrap ConversationList in
   *  `style={{ visibility: restoreVisible ? 'visible' : 'hidden' }}`. */
  restoreVisible: boolean
  /** Live ref to the scroll container DOM element. */
  containerRef: React.RefObject<HTMLDivElement | null>
  /** "↓ Scroll to latest" handler. Sets autoFollow=true and snaps to
   *  bottom. Use behavior='instant' for the user button (matches the
   *  Slack/Discord/Telegram pattern); 'smooth' is provided for callers
   *  that want the animation. */
  scrollToLatest: (behavior?: 'smooth' | 'instant' | 'auto') => void
}

export function useScrollAutoFollow(args: UseScrollAutoFollowArgs): UseScrollAutoFollowResult {
  const {
    repoId,
    branchId,
    tabId,
    sessionId,
    contentSeq,
    hasTurns,
    turnIds,
    listHandleRef,
  } = args

  const storageKey = scrollStorageKey(repoId, branchId, tabId || 'claude')

  // autoFollow = "user is at bottom". Initialised from the persisted
  // record so a tab switch back to a user who was reading earlier
  // doesn't yank them to bottom on remount.
  const [autoFollow, setAutoFollow] = useState<boolean>(() => {
    if (!sessionId) return true
    const stored = readPersistedScroll(storageKey, sessionId)
    if (!stored) return true
    return stored.atBottom
  })
  // Synchronous mirror — read by the pin effect without waiting for
  // React's batched setState commit.
  const autoFollowRef = useRef<boolean>(autoFollow)

  // Cold-load recovery: when sessionId becomes available *after*
  // mount (cache miss → WS init), re-check the persisted record once.
  const syncedFor = useRef<string>('')
  useEffect(() => {
    if (!sessionId) return
    if (syncedFor.current === sessionId) return
    syncedFor.current = sessionId
    const stored = readPersistedScroll(storageKey, sessionId)
    if (!stored) return
    if (stored.atBottom === autoFollowRef.current) return
    autoFollowRef.current = stored.atBottom
    setAutoFollow(stored.atBottom)
  }, [sessionId, storageKey])

  const containerRef = useRef<HTMLDivElement | null>(null)

  // 250ms input guard — synchronous mark of the user touching the
  // scrollbar (wheel / touchmove / keydown / mousedown). Updated by
  // ConversationList BEFORE the browser delivers the resulting `scroll`
  // event, so the pin effect can defer when a wheel and chunk-arrival
  // commit in the same React batch.
  const lastUserInputAtRef = useRef<number>(0)
  const onUserInput = useCallback(() => {
    lastUserInputAtRef.current = performance.now()
  }, [])

  // Initial-mount landing: when the scroll element resolves (react-
  // window installs it asynchronously after the imperative handle's
  // callback fires), do a one-time scroll-to-bottom if autoFollow
  // says we should be at the latest. The contentSeq effect below
  // handles every subsequent content event, but on a fresh mount
  // contentSeq may not change (no WS event arrives if the cache had
  // already supplied turns) — so we need an explicit "first paint
  // landing" path. Polls up to ~1s; aborts on session change.
  useEffect(() => {
    let cancelled = false
    let attempts = 0
    const tryLand = () => {
      if (cancelled) return
      if (!autoFollowRef.current) return
      const handle = listHandleRef.current
      const el = handle?.element() ?? null
      if (!el) {
        attempts++
        if (attempts < 20) window.setTimeout(tryLand, 50)
        return
      }
      handle?.scrollToBottom('instant')
    }
    tryLand()
    return () => { cancelled = true }
  }, [sessionId, listHandleRef])

  // SOLE recurring auto-scroll trigger: contentSeq changed (= a
  // content-arrival event was applied to the reducer). No status
  // check, no length diff, no ResizeObserver, no 5.2s tail.
  // Skips the very first dep value: on mount the initial-landing
  // effect above already handles positioning; this effect should
  // only fire for SUBSEQUENT content events.
  const firstContentSeqRef = useRef<number>(contentSeq)
  useEffect(() => {
    if (contentSeq === firstContentSeqRef.current) return
    if (!autoFollowRef.current) return
    // Defer if the user touched the scrollbar in the last 250 ms.
    // Falsy check: ref starts at 0; performance.now() right after
    // mount can be < 250 and would otherwise spuriously trip.
    if (
      lastUserInputAtRef.current > 0 &&
      performance.now() - lastUserInputAtRef.current < 250
    ) return
    listHandleRef.current?.scrollToBottom('instant')
  }, [contentSeq, listHandleRef])

  // Bridge scroll events from List → autoFollow. ONLY user-driven
  // scrolls toggle the flag. Programmatic scrolls (our pin / scroll-
  // restore / react-window layout adjustment) must never touch it.
  const onListScroll = useCallback(
    (
      scrollTop: number,
      scrollHeight: number,
      clientHeight: number,
      isUserDriven: boolean,
    ) => {
      if (isUserDriven) {
        const atBottom = scrollHeight - scrollTop - clientHeight < 32
        if (atBottom !== autoFollowRef.current) {
          autoFollowRef.current = atBottom
          setAutoFollow(atBottom)
        }
      }
      const el = listHandleRef.current?.element() ?? null
      containerRef.current = el
    },
    [listHandleRef],
  )

  // Resolve containerRef on mount. react-window installs its scroll
  // container asynchronously: List's `element()` returns null until
  // its imperative-API callback fires, which happens AFTER this
  // effect on a fresh mount.
  useEffect(() => {
    let id: number | undefined
    const tryResolve = () => {
      const el = listHandleRef.current?.element() ?? null
      if (el) {
        containerRef.current = el
        return
      }
      id = window.setTimeout(tryResolve, 50)
    }
    tryResolve()
    return () => { if (id) window.clearTimeout(id) }
  }, [sessionId, listHandleRef])

  const restoreScrollToRow = useCallback((index: number) => {
    listHandleRef.current?.scrollToRow(index, {
      align: 'start',
      behavior: 'instant',
    })
  }, [listHandleRef])

  // Visibility gate: hide the conversation until the saved scroll
  // position has been applied. Default to visible when there's
  // nothing to restore.
  const [restoreVisible, setRestoreVisible] = useState<boolean>(() => {
    if (!sessionId) return true
    const stored: PersistedScrollRecord | null = readPersistedScroll(storageKey, sessionId)
    return stored == null
  })
  // Safety net: reveal after a short timeout even if onSettled never
  // fires (effect cancelled mid-restore, sessionId changes, etc.).
  useEffect(() => {
    if (restoreVisible) return
    const t = window.setTimeout(() => setRestoreVisible(true), 1500)
    return () => window.clearTimeout(t)
  }, [restoreVisible])
  const onRestoreSettled = useCallback(() => setRestoreVisible(true), [])

  useScrollRestore({
    sessionId,
    storageKey,
    containerRef,
    hasTurns,
    turnIds,
    scrollToRow: restoreScrollToRow,
    onSettled: onRestoreSettled,
  })
  usePersistScroll({
    sessionId,
    storageKey,
    containerRef,
  })

  const scrollToLatest = useCallback((behavior: 'smooth' | 'instant' | 'auto' = 'instant') => {
    autoFollowRef.current = true
    setAutoFollow(true)
    listHandleRef.current?.scrollToBottom(behavior)
  }, [listHandleRef])

  return {
    autoFollow,
    onListScroll,
    onUserInput,
    restoreVisible,
    containerRef,
    scrollToLatest,
  }
}
