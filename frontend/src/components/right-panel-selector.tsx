import type { Branch, Repository } from '../lib/api'
import { usePalmuxStore } from '../stores/palmux-store'
import type { PanelTarget } from './panel'

import styles from './right-panel-selector.module.css'

interface Props {
  target: PanelTarget
  repo: Repository | undefined
  branch: Branch | undefined
  onChange: (next: PanelTarget) => void
}

export function RightPanelSelector({ target, repo, branch, onChange }: Props) {
  const repos = usePalmuxStore((s) => s.repos)

  const onRepoChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const id = e.target.value
    if (!id) {
      onChange({})
      return
    }
    const next = repos.find((r) => r.id === id)
    const firstBranch = next?.openBranches[0]
    const firstTab = firstBranch?.tabSet.tabs[0]
    onChange({
      repoId: id,
      branchId: firstBranch?.id,
      tabId: firstTab?.id,
    })
  }

  const onBranchChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    if (!repo) return
    const id = e.target.value
    const nextBranch = repo.openBranches.find((b) => b.id === id)
    const firstTab = nextBranch?.tabSet.tabs[0]
    onChange({
      repoId: repo.id,
      branchId: id,
      tabId: firstTab?.id,
    })
  }

  const decodedTabId = target.tabId ? decodeURIComponent(target.tabId) : undefined

  return (
    <div className={styles.bar} role="tablist">
      <select
        className={styles.select}
        value={target.repoId ?? ''}
        onChange={onRepoChange}
        title="Repository"
      >
        <option value="">— repo —</option>
        {repos.map((r) => (
          <option key={r.id} value={r.id}>
            {repoLabel(r.ghqPath)}
          </option>
        ))}
      </select>
      <span className={styles.sep}>/</span>
      <select
        className={styles.select}
        value={target.branchId ?? ''}
        onChange={onBranchChange}
        disabled={!repo || repo.openBranches.length === 0}
        title="Branch"
      >
        <option value="">— branch —</option>
        {repo?.openBranches.map((b) => (
          <option key={b.id} value={b.id}>
            {b.name}
          </option>
        ))}
      </select>
      <div className={styles.tabs}>
        {branch?.tabSet.tabs.map((t) => {
          const active = t.id === decodedTabId
          return (
            <button
              key={t.id}
              role="tab"
              aria-selected={active}
              className={active ? `${styles.tab} ${styles.tabActive}` : styles.tab}
              onClick={() =>
                onChange({
                  repoId: target.repoId,
                  branchId: target.branchId,
                  tabId: t.id,
                })
              }
              title={t.name}
            >
              <span className={styles.tabIcon}>{iconFor(t.type)}</span>
              <span className={styles.tabLabel}>{t.name}</span>
            </button>
          )
        })}
      </div>
    </div>
  )
}

function repoLabel(ghqPath: string): string {
  const parts = ghqPath.split('/')
  return parts.slice(1).join('/') || ghqPath
}

function iconFor(type: string): string {
  switch (type) {
    case 'claude':
      return '🧠'
    case 'bash':
      return '$'
    case 'files':
      return '📁'
    case 'git':
      return '⎇'
    default:
      return '•'
  }
}
