// S021: dialog for the "Clean up subagent worktrees" Drawer action. The
// component is purely presentational — the parent (Drawer) owns the
// "open / closed" state and supplies the candidate list it pre-fetched
// via `dryRun=true`.
import { useEffect, useMemo, useState } from 'react'

import styles from './subagent-cleanup-dialog.module.css'

export interface CleanupCandidate {
  branchId: string
  branchName: string
  worktreePath: string
  lastCommitIso?: string
  ageDays: number
  hasLock: boolean
  isPrimary: boolean
  reason: string
}

export interface CleanupResult {
  thresholdDays: number
  candidates: CleanupCandidate[]
  removed?: { branchId: string; branchName: string; worktreePath: string }[]
  failed?: {
    branchId: string
    branchName: string
    worktreePath: string
    error?: string
  }[]
}

export function SubagentCleanupDialog({
  open,
  thresholdDays,
  candidates,
  loading,
  onClose,
  onConfirm,
}: {
  open: boolean
  thresholdDays: number
  candidates: CleanupCandidate[]
  loading: boolean
  onClose: () => void
  onConfirm: (selected: string[]) => Promise<CleanupResult | undefined>
}) {
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [submitting, setSubmitting] = useState(false)
  const [result, setResult] = useState<CleanupResult | null>(null)

  // Re-prime selection whenever the candidate list changes (e.g. user
  // re-opens the dialog after a previous run).
  useEffect(() => {
    if (!open) return
    setSelected(new Set(candidates.map((c) => c.branchName)))
    setResult(null)
  }, [open, candidates])

  const allSelected = useMemo(
    () =>
      candidates.length > 0 &&
      candidates.every((c) => selected.has(c.branchName)),
    [candidates, selected],
  )

  if (!open) return null

  const toggleAll = () => {
    if (allSelected) {
      setSelected(new Set())
    } else {
      setSelected(new Set(candidates.map((c) => c.branchName)))
    }
  }

  const toggleOne = (name: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  const handleConfirm = async () => {
    setSubmitting(true)
    try {
      const r = await onConfirm(Array.from(selected))
      if (r) setResult(r)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div
      className={styles.backdrop}
      role="dialog"
      aria-modal="true"
      aria-label="Clean up subagent worktrees"
      onClick={(e) => {
        if (e.target === e.currentTarget && !submitting) onClose()
      }}
    >
      <div className={styles.panel} data-testid="subagent-cleanup-dialog">
        <header className={styles.header}>
          <h2 className={styles.title}>Clean up subagent worktrees</h2>
          <p className={styles.subtitle}>
            Stale = no <code>.claude/autopilot-*.lock</code> AND last commit
            older than {thresholdDays} day(s). Adjust the threshold in
            Settings (<code>subagentStaleAfterDays</code>).
          </p>
        </header>

        {loading ? (
          <div className={styles.empty}>Scanning for stale worktrees…</div>
        ) : candidates.length === 0 ? (
          <div className={styles.empty} data-testid="cleanup-empty">
            No stale subagent worktrees in this repository.
          </div>
        ) : (
          <>
            <div className={styles.tableWrap}>
              <table className={styles.table}>
                <thead>
                  <tr>
                    <th className={styles.checkboxCol}>
                      <input
                        type="checkbox"
                        checked={allSelected}
                        onChange={toggleAll}
                        disabled={submitting}
                        aria-label="Select all"
                        data-testid="cleanup-select-all"
                      />
                    </th>
                    <th>Branch</th>
                    <th>Last commit</th>
                    <th>Reason</th>
                  </tr>
                </thead>
                <tbody>
                  {candidates.map((c) => {
                    const removed = result?.removed?.some(
                      (r) => r.branchName === c.branchName,
                    )
                    const failed = result?.failed?.find(
                      (r) => r.branchName === c.branchName,
                    )
                    const checked = selected.has(c.branchName)
                    return (
                      <tr
                        key={c.branchId}
                        className={
                          removed
                            ? styles.rowRemoved
                            : failed
                              ? styles.rowFailed
                              : ''
                        }
                        data-testid={`cleanup-row-${c.branchName}`}
                      >
                        <td>
                          <input
                            type="checkbox"
                            checked={checked}
                            onChange={() => toggleOne(c.branchName)}
                            disabled={submitting || !!result}
                            aria-label={`Select ${c.branchName}`}
                          />
                        </td>
                        <td>
                          <div className={styles.branchName}>{c.branchName}</div>
                          <div className={styles.branchPath}>
                            {c.worktreePath}
                          </div>
                        </td>
                        <td className={styles.ageCell}>
                          {c.ageDays} day(s) ago
                        </td>
                        <td className={styles.reasonCell}>
                          {failed ? (
                            <span className={styles.failMsg}>
                              ✗ {failed.error ?? 'failed'}
                            </span>
                          ) : removed ? (
                            <span className={styles.okMsg}>✓ removed</span>
                          ) : (
                            c.reason
                          )}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          </>
        )}

        <footer className={styles.footer}>
          {result ? (
            <>
              <span className={styles.summary}>
                {result.removed?.length ?? 0} removed,{' '}
                {result.failed?.length ?? 0} failed
              </span>
              <button
                className={styles.btnPrimary}
                onClick={onClose}
                data-testid="cleanup-close"
              >
                Close
              </button>
            </>
          ) : (
            <>
              <button
                className={styles.btnSecondary}
                onClick={onClose}
                disabled={submitting}
                data-testid="cleanup-cancel"
              >
                Cancel
              </button>
              <button
                className={styles.btnDanger}
                onClick={handleConfirm}
                disabled={
                  submitting || candidates.length === 0 || selected.size === 0
                }
                data-testid="cleanup-confirm"
              >
                {submitting
                  ? 'Removing…'
                  : `Remove ${selected.size} worktree${selected.size === 1 ? '' : 's'}`}
              </button>
            </>
          )}
        </footer>
      </div>
    </div>
  )
}
