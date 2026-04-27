// Workspace-scoped header buttons. They belong next to the TabBar (left
// panel) or the right-panel selector (split mode), not in the app-global
// Header, because GitHub / portman links change per repo+branch.

import { useEffect, useState } from 'react'

import { api, type Branch, type PortmanLease, type Repository } from '../lib/api'

import styles from './workspace-actions.module.css'

interface Props {
  repoId: string
  branchId: string
  repo: Repository | undefined
  branch: Branch | undefined
}

export function WorkspaceActions({ repoId, branchId, repo, branch }: Props) {
  const ghURL = repo ? githubURL(repo.ghqPath, branch?.name) : null
  const leases = useRepoPortmanLeases(repoId, branchId)
  const live = leases.filter((l) => l.expose && l.status === 'listening')
  return (
    <div className={styles.wrap}>
      {live.length > 0 && <PortmanLinks leases={live} />}
      {ghURL && (
        <a
          className={styles.btn}
          href={ghURL}
          target="_blank"
          rel="noopener noreferrer"
          title="Open on GitHub"
          aria-label="Open on GitHub"
        >
          <GithubMark />
        </a>
      )}
    </div>
  )
}

function useRepoPortmanLeases(repoId: string, branchId: string): PortmanLease[] {
  const [leases, setLeases] = useState<PortmanLease[]>([])
  useEffect(() => {
    if (!repoId || !branchId) {
      setLeases([])
      return
    }
    let cancelled = false
    const fetchOnce = () =>
      api
        .get<PortmanLease[]>(
          `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/portman`,
        )
        .then((res) => {
          if (!cancelled) setLeases(res ?? [])
        })
        .catch(() => {})
    void fetchOnce()
    const t = window.setInterval(fetchOnce, 10000)
    return () => {
      cancelled = true
      window.clearInterval(t)
    }
  }, [repoId, branchId])
  return leases
}

function PortmanLinks({ leases }: { leases: PortmanLease[] }) {
  const [open, setOpen] = useState(false)
  return (
    <div className={styles.popoverHost}>
      <button
        className={styles.btn}
        onClick={() => setOpen((v) => !v)}
        title={`${leases.length} portman service${leases.length === 1 ? '' : 's'} live`}
        aria-label="Portman services"
      >
        🌐
      </button>
      {open && <PortmanPopover leases={leases} onClose={() => setOpen(false)} />}
    </div>
  )
}

function PortmanPopover({ leases, onClose }: { leases: PortmanLease[]; onClose: () => void }) {
  useEffect(() => {
    const onPointer = (e: PointerEvent) => {
      const t = e.target as Element
      if (!t.closest('[data-portman-popover]')) onClose()
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('pointerdown', onPointer, true)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', onPointer, true)
      window.removeEventListener('keydown', onKey)
    }
  }, [onClose])
  return (
    <div data-portman-popover className={styles.popover}>
      <header className={styles.popoverHead}>Portman services</header>
      <ul className={styles.popoverList}>
        {leases.map((l) => (
          <li key={l.name}>
            <a
              href={l.url}
              target="_blank"
              rel="noopener noreferrer"
              className={styles.popoverRow}
            >
              <span className={styles.dotLive} />
              <span className={styles.popoverName}>{l.name}</span>
              <span className={styles.popoverPort}>:{l.port}</span>
            </a>
          </li>
        ))}
      </ul>
    </div>
  )
}

function githubURL(ghqPath: string, branchName?: string): string | null {
  if (!ghqPath.startsWith('github.com/')) return null
  const repo = ghqPath.slice('github.com/'.length)
  if (!repo.includes('/')) return null
  const base = `https://github.com/${repo}`
  return branchName ? `${base}/tree/${encodeURIComponent(branchName)}` : base
}

// Official "mark-github" silhouette from primer/octicons (MIT-licensed).
function GithubMark() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 16 16"
      width="16"
      height="16"
      fill="currentColor"
      aria-hidden="true"
    >
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z" />
    </svg>
  )
}
