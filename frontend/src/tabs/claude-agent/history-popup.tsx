// Session history popup. Lists past sessions from /api/sessions filtered to
// the current branch by default. Clicking "Resume" sends a session.resume
// frame so the Agent kills its current CLI and respawns with --resume <id>.

import { useEffect, useMemo, useRef, useState } from 'react'

import { api } from '../../lib/api'

import styles from './history-popup.module.css'

interface SessionMeta {
  id: string
  repoId: string
  branchId: string
  title?: string
  model?: string
  createdAt?: string
  lastActivityAt?: string
  turnCount?: number
  totalCostUsd?: number
}

interface Props {
  repoId: string
  branchId: string
  currentSessionId: string
  open: boolean
  onClose: () => void
  onResume: (sessionId: string) => void
  onFork: (sessionId: string) => void
}

export function HistoryPopup({ repoId, branchId, currentSessionId, open, onClose, onResume, onFork }: Props) {
  const [sessions, setSessions] = useState<SessionMeta[]>([])
  const [filterAll, setFilterAll] = useState(false)
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(false)
  const ref = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    if (!open) return
    let cancelled = false
    setLoading(true)
    const url = filterAll
      ? '/api/sessions'
      : `/api/sessions?repo=${encodeURIComponent(repoId)}&branch=${encodeURIComponent(branchId)}`
    api
      .get<{ sessions: SessionMeta[] }>(url)
      .then((d) => {
        if (!cancelled) setSessions(d.sessions ?? [])
      })
      .catch(() => {
        if (!cancelled) setSessions([])
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, repoId, branchId, filterAll])

  // Click-outside / Esc closes.
  useEffect(() => {
    if (!open) return
    const onPointer = (e: PointerEvent) => {
      if (!ref.current) return
      if (!ref.current.contains(e.target as Node)) onClose()
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('pointerdown', onPointer, true)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', onPointer, true)
      window.removeEventListener('keydown', onKey)
    }
  }, [open, onClose])

  const filtered = useMemo(() => {
    const ql = query.trim().toLowerCase()
    if (!ql) return sessions
    return sessions.filter(
      (s) =>
        s.id.toLowerCase().includes(ql) ||
        (s.title ?? '').toLowerCase().includes(ql) ||
        (s.model ?? '').toLowerCase().includes(ql),
    )
  }, [sessions, query])

  if (!open) return null
  return (
    <div ref={ref} className={styles.popup} role="dialog" aria-label="Session history">
      <header className={styles.head}>
        <h3 className={styles.title}>History</h3>
        <label className={styles.scope}>
          <input
            type="checkbox"
            checked={filterAll}
            onChange={(e) => setFilterAll(e.target.checked)}
          />
          all branches
        </label>
        <button type="button" className={styles.closeBtn} onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <input
        className={styles.search}
        type="search"
        placeholder="Search title / id / model…"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        autoFocus
      />
      {loading ? (
        <p className={styles.empty}>Loading…</p>
      ) : filtered.length === 0 ? (
        <p className={styles.empty}>No past sessions.</p>
      ) : (
        <ul className={styles.list}>
          {filtered.map((s) => {
            const active = s.id === currentSessionId
            return (
              <li key={s.id} className={`${styles.item} ${active ? styles.active : ''}`.trim()}>
                <div className={styles.itemMain}>
                  <div className={styles.itemTitle}>{s.title || s.id.slice(0, 12)}</div>
                  <div className={styles.itemMeta}>
                    {s.model ? <span>{s.model}</span> : null}
                    {typeof s.turnCount === 'number' ? <span>{s.turnCount} turns</span> : null}
                    {typeof s.totalCostUsd === 'number' && s.totalCostUsd > 0 ? (
                      <span>${s.totalCostUsd.toFixed(4)}</span>
                    ) : null}
                    {s.lastActivityAt ? <span>{formatTime(s.lastActivityAt)}</span> : null}
                  </div>
                </div>
                <div className={styles.itemActions}>
                  {active ? (
                    <span className={styles.activeTag}>active</span>
                  ) : (
                    <>
                      <button
                        type="button"
                        className={styles.resumeBtn}
                        onClick={() => {
                          onResume(s.id)
                          onClose()
                        }}
                      >
                        Resume
                      </button>
                      <button
                        type="button"
                        className={styles.resumeBtn}
                        title="Fork — start a new session from this point"
                        onClick={() => {
                          onFork(s.id)
                          onClose()
                        }}
                      >
                        Fork
                      </button>
                    </>
                  )}
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

function formatTime(iso: string): string {
  try {
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return ''
    const delta = Date.now() - d.getTime()
    if (delta < 60_000) return 'just now'
    if (delta < 3_600_000) return `${Math.floor(delta / 60_000)}m ago`
    if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)}h ago`
    return d.toLocaleDateString()
  } catch {
    return ''
  }
}
