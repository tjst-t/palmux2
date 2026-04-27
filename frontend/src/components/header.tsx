import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'

import { useViewport } from '../hooks/use-viewport'
import { api, type PortmanLease } from '../lib/api'
import { selectBranchById, selectRepoById, usePalmuxStore } from '../stores/palmux-store'

import { useCommandPaletteStore } from './command-palette/store'
import { ActivityInbox } from './inbox/activity-inbox'
import styles from './header.module.css'

const SPLIT_MIN_WIDTH = 900

export function Header() {
  const { repoId, branchId } = useParams()
  const repo = usePalmuxStore((s) => (repoId ? selectRepoById(repoId)(s) : undefined))
  const branch = usePalmuxStore((s) =>
    repoId && branchId ? selectBranchById(repoId, branchId)(s) : undefined,
  )
  const status = usePalmuxStore((s) => s.connectionStatus)
  const drawerPinned = usePalmuxStore((s) => s.deviceSettings.drawerPinned)
  const splitEnabled = usePalmuxStore((s) => s.deviceSettings.splitEnabled)
  const theme = usePalmuxStore((s) => s.deviceSettings.theme)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)
  const mobileDrawerOpen = usePalmuxStore((s) => s.mobileDrawerOpen)
  const setMobileDrawerOpen = usePalmuxStore((s) => s.setMobileDrawerOpen)
  const portmanURL = usePalmuxStore((s) => s.serverInfo.portmanURL)
  const portmanLeases = useRepoPortmanLeases(repoId, branchId)
  const showPalette = useCommandPaletteStore((s) => s.show)
  const wide = useWideViewport(SPLIT_MIN_WIDTH)
  const viewport = useViewport()
  const mobile = viewport === 'mobile'

  const onToggleDrawer = () => {
    if (mobile) {
      setMobileDrawerOpen(!mobileDrawerOpen)
    } else {
      setDeviceSetting('drawerPinned', !drawerPinned)
    }
  }

  return (
    <header className={styles.header}>
      <div className={styles.left}>
        <button
          className={styles.iconBtn}
          onClick={onToggleDrawer}
          title="Toggle drawer"
          aria-label="Toggle drawer"
        >
          ☰
        </button>
        <span className={styles.brand}>Palmux v2</span>
        {branch && repo && (
          <span className={styles.branch}>
            <span className={styles.repoName}>{repoLabel(repo.ghqPath)}</span>
            <span className={styles.sep}>/</span>
            <span className={styles.branchName}>{branch.name}</span>
          </span>
        )}
      </div>
      <div className={styles.right}>
        <ActivityInbox />
        {portmanLeases.length > 0 && <PortmanLinks leases={portmanLeases} />}
        <button
          className={styles.iconBtn}
          onClick={() => showPalette()}
          title="Command palette (⌘K / Ctrl+K)"
          aria-label="Command palette"
        >
          ⌘
        </button>
        <button
          className={styles.iconBtn}
          onClick={() => setDeviceSetting('theme', theme === 'dark' ? 'light' : 'dark')}
          title={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
          aria-label="Toggle theme"
        >
          {theme === 'dark' ? '☾' : '☀'}
        </button>
        {portmanURL && (
          <a
            className={styles.iconBtn}
            href={portmanURL}
            target="_blank"
            rel="noopener noreferrer"
            title="Open portman dashboard"
            aria-label="Portman"
          >
            P
          </a>
        )}
        {repo && githubURL(repo.ghqPath, branch?.name) && (
          <a
            className={styles.iconBtn}
            href={githubURL(repo.ghqPath, branch?.name)!}
            target="_blank"
            rel="noopener noreferrer"
            title="Open on GitHub"
            aria-label="Open on GitHub"
          >
            ⎘
          </a>
        )}
        {wide && (
          <button
            className={
              splitEnabled ? `${styles.iconBtn} ${styles.iconBtnActive}` : styles.iconBtn
            }
            onClick={() => setDeviceSetting('splitEnabled', !splitEnabled)}
            title={splitEnabled ? 'Disable split' : 'Enable split'}
            aria-label="Toggle split panel"
            aria-pressed={splitEnabled}
          >
            ▥
          </button>
        )}
        <span className={`${styles.dot} ${styles[status]}`} title={status} />
      </div>
    </header>
  )
}

function useWideViewport(threshold: number): boolean {
  const [wide, setWide] = useState(() =>
    typeof window === 'undefined' ? true : window.innerWidth >= threshold,
  )
  useEffect(() => {
    if (typeof window === 'undefined') return
    const mql = window.matchMedia(`(min-width: ${threshold}px)`)
    const onChange = () => setWide(mql.matches)
    onChange()
    mql.addEventListener('change', onChange)
    return () => mql.removeEventListener('change', onChange)
  }, [threshold])
  return wide
}

function repoLabel(ghqPath: string): string {
  const parts = ghqPath.split('/')
  return parts.slice(1).join('/') || ghqPath
}

// useRepoPortmanLeases polls /api/repos/.../portman every 10s while the
// active branch is open. The endpoint always 200s (empty list when portman
// isn't installed), so this hook stays quiet on hosts without it.
function useRepoPortmanLeases(repoId: string | undefined, branchId: string | undefined): PortmanLease[] {
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

// PortmanLinks shows a single button; clicking opens a popover listing
// each LIVE portman-exposed service for the current branch. Stale leases
// are dropped so we never link to a dead service.
function PortmanLinks({ leases }: { leases: PortmanLease[] }) {
  const [open, setOpen] = useState(false)
  const live = leases.filter((l) => l.expose && l.status === 'listening')
  if (live.length === 0) return null
  return (
    <div style={{ position: 'relative', display: 'inline-flex' }}>
      <button
        className={styles.iconBtn}
        onClick={() => setOpen((v) => !v)}
        title={`${live.length} portman service${live.length === 1 ? '' : 's'} live`}
        aria-label="Portman services"
      >
        🌐
      </button>
      {open && <PortmanPopover leases={live} onClose={() => setOpen(false)} />}
    </div>
  )
}

function PortmanPopover({
  leases,
  onClose,
}: {
  leases: PortmanLease[]
  onClose: () => void
}) {
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
    <div
      data-portman-popover
      style={{
        position: 'fixed',
        top: 'calc(var(--header-height) + 4px)',
        right: 12,
        minWidth: 280,
        backgroundColor: 'var(--color-elevated)',
        border: '1px solid var(--color-border)',
        borderRadius: 'var(--radius-md)',
        boxShadow: '0 16px 32px rgba(0,0,0,0.5)',
        zIndex: 120,
        overflow: 'hidden',
      }}
    >
      <header
        style={{
          padding: '8px 12px',
          borderBottom: '1px solid var(--color-border)',
          fontSize: 12,
          fontWeight: 600,
          color: 'var(--color-fg)',
          fontFamily: 'var(--font-mono)',
        }}
      >
        Portman services
      </header>
      <ul style={{ listStyle: 'none', margin: 0, padding: '4px 0' }}>
        {leases.map((l) => (
          <li key={l.name}>
            <a
              href={l.url}
              target="_blank"
              rel="noopener noreferrer"
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                padding: '6px 12px',
                fontSize: 12,
                color: 'var(--color-fg)',
                textDecoration: 'none',
                fontFamily: 'var(--font-mono)',
              }}
            >
              <span
                style={{
                  width: 8,
                  height: 8,
                  borderRadius: '50%',
                  backgroundColor: l.status === 'listening' ? 'var(--color-success)' : 'var(--color-fg-faint)',
                  flexShrink: 0,
                }}
              />
              <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {l.name}
              </span>
              <span style={{ color: 'var(--color-fg-muted)', fontSize: 11 }}>:{l.port}</span>
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
