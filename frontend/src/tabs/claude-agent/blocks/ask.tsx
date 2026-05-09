/** AskQuestionBlock — AskUserQuestion tool render (S007). */
import { useMemo, useState } from 'react'

import type { AskOption, AskQuestion, Block } from '../types'
import { parseInputObject } from './helpers/format'
import styles from '../blocks.module.css'

/** AskHandlers wires the AskQuestionBlock to the WS layer. `onRespond`
 *  ships the chosen labels (one inner array per question) to the
 *  backend. `canRespond` is true while the question is still pending —
 *  a permission_id is registered server-side and no answer has been
 *  submitted yet. */
export interface AskHandlers {
  onRespond: (answers: string[][]) => void
  canRespond: boolean
}

// AskQuestionBlock renders an AskUserQuestion tool_use as a list of
// option buttons (or checkboxes for multiSelect). The CLI emits the
// question via the same `tool_use` envelope it uses for any other tool
// — Palmux re-tags it to kind:"ask" in normalize.go, then the
// permission_prompt MCP request that backs the tool stamps the
// permission_id onto the block once the request arrives.
export function AskQuestionBlock({ block, handlers }: { block: Block; handlers?: AskHandlers }) {
  const questions = useMemo(() => extractAskQuestions(block), [block])
  const streaming = !block.done
  const decidedAnswers = block.askAnswers
  const decided = !!decidedAnswers && decidedAnswers.length > 0
  // Local draft selections while the user is browsing options. One
  // string[] per question: chosen labels. Cleared once the answer
  // round-trips back from the server (decidedAnswers takes over).
  // Slot count tracks `questions.length` reactively without a useEffect
  // (the linter prefers we derive instead of mirroring) — when the
  // length grows mid-stream we pad with empty arrays on read.
  const [draft, setDraft] = useState<string[][]>([])
  const liveDraft = useMemo(() => {
    if (draft.length === questions.length) return draft
    const next = draft.slice(0, questions.length)
    while (next.length < questions.length) next.push([])
    return next
  }, [draft, questions.length])

  const canRespond = !decided && !streaming && (handlers?.canRespond ?? false)
  const allMultiSelect = questions.length > 0 && questions.every((q) => q.multiSelect === true)
  const allSingleSelect = questions.length > 0 && questions.every((q) => !q.multiSelect)

  const submit = (answers: string[][]) => {
    if (!handlers || !canRespond) return
    handlers.onRespond(answers)
  }

  // Single-question single-select shortcut: clicking an option submits
  // immediately. This matches the natural reading flow (questions come
  // one at a time the vast majority of the time).
  const handleSingleSelectClick = (qi: number, label: string) => {
    if (!canRespond) return
    if (questions.length === 1 && allSingleSelect) {
      submit([[label]])
      return
    }
    // Multi-question case: stash the selection in the draft and let the
    // user hit Submit when ready. (No CLI today emits multi-question
    // AskUserQuestion calls but the schema permits it.)
    setDraft((prev) => {
      const padded = padDraft(prev, questions.length)
      const next = padded.map((arr) => arr.slice())
      next[qi] = [label]
      return next
    })
  }

  const handleMultiSelectToggle = (qi: number, label: string) => {
    if (!canRespond) return
    setDraft((prev) => {
      const padded = padDraft(prev, questions.length)
      const next = padded.map((arr) => arr.slice())
      const arr = next[qi] ?? []
      const idx = arr.indexOf(label)
      if (idx >= 0) arr.splice(idx, 1)
      else arr.push(label)
      next[qi] = arr
      return next
    })
  }

  const handleSubmit = () => submit(liveDraft)

  return (
    <div className={styles.ask} data-testid="ask-question-block">
      {questions.length === 0 && streaming && (
        <div className={styles.askPending}>
          <span className={`${styles.toolBadge} ${styles.running}`}>asking</span>
          <span className={styles.askPendingText}>Preparing question…</span>
        </div>
      )}
      {questions.map((q, qi) => {
        const draftAns = liveDraft[qi] ?? []
        const decidedAns = decidedAnswers?.[qi] ?? []
        const isMulti = !!q.multiSelect
        return (
          <div key={qi} className={styles.askQuestion} data-testid={`ask-question-${qi}`}>
            <div className={styles.askHeader}>
              <span className={styles.askLabel}>Question</span>
              {streaming && <span className={`${styles.toolBadge} ${styles.running}`}>asking</span>}
              {q.header && <span className={styles.askSubheader}>{q.header}</span>}
            </div>
            <div className={styles.askPrompt}>{q.question || '…'}</div>
            <div className={styles.askOptions}>
              {q.options.map((opt: AskOption, oi) => {
                const isChosen = decided
                  ? decidedAns.includes(opt.label)
                  : draftAns.includes(opt.label)
                const isDimmed = decided && !isChosen
                const cls = [
                  styles.askOption,
                  isChosen ? styles.askOptionChosen : '',
                  isDimmed ? styles.askOptionDimmed : '',
                ].filter(Boolean).join(' ')
                if (isMulti) {
                  return (
                    <label
                      key={oi}
                      className={cls}
                      data-testid={`ask-option-${qi}-${oi}`}
                    >
                      <input
                        type="checkbox"
                        className={styles.askOptionCheckbox}
                        checked={isChosen}
                        disabled={!canRespond}
                        onChange={() => handleMultiSelectToggle(qi, opt.label)}
                      />
                      <span className={styles.askOptionBody}>
                        <span className={styles.askOptionLabel}>{opt.label}</span>
                        {opt.description && (
                          <span className={styles.askOptionDesc}>{opt.description}</span>
                        )}
                      </span>
                    </label>
                  )
                }
                return (
                  <button
                    key={oi}
                    type="button"
                    className={cls}
                    disabled={!canRespond && !isChosen}
                    onClick={() => handleSingleSelectClick(qi, opt.label)}
                    data-testid={`ask-option-${qi}-${oi}`}
                  >
                    <span className={styles.askOptionBody}>
                      <span className={styles.askOptionLabel}>{opt.label}</span>
                      {opt.description && (
                        <span className={styles.askOptionDesc}>{opt.description}</span>
                      )}
                    </span>
                  </button>
                )
              })}
            </div>
          </div>
        )
      })}
      {!decided && canRespond && (allMultiSelect || questions.length > 1) && (
        <div className={styles.askActions}>
          <button
            type="button"
            className={`${styles.askActionBtn} ${styles.askSubmit}`}
            onClick={handleSubmit}
            disabled={liveDraft.every((a) => a.length === 0)}
            data-testid="ask-submit"
          >
            Submit
          </button>
        </div>
      )}
      {decided && (
        <div className={styles.askDecision} data-testid="ask-decided">
          Answer{decidedAnswers && decidedAnswers.flat().length > 1 ? 's' : ''} sent: {' '}
          {decidedAnswers!.flat().join(', ') || '(empty)'}
        </div>
      )}
    </div>
  )
}

// padDraft grows or shrinks a draft answer array to match the current
// question count. Empty inner arrays fill any new slots.
function padDraft(prev: string[][], len: number): string[][] {
  if (prev.length === len) return prev
  const out = prev.slice(0, len)
  while (out.length < len) out.push([])
  return out
}

// extractAskQuestions tolerantly parses the AskUserQuestion input into
// a stable shape. Mirrors extractPlanText's idiom — we accept both the
// finalised `block.input` object and the partial-JSON accumulator in
// `block.text` so streaming questions render incrementally.
function extractAskQuestions(block: Block): AskQuestion[] {
  const obj = parseInputObject(block)
  const fromObj = (raw: Record<string, unknown> | null): AskQuestion[] | null => {
    if (!raw) return null
    const arr = raw.questions
    if (!Array.isArray(arr)) return null
    const out: AskQuestion[] = []
    for (const item of arr) {
      if (!item || typeof item !== 'object') continue
      const q = item as Record<string, unknown>
      const question = typeof q.question === 'string' ? q.question : ''
      const header = typeof q.header === 'string' ? q.header : undefined
      const multiSelect = q.multiSelect === true
      let options: AskOption[] = []
      if (Array.isArray(q.options)) {
        options = q.options
          .map((o: unknown) => {
            if (!o || typeof o !== 'object') return null
            const op = o as Record<string, unknown>
            if (typeof op.label !== 'string') return null
            const out: AskOption = { label: op.label }
            if (typeof op.description === 'string') out.description = op.description
            return out
          })
          .filter((x: AskOption | null): x is AskOption => x !== null)
      }
      out.push({ question, header, multiSelect, options })
    }
    return out
  }
  if (obj) {
    const parsed = fromObj(obj)
    if (parsed && parsed.length > 0) return parsed
  }
  // Streaming partial — try to parse the accumulator. Permissively
  // tolerate trailing junk by attempting JSON.parse first and falling
  // back to an empty list rather than crashing the renderer.
  const text = (block.text ?? '').trim()
  if (text.startsWith('{')) {
    try {
      const parsed = JSON.parse(text) as Record<string, unknown>
      const fromParsed = fromObj(parsed)
      if (fromParsed) return fromParsed
    } catch {
      // partial JSON — return nothing so the "Preparing question…"
      // pending state keeps showing instead of garbled text.
    }
  }
  return []
}
