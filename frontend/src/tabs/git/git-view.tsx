import { useMemo, useState } from 'react'

import type { TabViewProps } from '../../lib/tab-registry'

import { GitBranches } from './git-branches'
import { GitDiff } from './git-diff'
import { GitLog } from './git-log'
import { GitStatus } from './git-status'
import styles from './git-view.module.css'

type View = 'status' | 'diff' | 'log' | 'branches'

export function GitView({ repoId, branchId }: TabViewProps) {
  const [view, setView] = useState<View>('status')
  const apiBase = useMemo(
    () => `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/git`,
    [repoId, branchId],
  )

  return (
    <div className={styles.wrap}>
      <header className={styles.tabs}>
        <Tab active={view === 'status'} onClick={() => setView('status')}>
          Status
        </Tab>
        <Tab active={view === 'diff'} onClick={() => setView('diff')}>
          Diff
        </Tab>
        <Tab active={view === 'log'} onClick={() => setView('log')}>
          Log
        </Tab>
        <Tab active={view === 'branches'} onClick={() => setView('branches')}>
          Branches
        </Tab>
      </header>
      <div className={styles.body}>
        {view === 'status' && <GitStatus apiBase={apiBase} onJumpToDiff={() => setView('diff')} />}
        {view === 'diff' && <GitDiff apiBase={apiBase} />}
        {view === 'log' && <GitLog apiBase={apiBase} />}
        {view === 'branches' && <GitBranches apiBase={apiBase} />}
      </div>
    </div>
  )
}

function Tab({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button className={active ? `${styles.tab} ${styles.tabActive}` : styles.tab} onClick={onClick}>
      {children}
    </button>
  )
}
