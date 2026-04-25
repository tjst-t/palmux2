import { useEffect, useState } from 'react'

import { api } from '../../lib/api'

import styles from './git-diff.module.css'
import type { DiffFile, DiffHunk, DiffResponse } from './types'

interface Props {
  apiBase: string
}

type Mode = 'working' | 'staged'

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
    if (op === 'discard-hunk' && !confirm('Discard this hunk? This cannot be undone.')) return
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
        resp.files.map((file, fi) => (
          <FileBlock
            key={file.newPath || file.oldPath || fi}
            file={file}
            mode={mode}
            pending={pending}
            onHunkAction={onHunkAction}
          />
        ))
      ) : resp ? (
        <p className={styles.empty}>No diff for {mode}.</p>
      ) : null}
    </div>
  )
}

function FileBlock({
  file,
  mode,
  pending,
  onHunkAction,
}: {
  file: DiffFile
  mode: Mode
  pending: string | null
  onHunkAction: (file: DiffFile, hunk: DiffHunk, op: 'stage-hunk' | 'unstage-hunk' | 'discard-hunk') => void
}) {
  return (
    <section className={styles.file}>
      <header className={styles.fileHeader}>
        <span>{file.newPath || file.oldPath}</span>
        {file.isBinary && <span className={styles.binTag}>binary</span>}
      </header>
      {!file.isBinary && file.hunks?.map((h, hi) => {
        const op: 'stage-hunk' | 'unstage-hunk' = mode === 'staged' ? 'unstage-hunk' : 'stage-hunk'
        const opLabel = mode === 'staged' ? 'Unstage hunk' : 'Stage hunk'
        return (
          <div key={hi} className={styles.hunk}>
            <div className={styles.hunkHeader}>
              <span>{h.header}</span>
              <div className={styles.hunkActions}>
                <button
                  className={styles.btn}
                  disabled={!!pending}
                  onClick={() => onHunkAction(file, h, op)}
                >
                  {opLabel}
                </button>
                {mode === 'working' && (
                  <button
                    className={`${styles.btn} ${styles.danger}`}
                    disabled={!!pending}
                    onClick={() => onHunkAction(file, h, 'discard-hunk')}
                  >
                    Discard
                  </button>
                )}
              </div>
            </div>
            <pre className={styles.lines}>
              {h.lines.map((ln, li) => (
                <span
                  key={li}
                  className={
                    ln.kind === 'add'
                      ? styles.add
                      : ln.kind === 'del'
                        ? styles.del
                        : ln.kind === 'meta'
                          ? styles.meta
                          : styles.context
                  }
                >
                  {ln.kind === 'add' ? '+' : ln.kind === 'del' ? '-' : ' '}
                  {ln.text}
                  {'\n'}
                </span>
              ))}
            </pre>
          </div>
        )
      })}
    </section>
  )
}
