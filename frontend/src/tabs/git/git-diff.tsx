import { useEffect, useState } from 'react'

import { confirmDialog } from '../../components/context-menu/confirm-dialog'
import { DiffView, type HunkActionButton } from '../../components/diff/diff-view'
import type { DiffFile, DiffHunk } from '../../components/diff/types'
import { api } from '../../lib/api'

import styles from './git-diff.module.css'

interface Props {
  apiBase: string
}

type Mode = 'working' | 'staged'

interface DiffResponse {
  mode: 'working' | 'staged'
  raw: string
  files: DiffFile[] | null
}

export function GitDiff({ apiBase }: Props) {
  const [mode, setMode] = useState<Mode>('working')
  const [resp, setResp] = useState<DiffResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<string | null>(null)

  const reload = async () => {
    try {
      setResp(await api.get<DiffResponse>(`${apiBase}/diff?mode=${mode}`))
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  useEffect(() => {
    void reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiBase, mode])

  const onHunkAction = async (file: DiffFile, hunk: DiffHunk, op: 'stage-hunk' | 'unstage-hunk' | 'discard-hunk') => {
    if (op === 'discard-hunk') {
      const ok = await confirmDialog.ask({
        title: 'Discard hunk?',
        message: 'This change will be removed from the working tree and cannot be undone.',
        confirmLabel: 'Discard',
        danger: true,
      })
      if (!ok) return
    }
    const key = `${op}:${file.newPath}:${hunk.header}`
    setPending(key)
    try {
      await api.post(`${apiBase}/${op}`, { file, hunk })
      await reload()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  const hunkActions: HunkActionButton[] =
    mode === 'staged'
      ? [{ op: 'unstage-hunk', label: 'Unstage hunk' }]
      : [
          { op: 'stage-hunk', label: 'Stage hunk' },
          { op: 'discard-hunk', label: 'Discard', danger: true },
        ]

  return (
    <div className={styles.wrap}>
      <header className={styles.toolbar}>
        <button
          className={mode === 'working' ? `${styles.modeBtn} ${styles.modeBtnActive}` : styles.modeBtn}
          onClick={() => setMode('working')}
        >
          Working
        </button>
        <button
          className={mode === 'staged' ? `${styles.modeBtn} ${styles.modeBtnActive}` : styles.modeBtn}
          onClick={() => setMode('staged')}
        >
          Staged
        </button>
      </header>
      {error && <p className={styles.error}>{error}</p>}
      {resp?.files?.length ? (
        <DiffView
          files={resp.files}
          hunkActions={hunkActions}
          pending={pending}
          onHunkAction={onHunkAction}
        />
      ) : resp ? (
        <p className={styles.empty}>No diff for {mode}.</p>
      ) : null}
    </div>
  )
}
