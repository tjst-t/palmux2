// Stash manager (S013-1-12).
//
// One row per stash. Each row exposes Apply / Pop / Drop / Show diff.
// "Save" creates a new stash via a tiny inline form. The diff dialog
// reuses the existing Modal component and renders the unified diff in a
// monospace pre — heavier consumers (Monaco diff) live in
// git-monaco-diff but stash diffs are usually small.

import { useCallback, useEffect, useState } from 'react'

import { ApiError, api } from '../../lib/api'
import { Modal } from '../../components/modal'

import styles from './git-stash.module.css'
import type { StashEntry, StashPushOptions } from './types'

interface Props {
  apiBase: string
  /** Bumped after a stash op. */
  reloadKey?: number
  onChange?: () => void
}

export function GitStash({ apiBase, reloadKey = 0, onChange }: Props) {
  const [entries, setEntries] = useState<StashEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<string | null>(null)
  const [showSave, setShowSave] = useState(false)
  const [saveDraft, setSaveDraft] = useState<StashPushOptions>({ message: '', includeUntracked: false })
  const [diffDialog, setDiffDialog] = useState<{ name: string; raw: string } | null>(null)

  const fetchList = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const list = await api.get<StashEntry[]>(`${apiBase}/stash`)
      setEntries(list ?? [])
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [apiBase])

  useEffect(() => {
    void fetchList()
  }, [fetchList, reloadKey])

  const runOp = async (label: string, op: () => Promise<unknown>, key: string) => {
    setPending(key)
    try {
      await op()
      await fetchList()
      onChange?.()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : `${label}: ${String(e)}`)
    } finally {
      setPending(null)
    }
  }

  const onSave = async () => {
    await runOp(
      'stash push',
      () => api.post(`${apiBase}/stash`, saveDraft),
      'save',
    )
    setShowSave(false)
    setSaveDraft({ message: '', includeUntracked: false })
  }

  const onApply = (e: StashEntry) =>
    runOp(
      'stash apply',
      () => api.post(`${apiBase}/stash/${encodeURIComponent(e.name)}/apply`, {}),
      `apply:${e.name}`,
    )
  const onPop = (e: StashEntry) =>
    runOp(
      'stash pop',
      () => api.post(`${apiBase}/stash/${encodeURIComponent(e.name)}/pop`, {}),
      `pop:${e.name}`,
    )
  const onDrop = (e: StashEntry) => {
    if (!confirm(`Drop ${e.name}? This is irreversible.`)) return
    void runOp(
      'stash drop',
      () => api.delete(`${apiBase}/stash/${encodeURIComponent(e.name)}`),
      `drop:${e.name}`,
    )
  }
  const onShowDiff = async (e: StashEntry) => {
    setPending(`diff:${e.name}`)
    try {
      const res = await api.get<{ name: string; raw: string }>(
        `${apiBase}/stash/${encodeURIComponent(e.name)}/diff`,
      )
      setDiffDialog({ name: res.name, raw: res.raw ?? '' })
    } catch (e2) {
      setError(e2 instanceof ApiError ? e2.message : String(e2))
    } finally {
      setPending(null)
    }
  }

  return (
    <section className={styles.wrap} data-testid="git-stash">
      <header className={styles.header}>
        <h3 className={styles.title}>Stash</h3>
        <button
          className={styles.btn}
          onClick={() => setShowSave((s) => !s)}
          data-testid="stash-save-toggle"
        >
          {showSave ? 'Cancel' : 'Save current changes…'}
        </button>
      </header>

      {showSave && (
        <div className={styles.saveForm} data-testid="stash-save-form">
          <input
            className={styles.input}
            placeholder="Stash message (optional)"
            value={saveDraft.message ?? ''}
            onChange={(e) => setSaveDraft((d) => ({ ...d, message: e.target.value }))}
            data-testid="stash-save-message"
          />
          <label className={styles.checkbox}>
            <input
              type="checkbox"
              checked={!!saveDraft.includeUntracked}
              onChange={(e) =>
                setSaveDraft((d) => ({ ...d, includeUntracked: e.target.checked }))
              }
              data-testid="stash-save-untracked"
            />
            Include untracked files
          </label>
          <button
            className={`${styles.btn} ${styles.btnPrimary}`}
            disabled={pending === 'save'}
            onClick={onSave}
            data-testid="stash-save-confirm"
          >
            {pending === 'save' ? 'Saving…' : 'Save stash'}
          </button>
        </div>
      )}

      {error && <p className={styles.error}>{error}</p>}
      {loading && entries.length === 0 && <p className={styles.empty}>Loading…</p>}
      {!loading && entries.length === 0 && (
        <p className={styles.empty}>No stash entries.</p>
      )}

      <ul className={styles.list}>
        {entries.map((e) => (
          <li key={e.name} className={styles.row} data-testid="stash-row" data-stash={e.name}>
            <code className={styles.name}>{e.name}</code>
            <span className={styles.subject}>{e.subject}</span>
            <div className={styles.actions}>
              <button
                className={styles.linkBtn}
                onClick={() => void onShowDiff(e)}
                disabled={pending === `diff:${e.name}`}
                data-testid="stash-diff-btn"
              >
                Diff
              </button>
              <button
                className={styles.linkBtn}
                onClick={() => void onApply(e)}
                disabled={pending === `apply:${e.name}`}
                data-testid="stash-apply-btn"
              >
                Apply
              </button>
              <button
                className={styles.linkBtn}
                onClick={() => void onPop(e)}
                disabled={pending === `pop:${e.name}`}
                data-testid="stash-pop-btn"
              >
                Pop
              </button>
              <button
                className={`${styles.linkBtn} ${styles.danger}`}
                onClick={() => onDrop(e)}
                disabled={pending === `drop:${e.name}`}
                data-testid="stash-drop-btn"
              >
                Drop
              </button>
            </div>
          </li>
        ))}
      </ul>

      {diffDialog && (
        <Modal
          open
          onClose={() => setDiffDialog(null)}
          title={`Diff for ${diffDialog.name}`}
          width={720}
        >
          <pre className={styles.diff} data-testid="stash-diff-modal">
            {diffDialog.raw || '(no changes)'}
          </pre>
        </Modal>
      )}
    </section>
  )
}
