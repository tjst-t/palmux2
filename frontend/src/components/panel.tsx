import { useMemo } from 'react'

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
