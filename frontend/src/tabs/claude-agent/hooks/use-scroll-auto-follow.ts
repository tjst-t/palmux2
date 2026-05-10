/** useScrollAutoFollow — auto-follow + scroll-restore wiring for the
 *  Claude tab conversation list.
 *
 *  ## Mental model
 *
 *  Auto-follow is the composition of TWO orthogonal facts:
 *
 *    (1) "User wants to follow" ≡ "User is currently at the bottom of
 *        the conversation". Tracked as `autoFollow` (state + ref
 *        mirror). Updated EXCLUSIVELY by user-driven scrolls (wheel /
 *        touchmove / keyboard / mousedown) and the explicit
 *        "↓ Scroll to latest" button. Programmatic scrolls
 *        (our own pin, scroll-restore) NEVER toggle it.
 *
 *    (2) "AI / user just produced new content" ≡ `state.contentSeq`
 *        was bumped. The reducer bumps this counter ONLY when an
 *        event of a content-arrival type is applied. Status flips,
 *        mcp.update, init.info, etc. do NOT bump it.
 *
 *  Auto-follow trigger = (1) AND (2) AND no recent user input.
 *
 *  ## Simplified after dropping react-window virtualisation
 *
 *  Previously this hook had a containerRef-polling effect AND an
 *  initial-landing polling effect because react-window installed its
 *  scroll element asynchronously after the imperative-API callback
 *  fired. ConversationList is now a plain `<div ref={...}>` so the
 *  imperative `element()` returns the live DOM ref the moment the
 *  parent's effects run — no polling needed. The hook collapsed from
 *  ~280 lines to ~150.
 */
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'

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
   *  / rewind / session.init). */
  contentSeq: number
  hasTurns: boolean
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
   *  bottom. Default behavior is 'instant' (matches Slack/Discord/
   *  Telegram); 'smooth' is provided for callers that want it. */
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
    listHandleRef,
  } = args

  const storageKey = scrollStorageKey(repoId, branchId, tabId || 'claude')

  // autoFollow = "user is at bottom". Initialised from the persisted
  // record so a tab-switch back to a user who was reading earlier
  // doesn't yank them to bottom on remount.
  const [autoFollow, setAutoFollow] = useState<boolean>(() => {
    if (!sessionId) return true
    const stored = readPersistedScroll(storageKey, sessionId)
    if (!stored) return true
    return stored.atBottom
  })
  // Synchronous mirror — read by effects without waiting for React's
  // batched setState commit.
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
  // Resolve containerRef in a useLayoutEffect — children's
  // useImperativeHandle commits run BEFORE the parent's useLayoutEffects,
  // so by the time this fires `listHandleRef.current` is populated.
  // Doing it during render (before commit) would always read null on
  // the first mount, which silently broke useScrollRestore (it ran
  // its one-shot useLayoutEffect with containerRef=null and gave up).
  // The dep [listHandleRef] makes this a stable one-time assignment;
  // listHandleRef itself is a ref object, never changes identity.
  useLayoutEffect(() => {
    containerRef.current = listHandleRef.current?.element() ?? null
  }, [listHandleRef])

  // 250ms input guard — synchronous mark of the user touching the
  // scrollbar. ConversationList sets this BEFORE the browser delivers
  // the resulting `scroll` event, so the pin effect can defer when a
  // wheel and chunk-arrival commit in the same React batch.
  const lastUserInputAtRef = useRef<number>(0)
  const onUserInput = useCallback(() => {
    lastUserInputAtRef.current = performance.now()
  }, [])

  // Initial-landing fire: scroll to bottom on mount if autoFollow
  // says we should be at the latest. With native DOM the scroll
  // element is available synchronously, so a single fire suffices.
  // Re-runs on session change (tab switch back to a different
  // conversation). Subsequent content events go through the
  // contentSeq effect below.
  const landedFor = useRef<string>('')
  useEffect(() => {
    if (!sessionId) return
    if (landedFor.current === sessionId) return
    landedFor.current = sessionId
    if (!autoFollowRef.current) return
    listHandleRef.current?.scrollToBottom('instant')
  }, [sessionId, listHandleRef])

  // Recurring auto-scroll trigger: contentSeq changed (= a content-
  // arrival event was applied to the reducer). The first dep value
  // (initial mount with cached state) is skipped — the initial-
  // landing effect above already handles positioning.
  const firstContentSeqRef = useRef<number>(contentSeq)
  useEffect(() => {
    if (contentSeq === firstContentSeqRef.current) return
    if (!autoFollowRef.current) return
    if (
      lastUserInputAtRef.current > 0 &&
      performance.now() - lastUserInputAtRef.current < 250
    ) return
    listHandleRef.current?.scrollToBottom('instant')
  }, [contentSeq, listHandleRef])

  // Bridge scroll events from List → autoFollow. ONLY user-driven
  // scrolls toggle the flag. Programmatic scrolls (our pin / scroll-
  // restore) must never touch it.
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
    },
    [],
  )

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
