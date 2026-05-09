/** useScrollAutoFollow — auto-follow + scroll-restore wiring for the
 *  Claude tab conversation list.
 *
 *  Encapsulates the scroll race-condition logic that used to live
 *  inline in claude-agent-view.tsx (28 hooks deep). Hotfixes 93f2f78,
 *  bb1d963, 7410582, and b6d5517 all added more state to the same
 *  spot — this hook isolates the moving parts so future tweaks don't
 *  touch unrelated code.
 *
 *  Responsibilities:
 *    - autoFollow state + ref mirror (autoFollowRef so the
 *      scroll-to-bottom effect always sees the latest value, never a
 *      batched-stale React render)
 *    - lastUserInputAtRef + onUserInput: stamp the moment user
 *      gestures hit the scroller so an in-flight streaming chunk
 *      can defer to the user's intent
 *    - onListScroll: bridge react-window scroll events to autoFollow,
 *      distinguishing user-driven from programmatic scrolls (the
 *      programmatic ones can re-enable but never disable autoFollow)
 *    - scroll-to-bottom effect: parks the view at the latest content
 *      while autoFollow is on, AND defers when the user is actively
 *      scrolling (250ms guard mirrors ConversationList's own user
 *      window so the listener boundaries can't deadlock)
 *    - containerRef polling effect: react-window installs the scroll
 *      element asynchronously after the imperative-API callback fires
 *    - cold-load recovery: re-checks the persisted record once
 *      state.sessionId becomes available
 *    - useScrollRestore + usePersistScroll: turn-id anchored save/load
 *    - restoreVisible + onSettled: visibility gate so the user never
 *      sees a paint at scrollTop=0 before the saved anchor lands
 *
 *  Returns the wiring the parent splices into ConversationList +
 *  the visibility gate value.
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
  /** Reactive list of top-level turns — used to determine when to
   *  fire scroll-to-bottom (turns count change implies new content).
   *  Pass the same array you hand to ConversationList. */
  turns: { id: string }[]
  /** Reactive agent status — also drives scroll-to-bottom (status
   *  flipping from 'thinking' → 'idle' may end with content the user
   *  hasn't seen yet). */
  status: string
  hasTurns: boolean
  turnIds: readonly string[]
  /** Stable ref to ConversationList's imperative API. The hook reads
   *  scrollToBottom / scrollToRow / element() through this. */
  listHandleRef: React.RefObject<ConversationListHandle | null>
}

interface UseScrollAutoFollowResult {
  /** Whether auto-follow is currently on. Drive the "scroll to
   *  latest" button visibility off `!autoFollow`. */
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
  /** Whether the conversation should be visible yet — false until
   *  the saved scroll anchor lands. Wrap ConversationList in a
   *  `style={{ visibility: restoreVisible ? 'visible' : 'hidden' }}`. */
  restoreVisible: boolean
  /** Live ref to the scroll container DOM element. Re-resolved on
   *  every render so persist/restore hooks see the latest. */
  containerRef: React.RefObject<HTMLDivElement | null>
  /** Imperative scroll-to-latest, used by the "scroll to latest"
   *  button. Also re-enables autoFollow.
   *  @param behavior 'smooth' for the user button, 'instant' for
   *                  programmatic resync. Defaults to 'smooth'. */
  scrollToLatest: (behavior?: 'smooth' | 'instant' | 'auto') => void
}

export function useScrollAutoFollow(args: UseScrollAutoFollowArgs): UseScrollAutoFollowResult {
  const {
    repoId,
    branchId,
    tabId,
    sessionId,
    turns,
    status,
    hasTurns,
    turnIds,
    listHandleRef,
  } = args

  const storageKey = scrollStorageKey(repoId, branchId, tabId || 'claude')

  // Initialise autoFollow from the persisted record so a user who
  // was scrolled up reading earlier in the conversation, then
  // switched tabs, doesn't get yanked back to bottom on remount.
  const [autoFollow, setAutoFollow] = useState<boolean>(() => {
    if (!sessionId) return true
    const stored = readPersistedScroll(storageKey, sessionId)
    if (!stored) return true
    return stored.atBottom
  })
  // Mirror autoFollow synchronously. The scroll-to-bottom effect
  // reads this ref, not the state, because React batches the
  // setAutoFollow(false) update from a user scroll behind the next
  // streaming chunk's setState — leaving the effect with a stale
  // autoFollow=true that yanks the user back to the bottom right
  // after they scrolled up to read.
  const autoFollowRef = useRef<boolean>(autoFollow)

  // Cold-load recovery: when sessionId becomes available *after*
  // mount (cache miss → WS init), re-check the persisted record
  // once and downgrade autoFollow if the user had been scrolled up.
  // Guarded by a ref so this fires at most once per session.
  const autoFollowSyncedFor = useRef<string>('')
  useEffect(() => {
    if (!sessionId) return
    if (autoFollowSyncedFor.current === sessionId) return
    autoFollowSyncedFor.current = sessionId
    const stored = readPersistedScroll(storageKey, sessionId)
    if (!stored) return
    if (stored.atBottom) return
    autoFollowRef.current = false
    setAutoFollow(false)
  }, [sessionId, storageKey])

  const containerRef = useRef<HTMLDivElement | null>(null)

  // Hotfix: timestamp of the most-recent user input on the scroll
  // container (wheel / touchmove / keydown / mousedown). Updated
  // synchronously by ConversationList's `onUserInput` the moment the
  // event fires, BEFORE the browser applies the scroll and BEFORE
  // the resulting `scroll` event reaches `onListScroll`. The 250ms
  // guard mirrors the existing isUserDriven window in
  // ConversationList so listener boundaries can't deadlock.
  const lastUserInputAtRef = useRef<number>(0)
  const onUserInput = useCallback(() => {
    lastUserInputAtRef.current = performance.now()
  }, [])

  // S017: auto-scroll routes through the ConversationList imperative
  // API. We can't just bump scrollTop on the wrapper because the
  // wrapper isn't the scroll container any more — react-window owns
  // the scroller and only it knows the precomputed total height.
  useEffect(() => {
    if (!autoFollowRef.current) return
    // Hotfix b6d5517: defer to active user input. If the user
    // wheeled / touched / pressed a key in the last 250 ms, they're
    // trying to scroll — don't fight them by yanking back to bottom.
    if (performance.now() - lastUserInputAtRef.current < 250) return
    const handle = listHandleRef.current
    if (handle) handle.scrollToBottom('instant')
    // We watch the input refs implicitly via the closure — but the
    // dep array is `[turns, status]` so a new chunk / status change
    // re-fires the effect.
  }, [turns, status, listHandleRef])

  // Bridge scroll events from List → autoFollow flag. autoFollow is
  // strictly USER-OWNED: only user-driven scrolls (wheel / touch /
  // keys / mousedown) toggle it. Programmatic scrolls — our own
  // scrollToBottom call, scroll-restore on session load, react-window
  // layout adjustments when a row's height changes (= user opens the
  // edit editor) — must NEVER change autoFollow. The previous "land
  // at bottom + isUserDriven=false → re-engage" path silently flipped
  // autoFollow back on whenever a programmatic scroll happened to
  // land at the bottom (e.g. layout adjustment after a streaming
  // chunk landed exactly at scrollHeight, or react-window's row
  // estimate happened to put us within 32 px of max). That broke the
  // "I scrolled up and the AI keeps yanking me back" invariant in
  // edge cases that the regular wheel-then-stream test missed
  // (S43cfb1 manual smoke AC-2-7 / AC-4-5).
  const onListScroll = useCallback(
    (
      scrollTop: number,
      scrollHeight: number,
      clientHeight: number,
      isUserDriven: boolean,
    ) => {
      if (isUserDriven) {
        const atBottom = scrollHeight - scrollTop - clientHeight < 32
        autoFollowRef.current = atBottom
        setAutoFollow(atBottom)
      }
      // Also keep containerRef in sync so the persist/restore hooks
      // resolve the live element each render.
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
  // position has been applied. Default to visible (no gate) when
  // there's nothing to restore.
  const [restoreVisible, setRestoreVisible] = useState<boolean>(() => {
    if (!sessionId) return true
    const stored: PersistedScrollRecord | null = readPersistedScroll(storageKey, sessionId)
    return stored == null
  })
  // Safety net: even if onSettled never fires (effect cancelled
  // mid-restore, sessionId changes, etc.), uncover the conversation
  // after a short timeout so the user is never stuck staring at a
  // blank panel.
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

  const scrollToLatest = useCallback((behavior: 'smooth' | 'instant' | 'auto' = 'smooth') => {
    const handle = listHandleRef.current
    if (handle) handle.scrollToBottom(behavior)
    autoFollowRef.current = true
    setAutoFollow(true)
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
