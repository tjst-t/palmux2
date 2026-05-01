// Tag manager (S013-1-14).
//
// Lists local tags with annotated/lightweight indicator, exposes Create
// (with optional annotated message), Delete (with "also push delete to
// remote" toggle), and Push (single tag).

import { useCallback, useEffect, useState } from 'react'

import { ApiError, api } from '../../lib/api'

import styles from './git-tags.module.css'
import type { CreateTagOptions, PushTagOptions, TagEntry } from './types'

interface Props {
  apiBase: string
  reloadKey?: number
  onChange?: () => void
}

export function GitTags({ apiBase, reloadKey = 0, onChange }: Props) {
  const [entries, setEntries] = useState<TagEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [draft, setDraft] = useState<CreateTagOptions>({
    name: '',
    commitSha: '',
    message: '',
    annotated: false,
  })
  const [pending, setPending] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const list = await api.get<TagEntry[]>(`${apiBase}/tags`)
      setEntries(list ?? [])
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [apiBase])

  useEffect(() => {
    void load()
  }, [load, reloadKey])

  const onCreate = async () => {
    if (!draft.name.trim()) return
    setPending('create')
    try {
      await api.post(`${apiBase}/tags`, draft)
      setShowCreate(false)
      setDraft({ name: '', commitSha: '', message: '', annotated: false })
      await load()
      onChange?.()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e))
    } finally {
      setPending(null)
    }
  }

  const onDelete = async (t: TagEntry, alsoRemote: boolean) => {
    if (!confirm(`Delete tag ${t.name}${alsoRemote ? ' (and remote)' : ''}?`)) return
    setPending(`delete:${t.name}`)
    try {
      const url = alsoRemote
        ? `${apiBase}/tags/${encodeURIComponent(t.name)}?remote=origin`
        : `${apiBase}/tags/${encodeURIComponent(t.name)}`
      await api.delete(url)
      await load()
      onChange?.()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e))
    } finally {
      setPending(null)
    }
  }

  const onPush = async (t: TagEntry) => {
    setPending(`push:${t.name}`)
    try {
      const opts: PushTagOptions = { name: t.name, remote: 'origin' }
      await api.post(`${apiBase}/tags/push`, opts)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e))
    } finally {
      setPending(null)
    }
  }

  return (
    <section className={styles.wrap} data-testid="git-tags">
      <header className={styles.header}>
        <h3 className={styles.title}>Tags</h3>
        <button
          className={styles.btn}
          onClick={() => setShowCreate((s) => !s)}
          data-testid="tags-create-toggle"
        >
          {showCreate ? 'Cancel' : 'New tag…'}
        </button>
      </header>

      {showCreate && (
        <div className={styles.createForm} data-testid="tags-create-form">
          <input
            className={styles.input}
            placeholder="Tag name (e.g. v1.0.0)"
            value={draft.name}
            onChange={(e) => setDraft((d) => ({ ...d, name: e.target.value }))}
            data-testid="tags-name"
          />
          <input
            className={styles.input}
            placeholder="Commit (default: HEAD)"
            value={draft.commitSha ?? ''}
            onChange={(e) => setDraft((d) => ({ ...d, commitSha: e.target.value }))}
            data-testid="tags-sha"
          />
          <input
            className={styles.input}
            placeholder="Annotated message (optional)"
            value={draft.message ?? ''}
            onChange={(e) =>
              setDraft((d) => ({ ...d, message: e.target.value, annotated: !!e.target.value }))
            }
            data-testid="tags-message"
          />
          <button
            className={`${styles.btn} ${styles.btnPrimary}`}
            disabled={pending === 'create' || !draft.name.trim()}
            onClick={onCreate}
            data-testid="tags-create-confirm"
          >
            {pending === 'create' ? 'Creating…' : 'Create'}
          </button>
        </div>
      )}

      {error && <p className={styles.error}>{error}</p>}
      {loading && entries.length === 0 && <p className={styles.empty}>Loading…</p>}
      {!loading && entries.length === 0 && <p className={styles.empty}>No tags.</p>}

      <ul className={styles.list}>
        {entries.map((t) => (
          <li key={t.name} className={styles.row} data-testid="tag-row" data-tag={t.name}>
            <span className={styles.name}>{t.name}</span>
            <span className={styles.kind}>{t.annotated ? 'annotated' : 'lightweight'}</span>
            <code className={styles.sha}>{t.commitSha?.slice(0, 7)}</code>
            <span className={styles.subject}>{t.subject}</span>
            <div className={styles.actions}>
              <button
                className={styles.linkBtn}
                onClick={() => void onPush(t)}
                disabled={pending === `push:${t.name}`}
                data-testid="tag-push-btn"
              >
                Push
              </button>
              <button
                className={styles.linkBtn}
                onClick={() => onDelete(t, false)}
                disabled={pending === `delete:${t.name}`}
                data-testid="tag-delete-btn"
              >
                Delete
              </button>
              <button
                className={`${styles.linkBtn} ${styles.danger}`}
                onClick={() => onDelete(t, true)}
                disabled={pending === `delete:${t.name}`}
                data-testid="tag-delete-remote-btn"
              >
                Delete + remote
              </button>
            </div>
          </li>
        ))}
      </ul>
    </section>
  )
}
