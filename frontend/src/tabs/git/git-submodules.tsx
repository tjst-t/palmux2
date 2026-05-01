// Submodule panel (S014-1-13).
//
// Lists submodules and exposes Init / Update buttons per row. Mirrors the
// minimal git-submodule porcelain — add/deinit are out of scope (they
// cross repo boundaries).

import { useCallback, useEffect, useState } from 'react'

import { api } from '../../lib/api'

import styles from './git-conflict.module.css'
import type { Submodule } from './types'

interface Props {
  apiBase: string
  reloadKey?: number
}

export function GitSubmodules({ apiBase, reloadKey }: Props) {
  const [items, setItems] = useState<Submodule[]>([])
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<string | null>(null)
  const [output, setOutput] = useState<string>('')

  const reload = useCallback(async () => {
    try {
      setError(null)
      const r = await api.get<Submodule[] | null>(`${apiBase}/submodules`)
      setItems(r ?? [])
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [apiBase])

  useEffect(() => {
    void reload()
  }, [reload, reloadKey])

  const run = async (path: string, action: 'init' | 'update') => {
    setPending(`${action}:${path}`)
    setError(null)
    try {
      const r = await api.post<{ output?: string }>(
        `${apiBase}/submodules/${action}?path=${encodeURIComponent(path)}`,
      )
      setOutput(`${action} ${path}:\n${r?.output ?? ''}`)
      await reload()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  if (items.length === 0) {
    return (
      <div className={styles.wrap} data-testid="git-submodules">
        {error && <div className={styles.error}>{error}</div>}
        <p className={styles.empty}>No submodules.</p>
      </div>
    )
  }

  return (
    <div className={styles.wrap} data-testid="git-submodules">
      {error && <div className={styles.error}>{error}</div>}
      <ul className={styles.fileList}>
        {items.map((s) => (
          <li
            key={s.path}
            className={styles.fileItem}
            data-testid={`git-submodule-row-${s.path}`}
          >
            <span className={styles.path} style={{ flex: 1 }}>
              {s.path}
            </span>
            <span className={styles.path}>{s.commit.slice(0, 8)}</span>
            <span className={styles.path}>{s.status}</span>
            <span className={styles.actions}>
              {s.status === 'not-initialized' && (
                <button
                  className={styles.hunkBtn}
                  onClick={() => void run(s.path, 'init')}
                  disabled={pending === `init:${s.path}`}
                  data-testid={`git-submodule-init-${s.path}`}
                >
                  Init
                </button>
              )}
              <button
                className={styles.hunkBtn}
                onClick={() => void run(s.path, 'update')}
                disabled={pending === `update:${s.path}`}
                data-testid={`git-submodule-update-${s.path}`}
              >
                Update
              </button>
            </span>
          </li>
        ))}
      </ul>
      {output && <pre className={styles.output}>{output}</pre>}
    </div>
  )
}

