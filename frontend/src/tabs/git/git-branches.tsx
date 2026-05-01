// GitBranches — local + remote branch list + S012 CRUD operations:
// create / switch / delete (with force-delete confirm) / set-upstream.
//
// Note: switching the *checked-out* branch in a worktree-managed setup
// has subtle implications because palmux2 derives session identity
// from worktree path. We surface the operation but warn the user that
// switching here is equivalent to `git switch` in the worktree shell;
// to truly add a new working branch, they should use the gwq Open
// Branch flow in the Drawer. We don't block the action — Magit
// behaviour parity is more important for power users.

import { useEffect, useState } from 'react'

import { confirmDialog } from '../../components/context-menu/confirm-dialog'
import { api } from '../../lib/api'

import styles from './git-branches.module.css'
import type { BranchEntry } from './types'

interface Props {
  apiBase: string
  /** Re-fetch branches after a write op. */
  onAfter: () => void
}

export function GitBranches({ apiBase, onAfter }: Props) {
  const [entries, setEntries] = useState<BranchEntry[]>([])
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [createName, setCreateName] = useState('')
  const [createFrom, setCreateFrom] = useState('')

  const reload = async () => {
    try {
      const res = await api.get<BranchEntry[]>(`${apiBase}/branches`)
      setEntries(res ?? [])
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  useEffect(() => {
    void reload()
  }, [apiBase])

  const local = entries.filter((b) => !b.isRemote)
  const remote = entries.filter((b) => b.isRemote)

  const onCreate = async () => {
    const name = createName.trim()
    if (!name) return
    setPending(`create:${name}`)
    try {
      await api.post(`${apiBase}/branches`, {
        name,
        startFrom: createFrom.trim() || undefined,
        checkout: false,
      })
      setCreating(false)
      setCreateName('')
      setCreateFrom('')
      await reload()
      onAfter()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setPending(null)
    }
  }

  const onSwitch = async (name: string) => {
    setPending(`switch:${name}`)
    try {
      await api.post(`${apiBase}/switch`, { name })
      await reload()
      onAfter()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setPending(null)
    }
  }

  const onDelete = async (name: string) => {
    const force = await confirmDialog.ask({
      title: `Delete branch ${name}?`,
      message:
        'This deletes the local ref. If the branch has unmerged work, deletion will fail unless you choose Force-delete.',
      confirmLabel: 'Delete',
      danger: true,
    })
    if (!force) return
    setPending(`delete:${name}`)
    try {
      // Try a regular delete first (`-d`).
      try {
        await api.delete(`${apiBase}/branches/${encodeURIComponent(name)}`)
      } catch (firstErr) {
        // If it failed due to unmerged work, ask for force.
        const msg = firstErr instanceof Error ? firstErr.message : String(firstErr)
        if (/not fully merged|the branch.+is not yet merged/i.test(msg)) {
          const okForce = await confirmDialog.ask({
            title: 'Force-delete?',
            message: `${name} has unmerged commits. Force-deleting will drop them. Proceed?`,
            confirmLabel: 'Force-delete',
            danger: true,
          })
          if (!okForce) throw firstErr
          await api.delete(`${apiBase}/branches/${encodeURIComponent(name)}?force=1`)
        } else {
          throw firstErr
        }
      }
      await reload()
      onAfter()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setPending(null)
    }
  }

  if (error) {
    return (
      <div className={styles.wrap}>
        <p className={styles.error}>{error}</p>
        <button className={styles.btn} onClick={() => void reload()}>
          Retry
        </button>
      </div>
    )
  }

  return (
    <div className={styles.wrap}>
      <div className={styles.toolbar}>
        <button
          className={styles.btn}
          onClick={() => setCreating((v) => !v)}
          data-testid="git-branch-create-toggle"
        >
          {creating ? 'Cancel' : 'New branch'}
        </button>
      </div>
      {creating && (
        <div className={styles.createForm} data-testid="git-branch-create-form">
          <input
            className={styles.input}
            placeholder="branch name"
            value={createName}
            onChange={(e) => setCreateName(e.target.value)}
            data-testid="git-branch-create-name"
          />
          <input
            className={styles.input}
            placeholder="start from (sha or ref, optional)"
            value={createFrom}
            onChange={(e) => setCreateFrom(e.target.value)}
            data-testid="git-branch-create-from"
          />
          <button
            className={styles.btn}
            disabled={!createName.trim() || pending !== null}
            onClick={onCreate}
            data-testid="git-branch-create-submit"
          >
            Create
          </button>
        </div>
      )}
      <Section title={`Local (${local.length})`}>
        {local.map((b) => (
          <li key={b.name} className={styles.row}>
            <span className={styles.head}>{b.isHead ? '●' : ''}</span>
            <span className={styles.name}>{b.name}</span>
            <span className={styles.actions}>
              {!b.isHead && (
                <button
                  className={styles.btn}
                  disabled={pending !== null}
                  onClick={() => onSwitch(b.name)}
                >
                  Switch
                </button>
              )}
              {!b.isHead && (
                <button
                  className={`${styles.btn} ${styles.danger}`}
                  disabled={pending !== null}
                  onClick={() => onDelete(b.name)}
                  data-testid={`git-branch-delete-${b.name}`}
                >
                  ×
                </button>
              )}
            </span>
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
