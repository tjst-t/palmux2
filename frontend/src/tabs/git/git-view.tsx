// GitView — top-level Git tab. S012 reorganises the layout:
//
//   [ Sync bar (Push / Pull / Fetch / Force…) ]
//   [ View tabs: Status | Diff | Log | Branches ]
//   [ Body — selected view ]
//   [ Commit form (only on Status & Diff views) ]
//
// Status view auto-refreshes via the worktreewatch event stream
// (`git.statusChanged`); commit / push / pull / fetch trigger an
// immediate refresh too. The Magit-style single-key shortcuts live in
// GitStatus; we wire `c` / `p` / `f` callbacks here so they can hop
// between sub-views.

import { useCallback, useMemo, useRef, useState } from 'react'

import type { TabViewProps } from '../../lib/tab-registry'

import { GitBranches } from './git-branches'
import { GitCommit } from './git-commit'
import { GitDiff } from './git-diff'
import { GitLog } from './git-log'
import { GitStatus } from './git-status'
import { GitSync } from './git-sync'
import styles from './git-view.module.css'
import type { StatusReport } from './types'

type View = 'status' | 'diff' | 'log' | 'branches'

export function GitView({ repoId, branchId }: TabViewProps) {
  const [view, setView] = useState<View>('status')
  const [report, setReport] = useState<StatusReport | null>(null)
  const [selectedDiffPath, setSelectedDiffPath] = useState<string | null>(null)
  // Bumping this counter forces remounted children to re-fetch (used by
  // GitSync / GitBranches as the simplest way to refresh after a write).
  const [reloadKey, setReloadKey] = useState(0)
  const apiBase = useMemo(
    () => `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/git`,
    [repoId, branchId],
  )

  // Refs let GitStatus signal magit `p` / `f` to GitSync without
  // hoisting the busy state up. Each ref holds a click-trigger that
  // GitSync attaches to its buttons.
  const pushBtnRef = useRef<HTMLButtonElement | null>(null)
  const fetchBtnRef = useRef<HTMLButtonElement | null>(null)
  const commitTaRef = useRef<HTMLTextAreaElement | null>(null)

  const onMagitCommit = () => {
    // Scroll to and focus the commit textarea.
    const ta =
      commitTaRef.current ??
      (document.querySelector(
        '[data-testid="git-commit-message"]',
      ) as HTMLTextAreaElement | null)
    ta?.focus()
    ta?.scrollIntoView({ block: 'center' })
  }
  const onMagitPush = () => {
    const btn = (document.querySelector(
      '[data-testid="git-push-btn"]',
    ) as HTMLButtonElement | null) ?? pushBtnRef.current
    btn?.click()
  }
  const onMagitFetch = () => {
    const btn = (document.querySelector(
      '[data-testid="git-fetch-btn"]',
    ) as HTMLButtonElement | null) ?? fetchBtnRef.current
    btn?.click()
  }

  const stagedCount = report?.staged?.length ?? 0

  const onAfter = useCallback(() => {
    setReloadKey((k) => k + 1)
  }, [])

  return (
    <div className={styles.wrap}>
      <GitSync apiBase={apiBase} onAfter={onAfter} />
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
        {view === 'status' && (
          <GitStatus
            apiBase={apiBase}
            repoId={repoId}
            branchId={branchId}
            onJumpToDiff={(path) => {
              setSelectedDiffPath(path)
              setView('diff')
            }}
            onReport={setReport}
            onMagitCommit={onMagitCommit}
            onMagitPush={onMagitPush}
            onMagitFetch={onMagitFetch}
          />
        )}
        {view === 'diff' && <GitDiff apiBase={apiBase} initialPath={selectedDiffPath ?? undefined} />}
        {view === 'log' && <GitLog apiBase={apiBase} />}
        {view === 'branches' && (
          <GitBranches key={reloadKey} apiBase={apiBase} onAfter={onAfter} />
        )}
      </div>
      {(view === 'status' || view === 'diff') && (
        <GitCommit
          apiBase={apiBase}
          repoId={repoId}
          branchId={branchId}
          stagedCount={stagedCount}
          onCommitted={onAfter}
        />
      )}
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
