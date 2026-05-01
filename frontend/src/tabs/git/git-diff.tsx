// GitDiff — diff view with two presentations:
//
//   1. Hunk view (default): the existing structured DiffView component
//      that supports per-hunk Stage / Unstage / Discard buttons.
//   2. Monaco view: full side-by-side or inline syntax-highlighted diff
//      with line-range staging (S012-1-10, S012-1-11).
//
// On viewports < 900px the Monaco view is forced into inline mode
// (S012-1-18 mobile parity). Selecting a file from GitStatus deep-links
// to its path here via `initialPath`.

import { useEffect, useMemo, useState } from 'react'

import { confirmDialog } from '../../components/context-menu/confirm-dialog'
import { DiffView, type HunkActionButton } from '../../components/diff/diff-view'
import type { DiffFile, DiffHunk } from '../../components/diff/types'
import { api } from '../../lib/api'

import { GitMonacoDiff } from './git-monaco-diff'
import styles from './git-diff.module.css'

interface Props {
  apiBase: string
  /** Path selected from GitStatus, deep-linked into the diff view. */
  initialPath?: string
}

type Mode = 'working' | 'staged'
type Presentation = 'hunks' | 'monaco'

interface DiffResponse {
  mode: 'working' | 'staged'
  raw: string
  files: DiffFile[] | null
}

const useUnifiedForMobile = () => {
  const [unified, setUnified] = useState<boolean>(() =>
    typeof window !== 'undefined' ? window.matchMedia('(max-width: 899px)').matches : false,
  )
  useEffect(() => {
    const mq = window.matchMedia('(max-width: 899px)')
    const fn = () => setUnified(mq.matches)
    mq.addEventListener('change', fn)
    return () => mq.removeEventListener('change', fn)
  }, [])
  return unified
}

export function GitDiff({ apiBase, initialPath }: Props) {
  const [mode, setMode] = useState<Mode>('working')
  const [presentation, setPresentation] = useState<Presentation>('hunks')
  const [resp, setResp] = useState<DiffResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<string | null>(null)
  const [selectedPath, setSelectedPath] = useState<string | undefined>(initialPath)
  const [reloadKey, setReloadKey] = useState(0)
  const mobileUnified = useUnifiedForMobile()

  // Re-sync when the parent passes a new initialPath (Status → Diff
  // jump).
  useEffect(() => {
    if (initialPath) setSelectedPath(initialPath)
  }, [initialPath])

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
  }, [apiBase, mode, reloadKey])

  const onHunkAction = async (
    file: DiffFile,
    hunk: DiffHunk,
    op: 'stage-hunk' | 'unstage-hunk' | 'discard-hunk',
  ) => {
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
      setReloadKey((k) => k + 1)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  const hunkActions: HunkActionButton[] = useMemo(
    () =>
      mode === 'staged'
        ? [{ op: 'unstage-hunk', label: 'Unstage hunk' }]
        : [
            { op: 'stage-hunk', label: 'Stage hunk' },
            { op: 'discard-hunk', label: 'Discard', danger: true },
          ],
    [mode],
  )

  // The list of files used both as a sidebar (so the user can pick a
  // different file in Monaco mode) and to render Hunk view.
  const files = resp?.files ?? []

  return (
    <div className={styles.wrap}>
      <header className={styles.toolbar}>
        <div className={styles.modeGroup}>
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
        </div>
        <div className={styles.modeGroup}>
          <button
            className={
              presentation === 'hunks'
                ? `${styles.modeBtn} ${styles.modeBtnActive}`
                : styles.modeBtn
            }
            onClick={() => setPresentation('hunks')}
          >
            Hunks
          </button>
          <button
            className={
              presentation === 'monaco'
                ? `${styles.modeBtn} ${styles.modeBtnActive}`
                : styles.modeBtn
            }
            onClick={() => setPresentation('monaco')}
            data-testid="git-diff-monaco-toggle"
          >
            Monaco
          </button>
        </div>
      </header>
      {error && <p className={styles.error}>{error}</p>}
      {presentation === 'hunks' ? (
        files.length ? (
          <DiffView
            files={files}
            hunkActions={hunkActions}
            pending={pending}
            onHunkAction={onHunkAction}
          />
        ) : resp ? (
          <p className={styles.empty}>No diff for {mode}.</p>
        ) : null
      ) : files.length ? (
        <div className={styles.monacoLayout}>
          <aside className={styles.fileList}>
            {files.map((f) => {
              const path = f.newPath || f.oldPath
              return (
                <button
                  key={path}
                  className={
                    selectedPath === path
                      ? `${styles.fileBtn} ${styles.fileBtnActive}`
                      : styles.fileBtn
                  }
                  onClick={() => setSelectedPath(path)}
                >
                  {path}
                </button>
              )
            })}
          </aside>
          <div className={styles.monacoBody}>
            {selectedPath ? (
              <GitMonacoDiff
                apiBase={apiBase}
                path={selectedPath}
                unified={mobileUnified}
                reloadKey={reloadKey}
                onStaged={() => setReloadKey((k) => k + 1)}
              />
            ) : (
              <p className={styles.empty}>Select a file on the left.</p>
            )}
          </div>
        </div>
      ) : resp ? (
        <p className={styles.empty}>No diff for {mode}.</p>
      ) : null}
    </div>
  )
}
