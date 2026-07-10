import { useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'

import type { BranchPickerEntry } from '../lib/api'
import { usePalmuxStore } from '../stores/palmux-store'

import { Modal } from './modal'
import styles from './picker.module.css'

// Stable fallback to avoid creating a new array reference on every render.
const EMPTY_PICKER_ENTRIES: BranchPickerEntry[] = []

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
  const location = useLocation()

  const [filter, setFilter] = useState('')
  const [pending, setPending] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  // S13b16a-4: Track previous (open, repoId) tuple so we can reset
  // `error` inline when the modal is opened (or re-targeted to a
  // different repo) — the React 19 "deriving state from props" idiom.
  // The async `reload(repoId)` stays in useEffect since it's a side
  // effect that genuinely belongs there.
  const [openSession, setOpenSession] = useState<string | null>(null)
  const sessionKey = open && repoId ? repoId : null
  if (openSession !== sessionKey) {
    setOpenSession(sessionKey)
    if (sessionKey !== null) setError(null)
  }

  useEffect(() => {
    if (!open || !repoId) return
    void reload(repoId)
  }, [open, repoId, reload])

  const entries = useMemo(
    () => (picker?.repoId === repoId ? picker.entries : EMPTY_PICKER_ENTRIES),
    [picker, repoId],
  )
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

  // [AC-S4323c8-2-1] The filter box doubles as the "new branch name" field:
  // if the trimmed filter doesn't exactly match any known branch, offer to
  // create it. Matched against `entries` (not `filtered`) so an exact match
  // always suppresses the affordance regardless of substring filtering.
  const trimmedFilter = filter.trim()
  const hasExactMatch = useMemo(
    () => entries.some((e) => e.name === trimmedFilter),
    [entries, trimmedFilter],
  )
  const showCreate = trimmedFilter.length > 0 && !hasExactMatch

  const select = async (name: string) => {
    setPending(name)
    setError(null)
    try {
      const branch = await openBranch(repoId, name)
      onClose()
      navigate(`/${repoId}/${branch.id}/${branch.tabSet.tabs[0]?.id ?? 'claude'}${location.search}`)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  // [AC-S4323c8-2-2] Creation goes through the same `openBranch` path as
  // opening an existing entry — the backend (`Store.OpenBranch` →
  // `ensureWorktree`) already creates the worktree via `gwq add -b <name>`
  // when no worktree/branch with that name exists yet, then opens it.
  const createNew = async () => {
    if (!showCreate || pending !== null) return
    await select(trimmedFilter)
  }

  return (
    <Modal open={open} onClose={onClose} title="Open Branch" width={520}>
      <input
        autoFocus
        className={styles.input}
        placeholder="Filter or type a new branch name…"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
        onKeyDown={(e) => {
          // [AC-S4323c8-2-1/2] Enter creates only when the typed name has no
          // exact match — otherwise Enter does nothing here (there's no
          // single "top" entry to disambiguate to; the user clicks a row).
          if (e.key === 'Enter' && showCreate) void createNew()
        }}
      />
      {error && (
        <p className={styles.error} data-testid="branch-picker-error">
          {error}
        </p>
      )}
      {showCreate && (
        <button
          type="button"
          className={styles.createRow}
          disabled={pending !== null}
          onClick={() => void createNew()}
          data-testid="branch-picker-create-btn"
        >
          <span className={styles.createIcon}>＋</span>
          <span>
            &ldquo;{trimmedFilter}&rdquo; を作成
          </span>
        </button>
      )}
      {grouped.open.length > 0 && (
        <Section title="Open" entries={grouped.open} onPick={select} pending={pending} />
      )}
      {grouped.local.length > 0 && (
        <Section title="Local" entries={grouped.local} onPick={select} pending={pending} />
      )}
      {grouped.remote.length > 0 && (
        <Section title="Remote" entries={grouped.remote} onPick={select} pending={pending} />
      )}
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
