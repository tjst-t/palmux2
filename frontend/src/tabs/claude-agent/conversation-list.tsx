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
  useMemo,
  useRef,
  useState,
} from 'react'
import {
  List,
  type ListImperativeAPI,
  type RowComponentProps,
  useDynamicRowHeight,
} from 'react-window'

import type { Turn } from './types'

interface ConversationListHandle {
  /** Scroll the conversation so the last data turn's measured bottom
   *  edge aligns with the viewport bottom. Late content growth
   *  (Shiki / images / mermaid / markdown) is followed for ~5s with
   *  instant corrections so the smooth animation isn't fought. */
  scrollToBottom: (behavior?: 'auto' | 'instant' | 'smooth') => void
  /** Scroll the conversation so the row at `index` is centred in the
   *  viewport. Used by Cmd+F (S018) — when the user navigates to the
   *  next match we centre the corresponding turn so they see context. */
  scrollToRow: (index: number, opts?: { align?: 'start' | 'center' | 'end' | 'auto'; behavior?: 'auto' | 'instant' | 'smooth' }) => void
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
    <div
      style={style}
      data-row-last={index === turns.length - 1 ? '1' : undefined}
      data-turn-id={turn.id}
      {...ariaAttributes}
    >
      {/* Inner wrapper is what the ResizeObserver measures. We add a
          tiny bottom gap so consecutive turns don't visually fuse. */}
      <div style={{ paddingBottom: 6 }}>{renderTurn(turn, index)}</div>
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
  /** Notified when the scroll position changes. `isUserDriven` is
   *  true when the change was triggered by a recent wheel / touchmove
   *  / keydown — i.e. the user actually moved the scrollbar. It is
   *  false for programmatic scrolls (our own scrollToBottom call,
   *  scroll-restore, etc). The parent uses this to avoid unsetting
   *  autoFollow when a programmatic scroll lands a few pixels short
   *  of the absolute bottom (which happens during streaming because
   *  react-window scrolls against estimated row heights). */
  onScroll?: (
    scrollTop: number,
    scrollHeight: number,
    clientHeight: number,
    isUserDriven: boolean,
  ) => void
  /** Hotfix: notified the moment a wheel / touchmove / keydown fires
   *  on the scroll container — synchronously, BEFORE the browser
   *  applies the scroll and BEFORE the resulting `scroll` event
   *  reaches `onScroll`. The parent uses it as a "user is touching
   *  the scrollbar right now" signal so its auto-follow effect can
   *  skip the next yank-to-bottom even when a streaming chunk
   *  commits in the same React batch (the `scroll` event arrives
   *  after the next paint, by which point the effect has already
   *  fired and read a stale autoFollowRef). */
  onUserInput?: () => void
}

export const ConversationList = forwardRef<ConversationListHandle, ConversationListProps>(
  function ConversationList({ turns, renderTurn, sessionKey, onScroll, onUserInput }, ref) {
    // Use a callback ref + useState combo so the component re-renders
    // when react-window's imperative API actually becomes ready. Plain
    // useRef + a useEffect with stable deps wouldn't see the API land:
    // List stores its container DOM in its own useState(null) and only
    // populates it via a ref callback during commit, so listRef.current
    // is empty on the first useEffect tick — and stable-dep effects
    // never re-run to pick up the second render.
    const listRef = useRef<ListImperativeAPI | null>(null)
    const [scrollEl, setScrollEl] = useState<HTMLDivElement | null>(null)
    const setListApi = useCallback((api: ListImperativeAPI | null) => {
      listRef.current = api
      setScrollEl(api?.element ?? null)
    }, [])
    // 200px is a reasonable initial guess for an unmeasured turn
    // (one short user message + a tool block). Real heights replace
    // it as soon as ResizeObserver fires. The exact value doesn't
    // matter much for "↓" correctness — `scrollToBottom` no longer
    // depends on this estimate (it reads the last row's real DOM
    // bottom directly). The estimate only affects the rendered
    // window selection during initial scroll-to-row.
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
          // Two-phase scroll-to-bottom.
          //
          // Why this is hard: react-window's `scrollToRow({behavior:'smooth'})`
          // pins its scroll target at click time using the cumulative
          // row-height cache, where unrendered middle rows are 200px
          // estimates. The browser's smooth scroll then runs to that
          // *fixed* target. As the animation progresses, intermediate
          // rows render and ResizeObserver measures real heights, the
          // cache and sentinel update, and the *true* bottom shifts.
          // But the animation's destination doesn't follow — and
          // re-aiming via `scrollTo` restarts the animation, so a
          // re-aim per measurement never converges. The user observes
          // smooth motion stopping at a position that is 20–30%
          // short of the actual bottom. Worse, the last row never
          // enters the render window, so any DOM-based correction
          // (querySelector for `[data-row-last="1"]`) finds nothing
          // to align to.
          //
          // Strategy:
          //   1. Visible animation: `api.scrollToRow` with the
          //      requested behavior. Lands at whatever the cache
          //      thought "bottom" was at click time.
          //   2. Iterative settle (after the animation lands):
          //      `el.scrollTop = el.scrollHeight` — the browser
          //      clamps to the current real `scrollHeight - clientHeight`,
          //      so this immediately puts us at the *current* bottom
          //      (not the click-time bottom). That render commit also
          //      pulls the last row into the DOM. Wait one rAF + a
          //      short timer, repeat until `scrollHeight` stabilises
          //      (i.e. no more measurements changing the total). Each
          //      pop is invisible if the previous pop already landed
          //      at bottom; visible only as a tiny adjustment when a
          //      late measurement (Shiki / image / mermaid) actually
          //      grew the content.
          //   3. Aborts on user wheel/touch/keys.
          const api = listRef.current
          if (!api) return
          const lastIndex = turns.length - 1
          if (lastIndex < 0) return
          const el = api.element
          if (!el) return

          api.scrollToRow({ index: lastIndex, align: 'end', behavior })

          let aborted = false
          const abort = () => { aborted = true }
          el.addEventListener('wheel', abort, { once: true, passive: true })
          el.addEventListener('touchmove', abort, { once: true, passive: true })
          el.addEventListener('keydown', abort, { once: true })
          // Hotfix: also bail on `mousedown` so a scrollbar-drag — which
          // doesn't fire wheel/touchmove/keydown — kills the tail loop
          // and stops the user being yanked back to bottom.
          el.addEventListener('mousedown', abort, { once: true })

          // Browsers run the default smooth-scroll for ~300–500ms.
          // 550ms covers that comfortably. For instant we skip the
          // wait entirely.
          const animationBudget = behavior === 'smooth' ? 550 : 0

          window.setTimeout(() => {
            if (aborted) return
            // Iterative settle. `el.scrollTop = el.scrollHeight` asks
            // the browser to clamp to the current scrollable max,
            // which in turn forces react-window to render the bottom
            // rows. ResizeObserver measures them; cache and sentinel
            // update. We loop until scrollTop matches the new max
            // AND scrollHeight has stopped changing. The 50ms delay
            // gives ResizeObserver and react-window's render commit
            // time to land before we re-check.
            let prevHeight = -1
            const settle = () => {
              if (aborted) return
              const max = Math.max(0, el.scrollHeight - el.clientHeight)
              if (el.scrollTop < max) el.scrollTop = max
              if (el.scrollHeight === prevHeight) return  // converged
              prevHeight = el.scrollHeight
              window.setTimeout(settle, 50)
            }
            settle()

            // Tail tracker for very-late content (1–3s post-click for
            // images, mermaid, Shiki). Cheap because it only fires
            // when scrollHeight actually moves; we just keep clamping
            // scrollTop to the new max.
            let lastH = el.scrollHeight
            const tail = () => {
              if (aborted) return
              if (el.scrollHeight === lastH) return
              lastH = el.scrollHeight
              const max = Math.max(0, el.scrollHeight - el.clientHeight)
              if (el.scrollTop < max) el.scrollTop = max
            }
            let ro: ResizeObserver | null = null
            if (typeof ResizeObserver !== 'undefined') {
              ro = new ResizeObserver(tail)
              const observe = () => {
                if (!ro) return
                const sentinel = el.lastElementChild as HTMLElement | null
                if (sentinel && sentinel.style.zIndex === '-1') ro.observe(sentinel)
              }
              observe()
              const reobserve = window.setInterval(observe, 250)
              window.setTimeout(() => window.clearInterval(reobserve), 5000)
            }
            const intervalId = window.setInterval(tail, 100)
            window.setTimeout(() => {
              window.clearInterval(intervalId)
              if (ro) ro.disconnect()
              el.removeEventListener('wheel', abort)
              el.removeEventListener('touchmove', abort)
              el.removeEventListener('keydown', abort)
              el.removeEventListener('mousedown', abort)
            }, 5200 - animationBudget)
          }, animationBudget)
        },
        scrollToRow: (index, opts) => {
          const api = listRef.current
          if (!api) return
          if (index < 0 || index >= turns.length) return
          api.scrollToRow({
            index,
            align: opts?.align ?? 'center',
            behavior: opts?.behavior ?? 'smooth',
          })
        },
        element() {
          return listRef.current?.element ?? null
        },
      }),
      [turns.length],
    )

    // Wire scroll events from the inner scroll container up to the
    // parent so it can update auto-follow without us having to
    // duplicate that state here. Depends on the resolved element so it
    // attaches the moment react-window mounts the container.
    //
    // We deliberately do NOT fire onScroll on attach: react-window's
    // initial scrollTop is 0, but the parent treats autoFollow as
    // "user wants to follow latest" — defaulting that to true is the
    // right thing on session load (the auto-follow effect will scroll
    // to bottom as soon as turns arrive). Firing onScroll once here
    // would set autoFollow=false against the freshly mounted top
    // position and break auto-follow on the very first AI chunk.
    //
    // To let the parent distinguish "user dragged the scrollbar" from
    // "we just programmatically scrolled to bottom and react-window's
    // estimate landed a few pixels short", we tag the scroll event
    // with isUserDriven: true if a wheel/touchmove/keydown fired on
    // the scroll container within the last 250ms. Otherwise false.
    useEffect(() => {
      if (!scrollEl) return
      let lastUserInputAt = 0
      const markUser = () => {
        lastUserInputAt = performance.now()
        // Hotfix: also notify the parent synchronously so its
        // auto-follow effect can see the user is touching the
        // scrollbar right now — without waiting for the browser to
        // emit the deferred `scroll` event.
        onUserInput?.()
      }
      const onScrollEvt = () => {
        if (!onScroll) return
        const isUserDriven = performance.now() - lastUserInputAt < 250
        onScroll(
          scrollEl.scrollTop,
          scrollEl.scrollHeight,
          scrollEl.clientHeight,
          isUserDriven,
        )
      }
      scrollEl.addEventListener('wheel', markUser, { passive: true })
      scrollEl.addEventListener('touchmove', markUser, { passive: true })
      scrollEl.addEventListener('keydown', markUser)
      scrollEl.addEventListener('mousedown', markUser)
      scrollEl.addEventListener('scroll', onScrollEvt)
      return () => {
        scrollEl.removeEventListener('wheel', markUser)
        scrollEl.removeEventListener('touchmove', markUser)
        scrollEl.removeEventListener('keydown', markUser)
        scrollEl.removeEventListener('mousedown', markUser)
        scrollEl.removeEventListener('scroll', onScrollEvt)
      }
    }, [scrollEl, onScroll, onUserInput])

    return (
      <List
        listRef={setListApi}
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
