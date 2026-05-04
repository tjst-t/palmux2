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

import { api } from '../lib/api'
import type { Repository } from '../lib/api'
import { usePalmuxStore } from '../stores/palmux-store'

import styles from './repo-picker.module.css'

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

  const [filter, setFilter] = useState('')
  const [active, setActive] = useState(0)
  const [pending, setPending] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [cloneState, setCloneState] = useState<CloneState>('idle')
  const listRef = useRef<HTMLUListElement | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const isURL = detectCloneURL(filter)

  useEffect(() => {
    if (!open) return
    setError(null)
    setFilter('')
    setActive(0)
    setCloneState('idle')
    void reload()
    return () => {
      abortRef.current?.abort()
    }
  }, [open, reload])

  const filtered = useMemo(() => {
    if (isURL) return []
    const q = filter.toLowerCase()
    return repos
      .filter((r) => !r.open)
      .filter((r) => !q || r.ghqPath.toLowerCase().includes(q))
      .sort((a, b) => a.ghqPath.localeCompare(b.ghqPath))
  }, [repos, filter, isURL])

  // Keep `active` in range.
  useEffect(() => {
    const len = isURL ? 1 : filtered.length
    if (active >= len) setActive(Math.max(0, len - 1))
  }, [filtered.length, active, isURL])

  // Scroll highlighted row into view.
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

  const clone = async () => {
    if (!isURL) return
    abortRef.current?.abort()
    const ac = new AbortController()
    abortRef.current = ac
    setCloneState('cloning')
    setError(null)

    try {
      const result = await api.post<{ repoId: string; ghqPath: string; fullPath: string }>(
        '/api/repos/clone',
        { url: filter.trim() },
      )
      // Auto-open the repo (it was already opened server-side, just reload).
      await reloadRepos()
      // Open the primary branch if available.
      try {
        const reloadedRepos = usePalmuxStore.getState().repos
        const repo: Repository | undefined = reloadedRepos.find((r) => r.id === result.repoId)
        if (repo) {
          const primary = repo.openBranches.find((b) => b.isPrimary)
          if (primary) {
            // Branch already opened server-side; just reload to get tabs.
          }
        }
      } catch {
        // best-effort
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
      const max = isURL ? 0 : filtered.length - 1
      setActive((i) => Math.min(max, i + 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((i) => Math.max(0, i - 1))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (isURL) {
        void clone()
      } else {
        const target = filtered[active]
        if (target) void pick(target.id)
      }
    }
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
              const isActive = i === active
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
