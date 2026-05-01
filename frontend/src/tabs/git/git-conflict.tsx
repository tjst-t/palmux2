// 3-way merge conflict resolver (S014-1-10).
//
// Layout (desktop): three panes — Ours / Result (editable) / Theirs.
// On viewports < 900px the panes collapse into a tab strip so all three
// surfaces remain reachable on mobile.
//
// Per-hunk action bar lets the user accept-current / accept-incoming /
// accept-both. Manual edits go straight into the merged textarea. After
// the user is happy with the merged buffer they click "Mark resolved" to
// `git add` the file. Once *every* conflicting file is resolved, the
// parent surfaces a "Continue rebase / merge" button via
// rebase-status.tsx.

import { useCallback, useEffect, useState } from 'react'

import { api } from '../../lib/api'

import styles from './git-conflict.module.css'
import type { ConflictBody, ConflictFile, ConflictHunk } from './types'

interface Props {
  apiBase: string
  /** Called whenever a file's "Mark resolved" succeeds so the parent can
   *  refresh the conflict list. */
  onResolved?: () => void
  reloadKey?: number
}

export function GitConflict({ apiBase, onResolved, reloadKey }: Props) {
  const [files, setFiles] = useState<ConflictFile[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [body, setBody] = useState<ConflictBody | null>(null)
  const [hunks, setHunks] = useState<ConflictHunk[]>([])
  const [draft, setDraft] = useState<string>('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [mobileTab, setMobileTab] = useState<'ours' | 'merged' | 'theirs'>('merged')

  const loadList = useCallback(async () => {
    try {
      setError(null)
      const r = await api.get<{ files: ConflictFile[] | null }>(`${apiBase}/conflicts`)
      const list = r.files ?? []
      setFiles(list)
      if (selected && !list.find((f) => f.path === selected)) {
        setSelected(list[0]?.path ?? null)
      } else if (!selected && list.length > 0) {
        setSelected(list[0].path)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [apiBase, selected])

  useEffect(() => {
    void loadList()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reloadKey, apiBase])

  const loadFile = useCallback(
    async (path: string) => {
      try {
        setError(null)
        const r = await api.get<ConflictBody>(
          `${apiBase}/conflict-file?path=${encodeURIComponent(path)}`,
        )
        setBody(r)
        setDraft(r.merged)
        // Re-parse the merged buffer to surface hunk action bar.
        const parsed = parseConflictMarkers(r.merged)
        setHunks(parsed)
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err))
      }
    },
    [apiBase],
  )

  useEffect(() => {
    if (selected) void loadFile(selected)
    else {
      setBody(null)
      setHunks([])
      setDraft('')
    }
  }, [selected, loadFile])

  // Re-parse hunks whenever the user edits the merged buffer.
  useEffect(() => {
    setHunks(parseConflictMarkers(draft))
  }, [draft])

  const acceptHunk = (hunk: ConflictHunk, side: 'ours' | 'theirs' | 'both') => {
    const lines = draft.split('\n')
    // hunk.startLine / endLine are 1-based, inclusive of <<<<<<< / >>>>>>>.
    const start = hunk.startLine - 1
    const end = hunk.endLine
    let replacement: string[] = []
    if (side === 'ours') replacement = hunk.ours
    else if (side === 'theirs') replacement = hunk.theirs
    else replacement = [...hunk.ours, ...hunk.theirs]
    const next = [...lines.slice(0, start), ...replacement, ...lines.slice(end)]
    setDraft(next.join('\n'))
  }

  const save = useCallback(async () => {
    if (!selected) return
    setPending(true)
    try {
      await api.put(`${apiBase}/conflict-file?path=${encodeURIComponent(selected)}`, {
        content: draft,
      })
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(false)
    }
  }, [apiBase, selected, draft])

  const markResolved = useCallback(async () => {
    if (!selected) return
    setPending(true)
    try {
      // Save first (so the latest edits are recorded), then mark.
      await api.put(`${apiBase}/conflict-file?path=${encodeURIComponent(selected)}`, {
        content: draft,
      })
      await api.post(
        `${apiBase}/conflict-file/mark-resolved?path=${encodeURIComponent(selected)}`,
      )
      onResolved?.()
      await loadList()
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(false)
    }
  }, [apiBase, selected, draft, onResolved, loadList])

  const stillConflicted = hunks.length > 0

  if (!files.length) {
    return (
      <div className={styles.wrap}>
        <p className={styles.empty}>No conflicts to resolve.</p>
      </div>
    )
  }

  return (
    <div className={styles.wrap} data-testid="git-conflict">
      <header className={styles.header}>
        <h3 className={styles.title}>Resolve conflicts</h3>
        <span className={styles.path}>{files.length} file(s)</span>
        <div className={styles.spacer} />
      </header>
      <div style={{ display: 'flex', gap: 'var(--space-3)', flex: 1, minHeight: 0 }}>
        <ul className={styles.fileList} style={{ minWidth: 200, flex: '0 0 auto', overflow: 'auto' }}>
          {files.map((f) => (
            <li
              key={f.path}
              className={`${styles.fileItem} ${selected === f.path ? styles.fileSelected : ''}`}
              onClick={() => setSelected(f.path)}
              data-testid={`git-conflict-file-${f.path}`}
            >
              <span className={styles.path}>{f.path}</span>
            </li>
          ))}
        </ul>
        <div style={{ display: 'flex', flexDirection: 'column', flex: 1, minWidth: 0 }}>
          {error && <div className={`${styles.banner} ${styles.bannerError}`}>{error}</div>}
          {body && (
            <>
              <div className={styles.mobileTabs}>
                <button
                  className={`${styles.mobileTab} ${mobileTab === 'ours' ? styles.mobileTabActive : ''}`}
                  onClick={() => setMobileTab('ours')}
                >
                  Ours
                </button>
                <button
                  className={`${styles.mobileTab} ${mobileTab === 'merged' ? styles.mobileTabActive : ''}`}
                  onClick={() => setMobileTab('merged')}
                >
                  Result
                </button>
                <button
                  className={`${styles.mobileTab} ${mobileTab === 'theirs' ? styles.mobileTabActive : ''}`}
                  onClick={() => setMobileTab('theirs')}
                >
                  Theirs
                </button>
              </div>
              <div className={styles.panes}>
                <div className={`${styles.pane} ${onMobileHide(mobileTab, 'ours')}`}>
                  <div className={styles.paneHeader}>
                    <span className={styles.paneTitle}>Ours (HEAD)</span>
                  </div>
                  <pre className={styles.paneBody}>{body.ours}</pre>
                </div>
                <div className={`${styles.pane} ${onMobileHide(mobileTab, 'merged')}`}>
                  <div className={styles.paneHeader}>
                    <span className={styles.paneTitle}>Result (working tree)</span>
                  </div>
                  <textarea
                    className={styles.merged}
                    value={draft}
                    onChange={(e) => setDraft(e.target.value)}
                    spellCheck={false}
                    data-testid="git-conflict-merged"
                  />
                </div>
                <div className={`${styles.pane} ${onMobileHide(mobileTab, 'theirs')}`}>
                  <div className={styles.paneHeader}>
                    <span className={styles.paneTitle}>Theirs</span>
                  </div>
                  <pre className={styles.paneBody}>{body.theirs}</pre>
                </div>
              </div>
              {hunks.length > 0 && (
                <div className={styles.hunkBar}>
                  {hunks.map((h, idx) => (
                    <div key={idx} className={styles.hunkRow}>
                      <span className={styles.hunkLabel}>
                        Hunk {idx + 1} (line {h.startLine}-{h.endLine})
                      </span>
                      <button
                        className={styles.hunkBtn}
                        onClick={() => acceptHunk(h, 'ours')}
                        data-testid={`git-conflict-accept-ours-${idx}`}
                      >
                        Accept ours
                      </button>
                      <button
                        className={styles.hunkBtn}
                        onClick={() => acceptHunk(h, 'theirs')}
                        data-testid={`git-conflict-accept-theirs-${idx}`}
                      >
                        Accept theirs
                      </button>
                      <button
                        className={styles.hunkBtn}
                        onClick={() => acceptHunk(h, 'both')}
                      >
                        Accept both
                      </button>
                    </div>
                  ))}
                </div>
              )}
              <div className={styles.actions} style={{ marginTop: 'var(--space-2)' }}>
                <button
                  className={styles.btn}
                  onClick={() => void save()}
                  disabled={pending}
                  data-testid="git-conflict-save"
                >
                  Save
                </button>
                <button
                  className={`${styles.btn} ${styles.btnPrimary}`}
                  onClick={() => void markResolved()}
                  disabled={pending || stillConflicted}
                  title={stillConflicted ? 'Resolve all hunks first' : ''}
                  data-testid="git-conflict-mark-resolved"
                >
                  Mark resolved
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

function onMobileHide(active: string, name: string) {
  // Returns the paneHidden class on mobile when the pane isn't the active
  // tab. The CSS scopes paneHidden inside @media (max-width: 899px) so it's
  // a no-op on desktop where all three panes are visible.
  return active === name ? '' : styles.paneHidden
}

// parseConflictMarkers — mirror of the backend parser so we can rerender
// the hunk action bar in real-time as the user edits the merged buffer.
function parseConflictMarkers(body: string): ConflictHunk[] {
  const hunks: ConflictHunk[] = []
  const lines = body.split('\n')
  let i = 0
  while (i < lines.length) {
    const ln = lines[i]
    if (ln.startsWith('<<<<<<<')) {
      const start = i + 1 // 1-based
      let section: 'ours' | 'base' | 'theirs' = 'ours'
      const ours: string[] = []
      const base: string[] = []
      const theirs: string[] = []
      let oursLabel = ln.slice(7).trim()
      let theirsLabel = ''
      i++
      while (i < lines.length) {
        const cur = lines[i]
        if (cur.startsWith('|||||||')) {
          section = 'base'
        } else if (cur.startsWith('=======')) {
          section = 'theirs'
        } else if (cur.startsWith('>>>>>>>')) {
          theirsLabel = cur.slice(7).trim()
          i++
          break
        } else {
          if (section === 'ours') ours.push(cur)
          else if (section === 'base') base.push(cur)
          else theirs.push(cur)
        }
        i++
      }
      const end = i // 1-based (i now points one past >>>>>>>; in 1-based that's i)
      hunks.push({
        startLine: start,
        endLine: end,
        ours,
        base,
        theirs,
        oursLabel,
        theirsLabel,
      })
      continue
    }
    i++
  }
  return hunks
}
