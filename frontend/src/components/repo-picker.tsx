import { useEffect, useMemo, useState } from 'react'

import { usePalmuxStore } from '../stores/palmux-store'

import { Modal } from './modal'
import styles from './picker.module.css'

interface Props {
  open: boolean
  onClose: () => void
}

export function RepoPicker({ open, onClose }: Props) {
  const reload = usePalmuxStore((s) => s.reloadAvailableRepos)
  const repos = usePalmuxStore((s) => s.availableRepos)
  const openRepo = usePalmuxStore((s) => s.openRepo)
  const [filter, setFilter] = useState('')
  const [pending, setPending] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setError(null)
    void reload()
  }, [open, reload])

  const filtered = useMemo(() => {
    const q = filter.toLowerCase()
    return repos
      .filter((r) => !r.open)
      .filter((r) => !q || r.ghqPath.toLowerCase().includes(q))
      .sort((a, b) => a.ghqPath.localeCompare(b.ghqPath))
  }, [repos, filter])

  const pick = async (id: string) => {
    setPending(id)
    setError(null)
    try {
      await openRepo(id)
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="Open Repository" width={520}>
      <input
        autoFocus
        className={styles.input}
        placeholder="Filter repositories…"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
      />
      {error && <p className={styles.error}>{error}</p>}
      <ul className={styles.list}>
        {filtered.map((r) => (
          <li key={r.id}>
            <button
              className={styles.row}
              disabled={pending !== null}
              onClick={() => pick(r.id)}
            >
              <span className={styles.rowName}>{r.ghqPath}</span>
              <span className={styles.rowState}>{r.starred ? '★' : ''}</span>
            </button>
          </li>
        ))}
        {filtered.length === 0 && <li className={styles.empty}>No repositories.</li>}
      </ul>
    </Modal>
  )
}
