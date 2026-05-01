import { useRef, useState, type ReactNode } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'

import { useLongPress } from '../hooks/use-long-press'
import type { Branch, Tab } from '../lib/api'
import {
  selectBranchNotifications,
  selectRepoById,
  usePalmuxStore,
} from '../stores/palmux-store'

import { confirmDialog } from './context-menu/confirm-dialog'
import { promptDialog } from './context-menu/prompt-dialog'
import { useContextMenu } from './context-menu/store'
import { ClaudeIcon } from './icons/claude-icon'
import { WorkspaceActions } from './workspace-actions'
import styles from './tab-bar.module.css'

interface Props {
  branch: Branch
}

// S009: defaults match the server side (settings.maxClaudeTabsPerBranch /
// settings.maxBashTabsPerBranch). When the user configures different
// limits the FE picks them up via the loaded GlobalSettings; if the
// settings haven't been fetched yet we fall through to these so the `+`
// button is always available out of the gate.
const DEFAULT_LIMITS: Record<string, { min: number; max: number }> = {
  claude: { min: 1, max: 3 },
  bash: { min: 1, max: 5 },
  files: { min: 1, max: 1 },
  git: { min: 1, max: 1 },
}

export function TabBar({ branch }: Props) {
  const { repoId } = useParams()
  const { tabId } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  const addTab = usePalmuxStore((s) => s.addTab)
  const removeTab = usePalmuxStore((s) => s.removeTab)
  const renameTab = usePalmuxStore((s) => s.renameTab)
  const settings = usePalmuxStore((s) => s.globalSettings)
  const notifs = usePalmuxStore(
    repoId ? selectBranchNotifications(repoId, branch.id) : () => undefined,
  )
  const claudeUnread = notifs?.unreadCount ?? 0
  const [adding, setAdding] = useState<string | null>(null)
  const showContextMenu = useContextMenu()
  const repo = usePalmuxStore((s) =>
    repoId ? selectRepoById(repoId)(s) : undefined,
  )

  if (!repoId) return null

  // Per-type max from settings, falling back to built-in defaults so
  // the UI is functional before the settings round-trip lands.
  const limitsFor = (type: string): { min: number; max: number } => {
    const def = DEFAULT_LIMITS[type] ?? { min: 1, max: 1 }
    if (type === 'claude' && settings.maxClaudeTabsPerBranch) {
      return { min: def.min, max: settings.maxClaudeTabsPerBranch }
    }
    if (type === 'bash' && settings.maxBashTabsPerBranch) {
      return { min: def.min, max: settings.maxBashTabsPerBranch }
    }
    return def
  }

  const goToTab = (id: string) => {
    navigate(
      `/${repoId}/${branch.id}/${encodeURIComponent(id)}${location.search}`,
    )
  }

  const onAddOfType = async (type: string) => {
    setAdding(type)
    try {
      const t = await addTab(repoId, branch.id, type)
      goToTab(t.id)
    } finally {
      setAdding(null)
    }
  }

  // Drag-to-scroll the tab strip. Identical to pre-S009 behaviour.
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const dragRef = useRef<{
    startX: number
    startScroll: number
    moved: boolean
  } | null>(null)
  const dragHandlers = {
    onPointerDown(e: React.PointerEvent) {
      if (e.pointerType === 'mouse' && e.button !== 0) return
      const el = scrollRef.current
      if (!el) return
      dragRef.current = { startX: e.clientX, startScroll: el.scrollLeft, moved: false }
    },
    onPointerMove(e: React.PointerEvent) {
      const drag = dragRef.current
      const el = scrollRef.current
      if (!drag || !el) return
      const dx = e.clientX - drag.startX
      if (!drag.moved && Math.abs(dx) > 5) {
        drag.moved = true
        el.setPointerCapture?.(e.pointerId)
      }
      if (drag.moved) {
        el.scrollLeft = drag.startScroll - dx
      }
    },
    onPointerUp() {
      dragRef.current = null
    },
    onClickCapture(e: React.MouseEvent) {
      const el = scrollRef.current
      if (el?.dataset.justDragged === '1') {
        e.preventDefault()
        e.stopPropagation()
        delete el.dataset.justDragged
      }
    },
  }
  const wrappedPointerUp = (e: React.PointerEvent) => {
    const drag = dragRef.current
    if (drag?.moved && scrollRef.current) {
      scrollRef.current.dataset.justDragged = '1'
    }
    dragHandlers.onPointerUp()
    e.currentTarget.releasePointerCapture?.(e.pointerId)
  }

  // S009: split the flat tab list into consecutive same-type groups so
  // we can drop a `+` after each Multiple()=true group (Claude, Bash).
  const groups = groupTabsByType(branch.tabSet.tabs)

  // Currently-selected tab id (URL-decoded so colon-bearing ids match).
  const activeId = decodeURIComponent(tabId ?? '')

  // Activity Inbox unread badge belongs to the *first* Claude tab — the
  // notify hub is per-branch, not per-tab, and the canonical tab is
  // also where focus-clear fires today. Multi-tab parity comes in S014
  // when the inbox itself becomes per-tab aware.
  const claudeBadgeOwnerId = groups
    .find((g) => g.type === 'claude')
    ?.tabs[0]?.id

  return (
    <div className={styles.bar} role="tablist">
      <div
        ref={scrollRef}
        className={styles.scroll}
        onPointerDown={dragHandlers.onPointerDown}
        onPointerMove={dragHandlers.onPointerMove}
        onPointerUp={wrappedPointerUp}
        onPointerCancel={dragHandlers.onPointerUp}
        onClickCapture={dragHandlers.onClickCapture}
      >
        {groups.map((group) => {
          const lim = limitsFor(group.type)
          const atMax = group.tabs.length >= lim.max
          const atMin = group.tabs.length <= lim.min
          return (
            <span key={group.type} className={styles.group}>
              {group.tabs.map((t) => (
                <TabRow
                  key={t.id}
                  tab={t}
                  active={t.id === activeId}
                  unreadBadge={
                    t.id === claudeBadgeOwnerId && claudeUnread > 0
                      ? claudeUnread
                      : 0
                  }
                  onSelect={() => goToTab(t.id)}
                  onContext={(x, y) => openContext(t, atMin, x, y)}
                />
              ))}
              {/* S009: per-type + button sits at the right edge of each
                  Multiple()=true group. Disabled when at the configured
                  cap; tooltip surfaces the reason. */}
              {group.canAdd && (
                <button
                  data-testid={`tab-add-${group.type}`}
                  data-tab-type={group.type}
                  className={styles.addBtn}
                  onClick={() => onAddOfType(group.type)}
                  disabled={adding === group.type || atMax}
                  title={
                    atMax
                      ? `${capitalise(group.type)} tabs are at the cap (${lim.max}).`
                      : `New ${capitalise(group.type)} tab`
                  }
                >
                  +
                </button>
              )}
            </span>
          )
        })}
      </div>
      <WorkspaceActions
        repoId={repoId}
        branchId={branch.id}
        repo={repo}
        branch={branch}
      />
    </div>
  )

  function openContext(t: Tab, atMin: boolean, x: number, y: number) {
    // Old "Restart Claude / Resume Claude" entries pre-date stream-json
    // mode and rely on REST routes that no longer exist (the S004 wire
    // doesn't expose them). Removed here so the menu stays accurate;
    // both behaviours are now reachable from the in-tab Claude UI.
    const claudeItems: Array<
      | { label: string; onClick: () => void; danger?: boolean; disabled?: boolean }
      | { type: 'separator' }
    > = []
    const renameDisabled =
      // Singletons (Files, Git) have nothing to rename.
      !t.multiple ||
      // Bash tabs derive their name from the tmux window suffix; rename
      // works there. Claude tabs are auto-named "Claude" / "Claude 2" /
      // ... and renaming is deferred to the S020 backlog item, so we
      // disable it here for now to avoid wedge state.
      t.type === 'claude'
    const closeDisabled =
      // Files/Git can never close — singletons.
      !t.multiple ||
      // Last instance of a Multiple()=true group.
      atMin
    showContextMenu(
      [
        { type: 'heading', label: t.name },
        ...claudeItems,
        {
          label: 'Rename…',
          disabled: renameDisabled,
          onClick: async () => {
            const current = extractName(t)
            const next = await promptDialog.ask({
              title: 'Rename tab',
              defaultValue: current,
              confirmLabel: 'Rename',
              validate: (v) => {
                const trimmed = v.trim()
                if (!trimmed) return 'Name cannot be empty.'
                if (trimmed === current) return null
                return null
              },
            })
            if (next == null) return
            const trimmed = next.trim()
            if (trimmed && trimmed !== current) {
              await renameTab(repoId!, branch.id, t.id, trimmed)
            }
          },
        },
        { type: 'separator' },
        {
          label: closeDisabled
            ? atMin && t.multiple
              ? `Close tab (last ${capitalise(t.type)} — protected)`
              : 'Close tab (protected)'
            : 'Close tab',
          danger: true,
          disabled: closeDisabled,
          onClick: async () => {
            const ok = await confirmDialog.ask({
              title: 'Close tab?',
              message:
                t.type === 'bash'
                  ? `Close tab "${t.name}"? The tmux window will be killed.`
                  : `Close tab "${t.name}"? This Claude conversation will be detached (the session id is preserved and remains resumable from history).`,
              confirmLabel: 'Close',
              danger: true,
            })
            if (ok) await removeTab(repoId!, branch.id, t.id)
          },
        },
      ],
      x,
      y,
    )
  }
}

function TabRow({
  tab,
  active,
  unreadBadge,
  onSelect,
  onContext,
}: {
  tab: Tab
  active: boolean
  unreadBadge: number
  onSelect: () => void
  onContext: (x: number, y: number) => void
}) {
  const longPress = useLongPress((x, y) => onContext(x, y))
  return (
    <button
      role="tab"
      data-testid={`tab-${tab.id}`}
      data-tab-type={tab.type}
      data-tab-id={tab.id}
      aria-selected={active}
      className={active ? `${styles.tab} ${styles.tabActive}` : styles.tab}
      onClick={onSelect}
      onContextMenu={(e) => {
        e.preventDefault()
        onContext(e.clientX, e.clientY)
      }}
      title={
        tab.protected
          ? `${tab.name} — Right-click / long-press for actions`
          : `${tab.name} — Right-click / long-press for actions`
      }
      {...longPress}
    >
      <span className={styles.tabIcon}>{iconFor(tab.type)}</span>
      <span className={styles.tabLabel}>{tab.name}</span>
      {unreadBadge > 0 && <span className={styles.tabBadge}>{unreadBadge}</span>}
    </button>
  )
}

interface TabGroup {
  type: string
  tabs: Tab[]
  canAdd: boolean // tab.multiple === true (only multi-instance groups get +)
}

function groupTabsByType(tabs: Tab[]): TabGroup[] {
  const groups: TabGroup[] = []
  let cur: TabGroup | null = null
  for (const t of tabs) {
    if (!cur || cur.type !== t.type) {
      cur = { type: t.type, tabs: [], canAdd: t.multiple === true }
      groups.push(cur)
    }
    cur.tabs.push(t)
    // If any tab in the group claims multiple-allowed, the group can
    // host a + button. (Should be uniform per type, but be defensive.)
    if (t.multiple) cur.canAdd = true
  }
  return groups
}

function extractName(t: Tab): string {
  if (!t.id.includes(':')) return t.name
  return t.id.split(':')[1] ?? t.name
}

function capitalise(s: string): string {
  return s.length === 0 ? s : s[0].toUpperCase() + s.slice(1)
}

function iconFor(type: string): ReactNode {
  switch (type) {
    case 'claude':
      return <ClaudeIcon style={{ color: 'var(--color-accent-light)' }} />
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
