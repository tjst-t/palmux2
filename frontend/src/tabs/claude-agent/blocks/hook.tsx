/** HookBlock + HookBody — collapsible CLI hook lifecycle event (S005).
 *
 *  The CLI emits these only when --include-hook-events is on; the
 *  backend normalize layer turns hook_started / hook_response envelopes
 *  into a single kind:"hook" Block.
 *
 *  UX mirrors ToolUseBlock: the row is collapsed by default once Done,
 *  expanded while running so the user sees activity in real time. The
 *  header has a tone pip (success / warning / error) keyed off
 *  outcome + exit_code so a glance distinguishes a failed hook from a
 *  successful one without expanding.
 */
import { useEffect, useRef, useState } from 'react'

import { hookSummaryLine, hookTone } from './helpers/hook-style'
import { safeStringify } from './helpers/format'

import type { Block } from '../types'
import styles from '../blocks.module.css'

export function HookBlock({ block }: { block: Block }) {
  // Default-expanded while still running (Done=false). Once the
  // response lands and Done flips, auto-collapse to keep the timeline
  // visually compact — but track previous state so a user who manually
  // opened a finished hook isn't slammed shut on every re-render.
  const running = !block.done
  const [expanded, setExpanded] = useState(running)
  const prevDone = useRef(block.done)
  useEffect(() => {
    if (!prevDone.current && block.done) {
      setExpanded(false)
    }
    prevDone.current = block.done
  }, [block.done])

  const eventLabel = block.hookName || block.hookEvent || 'hook'
  const tone = hookTone(block)
  const summaryText = !running ? hookSummaryLine(block) : ''
  const badge = running ? 'running' : ''
  return (
    <div
      className={`${styles.toolUse} ${styles.hookBlock}`}
      data-testid="hook-block"
      data-hook-id={block.hookId ?? ''}
      data-hook-event={block.hookEvent ?? ''}
    >
      <div
        className={styles.toolHeader}
        role="button"
        tabIndex={0}
        onClick={() => setExpanded((v) => !v)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            setExpanded((v) => !v)
          }
        }}
      >
        <span className={`${styles.chevron} ${expanded ? styles.expanded : ''}`}>›</span>
        <span className={styles.toolName}>hook: {eventLabel}</span>
        {summaryText && (
          <span className={styles.toolSummary}>{summaryText}</span>
        )}
        {tone && (
          <span
            className={`${styles.toolBadge} ${tone === 'error' ? styles.error : tone === 'warning' ? styles.running : ''}`}
          >
            {tone === 'error'
              ? `exit ${block.hookExitCode ?? '?'}`
              : block.hookOutcome || 'ok'}
          </span>
        )}
        {badge && <span className={`${styles.toolBadge} ${styles.running}`}>{badge}</span>}
      </div>
      {expanded && (
        <div className={styles.toolBody}>
          <HookBody block={block} />
        </div>
      )}
    </div>
  )
}

// HookBody renders the expanded hook detail: stdout, stderr, exit_code,
// outcome, and (when present) the modified payload the hook returned.
// Each section is omitted when empty — a hook that prints nothing on
// stderr should not show an empty `STDERR` panel.
function HookBody({ block }: { block: Block }) {
  const stdout = (block.hookStdout ?? '').trim()
  const stderr = (block.hookStderr ?? '').trim()
  const payload = block.hookPayload
  const hasPayload =
    payload != null && (typeof payload !== 'object' || Object.keys(payload as object).length > 0)
  return (
    <>
      <div className={styles.hookMeta}>
        <span className={styles.hookMetaLabel}>event</span>
        <span className={styles.hookMetaValue}>{block.hookEvent ?? '—'}</span>
        <span className={styles.hookMetaLabel}>exit</span>
        <span className={styles.hookMetaValue}>
          {typeof block.hookExitCode === 'number' ? block.hookExitCode : '—'}
        </span>
        <span className={styles.hookMetaLabel}>outcome</span>
        <span className={styles.hookMetaValue}>{block.hookOutcome || '—'}</span>
      </div>
      {stdout && (
        <div>
          <div className={styles.toolLabel}>stdout</div>
          <pre className={styles.toolPre} data-testid="hook-stdout">{stdout}</pre>
        </div>
      )}
      {stderr && (
        <div>
          <div className={styles.toolLabel}>stderr</div>
          <pre
            className={`${styles.toolPre} ${styles.hookStderr}`}
            data-testid="hook-stderr"
          >{stderr}</pre>
        </div>
      )}
      {hasPayload && (
        <div>
          <div className={styles.toolLabel}>modified payload</div>
          <pre className={styles.toolPre} data-testid="hook-payload">
            {safeStringify(payload)}
          </pre>
        </div>
      )}
    </>
  )
}
