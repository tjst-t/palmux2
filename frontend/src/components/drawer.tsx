import { useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'

import { useLongPress } from '../hooks/use-long-press'
import type { Branch, Repository } from '../lib/api'
import { selectBranchNotifications, usePalmuxStore } from '../stores/palmux-store'

import { BranchPicker } from './branch-picker'
import { confirmDialog } from './context-menu/confirm-dialog'
import { useContextMenu } from './context-menu/store'
import { OrphanAttachModal } from './orphan/orphan-modal'
import { RepoPicker } from './repo-picker'
import styles from './drawer.module.css'

export function Drawer() {
  const repos = usePalmuxStore((s) => s.repos)
  const drawerWidth = usePalmuxStore((s) => s.deviceSettings.drawerWidth)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)
  const branchSortOrder = usePalmuxStore((s) => s.deviceSettings.branchSortOrder)
  const orphanSessions = usePalmuxStore((s) => s.orphanSessions)
  const reloadOrphanSessions = usePalmuxStore((s) => s.reloadOrphanSessions)

  const [pickerType, setPickerType] = useState<'repo' | { branchOf: string } | null>(null)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [orphanTarget, setOrphanTarget] = useState<{
    name: string
    idx: number
    windowName?: string
  } | null>(null)

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
        <OrphanSection
          sessions={orphanSessions}
          onAttach={(name, idx, windowName) => setOrphanTarget({ name, idx, windowName })}
          onRefresh={reloadOrphanSessions}
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
      {orphanTarget && (
        <OrphanAttachModal
          sessionName={orphanTarget.name}
          windowIdx={orphanTarget.idx}
          windowName={orphanTarget.windowName}
          onClose={() => setOrphanTarget(null)}
        />
      )}
    </aside>
  )
}

function OrphanSection({
  sessions,
  onAttach,
  onRefresh,
}: {
  sessions: { name: string; attached: boolean; windows: { index: number; name: string }[] }[]
  onAttach: (sessionName: string, idx: number, windowName: string) => void
  onRefresh: () => void
}) {
  // The Orphans section is collapsed by default — most users don't have any
  // and the noise of a permanently-visible empty header is annoying.
  const [sectionOpen, setSectionOpen] = useState(false)
  const [openSessions, setOpenSessions] = useState<Set<string>>(new Set())

  return (
    <section className={styles.section}>
      <header
        className={styles.sectionHeader}
        title="Tmux sessions Palmux did not create"
        style={{ display: 'flex', alignItems: 'center', cursor: 'pointer' }}
        onClick={() => setSectionOpen((v) => !v)}
      >
        <span className={styles.repoChevron} style={{ marginRight: 6 }}>
          {sectionOpen ? '▼' : '▶'}
        </span>
        <span style={{ flex: 1 }}>Orphans{sessions.length > 0 ? ` (${sessions.length})` : ''}</span>
        <button
          className={styles.addBranchBtn}
          onClick={(e) => {
            e.stopPropagation()
            onRefresh()
          }}
          title="Refresh orphan list"
          aria-label="Refresh"
          style={{ width: 'auto', padding: '0 8px' }}
        >
          ↻
        </button>
      </header>
      {sectionOpen && sessions.length === 0 && (
        <p
          style={{
            margin: 0,
            padding: '4px 14px 8px',
            fontSize: 11,
            color: 'var(--color-fg-muted)',
          }}
        >
          No non-Palmux tmux sessions.
        </p>
      )}
      {sectionOpen && sessions.length > 0 && (
        <ul className={styles.repoList}>
          {sessions.map((s) => {
            const expanded = openSessions.has(s.name)
            return (
              <li key={s.name} className={styles.repo}>
                <div className={styles.repoRow}>
                  <button
                    className={styles.repoToggle}
                    onClick={() =>
                      setOpenSessions((prev) => {
                        const next = new Set(prev)
                        if (next.has(s.name)) next.delete(s.name)
                        else next.add(s.name)
                        return next
                      })
                    }
                    aria-expanded={expanded}
                  >
                    <span className={styles.repoChevron}>{expanded ? '▼' : '▶'}</span>
                    <span className={styles.repoName} title={s.name}>
                      {s.name}
                    </span>
                  </button>
                </div>
                {expanded && (
                  <ul className={styles.branchList}>
                    {s.windows.map((w) => (
                      <li key={w.index}>
                        <button
                          className={styles.branch}
                          onClick={() => onAttach(s.name, w.index, w.name)}
                        >
                          <span className={styles.branchName}>
                            {w.index}: {w.name}
                          </span>
                        </button>
                      </li>
                    ))}
                  </ul>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </section>
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
  const showContextMenu = useContextMenu()

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

  const showMenuAt = (x: number, y: number) => {
    showContextMenu(
      [
        { type: 'heading', label: repoDisplayName(repo) },
        {
          label: repo.starred ? 'Unstar' : 'Star',
          onClick: () => star(repo.id, !repo.starred),
        },
        { type: 'separator' },
        {
          label: 'Close repository',
          danger: true,
          onClick: async () => {
            const ok = await confirmDialog.ask({
              title: 'Close repository?',
              message: `${repo.ghqPath} will be removed from Palmux. Linked worktrees stay on disk.`,
              confirmLabel: 'Close',
              danger: true,
            })
            if (ok) await closeRepo(repo.id)
          },
        },
      ],
      x,
      y,
    )
  }
  const longPress = useLongPress((x, y) => showMenuAt(x, y))

  return (
    <li className={styles.repo}>
      <div
        className={styles.repoRow}
        onContextMenu={(e) => {
          e.preventDefault()
          showMenuAt(e.clientX, e.clientY)
        }}
        {...longPress}
      >
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
  const notifs = usePalmuxStore(selectBranchNotifications(repoId, branch.id))
  const unread = notifs?.unreadCount ?? 0
  const showContextMenu = useContextMenu()
  const showMenuAt = (x: number, y: number) => {
    showContextMenu(
      [
        { type: 'heading', label: branch.name },
        {
          label: 'Close branch',
          danger: true,
          onClick: async () => {
            const ok = await confirmDialog.ask({
              title: 'Close branch?',
              message: branch.isPrimary
                ? `${branch.name} is the primary worktree. The tmux session will be killed but the worktree stays on disk.`
                : `${branch.name}'s tmux session will be killed and its worktree removed.`,
              confirmLabel: 'Close',
              danger: true,
            })
            if (ok) await closeBranch(repoId, branch.id)
          },
        },
      ],
      x,
      y,
    )
  }
  const longPress = useLongPress((x, y) => showMenuAt(x, y))
  return (
    <li>
      <button
        className={isActive ? `${styles.branch} ${styles.branchActive}` : styles.branch}
        onClick={() => onSelect()}
        onContextMenu={(e) => {
          e.preventDefault()
          showMenuAt(e.clientX, e.clientY)
        }}
        {...longPress}
        title={
          notifs?.lastMessage
            ? `${branch.worktreePath}\n${notifs.lastMessage}`
            : branch.worktreePath
        }
      >
        <span className={styles.branchName}>{branch.name}</span>
        {branch.isPrimary && <span className={styles.primaryTag}>main</span>}
        {unread > 0 && <span className={styles.notifyDot} aria-label={`${unread} unread`} />}
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
