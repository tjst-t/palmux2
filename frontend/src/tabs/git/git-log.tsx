import { useEffect, useState } from 'react'

import { api } from '../../lib/api'

import styles from './git-log.module.css'
import type { LogEntry } from './types'

interface Props {
  apiBase: string
}

export function GitLog({ apiBase }: Props) {
  const [entries, setEntries] = useState<LogEntry[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const res = await api.get<LogEntry[]>(`${apiBase}/log?limit=200`)
        if (!cancelled) setEntries(res ?? [])
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      }
    })()
    return () => {
      cancelled = true
    }
  }, [apiBase])

  if (error) return <p className={styles.error}>{error}</p>
  if (entries.length === 0) return <p className={styles.empty}>No commits.</p>

  return (
    <ol className={styles.list}>
      {entries.map((e) => (
        <li key={e.hash} className={styles.row}>
          <span className={styles.hash}>{e.hash.slice(0, 7)}</span>
          <span className={styles.subject}>{e.subject}</span>
          <span className={styles.author}>{e.author}</span>
          <span className={styles.date}>{relativeDate(e.date)}</span>
        </li>
      ))}
    </ol>
  )
}

function relativeDate(iso: string): string {
  const d = new Date(iso)
  const diff = Date.now() - +d
  const m = 60 * 1000, h = 60 * m, day = 24 * h
  if (diff < m) return 'just now'
  if (diff < h) return `${Math.floor(diff / m)}m ago`
  if (diff < day) return `${Math.floor(diff / h)}h ago`
  if (diff < 30 * day) return `${Math.floor(diff / day)}d ago`
  return d.toLocaleDateString()
}
