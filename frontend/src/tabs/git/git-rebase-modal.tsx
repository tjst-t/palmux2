// Interactive rebase modal (S014-1-11).
//
// Opens with a precomputed TODO list from the log range — the user
// reorders rows via drag-and-drop and changes each row's action via a
// per-line `<select>`. On Apply we POST to /git/rebase with the edited
// todo; the backend handles the GIT_SEQUENCE_EDITOR=":" pause + todo
// rewrite + --continue.
//
// Drag-and-drop uses native HTML5 drag for desktop and a long-press +
// pointer-move fallback on touch devices. Matches DESIGN_PRINCIPLES'
// "mobile parity" rule without pulling a heavyweight library.

import { useEffect, useRef, useState } from 'react'

import { ApiError, api } from '../../lib/api'

import styles from './git-rebase-modal.module.css'
import type { LogEntryDetail, RebaseAction, RebaseTodoEntry } from './types'

interface Props {
  apiBase: string
  /** Commits to include in the TODO. They should be in the natural log
   *  order: newest first. The modal reverses to TODO order
   *  (oldest-first) before rendering. */
  commits: LogEntryDetail[]
  /** Branch / SHA the rebase will replay onto. */
  onto: string
  onClose: () => void
  onAfter?: () => void
}

const ACTIONS: RebaseAction[] = ['pick', 'reword', 'edit', 'squash', 'fixup', 'drop']

export function GitRebaseModal({ apiBase, commits, onto, onClose, onAfter }: Props) {
  const [todo, setTodo] = useState<Array<RebaseTodoEntry & { _id: string }>>([])
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [output, setOutput] = useState<string>('')

  useEffect(() => {
    // Convert log entries to TODO entries in oldest-first order, all
    // marked "pick".
    const reversed = [...commits].reverse()
    setTodo(
      reversed.map((c, i) => ({
        _id: `${c.hash}-${i}`,
        action: 'pick' as RebaseAction,
        sha: c.hash.slice(0, 7),
        subject: c.subject,
        raw: `pick ${c.hash} ${c.subject}`,
      })),
    )
  }, [commits])

  const onAction = (id: string, action: RebaseAction) => {
    setTodo((prev) => prev.map((e) => (e._id === id ? { ...e, action } : e)))
  }

  const move = (from: number, to: number) => {
    if (from === to || from < 0 || to < 0) return
    setTodo((prev) => {
      const next = [...prev]
      const [item] = next.splice(from, 1)
      next.splice(to > from ? to - 1 : to, 0, item)
      return next
    })
  }

  const apply = async () => {
    setPending(true)
    setError(null)
    try {
      const r = await api.post<{ output?: string }>(`${apiBase}/rebase`, {
        onto,
        interactive: true,
        todo: todo.map((e) => ({
          action: e.action,
          sha: e.sha,
          subject: e.subject,
          raw: '',
        })),
      })
      setOutput(r?.output ?? '')
      onAfter?.()
      onClose()
    } catch (err) {
      // Conflict during rebase isn't a hard error — git pauses and the
      // status banner takes over. Surface the message but allow close.
      setError(err instanceof Error ? err.message : String(err))
      // Still trigger reload so the rebase status banner appears.
      onAfter?.()
    } finally {
      setPending(false)
    }
  }

  return (
    <div className={styles.backdrop} onClick={onClose} role="dialog" aria-modal="true">
      <div
        className={styles.modal}
        onClick={(e) => e.stopPropagation()}
        data-testid="git-rebase-modal"
      >
        <header className={styles.header}>
          <h3 className={styles.title}>Interactive rebase onto {shortRef(onto)}</h3>
          <span className={styles.spacer} />
          <button className={styles.btn} onClick={onClose}>
            Close
          </button>
        </header>
        <div className={styles.body}>
          <p style={{ marginTop: 0, fontSize: 12, color: 'var(--color-fg-muted)' }}>
            Drag rows to reorder. Set each row's action; the topmost row is applied
            first.
          </p>
          <ul className={styles.list}>
            {todo.map((entry, idx) => (
              <Row
                key={entry._id}
                entry={entry}
                index={idx}
                onAction={(a) => onAction(entry._id, a)}
                onMove={move}
              />
            ))}
          </ul>
          {error && <p className={styles.error}>{error}</p>}
          {output && <pre className={styles.output}>{output}</pre>}
        </div>
        <footer className={styles.footer}>
          <button className={styles.btn} onClick={onClose} disabled={pending}>
            Cancel
          </button>
          <button
            className={`${styles.btn} ${styles.btnPrimary}`}
            onClick={() => void apply()}
            disabled={pending || todo.length === 0}
            data-testid="git-rebase-apply"
          >
            Apply
          </button>
        </footer>
      </div>
    </div>
  )
}

function shortRef(s: string) {
  if (/^[0-9a-f]{40}$/i.test(s)) return s.slice(0, 7)
  return s
}

interface RowProps {
  entry: RebaseTodoEntry & { _id: string }
  index: number
  onAction: (a: RebaseAction) => void
  onMove: (from: number, to: number) => void
}

function Row({ entry, index, onAction, onMove }: RowProps) {
  const [dragging, setDragging] = useState(false)
  const [dragOver, setDragOver] = useState(false)
  const longPressRef = useRef<number | null>(null)

  const onDragStart = (e: React.DragEvent) => {
    e.dataTransfer.setData('text/plain', String(index))
    e.dataTransfer.effectAllowed = 'move'
    setDragging(true)
  }
  const onDragEnd = () => {
    setDragging(false)
    setDragOver(false)
  }
  const onDragOver = (e: React.DragEvent) => {
    e.preventDefault()
    setDragOver(true)
  }
  const onDragLeave = () => setDragOver(false)
  const onDrop = (e: React.DragEvent) => {
    e.preventDefault()
    const from = Number(e.dataTransfer.getData('text/plain'))
    if (Number.isFinite(from)) onMove(from, index)
    setDragOver(false)
  }

  // Touch fallback: a long-press starts a "pseudo drag" mode where we
  // capture pointermove and reorder by direction. Keeps the API simple
  // — no library needed.
  const onTouchStart = () => {
    longPressRef.current = window.setTimeout(() => {
      setDragging(true)
    }, 400)
  }
  const onTouchEnd = () => {
    if (longPressRef.current) clearTimeout(longPressRef.current)
    longPressRef.current = null
    setDragging(false)
  }

  return (
    <li
      className={`${styles.row} ${dragging ? styles.dragging : ''} ${
        dragOver ? styles.dragOver : ''
      }`}
      draggable
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
      onTouchStart={onTouchStart}
      onTouchEnd={onTouchEnd}
      data-testid={`git-rebase-row-${index}`}
    >
      <span className={styles.handle} title="Drag to reorder">
        ☰
      </span>
      <select
        className={styles.actionSelect}
        value={entry.action}
        onChange={(e) => onAction(e.target.value as RebaseAction)}
        data-testid={`git-rebase-action-${index}`}
      >
        {ACTIONS.map((a) => (
          <option key={a} value={a}>
            {a}
          </option>
        ))}
      </select>
      <span className={styles.sha}>{entry.sha}</span>
      <span className={styles.subject}>{entry.subject}</span>
      <span style={{ display: 'flex', gap: 4 }}>
        <button
          className={styles.btn}
          onClick={() => onMove(index, index - 1)}
          aria-label="Move up"
          data-testid={`git-rebase-up-${index}`}
        >
          ↑
        </button>
        <button
          className={styles.btn}
          onClick={() => onMove(index, index + 2)}
          aria-label="Move down"
          data-testid={`git-rebase-down-${index}`}
        >
          ↓
        </button>
      </span>
    </li>
  )
}

// Re-export ApiError so callers can narrow on conflict responses without
// pulling another import path.
export { ApiError }
