import { useEffect, useMemo, useRef, useState } from 'react'

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
  const [active, setActive] = useState(0)
  const [pending, setPending] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const listRef = useRef<HTMLUListElement | null>(null)

  useEffect(() => {
    if (!open) return
    setError(null)
    setFilter('')
    setActive(0)
    void reload()
  }, [open, reload])

  const filtered = useMemo(() => {
    const q = filter.toLowerCase()
    return repos
      .filter((r) => !r.open)
      .filter((r) => !q || r.ghqPath.toLowerCase().includes(q))
      .sort((a, b) => a.ghqPath.localeCompare(b.ghqPath))
  }, [repos, filter])

  // Keep `active` in range whenever the filtered list shrinks/expands.
  useEffect(() => {
    if (active >= filtered.length) setActive(Math.max(0, filtered.length - 1))
  }, [filtered.length, active])

  // Scroll the highlighted row into view as it moves.
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-row="${active}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [active])

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
        placeholder="Filter repositories… (↑↓ to move, Enter to open)"
        value={filter}
        onChange={(e) => {
          setFilter(e.target.value)
          setActive(0) // jump back to the top whenever the user types
        }}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown') {
            e.preventDefault()
            setActive((i) => Math.min(filtered.length - 1, i + 1))
          } else if (e.key === 'ArrowUp') {
            e.preventDefault()
            setActive((i) => Math.max(0, i - 1))
          } else if (e.key === 'Enter') {
            e.preventDefault()
            const target = filtered[active]
            if (target) void pick(target.id)
          }
        }}
      />
      {error && <p className={styles.error}>{error}</p>}
      <ul className={styles.list} ref={listRef}>
        {filtered.map((r, i) => {
          const isActive = i === active
          return (
            <li key={r.id}>
              <button
                data-row={i}
                className={isActive ? `${styles.row} ${styles.rowActive}` : styles.row}
                disabled={pending !== null}
                onMouseEnter={() => setActive(i)}
                onClick={() => pick(r.id)}
              >
                <span className={styles.rowName}>{r.ghqPath}</span>
                <span className={styles.rowState}>{r.starred ? '★' : ''}</span>
              </button>
            </li>
          )
        })}
        {filtered.length === 0 && <li className={styles.empty}>No repositories.</li>}
      </ul>
    </Modal>
  )
}
