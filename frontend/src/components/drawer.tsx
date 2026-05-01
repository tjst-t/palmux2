import { useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'

import { useLongPress } from '../hooks/use-long-press'
import { useSectionCollapsed } from '../hooks/use-section-collapsed'
import type { Branch, BranchCategory, Repository } from '../lib/api'
import {
  selectAgentState,
  selectBranchNotifications,
  usePalmuxStore,
  type AgentStatus,
} from '../stores/palmux-store'

import { BranchPicker } from './branch-picker'
import { confirmDialog } from './context-menu/confirm-dialog'
import { useContextMenu } from './context-menu/store'
import { OrphanAttachModal } from './orphan/orphan-modal'
import { RepoPicker } from './repo-picker'
import styles from './drawer.module.css'

// S015: drawer category metadata. The order here is the rendering order
// inside each repo (my → unmanaged → subagent). The FE remaps the
// server-side `user` value to the user-facing label `my`.
type CategoryKey = 'my' | 'unmanaged' | 'subagent'
const CATEGORY_ORDER: CategoryKey[] = ['my', 'unmanaged', 'subagent']
const CATEGORY_DEFAULT_COLLAPSED: Record<CategoryKey, boolean> = {
  my: false,
  unmanaged: false,
  subagent: true,
}
const CATEGORY_LABELS: Record<CategoryKey, string> = {
  my: 'my',
  unmanaged: 'unmanaged',
  subagent: 'subagent / autopilot',
}

function categoryKey(category: BranchCategory | undefined): CategoryKey {
  if (category === 'subagent') return 'subagent'
  if (category === 'unmanaged') return 'unmanaged'
  // Treat undefined / 'user' alike: a missing category means the server is
  // pre-S015 (or hasn't applied derivation yet). Bucket as `my` so the
  // user's own branches still show up in the most prominent section.
  return 'my'
}

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

  // S015: bucket branches by category, then sort within each bucket.
  const branchesByCategory = useMemo(() => {
    const buckets: Record<CategoryKey, Branch[]> = {
      my: [],
      unmanaged: [],
      subagent: [],
    }
    for (const b of repo.openBranches) {
      buckets[categoryKey(b.category)].push(b)
    }
    const sorter = (a: Branch, b: Branch) => {
      if (sortOrder === 'activity') return b.lastActivity.localeCompare(a.lastActivity)
      if (a.isPrimary !== b.isPrimary) return a.isPrimary ? -1 : 1
      return a.name.localeCompare(b.name)
    }
    for (const k of CATEGORY_ORDER) buckets[k].sort(sorter)
    return buckets
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
        <div className={styles.branchList}>
          {CATEGORY_ORDER.map((key) => {
            const branches = branchesByCategory[key]
            // Hide empty `unmanaged` and `subagent` sub-sections to avoid
            // visual noise. Always render `my` so the `+ Open Branch…`
            // affordance has a stable home.
            if (branches.length === 0 && key !== 'my') return null
            return (
              <BranchSubSection
                key={key}
                categoryKey={key}
                branches={branches}
                repoId={repo.id}
                activeRepoId={activeRepo ?? null}
                activeBranchId={activeBranch ?? null}
                onSelect={(branch, tab) => {
                  const target = tab ?? branch.tabSet.tabs[0]?.id ?? 'claude'
                  navigate(
                    `/${repo.id}/${branch.id}/${encodeURIComponent(target)}${location.search}`,
                  )
                }}
                onAddBranch={onAddBranch}
              />
            )
          })}
        </div>
      )}
    </li>
  )
}

// BranchSubSection renders one categorised group (my / unmanaged /
// subagent) inside a repo. Header is clickable to toggle collapsed state
// (persisted to localStorage). The `+ Open Branch…` action lives only in
// the `my` section; promote (`+ Add to my worktrees`) lives on each row of
// the `unmanaged` section.
function BranchSubSection({
  categoryKey: cat,
  branches,
  repoId,
  activeRepoId,
  activeBranchId,
  onSelect,
  onAddBranch,
}: {
  categoryKey: CategoryKey
  branches: Branch[]
  repoId: string
  activeRepoId: string | null
  activeBranchId: string | null
  onSelect: (branch: Branch, tabId?: string) => void
  onAddBranch: () => void
}) {
  const [collapsed, setCollapsed] = useSectionCollapsed(
    cat,
    CATEGORY_DEFAULT_COLLAPSED[cat],
  )
  const showCount = cat === 'subagent' || cat === 'unmanaged'
  return (
    <section className={styles.branchSection}>
      <button
        type="button"
        className={styles.branchSectionHeader}
        onClick={() => setCollapsed(!collapsed)}
        aria-expanded={!collapsed}
        data-section={cat}
      >
        <span className={styles.branchSectionChevron}>{collapsed ? '▶' : '▼'}</span>
        <span className={styles.branchSectionTitle}>{CATEGORY_LABELS[cat]}</span>
        {showCount && branches.length > 0 && (
          <span className={styles.branchSectionBadge}>{branches.length}</span>
        )}
      </button>
      {!collapsed && (
        <ul className={styles.repoList}>
          {branches.map((branch) => (
            <BranchItem
              key={branch.id}
              branch={branch}
              repoId={repoId}
              category={cat}
              isActive={activeRepoId === repoId && activeBranchId === branch.id}
              onSelect={(tab) => onSelect(branch, tab)}
            />
          ))}
          {cat === 'my' && (
            <li>
              <button className={styles.addBranchBtn} onClick={onAddBranch}>
                + Open Branch…
              </button>
            </li>
          )}
        </ul>
      )}
    </section>
  )
}

function BranchItem({
  branch,
  repoId,
  category,
  isActive,
  onSelect,
}: {
  branch: Branch
  repoId: string
  category: CategoryKey
  isActive: boolean
  onSelect: (tabId?: string) => void
}) {
  const closeBranch = usePalmuxStore((s) => s.closeBranch)
  const promoteBranch = usePalmuxStore((s) => s.promoteBranch)
  const demoteBranch = usePalmuxStore((s) => s.demoteBranch)
  const notifs = usePalmuxStore(selectBranchNotifications(repoId, branch.id))
  const agent = usePalmuxStore(selectAgentState(repoId, branch.id))
  const unread = notifs?.unreadCount ?? 0
  const agentPipClass = agentPipClassFor(agent?.status)
  const showContextMenu = useContextMenu()
  const isSubagent = category === 'subagent'
  const showMenuAt = (x: number, y: number) => {
    showContextMenu(
      [
        { type: 'heading', label: branch.name },
        ...(category === 'unmanaged'
          ? [
              {
                label: '+ Add to my worktrees',
                onClick: async () => {
                  try {
                    await promoteBranch(repoId, branch.id)
                  } catch (err) {
                    // The store reverts the optimistic update; surfacing
                    // the error via console keeps the menu lightweight.
                    console.error('promote failed', err)
                  }
                },
              },
              { type: 'separator' as const },
            ]
          : []),
        ...(category === 'my' && !branch.isPrimary
          ? [
              {
                label: 'Remove from my worktrees',
                onClick: async () => {
                  try {
                    await demoteBranch(repoId, branch.id)
                  } catch (err) {
                    console.error('demote failed', err)
                  }
                },
              },
              { type: 'separator' as const },
            ]
          : []),
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

  const branchClassNames = [
    styles.branch,
    isActive ? styles.branchActive : '',
    isSubagent ? styles.branchSubagent : '',
  ]
    .filter(Boolean)
    .join(' ')

  return (
    <li>
      <button
        className={branchClassNames}
        onClick={() => onSelect()}
        onContextMenu={(e) => {
          e.preventDefault()
          showMenuAt(e.clientX, e.clientY)
        }}
        {...longPress}
        data-category={category}
        title={
          notifs?.lastMessage
            ? `${branch.worktreePath}\n${notifs.lastMessage}`
            : branch.worktreePath
        }
      >
        {isSubagent && (
          <span className={styles.branchTypeIcon} aria-hidden title="Auto-generated worktree">
            🤖
          </span>
        )}
        <span className={styles.branchName}>{branch.name}</span>
        {branch.isPrimary && <span className={styles.primaryTag}>main</span>}
        {agentPipClass && (
          <span
            className={`${styles.agentPip} ${agentPipClass}`}
            title={`Claude: ${agent?.status ?? 'idle'}`}
            aria-hidden
          />
        )}
        {unread > 0 && <span className={styles.notifyDot} aria-label={`${unread} unread`} />}
        {isActive && <span className={styles.activeDot}>●</span>}
        {category === 'unmanaged' && (
          <button
            type="button"
            className={styles.promoteBtn}
            onClick={(e) => {
              // Stop the row click (which would navigate) so the action
              // is purely "promote, do not switch view".
              e.stopPropagation()
              promoteBranch(repoId, branch.id).catch((err) => {
                console.error('promote failed', err)
              })
            }}
            title="Add this worktree to your `my` section"
            aria-label={`Add ${branch.name} to my worktrees`}
            data-action="promote"
          >
            +
          </button>
        )}
      </button>
    </li>
  )
}

// agentPipClassFor maps agent status to a CSS class name, or '' if no
// pip should render. Idle isn't visually noisy — only "doing something"
// states light up.
function agentPipClassFor(s?: AgentStatus): string {
  switch (s) {
    case 'thinking':            return styles.agentPipThinking
    case 'tool_running':        return styles.agentPipTool
    case 'awaiting_permission': return styles.agentPipPerm
    case 'starting':            return styles.agentPipStart
    case 'error':               return styles.agentPipErr
    default:                    return ''
  }
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
