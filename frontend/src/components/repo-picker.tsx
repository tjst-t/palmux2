// S030: Enhanced RepoPicker — unified browse + clone.
//
// When the user pastes a URL (http(s)://, git@host:, or owner/repo shorthand)
// the modal switches into "clone" mode: it pins a "Clone <url>" affordance at
// the top of the list (as per prototype/open-repo-modal-clone-detected.html).
// Pressing Enter or clicking the clone row calls POST /api/repos/clone, then
// auto-opens the repo + primary branch.
//
// Browse mode (no URL detected) matches prototype/open-repo-modal-browse.html.
// Cloning in progress matches prototype/open-repo-modal-cloning.html.
// Clone error matches prototype/open-repo-modal-clone-error.html.

import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'

import { api } from '../lib/api'
import type { Repository } from '../lib/api'
import { usePalmuxStore } from '../stores/palmux-store'

import { RuntimeSelector } from './runtime-selector'
import styles from './repo-picker.module.css'

// hotfix: after a successful open / clone we navigate to the opened
// repo so the user lands on the freshly-Open repository instead of
// staying wherever they were. Pick the primary branch's claude tab,
// fall back to the first available tab.
function urlForRepo(repo: Repository): string | null {
  const branch = repo.openBranches.find((b) => b.isPrimary) ?? repo.openBranches[0]
  if (!branch) return null
  const tab =
    branch.tabSet.tabs.find((t) => t.type === 'claude') ?? branch.tabSet.tabs[0]
  if (!tab) return null
  return `/${encodeURIComponent(repo.id)}/${encodeURIComponent(branch.id)}/${encodeURIComponent(tab.id)}`
}

interface Props {
  open: boolean
  onClose: () => void
  /** hotfix: when supplied, each browse-mode row shows a small × that
   *  invokes this callback (typically opens RepoDeleteModal at the
   *  Drawer level so we don't need to mount it twice). */
  onRequestDelete?: (repoId: string, ghqPath: string) => void
}

type CloneState = 'idle' | 'cloning' | 'error'

/** Returns true when the string looks like a clonable URL or shorthand. */
function detectCloneURL(s: string): boolean {
  const t = s.trim()
  if (!t) return false
  if (t.startsWith('http://') || t.startsWith('https://')) return true
  if (t.startsWith('git@')) return true
  // owner/repo shorthand: exactly two /-separated tokens, no spaces.
  const parts = t.split('/')
  if (parts.length === 2 && !t.includes(' ') && !t.includes(':')) return true
  return false
}

/** Extract a short "owner/repo" label from any URL for display. */
function shortRepoLabel(url: string): string {
  let s = url.trim()
  // Strip .git
  s = s.replace(/\.git$/, '')
  // git@github.com:owner/repo → owner/repo
  if (s.startsWith('git@')) {
    const after = s.replace(/^git@[^:]+:/, '')
    const parts = after.split('/')
    return parts.slice(-2).join('/')
  }
  // https://host/owner/repo → owner/repo
  for (const pf of ['https://', 'http://']) {
    if (s.startsWith(pf)) {
      const parts = s.slice(pf.length).split('/')
      return parts.slice(-2).join('/')
    }
  }
  return s
}

export function RepoPicker({ open, onClose, onRequestDelete }: Props) {
  const reload = usePalmuxStore((s) => s.reloadAvailableRepos)
  const repos = usePalmuxStore((s) => s.availableRepos)
  const openRepo = usePalmuxStore((s) => s.openRepo)
  const reloadRepos = usePalmuxStore((s) => s.reloadRepos)
  const runtimeCaps = usePalmuxStore((s) => s.runtimeCaps)
  const loadRuntimeCaps = usePalmuxStore((s) => s.loadRuntimeCaps)
  const patchWorkspaceRuntime = usePalmuxStore((s) => s.patchWorkspaceRuntime)
  const navigate = useNavigate()

  const [filter, setFilter] = useState('')
  const [active, setActive] = useState(0)
  const [pending, setPending] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [cloneState, setCloneState] = useState<CloneState>('idle')
  // S8478ca-5: selected runtime kind for the repo about to be opened/cloned
  // (undefined = not yet chosen; RuntimeSelector shows its own default —
  // incus-container when available — and pick()/clone() below resolve the
  // same effective default before applying it, so the UI's visible choice
  // always matches what actually gets used).
  const [runtimeKind, setRuntimeKind] = useState<'host' | 'incus-container' | undefined>(undefined)
  const [runtimeError, setRuntimeError] = useState<string | null>(null)
  const listRef = useRef<HTMLUListElement | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const isURL = detectCloneURL(filter)

  // S13b16a-4: Reset transient modal state inline when `open` flips
  // false→true (the "deriving state from props" idiom). The async
  // reload() + AbortController stay in useEffect because they're
  // genuine side effects.
  const [openedOnce, setOpenedOnce] = useState(false)
  if (open && !openedOnce) {
    setOpenedOnce(true)
    setError(null)
    setFilter('')
    setActive(0)
    setCloneState('idle')
  } else if (!open && openedOnce) {
    setOpenedOnce(false)
  }

  useEffect(() => {
    if (!open) return
    void reload()
    // S8478ca-5: load runtime caps when picker opens (idempotent if already loaded)
    void loadRuntimeCaps().catch(() => {})
    return () => {
      abortRef.current?.abort()
    }
  }, [open, reload, loadRuntimeCaps])

  const filtered = useMemo(() => {
    if (isURL) return []
    const q = filter.toLowerCase()
    return repos
      .filter((r) => !r.open)
      .filter((r) => !q || r.ghqPath.toLowerCase().includes(q))
      .sort((a, b) => a.ghqPath.localeCompare(b.ghqPath))
  }, [repos, filter, isURL])

  // S13b16a-4: clamp `active` to the available row count via derived
  // value (computed during render). The previous useEffect+setState
  // version produced an extra render; this one renders the right value
  // first time. We still keep `setActive` calls from event handlers so
  // user-driven navigation persists; whenever the list shrinks we
  // simply clamp on the way out.
  const maxIdx = isURL ? 0 : Math.max(0, filtered.length - 1)
  const clampedActive = Math.min(active, maxIdx)

  // Scroll highlighted row into view.
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-row="${clampedActive}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [clampedActive])

  // S8478ca-5 hotfix: resolve the same "incus if available, else host"
  // default RuntimeSelector shows visually (effective = value ?? default),
  // so a user who never touches the selector still gets the runtime the UI
  // displayed as selected — not a silent fall-through to the server's own
  // "host" default. Applied to the newly-opened repo's PRIMARY branch right
  // after open/clone, before the user has had a chance to type anything in
  // its terminal, so there is no "unsaved work" risk this PATCH could lose
  // — PROVIDED the repo was genuinely not-yet-open before this call. Callers
  // MUST pass `wasAlreadyOpen` computed from a pre-call snapshot: pasting a
  // URL for an already-open repo (clone() has no "not open" filter the way
  // browse mode's `filtered` list does) or a race with another device/tab
  // opening the same repo first must NOT silently PATCH a live session's
  // runtime out from under it — that's exactly the destructive,
  // needs-confirmation case the (correctly-scoped, still-intact) Header
  // runtime chip's own RuntimeChangeConfirm dialog exists for.
  const applyChosenRuntime = async (repo: Repository, wasAlreadyOpen: boolean) => {
    if (wasAlreadyOpen) return
    const incusAvailable =
      runtimeCaps?.kinds.find((k) => k.kind === 'incus-container')?.available ?? false
    const effective = runtimeKind ?? (incusAvailable ? 'incus-container' : 'host')
    const branch = repo.openBranches.find((b) => b.isPrimary) ?? repo.openBranches[0]
    if (!branch) {
      // e.g. a detached-HEAD worktree that buildBranchFromWorktree skips —
      // openBranches can legitimately be empty right after open. Surface
      // this rather than silently dropping the chosen runtime.
      console.warn(
        `palmux: opened ${repo.id} but found no branch to apply the chosen runtime (${effective}) to`,
      )
      return
    }
    try {
      await patchWorkspaceRuntime(repo.id, branch.id, effective)
    } catch (err) {
      // Non-fatal: the repo is already open on whatever the server
      // defaulted to (host). Don't block navigation, but don't pretend
      // this didn't happen either — the modal is about to unmount, so
      // `runtimeError`/the picker's own error box can't display this.
      console.error(`palmux: failed to apply chosen runtime (${effective}) to ${repo.id}:`, err)
    }
  }

  const pick = async (id: string) => {
    setPending(id)
    setError(null)
    try {
      // `repos` in this component's scope is availableRepos (not-yet-open
      // candidates) — the actual open-repos list lives at the top-level
      // store slice, so check that one for the pre-call snapshot.
      const wasAlreadyOpen = usePalmuxStore
        .getState()
        .repos.some((r) => r.id === id)
      const repo = await openRepo(id)
      await applyChosenRuntime(repo, wasAlreadyOpen)
      // hotfix: navigate the user to the freshly-opened repo so the
      // drawer focus + main-area both reflect their action. Without
      // this, the modal closes and the user is left wherever they
      // were before — the new repo only shows up in the drawer list.
      const target = urlForRepo(repo)
      if (target) navigate(target)
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  const clone = async () => {
    if (!isURL) return
    abortRef.current?.abort()
    const ac = new AbortController()
    abortRef.current = ac
    setCloneState('cloning')
    setError(null)

    try {
      // Snapshot BEFORE the clone call: a pasted URL can resolve to a repo
      // that's already open (clone mode has no "not open" pre-filter the
      // way browse mode's `filtered` list does), and we must not treat
      // that as a fresh open eligible for a runtime-changing PATCH.
      const preCloneRepos = usePalmuxStore.getState().repos
      const result = await api.post<{ repoId: string; ghqPath: string; fullPath: string }>(
        '/api/repos/clone',
        { url: filter.trim() },
      )
      const wasAlreadyOpen = preCloneRepos.some((r) => r.id === result.repoId)
      // Auto-open the repo (it was already opened server-side, just reload).
      await reloadRepos()
      // hotfix: navigate to the cloned repo's primary branch claude tab
      // so the user lands on the new repo immediately. Mirrors the same
      // post-open jump that browse-mode pick() performs.
      const reloadedRepos = usePalmuxStore.getState().repos
      const repo = reloadedRepos.find((r) => r.id === result.repoId)
      if (repo) {
        await applyChosenRuntime(repo, wasAlreadyOpen)
        const target = urlForRepo(repo)
        if (target) navigate(target)
      }
      onClose()
    } catch (err) {
      if ((err as Error).name === 'AbortError') return
      setCloneState('error')
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  const cancelClone = () => {
    abortRef.current?.abort()
    setCloneState('idle')
    setError(null)
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (cloneState === 'cloning') return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActive((i) => Math.min(maxIdx, Math.min(i, maxIdx) + 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((i) => Math.max(0, Math.min(i, maxIdx) - 1))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (isURL) {
        void clone()
      } else {
        const target = filtered[clampedActive]
        if (target) void pick(target.id)
      }
    }
  }

  // S8478ca-5 hotfix: this picker only ever lists repos that are NOT open
  // yet (`filtered` above filters `!r.open`), so selecting a runtime here
  // is always "choose the runtime this new workspace will open with" —
  // never "change an already-running workspace's runtime" (that flow
  // belongs to the Header's runtime chip, which owns its own confirm
  // dialog for the real "this will restart your tmux session" case). No
  // confirmation is needed or shown here; the effective choice is applied
  // by applyChosenRuntime() once the repo actually opens, above.
  const handleRuntimeChange = (kind: 'host' | 'incus-container') => {
    setRuntimeKind(kind)
    setRuntimeError(null)
  }

  const handleClose = () => {
    if (cloneState === 'cloning') {
      cancelClone()
    }
    onClose()
  }

  if (!open) return null

  const label = isURL ? shortRepoLabel(filter) : ''

  return (
    <div className={styles.overlay} onClick={handleClose} data-testid="open-repo-modal">
      <div
        className={styles.card}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="open-repo-title"
      >
        {/* Header */}
        <div className={styles.header}>
          <h2 className={styles.title} id="open-repo-title">Open Repository</h2>
          <p className={styles.sub}>
            {cloneState === 'cloning'
              ? 'Cloning — Cancel to keep working, the session will pick up where it left off.'
              : cloneState === 'error'
              ? 'Clone failed — check the URL and credentials, then retry.'
              : isURL
              ? <>URL detected — Press <kbd className={styles.kbd}>↵</kbd> to clone, or pick from the list below.</>
              : 'Filter your ghq repositories — or paste a URL to clone a new one.'}
          </p>
        </div>

        {/* Input */}
        <div className={styles.inputRow}>
          <input
            autoFocus
            className={`${styles.input} ${isURL ? styles.inputURL : ''} ${cloneState === 'error' ? styles.inputError : ''}`}
            type="text"
            placeholder="Filter by name, or paste a URL to clone…"
            value={filter}
            disabled={cloneState === 'cloning'}
            data-testid="open-repo-input"
            onChange={(e) => {
              setFilter(e.target.value)
              setActive(0)
              setCloneState('idle')
              setError(null)
            }}
            onKeyDown={handleKeyDown}
          />
        </div>

        {/* S8478ca-5: Runtime selector — shown when not cloning */}
        {cloneState !== 'cloning' && (
          <div style={{ padding: '0 18px 12px' }}>
            <RuntimeSelector
              value={runtimeKind}
              caps={runtimeCaps}
              onChange={handleRuntimeChange}
              error={runtimeError}
              disabled={pending !== null}
            />
          </div>
        )}

        {/* Body */}
        {cloneState === 'cloning' ? (
          <div className={styles.cloningBody}>
            <div className={styles.cloningCard}>
              <span className={styles.spinner} aria-hidden="true" />
              <div>
                <div className={styles.cloningLabel}>
                  Cloning <code>{label}</code>…
                </div>
                <div className={styles.cloningMeta}>
                  ghq get · auto-open primary branch on success
                </div>
              </div>
            </div>
          </div>
        ) : cloneState === 'error' && error ? (
          <div className={styles.errorBody}>
            <pre className={styles.errorBox} data-testid="open-repo-error">{error}</pre>
            <p className={styles.errorTip}>
              Tip: ensure your SSH agent has the right key, or use the HTTPS URL with a personal access token.
            </p>
          </div>
        ) : (
          <ul className={styles.list} ref={listRef}>
            {isURL && (
              <>
                <li className={styles.section}>Clone new</li>
                <li>
                  <button
                    className={`${styles.row} ${styles.rowActive} ${styles.rowClone}`}
                    data-row={0}
                    data-testid="open-repo-clone-row"
                    onClick={() => void clone()}
                    disabled={pending !== null}
                  >
                    <span className={styles.cloneIcon}>⤓</span>
                    <span className={styles.rowLabel}>
                      Clone <code className={styles.cloneCode}>{label}</code>
                      <span className={styles.rowMeta}>→ ghq get</span>
                    </span>
                    <span className={styles.rowState}>new</span>
                  </button>
                </li>
                <li className={styles.section}>No matching local repo</li>
                <li className={styles.empty}>{label} is not yet on this machine.</li>
              </>
            )}
            {!isURL && filtered.map((r, i) => {
              const isActive = i === clampedActive
              return (
                <li key={r.id} className={styles.rowItem}>
                  <button
                    data-row={i}
                    className={isActive ? `${styles.row} ${styles.rowActive}` : styles.row}
                    disabled={pending !== null}
                    onMouseEnter={() => setActive(i)}
                    onClick={() => pick(r.id)}
                  >
                    <span className={styles.ghqIcon}>⌂</span>
                    <span className={styles.rowName}>{r.ghqPath}</span>
                    <span className={styles.rowState}>{r.starred ? '★' : ''}</span>
                  </button>
                  {onRequestDelete && (
                    <button
                      type="button"
                      className={styles.rowDeleteBtn}
                      title={`Delete ${r.ghqPath}`}
                      aria-label={`Delete repository ${r.ghqPath}`}
                      data-testid={`open-repo-row-delete-${r.id}`}
                      disabled={pending !== null}
                      onClick={(e) => {
                        e.stopPropagation()
                        onRequestDelete(r.id, r.ghqPath)
                      }}
                    >
                      🗑
                    </button>
                  )}
                </li>
              )
            })}
            {!isURL && filtered.length === 0 && filter && (
              <li className={styles.empty}>No matching repositories.</li>
            )}
            {!isURL && filtered.length === 0 && !filter && (
              <li className={styles.section}>Available · ghq tracked, not yet open</li>
            )}
          </ul>
        )}

        {/* Footer */}
        <div className={styles.footer}>
          {cloneState === 'cloning' ? (
            <>
              <button
                className={styles.btnGhost}
                data-testid="open-repo-cancel"
                onClick={cancelClone}
              >
                Cancel clone
              </button>
            </>
          ) : cloneState === 'error' ? (
            <>
              <button className={styles.btnGhost} onClick={handleClose}>Cancel</button>
              <button
                className={styles.btnPrimary}
                data-testid="open-repo-retry"
                onClick={() => { setCloneState('idle'); setError(null); void clone() }}
              >
                Retry clone
              </button>
            </>
          ) : isURL ? (
            <>
              <span><kbd className={styles.kbd}>↵</kbd> clone</span>
              <span><kbd className={styles.kbd}>Esc</kbd> cancel</span>
              <span className={styles.footerTip}>Detects http(s):// · git@host: · owner/repo shorthand</span>
            </>
          ) : (
            <>
              <span><kbd className={styles.kbd}>↑</kbd><kbd className={styles.kbd}>↓</kbd> navigate</span>
              <span><kbd className={styles.kbd}>↵</kbd> open</span>
              <span className={styles.footerTip}>Tip: paste a URL to clone instead</span>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
