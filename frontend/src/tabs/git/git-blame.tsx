// Blame view (S013-1-16).
//
// Reachable from:
//   - the file-preview "Blame" toggle (Files tab → click 'Blame' in
//     the preview header)
//   - the ⌘K palette: `git: blame current file`
//
// We render the file content as plain monospace lines with a left-side
// gutter showing the commit short-hash + author + date for each line.
// The S010 Monaco viewer is too heavy for this read-only mode so we
// build a lightweight table-based renderer.

import { useEffect, useState } from 'react'

import { ApiError, api } from '../../lib/api'

import styles from './git-blame.module.css'
import type { BlameLine, BlameResponse } from './types'

interface Props {
  apiBase: string
  /** Worktree-relative path. */
  path: string
  /** Optional revision; defaults to HEAD. */
  revision?: string
  onClose?: () => void
}

export function GitBlame({ apiBase, path, revision, onClose }: Props) {
  const [lines, setLines] = useState<BlameLine[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [hover, setHover] = useState<{ line: BlameLine; x: number; y: number } | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    setLines([])
    ;(async () => {
      try {
        const qs = new URLSearchParams({ path })
        if (revision) qs.set('revision', revision)
        const res = await api.get<BlameResponse>(`${apiBase}/blame?${qs.toString()}`)
        if (!cancelled) setLines(res.lines ?? [])
      } catch (e) {
        if (!cancelled) setError(e instanceof ApiError ? e.message : String(e))
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [apiBase, path, revision])

  if (loading) return <p className={styles.empty}>Loading blame for {path}…</p>
  if (error) return <p className={styles.error}>Blame error: {error}</p>

  return (
    <section className={styles.wrap} data-testid="git-blame">
      <header className={styles.header}>
        {onClose && (
          <button className={styles.backBtn} onClick={onClose} data-testid="blame-close">
            ← Close
          </button>
        )}
        <span className={styles.pathLabel}>Blame:</span>
        <code className={styles.path}>{path}</code>
      </header>
      <div className={styles.body}>
        <table className={styles.table}>
          <tbody>
            {lines.map((line, i) => (
              <tr
                key={i}
                className={styles.line}
                data-testid="blame-line"
                data-commit={line.hash}
                onMouseMove={(ev) => setHover({ line, x: ev.clientX, y: ev.clientY })}
                onMouseLeave={() => setHover(null)}
              >
                <td className={styles.gutterHash}>
                  <code>{line.hash.slice(0, 8)}</code>
                </td>
                <td className={styles.gutterAuthor}>{line.author}</td>
                <td className={styles.gutterDate}>{shortDate(line.authorTime)}</td>
                <td className={styles.lineNum}>{line.finalLine}</td>
                <td className={styles.lineContent}>
                  <pre>{line.content}</pre>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {hover && (
        <div
          className={styles.popover}
          style={{ top: hover.y + 12, left: hover.x + 12 }}
          data-testid="blame-popover"
        >
          <div className={styles.popHeader}>
            <code>{hover.line.hash.slice(0, 8)}</code>
            <span>{hover.line.author}</span>
          </div>
          {hover.line.summary && <div className={styles.popSummary}>{hover.line.summary}</div>}
          {hover.line.authorTime && (
            <div className={styles.popDate}>{shortDate(hover.line.authorTime)}</div>
          )}
        </div>
      )}
    </section>
  )
}

function shortDate(s?: string): string {
  if (!s) return ''
  // Backend emits "@<unix>" via isoTimeFromUnix; convert to a readable
  // date. Backwards compatible: if some other ISO string lands here we
  // fall through to the new Date() path.
  if (s.startsWith('@')) {
    const ts = Number(s.slice(1))
    if (!Number.isNaN(ts)) return new Date(ts * 1000).toISOString().slice(0, 10)
  }
  const d = new Date(s)
  if (!Number.isNaN(+d)) return d.toISOString().slice(0, 10)
  return s
}
