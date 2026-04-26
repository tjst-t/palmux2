import { useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'

import type { Branch, Repository } from '../lib/api'
import { usePalmuxStore } from '../stores/palmux-store'

import { BranchPicker } from './branch-picker'
import { RepoPicker } from './repo-picker'
import styles from './drawer.module.css'

export function Drawer() {
  const repos = usePalmuxStore((s) => s.repos)
  const drawerWidth = usePalmuxStore((s) => s.deviceSettings.drawerWidth)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)
  const branchSortOrder = usePalmuxStore((s) => s.deviceSettings.branchSortOrder)

  const [pickerType, setPickerType] = useState<'repo' | { branchOf: string } | null>(null)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  // Auto-expand the active repo when its branch is mounted.
  const { repoId: activeRepo } = useParams()
  useEffect(() => {
    if (activeRepo) setExpanded((prev) => new Set(prev).add(activeRepo))
  }, [activeRepo])

  const starred = useMemo(() => repos.filter((r) => r.starred), [repos])
  const unstarred = useMemo(() => repos.filter((r) => !r.starred), [repos])

  return (
    <aside className={styles.drawer} style={{ width: drawerWidth }}>
      <div className={styles.scroll}>
        {starred.length > 0 && (
          <DrawerSection
            title="★ Starred"
            repos={starred}
            expanded={expanded}
            setExpanded={setExpanded}
            onAddBranch={(repoId) => setPickerType({ branchOf: repoId })}
            sortOrder={branchSortOrder}
          />
        )}
        <DrawerSection
          title="Repositories"
          repos={unstarred}
          expanded={expanded}
          setExpanded={setExpanded}
          onAddBranch={(repoId) => setPickerType({ branchOf: repoId })}
          sortOrder={branchSortOrder}
        />
      </div>
      <footer className={styles.footer}>
        <button className={styles.addRepoBtn} onClick={() => setPickerType('repo')}>
          + Open Repository…
        </button>
      </footer>
      <DrawerResizer
        width={drawerWidth}
        onChange={(w) => setDeviceSetting('drawerWidth', w)}
      />
      <RepoPicker open={pickerType === 'repo'} onClose={() => setPickerType(null)} />
      <BranchPicker
        open={typeof pickerType === 'object' && pickerType !== null}
        repoId={typeof pickerType === 'object' && pickerType !== null ? pickerType.branchOf : ''}
        onClose={() => setPickerType(null)}
      />
    </aside>
  )
}

function DrawerSection({
  title,
  repos,
  expanded,
  setExpanded,
  onAddBranch,
  sortOrder,
}: {
  title: string
  repos: Repository[]
  expanded: Set<string>
  setExpanded: React.Dispatch<React.SetStateAction<Set<string>>>
  onAddBranch: (repoId: string) => void
  sortOrder: 'name' | 'activity'
}) {
  return (
    <section className={styles.section}>
      <header className={styles.sectionHeader}>{title}</header>
      <ul className={styles.repoList}>
        {repos.map((repo) => (
          <RepoItem
            key={repo.id}
            repo={repo}
            expanded={expanded.has(repo.id)}
            onToggle={() =>
              setExpanded((prev) => {
                const next = new Set(prev)
                if (next.has(repo.id)) next.delete(repo.id)
                else next.add(repo.id)
                return next
              })
            }
            onAddBranch={() => onAddBranch(repo.id)}
            sortOrder={sortOrder}
          />
        ))}
      </ul>
    </section>
  )
}

function RepoItem({
  repo,
  expanded,
  onToggle,
  onAddBranch,
  sortOrder,
}: {
  repo: Repository
  expanded: boolean
  onToggle: () => void
  onAddBranch: () => void
  sortOrder: 'name' | 'activity'
}) {
  const navigate = useNavigate()
  const location = useLocation()
  const { repoId: activeRepo, branchId: activeBranch } = useParams()
  const star = usePalmuxStore((s) => s.starRepo)
  const closeRepo = usePalmuxStore((s) => s.closeRepo)

  const sortedBranches = useMemo(() => {
    const arr = [...repo.openBranches]
    if (sortOrder === 'activity') {
      arr.sort((a, b) => b.lastActivity.localeCompare(a.lastActivity))
    } else {
      arr.sort((a, b) => {
        if (a.isPrimary !== b.isPrimary) return a.isPrimary ? -1 : 1
        return a.name.localeCompare(b.name)
      })
    }
    return arr
  }, [repo.openBranches, sortOrder])

  const onContext = (e: React.MouseEvent) => {
    e.preventDefault()
    if (confirm(`Close repository ${repo.ghqPath}? Linked worktrees will be removed.`)) {
      void closeRepo(repo.id)
    }
  }

  return (
    <li className={styles.repo}>
      <div className={styles.repoRow} onContextMenu={onContext}>
        <button className={styles.repoToggle} onClick={onToggle} aria-expanded={expanded}>
          <span className={styles.repoChevron}>{expanded ? '▼' : '▶'}</span>
          <span className={styles.repoName}>{repoDisplayName(repo)}</span>
        </button>
        <button
          className={styles.starBtn}
          onClick={() => star(repo.id, !repo.starred)}
          aria-label={repo.starred ? 'Unstar' : 'Star'}
          title={repo.starred ? 'Unstar' : 'Star'}
        >
          {repo.starred ? '★' : '☆'}
        </button>
      </div>
      {expanded && (
        <ul className={styles.branchList}>
          {sortedBranches.map((branch) => (
            <BranchItem
              key={branch.id}
              branch={branch}
              repoId={repo.id}
              isActive={activeRepo === repo.id && activeBranch === branch.id}
              onSelect={(tab) => {
                const target = tab ?? branch.tabSet.tabs[0]?.id ?? 'claude'
                navigate(`/${repo.id}/${branch.id}/${encodeURIComponent(target)}${location.search}`)
              }}
            />
          ))}
          <li>
            <button className={styles.addBranchBtn} onClick={onAddBranch}>
              + Open Branch…
            </button>
          </li>
        </ul>
      )}
    </li>
  )
}

function BranchItem({
  branch,
  repoId,
  isActive,
  onSelect,
}: {
  branch: Branch
  repoId: string
  isActive: boolean
  onSelect: (tabId?: string) => void
}) {
  const closeBranch = usePalmuxStore((s) => s.closeBranch)
  const onContext = (e: React.MouseEvent) => {
    e.preventDefault()
    if (confirm(`Close branch ${branch.name}?`)) {
      void closeBranch(repoId, branch.id)
    }
  }
  return (
    <li>
      <button
        className={isActive ? `${styles.branch} ${styles.branchActive}` : styles.branch}
        onClick={() => onSelect()}
        onContextMenu={onContext}
        title={branch.worktreePath}
      >
        <span className={styles.branchName}>{branch.name}</span>
        {branch.isPrimary && <span className={styles.primaryTag}>main</span>}
        {isActive && <span className={styles.activeDot}>●</span>}
      </button>
    </li>
  )
}

function repoDisplayName(repo: { ghqPath: string }): string {
  // Drop the host prefix: "github.com/owner/repo" → "owner/repo"
  const parts = repo.ghqPath.split('/')
  return parts.slice(1).join('/') || repo.ghqPath
}

function DrawerResizer({ width, onChange }: { width: number; onChange: (w: number) => void }) {
  const [dragging, setDragging] = useState(false)
  useEffect(() => {
    if (!dragging) return
    const onMove = (e: MouseEvent) => {
      const next = Math.min(600, Math.max(200, e.clientX))
      onChange(next)
    }
    const onUp = () => setDragging(false)
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
    return () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
  }, [dragging, onChange])
  return (
    <div
      className={styles.resizer}
      onMouseDown={() => setDragging(true)}
      style={{ left: width - 4 }}
      role="separator"
      aria-orientation="vertical"
    />
  )
}
