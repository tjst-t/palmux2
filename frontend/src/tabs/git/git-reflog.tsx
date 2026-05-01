// Reflog viewer (S014-1-14).
//
// Lists the most recent 100 HEAD movements. Each row exposes "Reset to
// here" which calls POST /git/reset with mode=hard to rescue commits
// orphaned by a bad rebase / reset. The orphan-rescue UX matches
// `git reflog` + `git reset --hard <ref>` muscle memory.

import { useCallback, useEffect, useState } from 'react'

import { confirmDialog } from '../../components/context-menu/confirm-dialog'
import { api } from '../../lib/api'

import styles from './git-conflict.module.css'
import type { ReflogEntry } from './types'

interface Props {
  apiBase: string
  reloadKey?: number
  onAfter?: () => void
}

export function GitReflog({ apiBase, reloadKey, onAfter }: Props) {
  const [entries, setEntries] = useState<ReflogEntry[]>([])
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<string | null>(null)
  const [output, setOutput] = useState<string>('')

  const reload = useCallback(async () => {
    try {
      setError(null)
      const r = await api.get<ReflogEntry[] | null>(`${apiBase}/reflog?limit=100`)
      setEntries(r ?? [])
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [apiBase])

  useEffect(() => {
    void reload()
  }, [reload, reloadKey])

  const resetHere = async (e: ReflogEntry) => {
    const ok = await confirmDialog.ask({
      title: 'Reset HEAD to this entry?',
      message: `This runs git reset --hard ${e.hash.slice(0, 8)} (${e.action}: ${e.message}). Uncommitted changes will be lost.`,
      confirmLabel: 'Reset (hard)',
      danger: true,
    })
    if (!ok) return
    setPending(e.ref)
    setError(null)
    try {
      const r = await api.post<{ output?: string }>(`${apiBase}/reset`, {
        commitSha: e.hash,
        mode: 'hard',
      })
      setOutput(`reset --hard ${e.hash.slice(0, 8)}:\n${r?.output ?? ''}`)
      await reload()
      onAfter?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  if (entries.length === 0) {
    return (
      <div className={styles.wrap} data-testid="git-reflog">
        {error && <div className={styles.error}>{error}</div>}
        <p className={styles.empty}>Reflog empty.</p>
      </div>
    )
  }

  return (
    <div className={styles.wrap} data-testid="git-reflog">
      {error && <div className={styles.error}>{error}</div>}
      <ul className={styles.fileList}>
        {entries.map((e) => (
          <li
            key={e.ref}
            className={styles.fileItem}
            data-testid={`git-reflog-row-${e.ref}`}
          >
            <span className={styles.path} style={{ minWidth: 80 }}>
              {e.ref}
            </span>
            <span className={styles.path} style={{ minWidth: 70 }}>
              {e.hash.slice(0, 8)}
            </span>
            <span className={styles.path} style={{ minWidth: 60 }}>
              {e.action}
            </span>
            <span className={styles.path} style={{ flex: 1 }}>
              {e.message}
            </span>
            <button
              className={styles.hunkBtn}
              onClick={() => void resetHere(e)}
              disabled={pending === e.ref}
              data-testid={`git-reflog-reset-${e.ref}`}
              title="Reset HEAD --hard to this entry"
            >
              Reset to here
            </button>
          </li>
        ))}
      </ul>
      {output && <pre className={styles.output}>{output}</pre>}
    </div>
  )
}
