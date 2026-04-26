import { useState } from 'react'

import { api } from '../../lib/api'

import styles from './file-search.module.css'
import type { Entry, GrepHit } from './types'

interface Props {
  apiBase: string
  basePath: string
  onPick: (target: { path: string; isDir?: boolean; lineNum?: number }) => void
}

type Mode = 'name' | 'grep'

interface NameSearchResp {
  results: Entry[] | null
}
interface GrepResp {
  hits: GrepHit[] | null
}

export function FileSearch({ apiBase, basePath, onPick }: Props) {
  const [mode, setMode] = useState<Mode>('name')
  const [query, setQuery] = useState('')
  const [caseSensitive, setCaseSensitive] = useState(false)
  const [results, setResults] = useState<Entry[]>([])
  const [hits, setHits] = useState<GrepHit[]>([])
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const run = async () => {
    if (!query.trim()) return
    setError(null)
    setRunning(true)
    try {
      if (mode === 'name') {
        const res = await api.get<NameSearchResp>(
          `${apiBase}/search?path=${encodeURIComponent(basePath)}&query=${encodeURIComponent(query)}&case=${caseSensitive ? 1 : 0}`,
        )
        setResults(res.results ?? [])
        setHits([])
      } else {
        const res = await api.get<GrepResp>(
          `${apiBase}/grep?path=${encodeURIComponent(basePath)}&pattern=${encodeURIComponent(query)}&case=${caseSensitive ? 1 : 0}`,
        )
        setHits(res.hits ?? [])
        setResults([])
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className={styles.wrap}>
      <div className={styles.controls}>
        <input
          autoFocus
          className={styles.input}
          placeholder={mode === 'name' ? 'Filename contains…' : 'Grep pattern…'}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void run()
          }}
        />
        <div className={styles.modes}>
          <ModeBtn active={mode === 'name'} onClick={() => setMode('name')}>
            Name
          </ModeBtn>
          <ModeBtn active={mode === 'grep'} onClick={() => setMode('grep')}>
            Grep
          </ModeBtn>
          <label className={styles.case}>
            <input
              type="checkbox"
              checked={caseSensitive}
              onChange={(e) => setCaseSensitive(e.target.checked)}
            />
            Aa
          </label>
          <button className={styles.run} onClick={run} disabled={running}>
            {running ? '…' : 'Run'}
          </button>
        </div>
      </div>
      {error && <p className={styles.error}>{error}</p>}
      {mode === 'name' && (
        <ul className={styles.results}>
          {results.map((r) => (
            <li key={r.path}>
              <button
                className={styles.row}
                onClick={() => onPick({ path: r.path, isDir: r.isDir })}
              >
                <span className={styles.icon}>{r.isDir ? '📁' : '📄'}</span>
                <span className={styles.path}>{r.path}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {mode === 'grep' && (
        <ul className={styles.results}>
          {hits.map((h, i) => (
            <li key={`${h.path}-${h.lineNum}-${i}`}>
              <button
                className={styles.row}
                onClick={() => onPick({ path: h.path, lineNum: h.lineNum })}
              >
                <span className={styles.icon}>📄</span>
                <span className={styles.path}>{h.path}</span>
                <span className={styles.lineNum}>:{h.lineNum}</span>
                <span className={styles.line}>{h.line.trim()}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function ModeBtn({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button className={active ? `${styles.modeBtn} ${styles.modeBtnActive}` : styles.modeBtn} onClick={onClick}>
      {children}
    </button>
  )
}
