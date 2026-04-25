import { useEffect, useState } from 'react'

import { api } from '../../lib/api'

import styles from './git-branches.module.css'
import type { BranchEntry } from './types'

interface Props {
  apiBase: string
}

export function GitBranches({ apiBase }: Props) {
  const [entries, setEntries] = useState<BranchEntry[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const res = await api.get<BranchEntry[]>(`${apiBase}/branches`)
        if (!cancelled) setEntries(res ?? [])
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      }
    })()
    return () => {
      cancelled = true
    }
  }, [apiBase])

  const local = entries.filter((b) => !b.isRemote)
  const remote = entries.filter((b) => b.isRemote)

  if (error) return <p className={styles.error}>{error}</p>

  return (
    <div className={styles.wrap}>
      <Section title={`Local (${local.length})`}>
        {local.map((b) => (
          <li key={b.name} className={styles.row}>
            <span className={styles.head}>{b.isHead ? '●' : ''}</span>
            <span className={styles.name}>{b.name}</span>
          </li>
        ))}
      </Section>
      <Section title={`Remote (${remote.length})`}>
        {remote.map((b) => (
          <li key={b.name} className={styles.row}>
            <span className={styles.head} />
            <span className={styles.name}>{b.name}</span>
          </li>
        ))}
      </Section>
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className={styles.section}>
      <h3 className={styles.sectionTitle}>{title}</h3>
      <ul className={styles.list}>{children}</ul>
    </section>
  )
}
