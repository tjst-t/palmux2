import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'

import { useViewport } from '../hooks/use-viewport'
import { selectBranchById, selectRepoById, usePalmuxStore } from '../stores/palmux-store'

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
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)
  const mobileDrawerOpen = usePalmuxStore((s) => s.mobileDrawerOpen)
  const setMobileDrawerOpen = usePalmuxStore((s) => s.setMobileDrawerOpen)
  const navigate = useNavigate()
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
        <button
          className={styles.iconBtn}
          onClick={() => navigate('/')}
          title="Home"
          aria-label="Home"
        >
          ⌂
        </button>
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
