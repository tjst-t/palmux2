// Rebase / merge progress banner (S014-1-12).
//
// Polls `/git/rebase-todo` to surface "rebase in progress" + the current
// step. When merging (no rebase dir but `.git/MERGE_HEAD` exists) the
// banner falls back to status-driven detection: if there are conflicts
// and HEAD has MERGE_HEAD, render "Merging" with abort + continue
// buttons.
//
// We intentionally don't poll continuously — the GitView already
// subscribes to git.statusChanged events (S012 worktreewatch), and we
// reload via `reloadKey` whenever the parent says so. This component is
// a single-row banner; it disappears when nothing is in progress.

import { useCallback, useEffect, useState } from 'react'

import { api } from '../../lib/api'

import styles from './git-conflict.module.css'
import type { RebaseStatus } from './types'

interface Props {
  apiBase: string
  reloadKey?: number
  onAfter?: () => void
  /** Notifies the parent whether a rebase / merge is in progress so it
   *  can show / hide other actions (e.g. the Commit form). */
  onActiveChange?: (active: boolean, kind: 'rebase' | 'merge' | null) => void
}

export function GitRebaseStatus({ apiBase, reloadKey, onAfter, onActiveChange }: Props) {
  const [status, setStatus] = useState<RebaseStatus | null>(null)
  const [merging, setMerging] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [output, setOutput] = useState<string>('')
  const [pending, setPending] = useState(false)

  const reload = useCallback(async () => {
    try {
      setError(null)
      const r = await api.get<RebaseStatus>(`${apiBase}/rebase-todo`)
      setStatus(r)
      // Detect merge-in-progress via the status report (cheap: same
      // endpoint we already poll).
      try {
        const sr = await api.get<{ conflicts?: unknown[] | null }>(`${apiBase}/status`)
        // We don't have a direct "MERGE_HEAD exists" flag; conflicts
        // present without an active rebase strongly implies an in-flight
        // merge. The handler still allows merge --abort either way.
        const hasConflicts = (sr.conflicts ?? []).length > 0
        setMerging(!r.active && hasConflicts)
      } catch {
        setMerging(false)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [apiBase])

  useEffect(() => {
    void reload()
  }, [reload, reloadKey])

  useEffect(() => {
    const active = !!status?.active || merging
    const kind: 'rebase' | 'merge' | null = status?.active ? 'rebase' : merging ? 'merge' : null
    onActiveChange?.(active, kind)
  }, [status, merging, onActiveChange])

  const run = useCallback(
    async (path: string, label: string) => {
      setPending(true)
      try {
        const r = await api.post<{ output?: string }>(`${apiBase}/${path}`)
        setOutput(`${label}:\n${r?.output ?? ''}`)
        await reload()
        onAfter?.()
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err))
      } finally {
        setPending(false)
      }
    },
    [apiBase, reload, onAfter],
  )

  if (!status) return null
  if (!status.active && !merging) return null

  return (
    <div className={styles.banner} data-testid="git-rebase-status">
      <strong>{status.active ? 'Rebasing' : 'Merging'}</strong>
      {status.active && status.onto && <span className={styles.path}>onto {status.onto}</span>}
      {status.active && status.todo && (
        <span className={styles.path}>
          {status.todo.length} step(s) remaining
          {status.done && status.done.length > 0 ? ` (${status.done.length} done)` : ''}
        </span>
      )}
      <div className={styles.spacer} />
      {error && <span className={styles.error}>{error}</span>}
      <div className={styles.actions}>
        {status.active && (
          <>
            <button
              className={styles.btn}
              onClick={() => void run('rebase/continue', 'rebase --continue')}
              disabled={pending}
              data-testid="git-rebase-continue"
            >
              Continue
            </button>
            <button
              className={styles.btn}
              onClick={() => void run('rebase/skip', 'rebase --skip')}
              disabled={pending}
              data-testid="git-rebase-skip"
            >
              Skip
            </button>
            <button
              className={`${styles.btn} ${styles.btnDanger}`}
              onClick={() => void run('rebase/abort', 'rebase --abort')}
              disabled={pending}
              data-testid="git-rebase-abort"
            >
              Abort
            </button>
          </>
        )}
        {merging && (
          <>
            <button
              className={`${styles.btn} ${styles.btnDanger}`}
              onClick={() => void run('merge/abort', 'merge --abort')}
              disabled={pending}
              data-testid="git-merge-abort"
            >
              Abort merge
            </button>
          </>
        )}
      </div>
      {output && <pre className={styles.output}>{output}</pre>}
    </div>
  )
}
