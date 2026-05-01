// Bisect helper (S014-1-15).
//
// Three states the panel can be in:
//
//   1. Idle (no bisect in progress) — user enters a Good and Bad SHA, hits
//      Start. The backend resets any prior bisect, then runs `git bisect
//      start <bad> <good>` so git checks out the midpoint.
//
//   2. Bisecting — show the current SHA and three buttons: Good / Bad /
//      Skip. After each click the backend runs `git bisect <term>` and
//      git auto-checks-out the next candidate; we reload the status so
//      the user sees where they are.
//
//   3. Finished — `git bisect log` reports a "first bad commit" line.
//      Surface the verdict and a Reset button.
//
// The panel doesn't try to render a fancy progress bar — the
// `Remaining ~N commits` count from the backend is good enough for the
// MVP.

import { useCallback, useEffect, useState } from 'react'

import { api } from '../../lib/api'

import styles from './git-conflict.module.css'
import type { BisectStatus } from './types'

interface Props {
  apiBase: string
  reloadKey?: number
  onAfter?: () => void
}

export function GitBisect({ apiBase, reloadKey, onAfter }: Props) {
  const [status, setStatus] = useState<BisectStatus | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<string | null>(null)
  const [output, setOutput] = useState<string>('')

  const [good, setGood] = useState('')
  const [bad, setBad] = useState('HEAD')

  const reload = useCallback(async () => {
    try {
      setError(null)
      const r = await api.get<BisectStatus>(`${apiBase}/bisect/status`)
      setStatus(r)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [apiBase])

  useEffect(() => {
    void reload()
  }, [reload, reloadKey])

  const start = async () => {
    if (!bad) {
      setError('bad commit required')
      return
    }
    setPending('start')
    try {
      const r = await api.post<{ output?: string }>(`${apiBase}/bisect/start`, {
        good,
        bad,
      })
      setOutput(`bisect start:\n${r?.output ?? ''}`)
      await reload()
      onAfter?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  const mark = async (term: 'good' | 'bad' | 'skip') => {
    setPending(term)
    try {
      const r = await api.post<{ output?: string }>(`${apiBase}/bisect/${term}`)
      setOutput(`bisect ${term}:\n${r?.output ?? ''}`)
      await reload()
      onAfter?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  const reset = async () => {
    setPending('reset')
    try {
      const r = await api.post<{ output?: string }>(`${apiBase}/bisect/reset`)
      setOutput(`bisect reset:\n${r?.output ?? ''}`)
      await reload()
      onAfter?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  // Detect "verdict reached" from the backend log.
  const verdict = (() => {
    if (!status?.log) return null
    const m = status.log.match(/^([0-9a-f]{40}) is the first bad commit$/m)
    return m ? m[1] : null
  })()

  return (
    <div className={styles.wrap} data-testid="git-bisect">
      {error && <div className={styles.error}>{error}</div>}

      {!status?.active && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--space-2)' }}>
          <p style={{ margin: 0, fontSize: 12, color: 'var(--color-fg-muted)' }}>
            Bisect helps locate the commit that introduced a bug. Provide a known
            "good" SHA (or branch) and a known "bad" SHA, then mark each candidate.
          </p>
          <label>
            <span className={styles.path}>Good (older / known working)</span>
            <input
              className={styles.merged}
              style={{ height: 28, padding: '4px 8px' }}
              value={good}
              onChange={(e) => setGood(e.target.value)}
              placeholder="e.g. v1.0 or 1234abc"
              data-testid="git-bisect-good"
            />
          </label>
          <label>
            <span className={styles.path}>Bad (newer / broken)</span>
            <input
              className={styles.merged}
              style={{ height: 28, padding: '4px 8px' }}
              value={bad}
              onChange={(e) => setBad(e.target.value)}
              placeholder="e.g. HEAD or abcdef0"
              data-testid="git-bisect-bad"
            />
          </label>
          <div className={styles.actions}>
            <button
              className={`${styles.btn} ${styles.btnPrimary}`}
              onClick={() => void start()}
              disabled={pending !== null || !bad}
              data-testid="git-bisect-start"
            >
              Start bisect
            </button>
          </div>
        </div>
      )}

      {status?.active && !verdict && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--space-2)' }}>
          <p style={{ margin: 0 }}>
            <strong>Bisecting…</strong>{' '}
            {status.currentSha && (
              <span className={styles.path}>at {status.currentSha.slice(0, 8)}</span>
            )}
          </p>
          <div className={styles.actions}>
            <button
              className={styles.btn}
              onClick={() => void mark('good')}
              disabled={pending !== null}
              data-testid="git-bisect-mark-good"
            >
              Good
            </button>
            <button
              className={`${styles.btn} ${styles.btnDanger}`}
              onClick={() => void mark('bad')}
              disabled={pending !== null}
              data-testid="git-bisect-mark-bad"
            >
              Bad
            </button>
            <button
              className={styles.btn}
              onClick={() => void mark('skip')}
              disabled={pending !== null}
              data-testid="git-bisect-mark-skip"
            >
              Skip
            </button>
            <button
              className={styles.btn}
              onClick={() => void reset()}
              disabled={pending !== null}
              data-testid="git-bisect-reset"
            >
              Reset
            </button>
          </div>
        </div>
      )}

      {verdict && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--space-2)' }}>
          <div className={styles.banner} data-testid="git-bisect-verdict">
            <strong>First bad commit:</strong>
            <span className={styles.path}>{verdict}</span>
          </div>
          <div className={styles.actions}>
            <button
              className={styles.btn}
              onClick={() => void reset()}
              disabled={pending !== null}
              data-testid="git-bisect-reset-after"
            >
              Reset bisect
            </button>
          </div>
        </div>
      )}

      {output && <pre className={styles.output}>{output}</pre>}
      {status?.log && (
        <details style={{ marginTop: 'var(--space-2)' }}>
          <summary className={styles.path}>git bisect log</summary>
          <pre className={styles.output}>{status.log}</pre>
        </details>
      )}
    </div>
  )
}
