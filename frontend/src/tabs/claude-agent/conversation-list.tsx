// Conversation list — full DOM render with `content-visibility: auto`.
//
// Previously this used react-window v2's `<List>` + `useDynamicRowHeight`
// for virtualisation. That choice forced an estimated row height (200px)
// for unmeasured rows, and the cumulative scroll-height estimate
// fluctuated wildly as the user scrolled into older rows whose actual
// heights were measured for the first time. Symptom: scrolling up "got
// stuck" because every wheel that revealed a new row updated the
// estimate, and scrollHeight shrank in lockstep with the wheel —
// negating the user's motion.
//
// We now render every turn into the DOM and let the browser handle
// virtualisation natively via `content-visibility: auto`. Off-screen
// turns skip rendering / paint until they enter the viewport. The
// scroll container has REAL heights for everything, so:
//   - scrollHeight is stable as the user scrolls
//   - scrollToBottom is a one-line `el.scrollTop = max`
//   - scrollToRow is `element.scrollIntoView`
//   - no estimation bookkeeping, no 5.2s settle loop, no abort dance
//
// This matches what claude.ai and (until recently) chatgpt do — full
// DOM render, native scrolling. The DOM cost for 100–500 turns is
// negligible on modern browsers.

import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useRef,
} from 'react'

import type { Turn } from './types'
import styles from './conversation-list.module.css'

interface ConversationListHandle {
  /** Scroll the conversation so the last turn's bottom edge sits at
   *  the viewport bottom. With real heights this is just one line. */
  scrollToBottom: (behavior?: 'auto' | 'instant' | 'smooth') => void
  /** Scroll the conversation so the row at `index` is centred (or
   *  aligned per `opts.align`). Used by Cmd+F (S018). */
  scrollToRow: (
    index: number,
    opts?: {
      align?: 'start' | 'center' | 'end' | 'auto'
      behavior?: 'auto' | 'instant' | 'smooth'
    },
  ) => void
  /** Returns the wrapping HTMLDivElement (the scroll container). */
  element(): HTMLDivElement | null
}

interface ConversationListProps {
  turns: Turn[]
  renderTurn: (turn: Turn, index: number) => React.ReactNode
  /** Notified when scroll position changes. `isUserDriven` is true
   *  when the change was triggered by a recent wheel / touchmove /
   *  keydown / mousedown. Programmatic scrolls report false. */
  onScroll?: (
    scrollTop: number,
    scrollHeight: number,
    clientHeight: number,
    isUserDriven: boolean,
  ) => void
  /** Notified the moment a wheel / touchmove / keydown / mousedown
   *  fires on the scroll container — synchronously, before the
   *  browser applies the scroll and before the resulting `scroll`
   *  event reaches `onScroll`. Used by the parent's auto-follow
   *  effect to skip a yank-to-bottom that's racing a user wheel. */
  onUserInput?: () => void
}

export const ConversationList = forwardRef<ConversationListHandle, ConversationListProps>(
  function ConversationList({ turns, renderTurn, onScroll, onUserInput }, ref) {
    const scrollRef = useRef<HTMLDivElement | null>(null)
    // Synchronous mark of "user is touching the scrollbar right now".
    // Read by the scroll listener (to tag isUserDriven) AND by
    // scrollToBottom (to skip the pin if a user gesture is in flight).
    const lastUserInputAtRef = useRef<number>(0)

    useImperativeHandle(
      ref,
      () => ({
        scrollToBottom: (behavior = 'instant') => {
          // User-input guard (falsy check on the ref because the ref
          // starts at 0 and performance.now() right after page load
          // can be < 250 ms; naïve subtraction would block the very
          // first scroll-to-bottom).
          if (
            lastUserInputAtRef.current > 0 &&
            performance.now() - lastUserInputAtRef.current < 250
          ) return
          const el = scrollRef.current
          if (!el) return
          const max = Math.max(0, el.scrollHeight - el.clientHeight)
          if (behavior === 'smooth') {
            el.scrollTo({ top: max, behavior: 'smooth' })
          } else {
            el.scrollTop = max
          }
        },
        scrollToRow: (index, opts) => {
          const el = scrollRef.current
          if (!el) return
          if (index < 0 || index >= turns.length) return
          const row = el.querySelector<HTMLElement>(
            `[data-turn-index="${index}"]`,
          )
          if (!row) return
          const align = opts?.align ?? 'center'
          row.scrollIntoView({
            behavior: opts?.behavior ?? 'smooth',
            block:
              align === 'start' ? 'start'
              : align === 'end' ? 'end'
              : align === 'auto' ? 'nearest'
              : 'center',
          })
        },
        element() {
          return scrollRef.current
        },
      }),
      [turns.length],
    )

    // Listen for user gestures + scroll events on the container. Both
    // useEffect (event-listener install) and useImperativeHandle attach
    // to the same scrollRef, which is set synchronously via the JSX ref.
    useEffect(() => {
      const el = scrollRef.current
      if (!el) return
      const markUser = () => {
        lastUserInputAtRef.current = performance.now()
        onUserInput?.()
      }
      const onScrollEvt = () => {
        if (!onScroll) return
        const isUserDriven =
          lastUserInputAtRef.current > 0 &&
          performance.now() - lastUserInputAtRef.current < 250
        onScroll(el.scrollTop, el.scrollHeight, el.clientHeight, isUserDriven)
      }
      el.addEventListener('wheel', markUser, { passive: true })
      el.addEventListener('touchmove', markUser, { passive: true })
      el.addEventListener('keydown', markUser)
      el.addEventListener('mousedown', markUser)
      el.addEventListener('scroll', onScrollEvt)
      return () => {
        el.removeEventListener('wheel', markUser)
        el.removeEventListener('touchmove', markUser)
        el.removeEventListener('keydown', markUser)
        el.removeEventListener('mousedown', markUser)
        el.removeEventListener('scroll', onScrollEvt)
      }
    }, [onScroll, onUserInput])

    return (
      <div ref={scrollRef} className={styles.scroll} role="list">
        {turns.map((turn, index) => (
          <Row
            key={turn.id}
            turn={turn}
            index={index}
            isLast={index === turns.length - 1}
            renderTurn={renderTurn}
          />
        ))}
      </div>
    )
  },
)

interface RowMemoProps {
  turn: Turn
  index: number
  isLast: boolean
  renderTurn: (turn: Turn, index: number) => React.ReactNode
}

// Lightweight row wrapper. We don't memoise across turns array
// reference changes because renderTurn closes over per-turn handlers
// that may legitimately change. Memoisation overhead would exceed the
// render saved for the typical chat workload.
const Row = function Row({ turn, index, isLast, renderTurn }: RowMemoProps) {
  return (
    <div
      className={styles.turn}
      data-turn-id={turn.id}
      data-turn-index={index}
      data-row-last={isLast ? '1' : undefined}
    >
      {renderTurn(turn, index)}
    </div>
  )
}

export type { ConversationListHandle }
