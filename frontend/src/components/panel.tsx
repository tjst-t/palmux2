import { useEffect, useMemo } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'

import { selectBranchById, selectRepoById, usePalmuxStore } from '../stores/palmux-store'
import { TabContent } from '../tabs/tab-content'

import { RightPanelSelector } from './right-panel-selector'
import { TabBar } from './tab-bar'
import styles from './main-area.module.css'

export interface PanelTarget {
  repoId?: string
  branchId?: string
  tabId?: string
}

interface Props {
  side: 'left' | 'right'
  target: PanelTarget
  focused: boolean
  onFocus: () => void
  onRightTargetChange?: (next: PanelTarget) => void
  full?: boolean
}

export function Panel({ side, target, focused, onFocus, onRightTargetChange, full }: Props) {
  const { repoId, branchId, tabId } = target
  const branch = usePalmuxStore((s) =>
    repoId && branchId ? selectBranchById(repoId, branchId)(s) : undefined,
  )
  const repo = usePalmuxStore((s) => (repoId ? selectRepoById(repoId)(s) : undefined))

  const decodedTabId = tabId ? decodeURIComponent(tabId) : undefined
  const activeTab = useMemo(
    () => branch?.tabSet.tabs.find((t) => t.id === decodedTabId),
    [branch, decodedTabId],
  )

  // S009-fix-1: legacy URL fold-up. The pre-S009 single Claude tab had id
  // "claude"; post-S009 the canonical id is "claude:claude". Old links
  // (bookmarks, server-side notifications, the s008 test) and any caller
  // that still navigates to `/claude` would land on a "Pick a tab" stub
  // because no tab in the live list has id="claude". Detect that exact
  // mismatch on the LEFT panel only (the right panel is selector-driven)
  // and rewrite the URL to the canonical id.
  const navigate = useNavigate()
  const location = useLocation()
  useEffect(() => {
    if (side !== 'left') return
    if (!repoId || !branchId || !decodedTabId || !branch) return
    if (activeTab) return
    // Only rewrite when the bare type matches a known multi-instance
    // type — this is the legacy single-instance shape. Anything else
    // (typo, deleted tab, etc.) we leave to the existing "Pick a tab"
    // empty state.
    const bareTypes = new Set(['claude', 'bash'])
    if (!bareTypes.has(decodedTabId)) return
    const canonical = branch.tabSet.tabs.find(
      (t) => t.id === `${decodedTabId}:${decodedTabId}`,
    )
    if (!canonical) return
    navigate(
      `/${encodeURIComponent(repoId)}/${encodeURIComponent(branchId)}/${encodeURIComponent(canonical.id)}${location.search}`,
      { replace: true },
    )
  }, [side, repoId, branchId, decodedTabId, branch, activeTab, navigate, location.search])

  const className =
    (full ? `${styles.panel} ${styles.panelFull}` : styles.panel) +
    (focused && side === 'right' ? ` ${styles.panelFocusBorder}` : '')

  return (
    <section
      className={className}
      onPointerDownCapture={onFocus}
      onFocusCapture={onFocus}
      aria-label={side === 'left' ? 'Left panel' : 'Right panel'}
    >
      {side === 'left' ? (
        branch ? (
          <TabBar branch={branch} />
        ) : null
      ) : (
        <RightPanelSelector
          target={target}
          repo={repo}
          branch={branch}
          onChange={onRightTargetChange ?? (() => {})}
        />
      )}
      <div className={styles.panelBody}>
        {!repoId || !branchId ? (
          <EmptyMessage side={side} />
        ) : !branch ? (
          <div className={styles.panelEmpty}>
            <p>Branch not open.</p>
          </div>
        ) : activeTab ? (
          <TabContent tab={activeTab} repoId={repoId} branchId={branchId} />
        ) : (
          <div className={styles.panelEmpty}>
            <p>Pick a tab.</p>
          </div>
        )}
      </div>
    </section>
  )
}

function EmptyMessage({ side }: { side: 'left' | 'right' }) {
  return (
    <div className={styles.panelEmpty}>
      <p>
        {side === 'left'
          ? 'Pick a branch from the drawer.'
          : 'Pick a repository and branch for the right panel.'}
      </p>
    </div>
  )
}
