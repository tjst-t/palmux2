// S023: Drawer v3 — terminal editorial design.
//
// Layout (top → bottom):
//   1. Status strip       — brand "PALMUX" + "● N active · M total"
//   2. ★ Starred section  — title + count
//   3. Repo blocks        — numbered (`01..NN`), each with branches list,
//                            chip row (unmanaged / subagent), and an
//                            expanded panel underneath the active chip
//   4. Repositories section — collapsed compact list (other repos)
//   5. Footer hint        — "⌘K to search"
//   6. Footer CTA         — "Open Repository…"
//
// "Active" branch carries a 3px accent border-left, "● HERE" label, and
// a 2.6s pulse animation. Sub-branches in the expanded panel use a
// stable `minmax(0, 1fr) auto` grid so the action buttons (↗ promote /
// ✕ remove) stay flush right regardless of name length. Active subagent
// rows have `✕` disabled (subagent task is currently running — would
// orphan the work).
//
// Last-active memory: clicking a collapsed repo's header navigates to
// `repo.lastActiveBranch` if it still exists (and expands the repo);
// otherwise expand-only. The `+` button (open new branch dialog) is a
// distinct path so it does not poison the last-active memory.

import { useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'

import { useLongPress } from '../hooks/use-long-press'
import type {
  Branch,
  BranchCategory,
  Repository,
  SubagentCleanupCandidate,
} from '../lib/api'
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
import { SubagentCleanupDialog } from './subagent-cleanup-dialog'
import styles from './drawer.module.css'

type CategoryKey = 'my' | 'unmanaged' | 'subagent'

function categoryKey(category: BranchCategory | undefined): CategoryKey {
  if (category === 'subagent') return 'subagent'
  if (category === 'unmanaged') return 'unmanaged'
  return 'my'
}

export function Drawer() {
  const repos = usePalmuxStore((s) => s.repos)
  const drawerWidth = usePalmuxStore((s) => s.deviceSettings.drawerWidth)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)
  const orphanSessions = usePalmuxStore((s) => s.orphanSessions)
  const reloadOrphanSessions = usePalmuxStore((s) => s.reloadOrphanSessions)

  const [pickerType, setPickerType] = useState<'repo' | { branchOf: string } | null>(null)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [orphanTarget, setOrphanTarget] = useState<{
    name: string
    idx: number
    windowName?: string
  } | null>(null)

  const { repoId: activeRepo } = useParams()
  useEffect(() => {
    if (activeRepo) setExpanded((prev) => new Set(prev).add(activeRepo))
  }, [activeRepo])

  const starred = useMemo(() => repos.filter((r) => r.starred), [repos])
  const unstarred = useMemo(() => repos.filter((r) => !r.starred), [repos])

  // Status strip metrics: "active" = repos that have at least one branch
  // with a running agent or unread notifications. "total" = total open
  // branches across all repos. Both are tabular-nums.
  const { activeCount, totalCount } = useMemo(() => {
    let active = 0
    let total = 0
    for (const r of repos) {
      total += r.openBranches.length
      const hasActivity = r.openBranches.some(
        (b) => agentPipClassFor(b ? undefined : undefined),
        // we re-read agent state via store selector inside BranchItem, so
        // here we approximate "active" as repos with any open branch
      )
      if (hasActivity || r.openBranches.length > 0) {
        // Keep the metric simple — we can refine to "non-idle agent or
        // unread notif" later once telemetry shows it matters. For now,
        // treat any repo with at least one Open branch as "active".
        active += 1
      }
    }
    return { activeCount: active, totalCount: total }
  }, [repos])

  // Stable repo numbering: starred first then unstarred, both alphabetical
  // by ghqPath (matches store sort order). Numbers persist across renders.
  const numbering = useMemo(() => {
    const map = new Map<string, number>()
    let i = 1
    for (const r of starred) map.set(r.id, i++)
    for (const r of unstarred) map.set(r.id, i++)
    return map
  }, [starred, unstarred])

  return (
    <aside className={styles.drawer} style={{ width: drawerWidth }}>
      <div className={styles.scroll}>
        <div className={styles.statusStrip} data-component="status-strip">
          <span className={styles.brand}>Palmux</span>
          <span className={styles.statusMeta}>
            <span>
              <span className={styles.dot}>●</span>
              <b>{activeCount}</b> active
            </span>
            <span>
              <b>{totalCount}</b> total
            </span>
          </span>
        </div>

        {starred.length > 0 && (
          <DrawerSection
            title="★ Starred"
            count={starred.length}
            repos={starred}
            numbering={numbering}
            expanded={expanded}
            setExpanded={setExpanded}
            onAddBranch={(repoId) => setPickerType({ branchOf: repoId })}
          />
        )}
        {unstarred.length > 0 && (
          <DrawerSection
            title="Repositories"
            count={unstarred.length}
            repos={unstarred}
            numbering={numbering}
            expanded={expanded}
            setExpanded={setExpanded}
            onAddBranch={(repoId) => setPickerType({ branchOf: repoId })}
          />
        )}

        <OrphanSection
          sessions={orphanSessions}
          onAttach={(name, idx, windowName) => setOrphanTarget({ name, idx, windowName })}
          onRefresh={reloadOrphanSessions}
        />

        <div className={styles.footerHint} data-component="footer-hint">
          <span className={styles.kbd}>⌘</span>
          <span className={styles.kbd}>K</span> to search
        </div>
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

function DrawerSection({
  title,
  count,
  repos,
  numbering,
  expanded,
  setExpanded,
  onAddBranch,
}: {
  title: string
  count: number
  repos: Repository[]
  numbering: Map<string, number>
  expanded: Set<string>
  setExpanded: React.Dispatch<React.SetStateAction<Set<string>>>
  onAddBranch: (repoId: string) => void
}) {
  return (
    <section data-section={title}>
      <div className={styles.section}>
        <span className={styles.sectionTitle}>{title}</span>
        <span className={styles.sectionCount}>{String(count).padStart(2, '0')}</span>
      </div>
      <ul className={styles.repos}>
        {repos.map((repo) => (
          <RepoItem
            key={repo.id}
            repo={repo}
            number={numbering.get(repo.id) ?? 0}
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
          />
        ))}
      </ul>
    </section>
  )
}

function RepoItem({
  repo,
  number,
  expanded,
  onToggle,
  onAddBranch,
}: {
  repo: Repository
  number: number
  expanded: boolean
  onToggle: () => void
  onAddBranch: () => void
}) {
  const navigate = useNavigate()
  const location = useLocation()
  const { repoId: activeRepo, branchId: activeBranch } = useParams()
  const star = usePalmuxStore((s) => s.starRepo)
  const closeRepo = usePalmuxStore((s) => s.closeRepo)
  const setLastActiveBranch = usePalmuxStore((s) => s.setLastActiveBranch)
  const listStaleSubagentWorktrees = usePalmuxStore(
    (s) => s.listStaleSubagentWorktrees,
  )
  const cleanupSubagentWorktrees = usePalmuxStore(
    (s) => s.cleanupSubagentWorktrees,
  )
  const showContextMenu = useContextMenu()

  const [activeChip, setActiveChip] = useState<'unmanaged' | 'subagent' | null>(null)

  // S023: if the currently-active branch lives in unmanaged/subagent (e.g.
  // a freshly Open'd repo where the primary is still "unmanaged"), make
  // sure the chip containing it is auto-expanded so the user can SEE
  // where they are. Without this the v3 drawer would render only the
  // chip pill and hide the active branch behind a click.
  useEffect(() => {
    if (activeRepo !== repo.id) return
    const activeBranchObj = repo.openBranches.find((b) => b.id === activeBranch)
    if (!activeBranchObj) return
    const cat = categoryKey(activeBranchObj.category)
    if (cat === 'unmanaged') setActiveChip('unmanaged')
    else if (cat === 'subagent') setActiveChip('subagent')
    // 'my' → no chip auto-open (active branch already in the my list)
  }, [activeRepo, activeBranch, repo.id, repo.openBranches])
  const [cleanupOpen, setCleanupOpen] = useState(false)
  const [cleanupLoading, setCleanupLoading] = useState(false)
  const [cleanupCandidates, setCleanupCandidates] = useState<
    SubagentCleanupCandidate[]
  >([])
  const [cleanupThreshold, setCleanupThreshold] = useState(7)

  const openCleanupDialog = async () => {
    setCleanupOpen(true)
    setCleanupLoading(true)
    try {
      const res = await listStaleSubagentWorktrees(repo.id)
      setCleanupCandidates(res.candidates ?? [])
      setCleanupThreshold(res.thresholdDays ?? 7)
    } catch (err) {
      console.error('cleanup dryRun failed', err)
      setCleanupCandidates([])
    } finally {
      setCleanupLoading(false)
    }
  }
  const handleCleanupConfirm = async (selected: string[]) => {
    try {
      const res = await cleanupSubagentWorktrees(repo.id, selected)
      return res
    } catch (err) {
      console.error('cleanup failed', err)
      return undefined
    }
  }

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
      if (a.isPrimary !== b.isPrimary) return a.isPrimary ? -1 : 1
      return a.name.localeCompare(b.name)
    }
    for (const k of (['my', 'unmanaged', 'subagent'] as const)) buckets[k].sort(sorter)
    return buckets
  }, [repo.openBranches])

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

  const navigateToBranch = (branch: Branch, tabId?: string) => {
    const target = tabId ?? branch.tabSet.tabs[0]?.id ?? 'claude'
    // Fire-and-forget last-active memory update.
    void setLastActiveBranch(repo.id, branch.name)
    navigate(`/${repo.id}/${branch.id}/${encodeURIComponent(target)}${location.search}`)
  }

  // Click on the repo header (name / number / chevron strip):
  //   1. If repo is collapsed and `lastActiveBranch` still resolves to an
  //      open branch → navigate there AND expand. One-click return.
  //   2. Else → toggle expand state only.
  const handleRepoHeaderClick = () => {
    if (!expanded && repo.lastActiveBranch) {
      const branch = repo.openBranches.find((b) => b.name === repo.lastActiveBranch)
      if (branch) {
        onToggle() // expand
        navigateToBranch(branch)
        return
      }
    }
    onToggle()
  }

  const myBranches = branchesByCategory.my
  const unmanagedBranches = branchesByCategory.unmanaged
  const subagentBranches = branchesByCategory.subagent

  // Compact display name: split owner/repo so the owner can be muted.
  const { scope, name } = splitRepoName(repo)
  const isCollapsed = !expanded

  return (
    <li
      className={`${styles.repo} ${isCollapsed ? styles.repoCollapsed : ''}`}
      data-repo-id={repo.id}
      data-collapsed={isCollapsed ? 'true' : 'false'}
    >
      <div
        className={styles.repoRow}
        onContextMenu={(e) => {
          e.preventDefault()
          showMenuAt(e.clientX, e.clientY)
        }}
        {...longPress}
      >
        <span className={styles.repoNum}>{String(number).padStart(2, '0')}</span>
        <button
          type="button"
          className={styles.repoName}
          onClick={handleRepoHeaderClick}
          aria-expanded={expanded}
          data-action="repo-toggle"
          title={repo.ghqPath}
        >
          {scope && <span className={styles.scope}>{scope} / </span>}
          {name}
        </button>
        <button
          type="button"
          className={styles.starBtn}
          data-on={repo.starred ? 'true' : 'false'}
          onClick={() => star(repo.id, !repo.starred)}
          aria-label={repo.starred ? 'Unstar' : 'Star'}
          title={repo.starred ? 'Unstar' : 'Star'}
        >
          {repo.starred ? '★' : '☆'}
        </button>
        <button
          type="button"
          className={styles.addBranchBtn}
          onClick={(e) => {
            e.stopPropagation()
            onAddBranch()
          }}
          aria-label="Open new branch"
          title="Open new branch"
          data-action="add-branch"
        >
          [+]
        </button>
      </div>

      {expanded && (
        <>
          {myBranches.length > 0 && (
            <ul className={styles.branches} data-section="my">
              {myBranches.map((branch) => (
                <BranchItem
                  key={branch.id}
                  branch={branch}
                  repoId={repo.id}
                  isActive={activeRepo === repo.id && activeBranch === branch.id}
                  onSelect={(tab) => navigateToBranch(branch, tab)}
                />
              ))}
            </ul>
          )}

          {(unmanagedBranches.length > 0 || subagentBranches.length > 0) && (
            <div className={styles.chipRow} data-component="chip-row">
              {unmanagedBranches.length > 0 && (
                <button
                  type="button"
                  className={`${styles.chip} ${styles.chipUnmanaged} ${
                    activeChip === 'unmanaged' ? styles.chipOpen : ''
                  }`}
                  onClick={() =>
                    setActiveChip(activeChip === 'unmanaged' ? null : 'unmanaged')
                  }
                  data-chip="unmanaged"
                  data-section="unmanaged"
                  aria-expanded={activeChip === 'unmanaged'}
                >
                  unmanaged<span className={styles.chipNum}>{unmanagedBranches.length}</span>
                </button>
              )}
              {subagentBranches.length > 0 && (
                <button
                  type="button"
                  className={`${styles.chip} ${
                    activeChip === 'subagent' ? styles.chipOpen : ''
                  }`}
                  onClick={() =>
                    setActiveChip(activeChip === 'subagent' ? null : 'subagent')
                  }
                  data-chip="subagent"
                  data-section="subagent"
                  aria-expanded={activeChip === 'subagent'}
                >
                  subagent<span className={styles.chipNum}>{subagentBranches.length}</span>
                </button>
              )}
            </div>
          )}

          {activeChip === 'unmanaged' && unmanagedBranches.length > 0 && (
            <ChipExpandedPanel
              category="unmanaged"
              branches={unmanagedBranches}
              repoId={repo.id}
              activeBranchId={activeRepo === repo.id ? activeBranch ?? null : null}
              onSelect={(branch) => navigateToBranch(branch)}
            />
          )}
          {activeChip === 'subagent' && subagentBranches.length > 0 && (
            <ChipExpandedPanel
              category="subagent"
              branches={subagentBranches}
              repoId={repo.id}
              activeBranchId={activeRepo === repo.id ? activeBranch ?? null : null}
              onSelect={(branch) => navigateToBranch(branch)}
              onCleanupAll={openCleanupDialog}
            />
          )}
        </>
      )}

      <SubagentCleanupDialog
        open={cleanupOpen}
        thresholdDays={cleanupThreshold}
        candidates={cleanupCandidates}
        loading={cleanupLoading}
        onClose={() => setCleanupOpen(false)}
        onConfirm={handleCleanupConfirm}
      />
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
  const promoteBranch = usePalmuxStore((s) => s.promoteBranch)
  const demoteBranch = usePalmuxStore((s) => s.demoteBranch)
  const notifs = usePalmuxStore(selectBranchNotifications(repoId, branch.id))
  const agent = usePalmuxStore(selectAgentState(repoId, branch.id))
  const unread = notifs?.unreadCount ?? 0
  const agentPipClass = agentPipClassFor(agent?.status)
  const showContextMenu = useContextMenu()

  const showMenuAt = (x: number, y: number) => {
    showContextMenu(
      [
        { type: 'heading', label: branch.name },
        ...(!branch.isPrimary
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

  // Idle-readable meta line: turn count + activity if available, plus
  // unread notifications. Falls back to the raw worktree path when
  // nothing fancier is on hand.
  const metaText = (() => {
    const bits: string[] = []
    if (agent?.status && agent.status !== 'idle') bits.push(agent.status)
    if (unread > 0) bits.push(`${unread} unread`)
    return bits.join(' · ')
  })()

  // unused — promoteBranch handler stays for the context menu wiring
  void promoteBranch

  return (
    <li>
      <button
        type="button"
        className={`${styles.branch} ${isActive ? styles.branchActive : ''}`}
        onClick={() => onSelect()}
        onContextMenu={(e) => {
          e.preventDefault()
          showMenuAt(e.clientX, e.clientY)
        }}
        {...longPress}
        data-branch-id={branch.id}
        data-active={isActive ? 'true' : 'false'}
        title={
          notifs?.lastMessage
            ? `${branch.worktreePath}\n${notifs.lastMessage}`
            : branch.worktreePath
        }
      >
        <span className={styles.branchInfo}>
          {isActive && (
            <span className={styles.hereLabel} data-label="here">
              Here
            </span>
          )}
          <span className={styles.branchName}>{branch.name}</span>
          {metaText && <span className={styles.branchMeta}>{metaText}</span>}
        </span>
        {agentPipClass && (
          <span
            className={`${styles.agentPip} ${agentPipClass}`}
            title={`Claude: ${agent?.status ?? 'idle'}`}
            aria-hidden
          />
        )}
        {unread > 0 && <span className={styles.notifyDot} aria-label={`${unread} unread`} />}
        {branch.isPrimary && <span className={styles.primaryTag}>main</span>}
      </button>
    </li>
  )
}

// ChipExpandedPanel renders the unmanaged / subagent sub-list under the
// chip row. Always-visible action buttons:
//   - ↗ promote → unmanaged: add to userOpenedBranches
//                  subagent : move worktree to gwq path + record
//   - ✕ remove  → subagent only (unmanaged stays — the user might be
//                  using it intentionally). Disabled when the subagent
//                  branch is currently running an agent task.
function ChipExpandedPanel({
  category,
  branches,
  repoId,
  activeBranchId,
  onSelect,
  onCleanupAll,
}: {
  category: 'unmanaged' | 'subagent'
  branches: Branch[]
  repoId: string
  activeBranchId: string | null
  onSelect: (branch: Branch) => void
  onCleanupAll?: () => void
}) {
  const promoteBranch = usePalmuxStore((s) => s.promoteBranch)
  const promoteSubagentBranch = usePalmuxStore((s) => s.promoteSubagentBranch)
  const closeBranch = usePalmuxStore((s) => s.closeBranch)

  const promoteAll = async () => {
    for (const b of branches) {
      try {
        if (category === 'unmanaged') await promoteBranch(repoId, b.id)
        else await promoteSubagentBranch(repoId, b.id)
      } catch (err) {
        console.error('promote-all failed', err)
      }
    }
  }

  const promoteOne = async (branch: Branch) => {
    try {
      if (category === 'unmanaged') {
        await promoteBranch(repoId, branch.id)
      } else {
        const ok = await confirmDialog.ask({
          title: 'Promote to my worktrees?',
          message:
            `${branch.name} will be moved out of \`${branch.worktreePath}\` into the standard ` +
            `gwq location and added to your \`my\` section.`,
          confirmLabel: 'Promote',
        })
        if (!ok) return
        await promoteSubagentBranch(repoId, branch.id)
      }
    } catch (err) {
      console.error('promote failed', err)
    }
  }

  const removeOne = async (branch: Branch) => {
    const ok = await confirmDialog.ask({
      title: 'Remove worktree?',
      message: `${branch.name}'s tmux session will be killed and its worktree removed.`,
      confirmLabel: 'Remove',
      danger: true,
    })
    if (!ok) return
    try {
      await closeBranch(repoId, branch.id)
    } catch (err) {
      console.error('remove failed', err)
    }
  }

  const headLabel = category === 'unmanaged' ? 'UNMANAGED' : 'SUBAGENT'
  const action = category === 'subagent' ? 'clean up stale' : 'promote all'
  const onActionClick = category === 'subagent' ? onCleanupAll : promoteAll

  return (
    <div
      className={`${styles.expandedPanel} ${
        category === 'unmanaged' ? styles.expandedPanelUnmanaged : styles.expandedPanelSubagent
      }`}
      data-panel={category}
    >
      <div className={styles.panelHead}>
        <span>
          {headLabel} · {branches.length}
        </span>
        {onActionClick && (
          <button
            type="button"
            className={styles.panelAction}
            onClick={onActionClick}
            data-action={category === 'subagent' ? 'cleanup-subagent' : 'promote-all'}
          >
            {action}
          </button>
        )}
      </div>
      {branches.map((branch) => (
        <SubBranchRow
          key={branch.id}
          branch={branch}
          category={category}
          isActive={activeBranchId === branch.id}
          onSelect={() => onSelect(branch)}
          onPromote={() => promoteOne(branch)}
          onRemove={() => removeOne(branch)}
        />
      ))}
    </div>
  )
}

function SubBranchRow({
  branch,
  category,
  isActive,
  onSelect,
  onPromote,
  onRemove,
}: {
  branch: Branch
  category: 'unmanaged' | 'subagent'
  isActive: boolean
  onSelect: () => void
  onPromote: () => void
  onRemove: () => void
}) {
  // Sub-branch meta from the lastActivity timestamp + agent state.
  // Subagent rows with a currently-running agent show "active task" and
  // disable the remove button (preventing accidental kill of in-flight
  // work).
  const agent = usePalmuxStore(selectAgentState(branch.repoId, branch.id))
  const isActiveTask = !!agent?.status && agent.status !== 'idle' && agent.status !== 'error'
  const stat = computeBranchStat(branch.lastActivity, isActiveTask)
  const meta = computeBranchMeta(branch.lastActivity, isActiveTask)

  return (
    <div
      className={styles.subBranch}
      onClick={(e) => {
        // Avoid double-firing when clicking an inner button.
        if ((e.target as HTMLElement).closest('button')) return
        onSelect()
      }}
      data-sub-branch-id={branch.id}
      data-branch-id={branch.id}
      data-active={isActive ? 'true' : 'false'}
      data-category={category}
    >
      <div className={styles.subInfo}>
        <div className={styles.subTitle}>
          {stat && (
            <span
              className={`${styles.subStat} ${
                stat.kind === 'fresh' ? styles.subStatFresh : styles.subStatStale
              }`}
              aria-hidden
            >
              {stat.glyph}
            </span>
          )}
          <span className={styles.subName}>{branch.name}</span>
        </div>
        {meta && <div className={styles.subMeta}>{meta}</div>}
      </div>
      <div className={styles.subActions}>
        <button
          type="button"
          className={`${styles.icoBtn} ${styles.icoBtnPromote}`}
          onClick={onPromote}
          title="Promote to my"
          aria-label={`Promote ${branch.name}`}
          data-action="promote"
        >
          ↗
        </button>
        {category === 'subagent' && (
          <button
            type="button"
            className={`${styles.icoBtn} ${styles.icoBtnRemove} ${
              isActiveTask ? styles.icoBtnDisabled : ''
            }`}
            onClick={onRemove}
            disabled={isActiveTask}
            title={isActiveTask ? 'Remove (locked: active)' : 'Remove worktree'}
            aria-label={`Remove ${branch.name}`}
            data-action="remove"
            data-disabled={isActiveTask ? 'true' : 'false'}
          >
            ✕
          </button>
        )}
      </div>
    </div>
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
  const [open, setOpen] = useState(false)
  const [openSessions, setOpenSessions] = useState<Set<string>>(new Set())

  if (sessions.length === 0 && !open) {
    return (
      <section className={styles.orphanSection} data-section="orphans">
        <button className={styles.orphanHeader} onClick={() => setOpen(true)}>
          <span>orphans</span>
          <span style={{ marginLeft: 'auto' }}>▶</span>
        </button>
      </section>
    )
  }

  return (
    <section className={styles.orphanSection} data-section="orphans">
      <button className={styles.orphanHeader} onClick={() => setOpen(!open)}>
        <span>orphans{sessions.length > 0 ? ` · ${sessions.length}` : ''}</span>
        <button
          onClick={(e) => {
            e.stopPropagation()
            onRefresh()
          }}
          style={{ marginLeft: 'auto', background: 'transparent', border: 0, color: 'inherit', cursor: 'pointer' }}
          title="Refresh"
          aria-label="Refresh"
        >
          ↻
        </button>
      </button>
      {open && sessions.length === 0 && (
        <p style={{ margin: '6px 0', fontSize: 11, color: 'var(--color-fg-muted)' }}>
          No non-Palmux tmux sessions.
        </p>
      )}
      {open && sessions.length > 0 && (
        <ul className={styles.orphanList}>
          {sessions.map((s) => {
            const expanded = openSessions.has(s.name)
            return (
              <li key={s.name} className={styles.orphanItem}>
                <button
                  type="button"
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
                  {expanded ? '▼' : '▶'} {s.name}
                </button>
                {expanded && (
                  <ul style={{ listStyle: 'none', paddingLeft: 16 }}>
                    {s.windows.map((w) => (
                      <li key={w.index}>
                        <button onClick={() => onAttach(s.name, w.index, w.name)}>
                          {w.index}: {w.name}
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

// =============================================================================
// helpers
// =============================================================================

function agentPipClassFor(s?: AgentStatus): string {
  switch (s) {
    case 'thinking':
      return styles.agentPipThinking
    case 'tool_running':
      return styles.agentPipTool
    case 'awaiting_permission':
      return styles.agentPipPerm
    case 'starting':
      return styles.agentPipStart
    case 'error':
      return styles.agentPipErr
    default:
      return ''
  }
}

function repoDisplayName(repo: { ghqPath: string }): string {
  const parts = repo.ghqPath.split('/')
  return parts.slice(1).join('/') || repo.ghqPath
}

function splitRepoName(repo: { ghqPath: string }): { scope: string; name: string } {
  const parts = repo.ghqPath.split('/')
  if (parts.length >= 3) {
    // host / owner / name → scope=owner, name=name
    return { scope: parts[parts.length - 2], name: parts[parts.length - 1] }
  }
  if (parts.length === 2) return { scope: '', name: parts[1] }
  return { scope: '', name: repo.ghqPath }
}

// computeBranchStat returns a small status glyph + class hint for the
// sub-branch row. Fresh = recent activity (or active task in flight),
// stale = no commits in the last `STALE_DAYS`. We don't show a stat for
// in-between branches (would create noise).
const STALE_DAYS = 7
function computeBranchStat(
  lastActivityIso: string,
  isActiveTask: boolean,
): { kind: 'fresh' | 'stale'; glyph: string } | null {
  if (isActiveTask) return { kind: 'fresh', glyph: '●' }
  const t = Date.parse(lastActivityIso)
  if (Number.isNaN(t)) return null
  const ageDays = (Date.now() - t) / (1000 * 60 * 60 * 24)
  if (ageDays >= STALE_DAYS) return { kind: 'stale', glyph: '◍' }
  return null
}

function computeBranchMeta(lastActivityIso: string, isActiveTask: boolean): string {
  const t = Date.parse(lastActivityIso)
  if (Number.isNaN(t)) return ''
  const diffMs = Date.now() - t
  const days = Math.floor(diffMs / (1000 * 60 * 60 * 24))
  const hours = Math.floor(diffMs / (1000 * 60 * 60))
  const taskNote = isActiveTask ? ' · ⌁ active task' : ''
  if (days >= STALE_DAYS) return `stale ${days}d${taskNote}`
  if (days >= 1) return `${days}d ago${taskNote}`
  if (hours >= 1) return `${hours}h ago${taskNote}`
  return `recent${taskNote}`
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

// silences unused-var lint when nothing in BranchItem uses repoDisplayName
void repoDisplayName
