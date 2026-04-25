import { useNavigate, useParams } from 'react-router-dom'

import { selectBranchById, selectRepoById, usePalmuxStore } from '../stores/palmux-store'

import styles from './header.module.css'

export function Header() {
  const { repoId, branchId } = useParams()
  const repo = usePalmuxStore((s) => (repoId ? selectRepoById(repoId)(s) : undefined))
  const branch = usePalmuxStore((s) =>
    repoId && branchId ? selectBranchById(repoId, branchId)(s) : undefined,
  )
  const status = usePalmuxStore((s) => s.connectionStatus)
  const drawerPinned = usePalmuxStore((s) => s.deviceSettings.drawerPinned)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)
  const navigate = useNavigate()

  return (
    <header className={styles.header}>
      <div className={styles.left}>
        <button
          className={styles.iconBtn}
          onClick={() => setDeviceSetting('drawerPinned', !drawerPinned)}
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

function repoLabel(ghqPath: string): string {
  const parts = ghqPath.split('/')
  return parts.slice(1).join('/') || ghqPath
}
