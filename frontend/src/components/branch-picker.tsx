import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'

import type { BranchPickerEntry } from '../lib/api'
import { usePalmuxStore } from '../stores/palmux-store'

import { Modal } from './modal'
import styles from './picker.module.css'

interface Props {
  open: boolean
  repoId: string
  onClose: () => void
}

export function BranchPicker({ open, repoId, onClose }: Props) {
  const reload = usePalmuxStore((s) => s.reloadBranchPicker)
  const picker = usePalmuxStore((s) => s.branchPicker)
  const openBranch = usePalmuxStore((s) => s.openBranch)
  const navigate = useNavigate()

  const [filter, setFilter] = useState('')
  const [pending, setPending] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [draftName, setDraftName] = useState('')

  useEffect(() => {
    if (!open || !repoId) return
    setError(null)
    void reload(repoId)
  }, [open, repoId, reload])

  const entries = picker?.repoId === repoId ? picker.entries : []
  const filtered = useMemo(() => {
    const q = filter.toLowerCase()
    if (!q) return entries
    return entries.filter((e) => e.name.toLowerCase().includes(q))
  }, [entries, filter])

  const grouped = useMemo(() => {
    const out: Record<'open' | 'local' | 'remote', BranchPickerEntry[]> = { open: [], local: [], remote: [] }
    for (const e of filtered) out[e.state].push(e)
    return out
  }, [filtered])

  const select = async (name: string) => {
    setPending(name)
    setError(null)
    try {
      const branch = await openBranch(repoId, name)
      onClose()
      navigate(`/${repoId}/${branch.id}/${branch.tabSet.tabs[0]?.id ?? 'claude'}`)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  const createNew = async () => {
    if (!draftName.trim()) return
    await select(draftName.trim())
  }

  return (
    <Modal open={open} onClose={onClose} title="Open Branch" width={520}>
      <input
        autoFocus
        className={styles.input}
        placeholder="Filter branches…"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
      />
      {error && <p className={styles.error}>{error}</p>}
      {grouped.open.length > 0 && (
        <Section title="Open" entries={grouped.open} onPick={select} pending={pending} />
      )}
      {grouped.local.length > 0 && (
        <Section title="Local" entries={grouped.local} onPick={select} pending={pending} />
      )}
      {grouped.remote.length > 0 && (
        <Section title="Remote" entries={grouped.remote} onPick={select} pending={pending} />
      )}
      <div className={styles.newBranch}>
        <input
          className={styles.input}
          placeholder="Create new branch…"
          value={draftName}
          onChange={(e) => setDraftName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void createNew()
          }}
        />
        <button className={styles.btn} disabled={!draftName.trim() || pending !== null} onClick={createNew}>
          Create
        </button>
      </div>
    </Modal>
  )
}

function Section({
  title,
  entries,
  onPick,
  pending,
}: {
  title: string
  entries: BranchPickerEntry[]
  onPick: (name: string) => void
  pending: string | null
}) {
  return (
    <section className={styles.section}>
      <h3 className={styles.sectionTitle}>{title}</h3>
      <ul className={styles.list}>
        {entries.map((e) => (
          <li key={`${e.state}:${e.name}`}>
            <button
              className={styles.row}
              disabled={pending !== null}
              onClick={() => onPick(e.name)}
            >
              <span className={styles.rowName}>{e.name}</span>
              <span className={styles.rowState}>{e.state}</span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  )
}
