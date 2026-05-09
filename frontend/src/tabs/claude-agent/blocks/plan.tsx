/** PlanBlock + PlanEditDialog — ExitPlanMode plan presentation (S001).
 *
 *  PlanBlock renders an ExitPlanMode block. The CLI emits the plan via the
 *  same `tool_use` envelope it uses for any other tool — Palmux re-tags it
 *  to kind:"plan" in normalize.go so the frontend can present the plan as
 *  authored content (Markdown) rather than a tool input dump.
 */
import { useEffect, useMemo, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

import { extractPlanText, firstNonBlankLine } from './helpers/plan-parse'

import type { Block } from '../types'
import styles from '../blocks.module.css'

export interface PlanHandlers {
  /** Approve the plan, optionally with edits. `targetMode` is the
   *  permission mode the agent should switch to after the plan is
   *  approved (default "auto"). `editedPlan` carries the user's edited
   *  markdown when non-empty — the backend ships it as updatedInput.plan
   *  so the executing CLI sees the user's changes. */
  onApprove: (targetMode: string, editedPlan?: string) => void
  /** Reject — keep the agent in plan mode. */
  onReject: () => void
  /** Whether this plan is the live one and not yet decided. When
   *  false the action row hides entirely (read-only past plans, or
   *  already-decided plans). */
  canActOnPlan: boolean
  /** Optimistic decision indicator. Set to "approved"/"rejected" the
   *  moment the user clicks an action; the server's plan.decided
   *  echo is what makes it durable. */
  decided?: 'approved' | 'rejected'
  /** Available permission modes from the CLI probe. Used to populate
   *  the mode dropdown next to Approve. */
  modes: string[]
  /** Default mode (typically "auto") used as the dropdown's initial
   *  value. Falls back to "auto" if missing. */
  defaultMode: string
  /** When the plan is approved, this is the mode that was chosen.
   *  Used to render the post-approval status text. */
  targetMode?: string
}

export function PlanBlock({ block, handlers }: { block: Block; handlers?: PlanHandlers }) {
  const planText = useMemo(() => extractPlanText(block), [block])
  const streaming = !block.done
  const [expanded, setExpanded] = useState(true)
  const [editing, setEditing] = useState(false)
  const [editDraft, setEditDraft] = useState('')
  const previewLine = useMemo(() => firstNonBlankLine(planText), [planText])

  // Decision state: optimistic UI from handlers.decided, durable state
  // from block.planDecision (replayed from the snapshot on reload).
  const decision = handlers?.decided ?? block.planDecision
  const targetMode = handlers?.targetMode ?? block.planTargetMode
  // Hide the action row while the block is still drafting — the plan
  // body hasn't fully streamed yet, so showing Approve / Edit / Keep
  // planning would let the user act on a fragment. Once `block.done`,
  // the natural reading order is: header → body → action row.
  const showActions = !!handlers && handlers.canActOnPlan && !decision && block.done

  const fallbackModes = ['default', 'auto', 'acceptEdits', 'bypassPermissions']
  const allModes = handlers?.modes && handlers.modes.length > 0
    ? handlers.modes.filter((m) => m !== 'plan')
    : fallbackModes
  const initialMode = useMemo(() => {
    if (handlers?.defaultMode && allModes.includes(handlers.defaultMode)) {
      return handlers.defaultMode
    }
    if (allModes.includes('auto')) return 'auto'
    return allModes[0] ?? 'auto'
  }, [handlers?.defaultMode, allModes])
  const [selectedMode, setSelectedMode] = useState<string>(initialMode)
  // If the modes list arrives after first render (modes API is async),
  // re-pick the initial mode once.
  useEffect(() => {
    if (selectedMode && allModes.includes(selectedMode)) return
    setSelectedMode(initialMode)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialMode])

  const decisionLabel = decision === 'approved'
    ? `Approved — switching to ${targetMode || 'execution'} mode`
    : decision === 'rejected'
      ? 'Staying in plan mode'
      : ''

  const openEdit = () => {
    setEditDraft(planText)
    setEditing(true)
  }
  const cancelEdit = () => setEditing(false)
  const saveAndApprove = () => {
    if (!handlers) return
    handlers.onApprove(selectedMode, editDraft)
    setEditing(false)
  }

  return (
    <div className={styles.plan} data-testid="plan-block">
      <div
        className={styles.planHeader}
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
        <span className={styles.planLabel}>Plan</span>
        {streaming && <span className={`${styles.toolBadge} ${styles.running}`}>drafting</span>}
        {!expanded && previewLine && (
          <span className={styles.planPreview}>{previewLine}</span>
        )}
      </div>
      {(expanded || streaming) && planText && (
        <div className={styles.planBody}>
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{planText}</ReactMarkdown>
        </div>
      )}
      {showActions && (
        <div className={styles.planActions} data-testid="plan-actions">
          <div className={styles.planApproveGroup}>
            <button
              type="button"
              className={`${styles.planActionBtn} ${styles.planApprove}`}
              onClick={() => handlers!.onApprove(selectedMode)}
              title={`Approve the plan and switch to ${selectedMode}`}
              data-testid="plan-approve"
            >
              Approve
            </button>
            <select
              className={`${styles.planModeSelect} ${selectedMode === 'bypassPermissions' ? styles.planModeWarning : ''}`}
              value={selectedMode}
              onChange={(e) => setSelectedMode(e.target.value)}
              title="Permission mode to switch to after approval"
              data-testid="plan-mode-select"
            >
              {allModes.map((m) => (
                <option key={m} value={m}>
                  {m === 'bypassPermissions' ? `! ${m}` : m}
                </option>
              ))}
            </select>
          </div>
          <button
            type="button"
            className={styles.planActionBtn}
            onClick={openEdit}
            title="Edit the plan markdown before approving"
            data-testid="plan-edit"
          >
            Edit plan…
          </button>
          <button
            type="button"
            className={`${styles.planActionBtn} ${styles.planReject}`}
            onClick={handlers!.onReject}
            title="Decline and keep the agent in plan mode"
            data-testid="plan-reject"
          >
            Keep planning
          </button>
        </div>
      )}
      {decisionLabel && <div className={styles.planDecision} data-testid="plan-decided">{decisionLabel}</div>}
      {editing && (
        <PlanEditDialog
          initialText={editDraft}
          mode={selectedMode}
          modes={allModes}
          onModeChange={setSelectedMode}
          onChange={setEditDraft}
          onCancel={cancelEdit}
          onSubmit={saveAndApprove}
        />
      )}
    </div>
  )
}

// PlanEditDialog is a focused-state Markdown editor for the plan. We
// expose a mode dropdown here too so a power user can edit the plan
// AND change the target mode in a single round-trip.
export function PlanEditDialog({
  initialText,
  mode,
  modes,
  onModeChange,
  onChange,
  onCancel,
  onSubmit,
}: {
  initialText: string
  mode: string
  modes: string[]
  onModeChange: (m: string) => void
  onChange: (s: string) => void
  onCancel: () => void
  onSubmit: () => void
}) {
  const [draft, setDraft] = useState(initialText)
  useEffect(() => {
    onChange(draft)
  }, [draft, onChange])
  // Cmd/Ctrl+Enter submits, Esc cancels — keyboard-first behaviour
  // matches the rest of the Claude tab (Composer + AskQuestion).
  const onKey = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
      e.preventDefault()
      onSubmit()
    } else if (e.key === 'Escape') {
      e.preventDefault()
      onCancel()
    }
  }
  return (
    <div className={styles.planEditOverlay} role="dialog" aria-label="Edit plan" data-testid="plan-edit-dialog">
      <div className={styles.planEditCard}>
        <div className={styles.planEditHeader}>
          <span className={styles.planEditTitle}>Edit plan</span>
          <select
            className={`${styles.planModeSelect} ${mode === 'bypassPermissions' ? styles.planModeWarning : ''}`}
            value={mode}
            onChange={(e) => onModeChange(e.target.value)}
            title="Permission mode to switch to after approval"
            data-testid="plan-edit-mode"
          >
            {modes.map((m) => (
              <option key={m} value={m}>
                {m === 'bypassPermissions' ? `! ${m}` : m}
              </option>
            ))}
          </select>
        </div>
        <textarea
          className={styles.planEditArea}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={onKey}
          spellCheck={false}
          autoFocus
          data-testid="plan-edit-textarea"
        />
        <div className={styles.planEditActions}>
          <button type="button" className={styles.planActionBtn} onClick={onCancel} data-testid="plan-edit-cancel">
            Cancel
          </button>
          <button
            type="button"
            className={`${styles.planActionBtn} ${styles.planApprove}`}
            onClick={onSubmit}
            data-testid="plan-edit-submit"
          >
            Save & approve
          </button>
        </div>
      </div>
    </div>
  )
}
