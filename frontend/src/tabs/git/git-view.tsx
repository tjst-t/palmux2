// GitView — top-level Git tab.
//
// S012 introduced the [Sync bar | View tabs | Body | Commit form]
// layout for the daily review-and-commit flow. S013 extends the View
// tabs with Stash and Tags, and adds two URL-driven sub-views — File
// History and Blame — that take over the body when the user navigates
// in via "Show history" / "Blame" actions.
//
// We keep tab selection in component state but accept ?fileHistory= and
// ?blame= search params so that the Files tab and ⌘K palette can deep
// link into the Git tab without us having to add separate URLs for each
// sub-view.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'

import { isMobile, useViewport } from '../../hooks/use-viewport'
import type { TabViewProps } from '../../lib/tab-registry'

import { GitBisect } from './git-bisect'
import { GitBlame } from './git-blame'
import { GitBranches } from './git-branches'
import { GitCommit } from './git-commit'
import { GitConflict } from './git-conflict'
import { GitDiff } from './git-diff'
import { GitFileHistory } from './git-file-history'
import { GitLog } from './git-log'
import { GitRebaseStatus } from './git-rebase-status'
import { GitReflog } from './git-reflog'
import { GitStash } from './git-stash'
import { GitStatus } from './git-status'
import { GitSubmodules } from './git-submodules'
import { GitSync } from './git-sync'
import { GitTags } from './git-tags'
import styles from './git-view.module.css'
import type { StatusReport } from './types'

type View =
  | 'status'
  | 'diff'
  | 'log'
  | 'branches'
  | 'stash'
  | 'tags'
  | 'conflict'
  | 'submodules'
  | 'reflog'
  | 'bisect'

// S023: tab definitions are shared between the desktop horizontal-tabs
// renderer and the mobile `<select>` dropdown so the two paths cannot
// drift. `testId` mirrors the prior horizontal-tab data-testid values.
const VIEW_DEFS: { value: View; label: string; testId?: string }[] = [
  { value: 'status', label: 'Status' },
  { value: 'diff', label: 'Diff' },
  { value: 'log', label: 'Log' },
  { value: 'branches', label: 'Branches' },
  { value: 'stash', label: 'Stash', testId: 'git-tab-stash' },
  { value: 'tags', label: 'Tags', testId: 'git-tab-tags' },
  { value: 'conflict', label: 'Conflict', testId: 'git-tab-conflict' },
  { value: 'submodules', label: 'Submodules', testId: 'git-tab-submodules' },
  { value: 'reflog', label: 'Reflog', testId: 'git-tab-reflog' },
  { value: 'bisect', label: 'Bisect', testId: 'git-tab-bisect' },
]

export function GitView({ repoId, branchId }: TabViewProps) {
  const viewport = useViewport()
  const mobile = isMobile(viewport)
  const [searchParams, setSearchParams] = useSearchParams()
  const isView = (v: string | null): v is View =>
    v === 'status' ||
    v === 'diff' ||
    v === 'log' ||
    v === 'branches' ||
    v === 'stash' ||
    v === 'tags' ||
    v === 'conflict' ||
    v === 'submodules' ||
    v === 'reflog' ||
    v === 'bisect'
  const initialView = ((): View => {
    const v = searchParams.get('view')
    return isView(v) ? v : 'status'
  })()
  const [view, setView] = useState<View>(initialView)
  const [report, setReport] = useState<StatusReport | null>(null)
  const [selectedDiffPath, setSelectedDiffPath] = useState<string | null>(null)
  const [reloadKey, setReloadKey] = useState(0)
  const fileHistoryPath = searchParams.get('fileHistory')
  const blamePath = searchParams.get('blame')
  const blameRev = searchParams.get('blameRev') ?? undefined

  // Honour ?view= changes from the command palette (or external links)
  // even after the tab is already mounted.
  useEffect(() => {
    const v = searchParams.get('view')
    if (isView(v)) {
      setView(v)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchParams])

  const apiBase = useMemo(
    () => `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/git`,
    [repoId, branchId],
  )

  const pushBtnRef = useRef<HTMLButtonElement | null>(null)
  const fetchBtnRef = useRef<HTMLButtonElement | null>(null)
  const commitTaRef = useRef<HTMLTextAreaElement | null>(null)

  const onMagitCommit = () => {
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

  const closeFileHistory = () => {
    const next = new URLSearchParams(searchParams)
    next.delete('fileHistory')
    setSearchParams(next, { replace: true })
  }
  const closeBlame = () => {
    const next = new URLSearchParams(searchParams)
    next.delete('blame')
    next.delete('blameRev')
    setSearchParams(next, { replace: true })
  }

  // When the user enters via fileHistory / blame, hide the Sync bar +
  // Commit form so the deep-link feels like a focused page rather than
  // a sub-view glued on top of the daily UX.
  const isHistoryOrBlame = !!fileHistoryPath || !!blamePath

  // Auto-clear deep-link params once the tab unmounts so coming back to
  // the Git tab from another tab returns the user to the Status view.
  useEffect(() => {
    return () => {
      // Don't clear on every render; only on full unmount this is fine
      // because `searchParams` lifecycle is owned by react-router.
    }
  }, [])

  if (fileHistoryPath) {
    return (
      <div className={styles.wrap}>
        <GitFileHistory apiBase={apiBase} path={fileHistoryPath} onClose={closeFileHistory} />
      </div>
    )
  }
  if (blamePath) {
    return (
      <div className={styles.wrap}>
        <GitBlame apiBase={apiBase} path={blamePath} revision={blameRev} onClose={closeBlame} />
      </div>
    )
  }

  return (
    <div className={styles.wrap}>
      {!isHistoryOrBlame && <GitSync apiBase={apiBase} onAfter={onAfter} />}
      {!isHistoryOrBlame && (
        <GitRebaseStatus apiBase={apiBase} reloadKey={reloadKey} onAfter={onAfter} />
      )}
      {mobile ? (
        // S023: mobile (< 600px) — the 10-tab horizontal row overflows
        // and is hard to tap. Switch to a native `<select>` so the OS
        // can render its own picker (better screen-reader behaviour and
        // larger tap targets out of the box). The selection mutates the
        // same `view` state so the body wiring below is unchanged.
        <header className={styles.tabsMobile} data-component="git-tab-dropdown">
          <select
            className={styles.tabSelect}
            value={view}
            onChange={(e) => setView(e.target.value as View)}
            aria-label="Git view"
            data-testid="git-tab-select"
          >
            {VIEW_DEFS.map((def) => (
              <option key={def.value} value={def.value}>
                {def.label}
              </option>
            ))}
          </select>
        </header>
      ) : (
        <header className={styles.tabs} data-component="git-tab-list">
          {VIEW_DEFS.map((def) => (
            <Tab
              key={def.value}
              active={view === def.value}
              onClick={() => setView(def.value)}
              testId={def.testId}
            >
              {def.label}
            </Tab>
          ))}
        </header>
      )}
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
        {view === 'log' && <GitLog apiBase={apiBase} reloadKey={reloadKey} />}
        {view === 'branches' && (
          <GitBranches key={reloadKey} apiBase={apiBase} onAfter={onAfter} />
        )}
        {view === 'stash' && (
          <GitStash apiBase={apiBase} reloadKey={reloadKey} onChange={onAfter} />
        )}
        {view === 'tags' && (
          <GitTags apiBase={apiBase} reloadKey={reloadKey} onChange={onAfter} />
        )}
        {view === 'conflict' && (
          <GitConflict apiBase={apiBase} reloadKey={reloadKey} onResolved={onAfter} />
        )}
        {view === 'submodules' && <GitSubmodules apiBase={apiBase} reloadKey={reloadKey} />}
        {view === 'reflog' && (
          <GitReflog apiBase={apiBase} reloadKey={reloadKey} onAfter={onAfter} />
        )}
        {view === 'bisect' && (
          <GitBisect apiBase={apiBase} reloadKey={reloadKey} onAfter={onAfter} />
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
  testId,
  children,
}: {
  active: boolean
  onClick: () => void
  testId?: string
  children: React.ReactNode
}) {
  return (
    <button
      className={active ? `${styles.tab} ${styles.tabActive}` : styles.tab}
      onClick={onClick}
      data-testid={testId}
    >
      {children}
    </button>
  )
}
