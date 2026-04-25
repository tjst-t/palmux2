import { useEffect, useState } from 'react'

import { api } from '../../lib/api'

import styles from './git-status.module.css'
import type { FileStatus, StatusReport } from './types'

interface Props {
  apiBase: string
  onJumpToDiff: () => void
}

export function GitStatus({ apiBase, onJumpToDiff }: Props) {
  const [rep, setRep] = useState<StatusReport | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<string | null>(null)

  const reload = async () => {
    try {
      setRep(await api.get<StatusReport>(`${apiBase}/status`))
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  useEffect(() => {
    void reload()
  }, [apiBase])

  const act = async (path: string, op: 'stage' | 'unstage' | 'discard') => {
    setPending(`${op}:${path}`)
    try {
      await api.post(`${apiBase}/${op}`, { path })
      await reload()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  if (error) return <p className={styles.error}>{error}</p>
  if (!rep) return <p className={styles.empty}>Loading…</p>

  const empty =
    !rep.staged?.length && !rep.unstaged?.length && !rep.untracked?.length && !rep.conflicts?.length
  if (empty) return <p className={styles.clean}>✔ Working tree clean</p>

  return (
    <div className={styles.wrap}>
      {rep.conflicts && rep.conflicts.length > 0 && (
        <Section title={`Conflicts (${rep.conflicts.length})`}>
          {rep.conflicts.map((f) => (
            <Row key={f.path} f={f} pending={pending} kind="conflict" onAct={act} onJump={onJumpToDiff} />
          ))}
        </Section>
      )}
      {rep.staged && rep.staged.length > 0 && (
        <Section title={`Staged (${rep.staged.length})`}>
          {rep.staged.map((f) => (
            <Row key={f.path} f={f} pending={pending} kind="staged" onAct={act} onJump={onJumpToDiff} />
          ))}
        </Section>
      )}
      {rep.unstaged && rep.unstaged.length > 0 && (
        <Section title={`Unstaged (${rep.unstaged.length})`}>
          {rep.unstaged.map((f) => (
            <Row key={f.path} f={f} pending={pending} kind="unstaged" onAct={act} onJump={onJumpToDiff} />
          ))}
        </Section>
      )}
      {rep.untracked && rep.untracked.length > 0 && (
        <Section title={`Untracked (${rep.untracked.length})`}>
          {rep.untracked.map((f) => (
            <Row key={f.path} f={f} pending={pending} kind="untracked" onAct={act} onJump={onJumpToDiff} />
          ))}
        </Section>
      )}
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

function Row({
  f,
  pending,
  kind,
  onAct,
  onJump,
}: {
  f: FileStatus
  pending: string | null
  kind: 'staged' | 'unstaged' | 'untracked' | 'conflict'
  onAct: (path: string, op: 'stage' | 'unstage' | 'discard') => void
  onJump: () => void
}) {
  const code = (f.stagedCode + f.workingCode).trim()
  return (
    <li className={styles.row}>
      <button className={styles.path} onClick={onJump} title="Jump to diff">
        <span className={styles.code}>{code}</span>
        <span className={styles.name}>{f.path}</span>
      </button>
      <div className={styles.actions}>
        {(kind === 'unstaged' || kind === 'untracked') && (
          <button
            className={styles.btn}
            disabled={pending === `stage:${f.path}`}
            onClick={() => onAct(f.path, 'stage')}
          >
            Stage
          </button>
        )}
        {kind === 'staged' && (
          <button
            className={styles.btn}
            disabled={pending === `unstage:${f.path}`}
            onClick={() => onAct(f.path, 'unstage')}
          >
            Unstage
          </button>
        )}
        {(kind === 'unstaged' || kind === 'untracked') && (
          <button
            className={`${styles.btn} ${styles.danger}`}
            disabled={pending === `discard:${f.path}`}
            onClick={() => {
              if (confirm(`Discard changes to ${f.path}? This cannot be undone.`)) {
                onAct(f.path, 'discard')
              }
            }}
          >
            ×
          </button>
        )}
      </div>
    </li>
  )
}
