// S024: Drawer v7 — compact, single-expand, single-line.
//
// Layout (top → bottom):
//   1. Status strip       — "PALMUX" + "● N active · M total"
//   2. "Repositories" section (★ Starred and other repos merged into
//      one list; star marker stays on the row, sort order is starred
//      first then alphabetical)
//   3. Each repo row (numbered `01`..`NN`):
//      - Collapsed: name + ★ + [+], plus a 1-line "glance" preview of
//        what clicking the row will navigate to (last_active or
//        ghq folder). Active-containing repos show the glance line in
//        accent colour.
//      - Expanded: my-branches list (single-line each, with ⌂ + name +
//        optional `[main]` badge), chip row, and the chip-expanded
//        sub-panel under the active chip.
//   4. Orphan section
//   5. Footer hint
//   6. Footer CTA "Open Repository…"
//
// Single-expand (S024): only one repo can be expanded at any time. The
// `expandedRepoId` state holds the currently-open repo id (or null when
// everything is collapsed). Opening a different repo automatically
// auto-collapses the previous one. Initial value is the active repo
// (the one containing the current branch).
//
// HERE label removed (S024): active branch is identified visually by
// border-left + bg tint + bold + 2.6s glow keyframes. No "HERE" text
// label, no "● HERE" pseudo-element, no `here-label` class — the v7
// E2E asserts these are absent.
//
// Worktree single line (S024): both my-branch rows and sub-branch rows
// in chip-expanded panels are 1 line each. The previous "stale Nd" /
// "Nh ago" / "active task" / `idle` / `N turns` / `created YYYY` meta
// lines are deleted. What remains: branch name, `⌂` ghq mark when the
// branch is the canonical primary, `[main]` badge, and stat icon
// (● fresh / ◍ stale) for sub-branches.

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
import { RepoDeleteModal } from './repo-delete-modal'
import { RepoPicker } from './repo-picker'
import { SubagentCleanupDialog } from './subagent-cleanup-dialog'
import styles from './drawer.module.css'

type CategoryKey = 'my' | 'unmanaged' | 'subagent'

function categoryKey(category: BranchCategory | undefined): CategoryKey {
  if (category === 'subagent') return 'subagent'
  if (category === 'unmanaged') return 'unmanaged'
  return 'my'
}

// `git describe --tags --always --dirty` → compact form for the drawer.
//   v0.5.1                           → v0.5.1
//   v0.5.1-4-g3082711                → v0.5.1+4
//   v0.5.1-4-g3082711-dirty          → v0.5.1+4*
//   v0.5.1-dirty                     → v0.5.1*
// `*` always means a dirty worktree, `+N` means N commits past the tag.
// Mirror of `palmux:lastTab:{repoId}/{branchId}` writer in
// main-layout.tsx — read-only side. Returns null when no record yet
// exists or localStorage is unavailable; the caller decides the
// fallback (typically `branch.tabSet.tabs[0]`).
function readLastTabFor(repoId: string, branchId: string): string | null {
  try {
    return localStorage.getItem(`palmux:lastTab:${repoId}/${branchId}`)
  } catch {
    return null
  }
}

// Compact a `git describe` string for the Drawer chip.
//   v0.7.0                  → v0.7.0
//   v0.7.0-3-gabc1234       → v0.7.0+3
//   v0.7.0-3-gabc1234-dirty → v0.7.0+3*
//   v0.7.0-dirty            → v0.7.0*
//   dev / dev-abc1234       → unchanged
//   phase-10 (legacy tag)   → phase-10  (still rendered so the user
//                             can SEE that the binary's version source
//                             isn't a proper release — and rebuild)
// eslint-disable-next-line react-refresh/only-export-components -- helper coupled to component (HMR-only concern, no runtime impact)
export function formatCompactVersion(raw: string): string {
  if (!raw) return ''
  const dirty = raw.endsWith('-dirty')
  const core = dirty ? raw.slice(0, -'-dirty'.length) : raw
  const m = /^(.*)-(\d+)-g[0-9a-f]+$/.exec(core)
  if (m) return `${m[1]}+${m[2]}${dirty ? '*' : ''}`
  return `${core}${dirty ? '*' : ''}`
}

export function Drawer() {
  const repos = usePalmuxStore((s) => s.repos)
  const drawerWidth = usePalmuxStore((s) => s.deviceSettings.drawerWidth)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)
  const orphanSessions = usePalmuxStore((s) => s.orphanSessions)
  const reloadOrphanSessions = usePalmuxStore((s) => s.reloadOrphanSessions)
  const version = usePalmuxStore((s) => s.serverInfo.version)

  const [pickerType, setPickerType] = useState<'repo' | { branchOf: string } | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<{ repoId: string; ghqPath: string } | null>(null)
  const reloadRepos = usePalmuxStore((s) => s.reloadRepos)
  // S024: single-expand — one repo open at a time. Init lazily to the
  // repo containing the active branch (URL-driven).
  const { repoId: activeRepo, branchId: activeBranch } = useParams()
  const [expandedRepoId, setExpandedRepoId] = useState<string | null>(
    () => activeRepo ?? null,
  )
  // S13b16a-4: Whenever the URL switches to a new repo, follow it
  // (still single-expand). Uses inline (`trackedActiveRepo`) tracking so
  // the new state is set during render — React 19 "deriving state from
  // props" idiom, no useEffect+setState double pass.
  const [trackedActiveRepo, setTrackedActiveRepo] = useState(activeRepo)
  if (trackedActiveRepo !== activeRepo) {
    setTrackedActiveRepo(activeRepo)
    if (activeRepo) setExpandedRepoId(activeRepo)
  }

  const [orphanTarget, setOrphanTarget] = useState<{
    name: string
    idx: number
    windowName?: string
  } | null>(null)

  // S024: section unification — Starred + Repositories merged into one
  // "Repositories" list, sorted starred-first then by id. Star marker
  // stays on the row.
  const sortedRepos = useMemo(() => {
    const arr = [...repos]
    arr.sort((a, b) => {
      if (a.starred !== b.starred) return a.starred ? -1 : 1
      return a.id.localeCompare(b.id)
    })
    return arr
  }, [repos])

  // Stable repo numbering matches the merged sort order.
  const numbering = useMemo(() => {
    const map = new Map<string, number>()
    let i = 1
    for (const r of sortedRepos) map.set(r.id, i++)
    return map
  }, [sortedRepos])

  // hotfix: basename collisions among the currently-open repos.
  // RepoItem renders the owner prefix (`tjst-t/`) only when the
  // repo's basename appears in this set — most users have unique
  // basenames so the drawer reads as `cirrus`, `palmux2`, …
  // rather than `tjst-t/cirrus`, `tjst-t/palmux2`. When two
  // open repos share a basename (e.g. `tjst-t/blog` +
  // `hiroto/blog`), both show the full owner/repo to disambiguate.
  const duplicateBasenames = useMemo(() => {
    const counts = new Map<string, number>()
    for (const r of sortedRepos) {
      const parts = r.ghqPath.split('/')
      const base = parts[parts.length - 1]
      counts.set(base, (counts.get(base) ?? 0) + 1)
    }
    return new Set(
      Array.from(counts.entries())
        .filter(([, n]) => n > 1)
        .map(([base]) => base),
    )
  }, [sortedRepos])

  // Status-strip metrics.
  const { activeCount, totalCount } = useMemo(() => {
    let active = 0
    let total = 0
    for (const r of repos) {
      total += r.openBranches.length
      if (r.openBranches.length > 0) active += 1
    }
    return { activeCount: active, totalCount: total }
  }, [repos])

  const handleSetExpanded = (repoId: string | null) => setExpandedRepoId(repoId)

  return (
    <aside className={styles.drawer} style={{ width: drawerWidth }}>
      <div className={styles.scroll}>
        <div className={styles.statusStrip} data-component="status-strip">
          <span className={styles.brand}>
            Palmux
            {version && (
              <span
                className={styles.version}
                title={`palmux2 ${version}`}
                data-component="palmux-version"
              >
                {formatCompactVersion(version)}
              </span>
            )}
          </span>
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

        <DrawerSection
          title="Repositories"
          count={sortedRepos.length}
          repos={sortedRepos}
          numbering={numbering}
          duplicateBasenames={duplicateBasenames}
          expandedRepoId={expandedRepoId}
          setExpandedRepoId={handleSetExpanded}
          onAddBranch={(repoId) => setPickerType({ branchOf: repoId })}
          onDeleteRepo={(repoId, ghqPath) => setDeleteTarget({ repoId, ghqPath })}
        />

        <HostSection />

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
        <button
          className={styles.addRepoBtn}
          onClick={() => setPickerType('repo')}
          data-testid="repo-picker-open"
        >
          <span style={{ color: 'var(--color-fg-muted)' }}>📂</span>
          <span>Open Repository…</span>
        </button>
      </footer>
      <DrawerResizer
        width={drawerWidth}
        onChange={(w) => setDeviceSetting('drawerWidth', w)}
      />
      <RepoPicker
        open={pickerType === 'repo'}
        onClose={() => setPickerType(null)}
        onRequestDelete={(repoId, ghqPath) => {
          // Stage the deletion target. The picker stays open behind the
          // delete modal — after delete completes, reloadRepos() refreshes
          // the available list automatically (same store).
          setDeleteTarget({ repoId, ghqPath })
        }}
        activeRepoId={activeRepo}
        activeBranchId={activeBranch}
      />
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
      {deleteTarget && (
        <RepoDeleteModal
          open={true}
          repoId={deleteTarget.repoId}
          ghqPath={deleteTarget.ghqPath}
          onClose={() => setDeleteTarget(null)}
          onDeleted={async () => {
            setDeleteTarget(null)
            await reloadRepos()
          }}
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
  duplicateBasenames,
  expandedRepoId,
  setExpandedRepoId,
  onAddBranch,
  onDeleteRepo,
}: {
  title: string
  count: number
  repos: Repository[]
  numbering: Map<string, number>
  /** Set of basenames that occur on more than one repo — those rows
   *  show the owner prefix to disambiguate. Unique basenames render
   *  bare (just `cirrus`, not `tjst-t/cirrus`). */
  duplicateBasenames: Set<string>
  expandedRepoId: string | null
  setExpandedRepoId: (id: string | null) => void
  onAddBranch: (repoId: string) => void
  onDeleteRepo: (repoId: string, ghqPath: string) => void
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
            duplicateBasenames={duplicateBasenames}
            expanded={expandedRepoId === repo.id}
            onSetExpanded={setExpandedRepoId}
            onAddBranch={() => onAddBranch(repo.id)}
            onDeleteRepo={() => onDeleteRepo(repo.id, repo.ghqPath)}
          />
        ))}
      </ul>
    </section>
  )
}

function RepoItem({
  repo,
  number,
  duplicateBasenames,
  expanded,
  onSetExpanded,
  onAddBranch,
  onDeleteRepo,
}: {
  repo: Repository
  number: number
  duplicateBasenames: Set<string>
  expanded: boolean
  onSetExpanded: (id: string | null) => void
  onAddBranch: () => void
  onDeleteRepo: () => void
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

  // S023 carry-over: if the active branch lives in unmanaged/subagent, auto-
  // open the chip containing it so the user sees where they are.
  // S13b16a-4: Compute the auto-target during render and reconcile via
  // an inline tracking state (React 19 "deriving state from props"
  // idiom). When the URL or active branch changes we resolve which chip
  // it belongs to, and only call setActiveChip on a real change.
  let autoChip: 'unmanaged' | 'subagent' | null = null
  if (activeRepo === repo.id) {
    const activeBranchObj = repo.openBranches.find((b) => b.id === activeBranch)
    if (activeBranchObj) {
      const cat = categoryKey(activeBranchObj.category)
      if (cat === 'unmanaged' || cat === 'subagent') autoChip = cat
    }
  }
  const autoChipKey = `${activeRepo}/${activeBranch}/${autoChip ?? ''}`
  const [trackedAutoChipKey, setTrackedAutoChipKey] = useState(autoChipKey)
  if (trackedAutoChipKey !== autoChipKey) {
    setTrackedAutoChipKey(autoChipKey)
    if (autoChip !== null && autoChip !== activeChip) setActiveChip(autoChip)
  }

  // hotfix: overflow menu state retired — the ⋯ button now dispatches
  // through the shared ContextMenuRenderer (portal at body level,
  // Windows-style flip positioning).
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
    // Per-branch last-tab restore. main-layout.tsx writes
    // `palmux:lastTab:{repoId}/{branchId}` on every URL update, so we
    // can land the user back on whatever they were doing in this
    // branch (Bash session, Files path, etc.) instead of always
    // bouncing to claude. Validate the remembered id still matches a
    // live tab — it can be stale after a Bash tab close.
    const remembered = readLastTabFor(repo.id, branch.id)
    const rememberedTab = remembered
      ? branch.tabSet.tabs.find((t) => t.id === remembered)
      : undefined
    const target =
      tabId ?? rememberedTab?.id ?? branch.tabSet.tabs[0]?.id ?? 'claude'
    void setLastActiveBranch(repo.id, branch.name)
    navigate(`/${repo.id}/${branch.id}/${encodeURIComponent(target)}${location.search}`)
  }

  // Click on the repo header (name / number / glance line area):
  //   - Collapsed: navigate to last_active (or ghq primary) AND set this
  //                repo as the only expanded one (auto-collapses the prev).
  //   - Expanded:  collapse (no repo expanded after).
  const handleRepoHeaderClick = () => {
    if (!expanded) {
      const target = navigateTarget(repo, branchesByCategory.my)
      if (target) {
        navigateToBranch(target.branch)
      }
      onSetExpanded(repo.id)
    } else {
      onSetExpanded(null)
    }
  }

  const myBranches = branchesByCategory.my
  const unmanagedBranches = branchesByCategory.unmanaged
  const subagentBranches = branchesByCategory.subagent

  const { scope, name } = splitRepoName(repo)
  const isCollapsed = !expanded
  const containsActive = activeRepo === repo.id
  const target = navigateTarget(repo, myBranches)

  return (
    <li
      className={`${styles.repo} ${isCollapsed ? styles.repoCollapsed : ''} ${
        isCollapsed && containsActive ? styles.repoHasActive : ''
      }`}
      data-repo-id={repo.id}
      data-collapsed={isCollapsed ? 'true' : 'false'}
      data-has-active={containsActive ? 'true' : 'false'}
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
          {/* hotfix: hide owner scope when the basename is unique
             across the open repos. Show full `owner/name` only when
             two or more repos share the same basename (e.g.
             tjst-t/blog + hiroto/blog) so the user can tell them
             apart. The full ghqPath is still in the title= for
             on-hover discovery. */}
          {scope && duplicateBasenames.has(name) && (
            <span className={styles.scope}>{scope}/</span>
          )}
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
          +
        </button>
        <button
          type="button"
          className={styles.moreBtn}
          onClick={(e) => {
            e.stopPropagation()
            // hotfix: route through the shared ContextMenuRenderer
            // (portal-rendered at body level, Windows-style flip
            // positioning) instead of the in-drawer absolute popover
            // that was confined to the drawer column. The cursor
            // becomes the anchor and the menu can spill into the
            // main pane like a real OS context menu.
            const rect = (e.target as HTMLElement).getBoundingClientRect()
            showContextMenu(
              [
                { type: 'heading', label: repoDisplayName(repo) },
                {
                  label: repo.starred ? 'Unstar' : 'Pin to top',
                  onClick: () => star(repo.id, !repo.starred),
                },
                {
                  label: 'Copy ghq path',
                  onClick: () => {
                    void navigator.clipboard.writeText(repo.ghqPath).catch(() => {})
                  },
                },
                { type: 'separator' },
                {
                  label: 'Close repository',
                  onClick: async () => {
                    const ok = await confirmDialog.ask({
                      title: 'Close repository?',
                      message: `${repo.ghqPath} will be removed from Palmux. The ghq directory and any worktrees stay on disk — reopen any time from "Open Repository…".`,
                      confirmLabel: 'Close',
                    })
                    if (ok) await closeRepo(repo.id)
                  },
                },
                {
                  label: 'Delete repository…',
                  danger: true,
                  onClick: () => onDeleteRepo(),
                },
              ],
              // Anchor the menu just below the ⋯ button's right edge,
              // so the menu's top-left starts at that point and the
              // shared positioning logic flips it left when needed.
              rect.right - 4,
              rect.bottom + 2,
            )
          }}
          aria-label="More options"
          title="More options"
          data-testid="repo-more-btn"
        >
          ⋯
        </button>
      </div>

      {/* Glance line — only visible when collapsed. */}
      {isCollapsed && target && (
        <div
          className={styles.glanceLine}
          data-component="glance-line"
          data-target-source={target.source}
          onClick={handleRepoHeaderClick}
        >
          {target.source === 'ghq' && <span className={styles.ghqMark}>⌂</span>}
          {target.branch.name}
          <span className={styles.glanceArrow}>›</span>
        </div>
      )}

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

  // Suppress unused-var lint for the menu helper.
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
        <span className={styles.branchName}>
          {branch.isPrimary && <span className={styles.ghqMark}>⌂</span>}
          {branch.name}
        </span>
        <span className={styles.branchTrailing}>
          {agentPipClass && (
            <span
              className={`${styles.agentPip} ${agentPipClass}`}
              title={`Claude: ${agent?.status ?? 'idle'}`}
              aria-hidden
            />
          )}
          {unread > 0 && <span className={styles.notifyDot} aria-label={`${unread} unread`} />}
          {branch.isPrimary && <span className={styles.primaryTag}>main</span>}
          {/* S8478ca-5: runtime state badge (only for real workspaces, not host scope) */}
          {branch.runtime && (
            <span
              className={styles.runtimeBadge}
              data-testid="workspace-runtime-badge"
              data-runtime-state={branch.runtime.state}
              title={`${branch.runtime.kind} · ${branch.runtime.state}${branch.runtime.address ? ` (${branch.runtime.address})` : ''}${branch.runtime.error ? ` — ${branch.runtime.error}` : ''}`}
            >
              <span className={styles.rtDot} />
              {branch.runtime.state === 'error' ? 'error' : branch.runtime.state}
            </span>
          )}
          {/* S7364e3: "update available" pill when the incus container is stale */}
          {branch.runtime?.kind === 'incus-container' && branch.runtime?.stale && (
            <span
              className={styles.workspaceUpdateBadge}
              data-testid="workspace-update-badge"
              title="A newer palmux-ws image is available — update from the runtime chip"
            >
              ⬆ update
            </span>
          )}
        </span>
      </button>
    </li>
  )
}

// ChipExpandedPanel renders the unmanaged / subagent sub-list.
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
  // S024: single-line. We keep the stat icon (●/◍) but drop the meta line.
  // Active-task detection is still used to disable the remove (✕) button.
  const agent = usePalmuxStore(selectAgentState(branch.repoId, branch.id))
  const isActiveTask = !!agent?.status && agent.status !== 'idle' && agent.status !== 'error'
  const stat = computeBranchStat(branch.lastActivity, isActiveTask)

  return (
    <div
      className={styles.subBranch}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest('button')) return
        onSelect()
      }}
      data-sub-branch-id={branch.id}
      data-branch-id={branch.id}
      data-active={isActive ? 'true' : 'false'}
      data-category={category}
    >
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

// HostSection (S0c6a1b) renders the reserved, repository-independent Host
// scope as a single dedicated entry — separate from Repositories and Orphans
// — so the user can open a terminal (e.g. for `gh auth login` / `claude`
// login) before opening any repo. Clicking it opens the host terminal; the
// individual bash terminals are added/removed/switched from the top TabBar
// (like any workspace), so the drawer intentionally shows ONLY the "Host"
// entry — no per-terminal rows, no "+" (refine 2026-06-03: those duplicated
// the TabBar).
function HostSection() {
  const hostRepo = usePalmuxStore((s) => s.hostRepo)
  const navigate = useNavigate()
  const location = useLocation()
  const { repoId: activeRepoId } = useParams()

  if (!hostRepo) return null
  const branch = hostRepo.openBranches[0]
  if (!branch) return null
  const isActive = activeRepoId === hostRepo.id

  // Land on the last-active host tab if it still exists, else the first.
  const remembered = readLastTabFor(hostRepo.id, branch.id)
  const target =
    (remembered && branch.tabSet.tabs.some((t) => t.id === remembered)
      ? remembered
      : branch.tabSet.tabs[0]?.id) ?? 'bash:bash'

  return (
    <section className={styles.hostSection} data-section="host" data-testid="drawer-host-section">
      <button
        type="button"
        className={isActive ? styles.hostTitleActive : styles.hostTitle}
        data-testid="drawer-host-terminal"
        onClick={() =>
          navigate(`/${hostRepo.id}/${branch.id}/${encodeURIComponent(target)}${location.search}`)
        }
        title="Open a repository-independent terminal (gh / claude login, host commands)"
      >
        <span aria-hidden>🖥</span> {branch.name}
      </button>
    </section>
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
    return { scope: parts[parts.length - 2], name: parts[parts.length - 1] }
  }
  if (parts.length === 2) return { scope: '', name: parts[1] }
  return { scope: '', name: repo.ghqPath }
}

// navigateTarget picks where a collapsed-repo click should land:
//   1. last_active_branch when it still resolves to an open branch
//   2. otherwise the primary (ghq) branch
// Returns null if neither is available — caller falls back to expand-only.
function navigateTarget(
  repo: Repository,
  myBranches: Branch[],
): { branch: Branch; source: 'last_active' | 'ghq' } | null {
  if (repo.lastActiveBranch) {
    const b = repo.openBranches.find((x) => x.name === repo.lastActiveBranch)
    if (b) return { branch: b, source: 'last_active' }
  }
  // ghq folder = primary worktree. Always exists when repo is Open.
  const primary = myBranches.find((x) => x.isPrimary) ?? repo.openBranches.find((x) => x.isPrimary)
  if (primary) return { branch: primary, source: 'ghq' }
  // Last resort: first MY branch.
  if (myBranches.length > 0) return { branch: myBranches[0], source: 'ghq' }
  return null
}

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

void repoDisplayName
