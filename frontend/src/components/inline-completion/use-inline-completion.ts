// useInlineCompletion is a small headless primitive for textarea-driven
// completion popups (slash commands, @-mentions, etc.). Given the current
// `text` and `cursor`, it figures out which trigger is active, extracts
// the query string, asks each trigger's `fetchOptions` for candidates,
// and exposes:
//
//   - state.options       — the current candidate list
//   - state.activeTrigger — which trigger fired (or null = no popup)
//   - state.selected      — current keyboard-highlighted index
//   - actions.handleKey() — call from textarea onKeyDown for ↑↓/Enter/Esc
//   - actions.applyOption(opt, text, cursor) → { newText, newCursor }
//
// The component using this primitive owns the textarea and the popup
// rendering; this hook only coordinates state.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

export interface CompletionOption {
  /** Stable id (used for React keys, dedupe). */
  id: string
  /** Visible main label (typically what gets inserted). */
  label: string
  /** Optional secondary line. */
  detail?: string
  /** Override the inserted string — defaults to label. */
  insertText?: string
}

export interface CompletionTrigger {
  /** The single character that opens this completion. */
  char: string
  /** Human-readable name (rendered as the popup heading). */
  name: string
  /** Async fetch hook. Return the candidates for the given query. */
  fetchOptions: (query: string, signal: AbortSignal) => Promise<CompletionOption[]>
  /** Whether the trigger fires when char appears mid-word. Default false. */
  matchMidWord?: boolean
  /** Optional: additional non-word character class for the boundary. */
  charsAfterTrigger?: RegExp
}

export interface InlineCompletionState {
  activeTrigger: CompletionTrigger | null
  query: string
  /** Index in `text` where the trigger char sits (just before query). */
  triggerIndex: number
  options: CompletionOption[]
  selected: number
  loading: boolean
}

const initialState: InlineCompletionState = {
  activeTrigger: null,
  query: '',
  triggerIndex: -1,
  options: [],
  selected: 0,
  loading: false,
}

export interface UseInlineCompletionResult {
  state: InlineCompletionState
  /** Notify the hook that text/cursor changed. Call from textarea onChange. */
  update: (text: string, cursor: number) => void
  /** Apply the given option (or current selection if omitted). */
  apply: (
    text: string,
    cursor: number,
    option?: CompletionOption,
  ) => { text: string; cursor: number } | null
  /** Hook into textarea onKeyDown for ↑↓/Enter/Tab/Esc. */
  handleKey: (e: React.KeyboardEvent<HTMLTextAreaElement>) => boolean
  /** Force-close the popup without applying. */
  cancel: () => void
}

export function useInlineCompletion(triggers: CompletionTrigger[]): UseInlineCompletionResult {
  const [state, setState] = useState<InlineCompletionState>(initialState)
  const fetchSeq = useRef(0)
  const abortRef = useRef<AbortController | null>(null)

  const cancel = useCallback(() => {
    abortRef.current?.abort()
    setState(initialState)
  }, [])

  const update = useCallback(
    (text: string, cursor: number) => {
      const detected = detectTrigger(text, cursor, triggers)
      if (!detected) {
        if (state.activeTrigger) cancel()
        return
      }
      const { trigger, query, triggerIndex } = detected
      // If just the query changed, keep the existing options & selection
      // until the new fetch resolves.
      setState((s) => ({
        ...s,
        activeTrigger: trigger,
        query,
        triggerIndex,
        loading: true,
      }))
      const seq = ++fetchSeq.current
      abortRef.current?.abort()
      const ctrl = new AbortController()
      abortRef.current = ctrl
      void trigger
        .fetchOptions(query, ctrl.signal)
        .then((options) => {
          if (seq !== fetchSeq.current) return
          setState((s) => ({
            ...s,
            options,
            selected: clamp(s.selected, 0, Math.max(0, options.length - 1)),
            loading: false,
          }))
        })
        .catch(() => {
          if (seq !== fetchSeq.current) return
          setState((s) => ({ ...s, options: [], loading: false }))
        })
    },
    // triggers reference is allowed to change identity per render; we
    // re-run on every update call regardless. Keep the effect minimal.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [triggers, state.activeTrigger, cancel],
  )

  const apply = useCallback(
    (text: string, cursor: number, option?: CompletionOption) => {
      const opt = option ?? state.options[state.selected]
      if (!opt || !state.activeTrigger || state.triggerIndex < 0) return null
      // Replace from the trigger character (inclusive) up to the current
      // cursor. If insertText starts with the trigger char, it provides
      // its own copy; otherwise we keep the original trigger before the
      // insertion. This handles both
      //   '/' typed → option.insertText='/clear '   (replaces the '/')
      //   '@' typed → option.insertText='@path '    (replaces the '@')
      // without doubling the trigger character.
      const trigger = state.activeTrigger.char
      const insert = opt.insertText ?? opt.label
      const triggerIndex = state.triggerIndex
      const insertStartsWithTrigger = insert.startsWith(trigger)
      const replaceFrom = insertStartsWithTrigger ? triggerIndex : triggerIndex + 1
      const before = text.slice(0, replaceFrom)
      const after = text.slice(cursor)
      const newText = before + insert + after
      const newCursor = before.length + insert.length
      cancel()
      return { text: newText, cursor: newCursor }
    },
    [state.options, state.selected, state.activeTrigger, state.triggerIndex, cancel],
  )

  const handleKey = useCallback(
    (e: React.KeyboardEvent<HTMLTextAreaElement>): boolean => {
      if (!state.activeTrigger || state.options.length === 0) return false
      switch (e.key) {
        case 'ArrowDown':
          e.preventDefault()
          setState((s) => ({ ...s, selected: clamp(s.selected + 1, 0, s.options.length - 1) }))
          return true
        case 'ArrowUp':
          e.preventDefault()
          setState((s) => ({ ...s, selected: clamp(s.selected - 1, 0, s.options.length - 1) }))
          return true
        case 'Enter':
        case 'Tab':
          // The actual application is handled by the caller (which has
          // access to the textarea ref). We just signal "we want to
          // intercept this".
          return true
        case 'Escape':
          e.preventDefault()
          cancel()
          return true
      }
      return false
    },
    [state.activeTrigger, state.options.length, cancel],
  )

  // On unmount: cancel inflight fetches.
  useEffect(() => () => abortRef.current?.abort(), [])

  return useMemo(
    () => ({ state, update, apply, handleKey, cancel }),
    [state, update, apply, handleKey, cancel],
  )
}

interface DetectedTrigger {
  trigger: CompletionTrigger
  query: string
  /** Index in text of the trigger char itself. */
  triggerIndex: number
}

function detectTrigger(
  text: string,
  cursor: number,
  triggers: CompletionTrigger[],
): DetectedTrigger | null {
  // Walk back from cursor looking for a trigger char on a word boundary.
  // Stop at whitespace, newline, or quote.
  for (let i = cursor - 1; i >= 0; i--) {
    const ch = text[i]
    if (ch === '\n' || ch === ' ' || ch === '\t' || ch === '"' || ch === "'" || ch === '`') {
      return null
    }
    const trig = triggers.find((t) => t.char === ch)
    if (!trig) continue
    // Boundary check: the char before must be whitespace/newline/start
    // of string (unless matchMidWord is set).
    if (!trig.matchMidWord) {
      const prev = i > 0 ? text[i - 1] : ''
      if (prev !== '' && !/\s/.test(prev)) return null
    }
    const query = text.slice(i + 1, cursor)
    return { trigger: trig, query, triggerIndex: i }
  }
  return null
}

function clamp(n: number, lo: number, hi: number): number {
  if (hi < lo) return lo
  if (n < lo) return lo
  if (n > hi) return hi
  return n
}
