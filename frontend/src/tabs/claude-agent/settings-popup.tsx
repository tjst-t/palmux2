// Settings popup — view + delete entries from `.claude/settings.json`.
//
// Two scopes are loaded side-by-side (project = `<worktree>/.claude/`,
// user = `~/.claude/`). Only `permissions.allow` entries get a delete
// button — everything else (deny, hooks, custom keys) is read-only by
// design (S002 scope; see docs/sprint-logs/S002/decisions.md).
//
// The delete flow is:
//   1. user clicks ✕ on an entry
//   2. window.confirm asks "Remove <pattern> from <scope> scope?"
//   3. DELETE …/settings/permissions/allow?scope=…&pattern=…
//   4. on success, optimistically drop the entry from local state
//      AND refetch so the path / hooks block reflect the on-disk truth
//
// "Reflected in the CLI immediately" (the acceptance criterion): the CLI
// re-reads settings.json on each tool decision, so once the file is
// rewritten the next can_use_tool will skip the old allow rule. We do
// not need to respawn the CLI for this.

import { useCallback, useEffect, useState } from 'react'

import { api, ApiError } from '../../lib/api'
import { Modal } from '../../components/modal'

import styles from './settings-popup.module.css'

type SettingsScope = 'project' | 'user'

interface SettingsView {
  path: string
  exists: boolean
  permissionsAllow: string[]
  permissionsDeny: string[]
  hooks?: unknown
  other?: Record<string, unknown>
  parseError?: string
}

interface SettingsBundle {
  project: SettingsView
  user: SettingsView
}

interface Props {
  repoId: string
  branchId: string
  open: boolean
  onClose: () => void
}

export function SettingsPopup({ repoId, branchId, open, onClose }: Props) {
  const [bundle, setBundle] = useState<SettingsBundle | null>(null)
  // loading defaults to true so the modal opens with a "Loading…" message
  // rather than a flash of "no bundle". The fetch effect flips it to false
  // on completion.
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busyKey, setBusyKey] = useState<string | null>(null)

  const fetchBundle = useCallback(() => {
    setLoading(true)
    setError(null)
    return api
      .get<SettingsBundle>(
        `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(
          branchId,
        )}/tabs/claude/settings`,
      )
      .then((data) => {
        setBundle(normaliseBundle(data))
      })
      .catch((e) => {
        const msg = e instanceof ApiError ? e.message : String(e)
        setError(msg)
      })
      .finally(() => {
        setLoading(false)
      })
  }, [repoId, branchId])

  useEffect(() => {
    if (!open) return
    let cancelled = false
    api
      .get<SettingsBundle>(
        `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(
          branchId,
        )}/tabs/claude/settings`,
      )
      .then((data) => {
        if (!cancelled) {
          setBundle(normaliseBundle(data))
          setLoading(false)
        }
      })
      .catch((e) => {
        if (!cancelled) {
          const msg = e instanceof ApiError ? e.message : String(e)
          setError(msg)
          setLoading(false)
        }
      })
    return () => {
      cancelled = true
    }
  }, [open, repoId, branchId])

  const onDeleteAllow = useCallback(
    async (scope: SettingsScope, pattern: string) => {
      const ok = window.confirm(
        `Remove "${pattern}" from the ${scope === 'project' ? 'project (.claude/settings.json)' : 'user (~/.claude/settings.json)'} permissions.allow list?\n\nThis edits the file directly.`,
      )
      if (!ok) return
      const key = `${scope}:${pattern}`
      setBusyKey(key)
      setError(null)
      try {
        const url =
          `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}` +
          `/tabs/claude/settings/permissions/allow` +
          `?scope=${encodeURIComponent(scope)}&pattern=${encodeURIComponent(pattern)}`
        await api.delete(url)
        // Optimistic local update; refetch in the background to reconcile.
        setBundle((prev) => {
          if (!prev) return prev
          const next: SettingsBundle = {
            project: { ...prev.project },
            user: { ...prev.user },
          }
          const view = next[scope]
          view.permissionsAllow = view.permissionsAllow.filter((e) => e !== pattern)
          return next
        })
        await fetchBundle()
      } catch (e) {
        const msg = e instanceof ApiError ? e.message : String(e)
        setError(msg)
      } finally {
        setBusyKey(null)
      }
    },
    [repoId, branchId, fetchBundle],
  )

  return (
    <Modal open={open} onClose={onClose} title="Claude settings.json" width={760}>
      <div className={styles.body}>
        <div className={styles.banner}>
          Edits here change the actual <code>settings.json</code> files on disk.
          The CLI re-reads them on the next tool call. Removing an{' '}
          <code>allow</code> entry will cause Palmux to prompt for permission again
          the next time the matching tool runs.
        </div>

        {error && <div className={styles.errorBanner}>{error}</div>}

        {loading && !bundle ? (
          <div className={styles.loading}>Loading…</div>
        ) : bundle ? (
          <div className={styles.scopeGrid}>
            <ScopeCard
              scope="project"
              view={bundle.project}
              busyKey={busyKey}
              onDeleteAllow={onDeleteAllow}
            />
            <ScopeCard
              scope="user"
              view={bundle.user}
              busyKey={busyKey}
              onDeleteAllow={onDeleteAllow}
            />
          </div>
        ) : null}

        <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
          <button
            type="button"
            className={styles.refreshBtn}
            onClick={() => void fetchBundle()}
            disabled={loading}
          >
            {loading ? 'Refreshing…' : 'Refresh'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

interface ScopeCardProps {
  scope: SettingsScope
  view: SettingsView
  busyKey: string | null
  onDeleteAllow: (scope: SettingsScope, pattern: string) => void
}

function ScopeCard({ scope, view, busyKey, onDeleteAllow }: ScopeCardProps) {
  const badgeClass =
    scope === 'project' ? styles.scopeBadgeProject : styles.scopeBadgeUser
  const badgeLabel = scope === 'project' ? 'project' : 'user'
  const headline =
    scope === 'project' ? '.claude/settings.json' : '~/.claude/settings.json'
  return (
    <section className={styles.scopeCard}>
      <header className={styles.scopeHeader}>
        <h3 className={styles.scopeTitle}>
          {headline}
          <span className={`${styles.scopeBadge} ${badgeClass}`}>{badgeLabel}</span>
        </h3>
      </header>
      <div className={styles.scopePath} title={view.path}>{view.path}</div>

      {!view.exists ? (
        <div className={styles.empty}>No file yet.</div>
      ) : view.parseError ? (
        <div className={styles.parseError}>Parse error: {view.parseError}</div>
      ) : (
        <>
          <Section title="permissions.allow">
            {view.permissionsAllow.length === 0 ? (
              <div className={styles.empty}>(empty)</div>
            ) : (
              <ul className={styles.entryList}>
                {view.permissionsAllow.map((p) => {
                  const key = `${scope}:${p}`
                  const busy = busyKey === key
                  return (
                    <li key={p} className={styles.entryRow}>
                      <span className={styles.entryText} title={p}>{p}</span>
                      <button
                        type="button"
                        className={styles.deleteBtn}
                        onClick={() => onDeleteAllow(scope, p)}
                        disabled={busy}
                        title={`Remove from ${scope} scope`}
                      >
                        {busy ? '…' : 'Remove'}
                      </button>
                    </li>
                  )
                })}
              </ul>
            )}
          </Section>

          <Section title="permissions.deny (read-only)">
            {view.permissionsDeny.length === 0 ? (
              <div className={styles.empty}>(empty)</div>
            ) : (
              <ul className={styles.entryList}>
                {view.permissionsDeny.map((p) => (
                  <li key={p} className={styles.entryRow}>
                    <span className={styles.entryText} title={p}>{p}</span>
                  </li>
                ))}
              </ul>
            )}
          </Section>

          {view.hooks !== undefined && (
            <Section title="hooks (read-only)">
              <pre className={styles.rawBlock}>{stringifyJSON(view.hooks)}</pre>
            </Section>
          )}

          {view.other && Object.keys(view.other).length > 0 && (
            <Section title="other (read-only)">
              <pre className={styles.rawBlock}>{stringifyJSON(view.other)}</pre>
            </Section>
          )}
        </>
      )}
    </section>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className={styles.section}>
      <h4 className={styles.sectionTitle}>{title}</h4>
      {children}
    </div>
  )
}

function stringifyJSON(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

// normaliseBundle protects against the server omitting fields (json
// `omitempty` + an empty allow list arrives as undefined).
function normaliseBundle(b: SettingsBundle): SettingsBundle {
  return {
    project: normaliseView(b.project),
    user: normaliseView(b.user),
  }
}

function normaliseView(v: SettingsView): SettingsView {
  return {
    path: v.path,
    exists: !!v.exists,
    permissionsAllow: v.permissionsAllow ?? [],
    permissionsDeny: v.permissionsDeny ?? [],
    hooks: v.hooks,
    other: v.other,
    parseError: v.parseError,
  }
}
