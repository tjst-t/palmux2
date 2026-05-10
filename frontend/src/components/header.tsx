import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'

import { useViewport } from '../hooks/use-viewport'
import { selectBranchById, selectRepoById, usePalmuxStore } from '../stores/palmux-store'

import { useCommandPaletteStore } from './command-palette/store'
import { ActivityInbox } from './inbox/activity-inbox'
import { UploadsIndicator } from './uploads/uploads-indicator'
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
            {branch.runtime && (
              <RuntimeChip kind={branch.runtime.kind} state={branch.runtime.state} />
            )}
          </span>
        )}
      </div>
      <div className={styles.right}>
        <UploadsIndicator />
        <ActivityInbox />
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

// Sdd4ce1-5-5: Header chip showing the active Workspace's runtime kind
// + state (text-only — round-2 user feedback removed all icons). State
// is conveyed by a coloured dot (green=ready, amber=starting, red=failed,
// grey=stopped/stopping); the text label is the runtime kind.
function RuntimeChip({ kind, state }: { kind: string; state: string }) {
  const stateClass = stateClassFor(state)
  return (
    <span className={styles.runtimeChip} data-testid="header-runtime-chip" title={`runtime: ${kind} · ${state}`}>
      <span className={`${styles.runtimeDot} ${stateClass}`} aria-hidden="true" />
      <span className={styles.runtimeLabel}>{kind}</span>
      <span className={styles.runtimeSep}>·</span>
      <span className={styles.runtimeState}>{state}</span>
    </span>
  )
}

function stateClassFor(state: string): string {
  switch (state) {
    case 'ready':
      return styles.runtimeDotReady ?? ''
    case 'starting':
      return styles.runtimeDotStarting ?? ''
    case 'failed':
      return styles.runtimeDotFailed ?? ''
    case 'stopping':
      return styles.runtimeDotStopping ?? ''
    default:
      return styles.runtimeDotStopped ?? ''
  }
}
