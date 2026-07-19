import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'

import { useLongPress } from '../hooks/use-long-press'
import type { Branch, Tab } from '../lib/api'
import { agentCapabilities, isAgentTab, useAgentRegistryStore } from '../stores/agent-registry-store'
import {
  selectBranchNotifications,
  selectRepoById,
  usePalmuxStore,
} from '../stores/palmux-store'
import { DEFAULT_TAB_SETTINGS, useTabSettingsStore } from '../stores/tab-settings-store'

import { confirmDialog } from './context-menu/confirm-dialog'
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

// S2b5691-2: fallback max for a registry agent kind with no DEFAULT_LIMITS
// entry of its own (mirrors backend agenttab.Provider's
// defaultMaxTabsPerBranch — codex/opencode/a user-defined agent all use it).
const DEFAULT_GENERIC_AGENT_MAX = 3

export function TabBar({ branch }: Props) {
  const { repoId } = useParams()
  const { tabId } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  const addTab = usePalmuxStore((s) => s.addTab)
  const removeTab = usePalmuxStore((s) => s.removeTab)
  const renameTab = usePalmuxStore((s) => s.renameTab)
  const reorderTabs = usePalmuxStore((s) => s.reorderTabs)
  const settings = usePalmuxStore((s) => s.globalSettings)
  // S2b5691-2: enabled agent kinds (always includes at least claude — see
  // agent-registry-store's claude-only fallback). Reactive so the `+`
  // button's picker-vs-immediate-add behavior updates once GET /api/agents
  // resolves.
  const agents = useAgentRegistryStore((s) => s.agents)
  const notifs = usePalmuxStore(
    repoId ? selectBranchNotifications(repoId, branch.id) : () => undefined,
  )
  const claudeUnread = notifs?.unreadCount ?? 0
  const [adding, setAdding] = useState<string | null>(null)
  const [renamingTabId, setRenamingTabId] = useState<string | null>(null)
  const showContextMenu = useContextMenu()
  const repo = usePalmuxStore((s) =>
    repoId ? selectRepoById(repoId)(s) : undefined,
  )

  // Sadf90e: per-tab claude_mode settings — used to show the mode badge on
  // every Claude tab (not just the first one). Subscribe to the settings
  // dictionary so any patchSettings write triggers a re-render.
  const fetchTabSettings = useTabSettingsStore((s) => s.fetchSettings)
  const tabSettingsMap = useTabSettingsStore((s) => s.settings)
  const patchTabSettings = useTabSettingsStore((s) => s.patchSettings)

  useEffect(() => {
    if (!repoId) return
    // Fetch settings for every Claude tab on this branch so the badge can
    // render synchronously after the first paint.
    branch.tabSet.tabs.forEach((t) => {
      if (t.type === 'claude') {
        void fetchTabSettings(repoId, branch.id, t.id)
      }
    })
  }, [repoId, branch.id, branch.tabSet.tabs, fetchTabSettings])

  // S020: drag-and-drop reorder state. Tracked locally because the order
  // is committed to the server only on drop; while dragging we only show
  // visual indicators. `dragOverId` is the tab the cursor is hovering
  // over; `dragForbidden` is true when the hovered tab is in a different
  // group from the dragged tab (cross-group drop is rejected).
  const [draggingId, setDraggingId] = useState<string | null>(null)
  const [dragOverId, setDragOverId] = useState<string | null>(null)
  const [dragOverSide, setDragOverSide] = useState<'before' | 'after' | null>(null)
  const [dragForbidden, setDragForbidden] = useState(false)

  // S13b16a-3: hoisted above the early-return so the rules-of-hooks
  // ordering is preserved when repoId becomes nullable across renders.
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const dragRef = useRef<{
    startX: number
    startScroll: number
    moved: boolean
  } | null>(null)

  if (!repoId) return null

  // Per-type max from settings, falling back to built-in defaults so
  // the UI is functional before the settings round-trip lands.
  //
  // S2b5691-2: a registry agent kind with no DEFAULT_LIMITS entry (i.e.
  // every non-claude agent kind — codex, opencode, a user-defined agent)
  // falls back to DEFAULT_GENERIC_AGENT_MAX (mirrors backend
  // agenttab.Provider's defaultMaxTabsPerBranch) instead of the generic
  // {min:1,max:1} used for non-agent single-instance types (files/git). The
  // FE has no per-kind settings surface yet.
  const limitsFor = (type: string): { min: number; max: number } => {
    const def =
      DEFAULT_LIMITS[type] ??
      (agents.some((a) => a.kind === type)
        ? { min: 1, max: DEFAULT_GENERIC_AGENT_MAX }
        : { min: 1, max: 1 })
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

  // S2b5691-2: the agent-kind `+` buttons (claude's, and any other registry
  // kind's own group `+`) route through the agent-picker menu once more
  // than one agent kind is enabled. With only claude enabled (the default)
  // this is byte-for-byte the old behavior — onAddOfType(group.type) fires
  // immediately, no menu. Non-agent groups (bash/files/git) are untouched:
  // `agents` never contains those types, so isAgentGroupType is always
  // false for them.
  const isAgentGroupType = (type: string) => agents.some((a) => a.kind === type)
  const onAddClick = (e: React.MouseEvent, type: string) => {
    if (isAgentGroupType(type) && agents.length > 1) {
      const pickable = agents.filter((a) => {
        const lim = limitsFor(a.kind)
        const count = branch.tabSet.tabs.filter((t) => t.type === a.kind).length
        return count < lim.max
      })
      showContextMenu(
        pickable.map((a) => ({
          label: `New ${a.displayName}`,
          testId: `agent-picker-item-${a.kind}`,
          onClick: () => void onAddOfType(a.kind),
        })),
        e.clientX,
        e.clientY,
        { containerTestId: 'agent-picker' },
      )
      return
    }
    void onAddOfType(type)
  }

  // Drag-to-scroll the tab strip. Identical to pre-S009 behaviour.
  // (scrollRef + dragRef are declared above the early-return for rules-of-hooks.)
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
  // Sadf90e: the standalone `claude-tui` tab type is gone — claudetui's
  // Provider now returns 0 tabs from OnBranchOpen, so it never appears in
  // branch.tabSet.tabs in the first place. No filter needed.
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

  // S020 reorder: compute new order based on a drop and commit to server.
  const commitReorder = async (
    draggedId: string,
    targetId: string,
    side: 'before' | 'after',
  ) => {
    const draggedTab = branch.tabSet.tabs.find((t) => t.id === draggedId)
    const targetTab = branch.tabSet.tabs.find((t) => t.id === targetId)
    if (!draggedTab || !targetTab) return
    if (draggedTab.type !== targetTab.type) return // cross-group forbidden
    if (!draggedTab.multiple || !targetTab.multiple) return
    // Build the new order for this group only.
    const groupTabs = branch.tabSet.tabs.filter(
      (t) => t.type === draggedTab.type && t.multiple,
    )
    const without = groupTabs.filter((t) => t.id !== draggedId).map((t) => t.id)
    const insertAt = without.indexOf(targetId)
    if (insertAt < 0) return
    const newOrder =
      side === 'before'
        ? [...without.slice(0, insertAt), draggedId, ...without.slice(insertAt)]
        : [...without.slice(0, insertAt + 1), draggedId, ...without.slice(insertAt + 1)]
    if (sameSequence(newOrder, groupTabs.map((t) => t.id))) return
    try {
      await reorderTabs(repoId, branch.id, newOrder)
    } catch (err) {
      // The store rolls back optimistically; surface to console for
      // dev visibility. A toast layer is out of scope here.
      console.warn('reorderTabs failed:', err)
    }
  }

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
                  // Sadf90e hotfix 2026-05-27: only show the Agent badge —
                  // TUI is the default and doesn't need a chip. Falls back
                  // to the shared DEFAULT_TAB_SETTINGS module constant so
                  // Zustand selector returns a stable reference when
                  // fetchSettings hasn't completed yet.
                  modeBadge={(() => {
                    if (t.type !== 'claude') return undefined
                    const mode = (tabSettingsMap[`${repoId}/${branch.id}/${t.id}`]
                      ?? DEFAULT_TAB_SETTINGS).claude_mode
                    return mode === 'agent' ? 'Agent' : undefined
                  })()}
                  onSelect={() => {
                    if (renamingTabId) return
                    goToTab(t.id)
                  }}
                  onContext={(x, y) => openContext(t, atMin, x, y)}
                  draggable={t.multiple === true}
                  draggingId={draggingId}
                  dragOverId={dragOverId}
                  dragOverSide={dragOverSide}
                  dragForbidden={dragForbidden}
                  renaming={renamingTabId === t.id}
                  onCommitRename={async (newName) => {
                    setRenamingTabId(null)
                    if (newName == null) return
                    const trimmed = newName.trim()
                    const current = extractName(t)
                    if (!trimmed || trimmed === current) return
                    try {
                      await renameTab(repoId, branch.id, t.id, trimmed)
                    } catch (err) {
                      console.warn('renameTab failed:', err)
                    }
                  }}
                  onDragStart={() => setDraggingId(t.id)}
                  onDragEnd={() => {
                    setDraggingId(null)
                    setDragOverId(null)
                    setDragOverSide(null)
                    setDragForbidden(false)
                  }}
                  onDragOver={(side) => {
                    if (!draggingId || draggingId === t.id) {
                      setDragOverId(null)
                      setDragOverSide(null)
                      setDragForbidden(false)
                      return
                    }
                    const dragged = branch.tabSet.tabs.find(
                      (x) => x.id === draggingId,
                    )
                    const forbidden = !dragged || dragged.type !== t.type
                    setDragOverId(t.id)
                    setDragOverSide(side)
                    setDragForbidden(forbidden)
                  }}
                  onDrop={(side) => {
                    const target = t
                    const dragged = draggingId
                    setDraggingId(null)
                    setDragOverId(null)
                    setDragOverSide(null)
                    setDragForbidden(false)
                    if (!dragged || dragged === target.id) return
                    void commitReorder(dragged, target.id, side)
                  }}
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
                  onClick={(e) => onAddClick(e, group.type)}
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
    const claudeItems: Array<
      | { label: string; onClick: () => void; danger?: boolean; disabled?: boolean }
      | { type: 'separator' }
    > = []
    // Sadf90e hotfix 2026-05-27: Claude タブの右クリックメニューに mode 切替を追加。
    // active tab に依存せず、 右クリックされた t.id をそのまま操作する (= sibling
    // タブの mode は変えない、 Sadf90e の isolation 不変)。
    if (t.type === 'claude') {
      const currentMode = (tabSettingsMap[`${repoId}/${branch.id}/${t.id}`]
        ?? DEFAULT_TAB_SETTINGS).claude_mode
      const nextMode = currentMode === 'tui' ? 'agent' : 'tui'
      const nextLabel = nextMode === 'tui' ? 'TUI' : 'Agent'
      claudeItems.push({
        label: `Switch to ${nextLabel} mode`,
        onClick: () => {
          void patchTabSettings(repoId!, branch.id, t.id, { claude_mode: nextMode })
            .catch((err) => console.warn('switch claude mode failed:', err))
        },
      })
      claudeItems.push({ type: 'separator' })
    }
    // S020: rename now applies to every Multiple()=true tab type. The
    // backend persists Claude renames as a `tab_overrides.names` entry
    // and Bash renames via tmux RenameWindow + override migration.
    const renameDisabled = !t.multiple
    const closeDisabled =
      !t.multiple ||
      // Last instance of a Multiple()=true group.
      atMin
    // S020: Move-left / Move-right entries provide mobile parity for
    // drag-and-drop reorder (touch devices don't dispatch HTML5 drag
    // events). Disabled at boundaries.
    const moveDisabled = !t.multiple
    const groupTabs = branch.tabSet.tabs.filter(
      (x) => x.type === t.type && x.multiple,
    )
    const groupIdx = groupTabs.findIndex((x) => x.id === t.id)
    const canMoveLeft = !moveDisabled && groupIdx > 0
    const canMoveRight =
      !moveDisabled && groupIdx >= 0 && groupIdx < groupTabs.length - 1
    const moveTab = async (dir: 'left' | 'right') => {
      const idx = groupIdx
      if (idx < 0) return
      const targetIdx = dir === 'left' ? idx - 1 : idx + 1
      if (targetIdx < 0 || targetIdx >= groupTabs.length) return
      const newOrder = [...groupTabs]
      const [moved] = newOrder.splice(idx, 1)
      newOrder.splice(targetIdx, 0, moved)
      try {
        await reorderTabs(
          repoId!,
          branch.id,
          newOrder.map((x) => x.id),
        )
      } catch (err) {
        console.warn('moveTab failed:', err)
      }
    }
    showContextMenu(
      [
        { type: 'heading', label: t.name },
        ...claudeItems,
        {
          label: 'Rename…',
          disabled: renameDisabled,
          onClick: () => {
            setRenamingTabId(t.id)
          },
        },
        {
          label: 'Move left',
          disabled: !canMoveLeft,
          onClick: () => void moveTab('left'),
        },
        {
          label: 'Move right',
          disabled: !canMoveRight,
          onClick: () => void moveTab('right'),
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
                  : t.type === 'claude'
                    ? `Close tab "${t.name}"? This Claude conversation will be detached (the session id is preserved and remains resumable from history).`
                    // S2b5691-2: generic agent kinds (codex, opencode, a
                    // user-defined agent, …) share claude's "detach, not
                    // destroy" semantics (agenttab.Provider.RemoveTabForBranch
                    // tears down the daemon but keeps any resumable session id).
                    : `Close tab "${t.name}"? This session will be detached.`,
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

interface TabRowProps {
  tab: Tab
  active: boolean
  unreadBadge: number
  /** S1f75ec-2: when set, renders a mode chip next to the tab label.
   *  Used on the canonical Claude tab to show "Agent" or "TUI". */
  modeBadge?: 'Agent' | 'TUI'
  onSelect: () => void
  onContext: (x: number, y: number) => void
  draggable: boolean
  draggingId: string | null
  dragOverId: string | null
  dragOverSide: 'before' | 'after' | null
  dragForbidden: boolean
  renaming: boolean
  onCommitRename: (newName: string | null) => void
  onDragStart: () => void
  onDragEnd: () => void
  onDragOver: (side: 'before' | 'after') => void
  onDrop: (side: 'before' | 'after') => void
}

function TabRow({
  tab,
  active,
  unreadBadge,
  modeBadge,
  onSelect,
  onContext,
  draggable,
  draggingId,
  dragOverId,
  dragOverSide,
  dragForbidden,
  renaming,
  onCommitRename,
  onDragStart,
  onDragEnd,
  onDragOver,
  onDrop,
}: TabRowProps) {
  const longPress = useLongPress((x, y) => onContext(x, y))
  const isDragging = draggingId === tab.id
  const isDragOver = dragOverId === tab.id
  const inputRef = useRef<HTMLInputElement | null>(null)

  // Focus the rename input when entering rename mode.
  useEffect(() => {
    if (renaming && inputRef.current) {
      inputRef.current.focus()
      inputRef.current.select()
    }
  }, [renaming])

  let extra = ''
  if (isDragging) extra = ` ${styles.tabDragging}`
  if (isDragOver) {
    if (dragForbidden) {
      extra += ` ${styles.tabDropForbidden}`
    } else if (dragOverSide === 'before') {
      extra += ` ${styles.tabDropBefore}`
    } else if (dragOverSide === 'after') {
      extra += ` ${styles.tabDropAfter}`
    }
  }

  return (
    <button
      role="tab"
      data-testid={tab.type === 'claude' ? 'claude-tab' : `tab-${tab.id}`}
      data-tab-type={tab.type}
      data-tab-id={tab.id}
      data-rename-active={renaming ? '1' : undefined}
      data-drag-over={isDragOver ? (dragForbidden ? 'forbidden' : dragOverSide) : undefined}
      aria-selected={active}
      className={(active ? `${styles.tab} ${styles.tabActive}` : styles.tab) + extra}
      draggable={draggable && !renaming}
      onDragStart={(e) => {
        if (!draggable) return
        e.dataTransfer.effectAllowed = 'move'
        e.dataTransfer.setData('text/x-palmux-tab', tab.id)
        onDragStart()
      }}
      onDragEnd={onDragEnd}
      onDragOver={(e) => {
        if (!draggingId) return
        e.preventDefault()
        // Choose `before` if cursor is on the left half of the tab, else `after`.
        const rect = e.currentTarget.getBoundingClientRect()
        const side = e.clientX < rect.left + rect.width / 2 ? 'before' : 'after'
        e.dataTransfer.dropEffect = dragForbidden ? 'none' : 'move'
        onDragOver(side)
      }}
      onDrop={(e) => {
        if (!draggingId) return
        e.preventDefault()
        if (dragForbidden) {
          onDragEnd()
          return
        }
        const rect = e.currentTarget.getBoundingClientRect()
        const side = e.clientX < rect.left + rect.width / 2 ? 'before' : 'after'
        onDrop(side)
      }}
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
      {renaming ? (
        <input
          ref={inputRef}
          data-testid="tab-rename-input"
          className={styles.renameInput}
          defaultValue={extractName(tab)}
          onBlur={(e) => onCommitRename(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              onCommitRename((e.currentTarget as HTMLInputElement).value)
            } else if (e.key === 'Escape') {
              e.preventDefault()
              onCommitRename(null)
            }
            // Stop click handlers above from re-routing.
            e.stopPropagation()
          }}
          onClick={(e) => e.stopPropagation()}
        />
      ) : (
        <span className={styles.tabLabel}>{tab.name}</span>
      )}
      {/* S1f75ec-2: mode badge on the canonical Claude tab */}
      {modeBadge && (
        <span
          data-testid="claude-mode-badge"
          className={styles.modeBadge}
          title={`Claude mode: ${modeBadge}`}
        >
          {modeBadge}
        </span>
      )}
      <NotifyCapabilityBadge tabType={tab.type} />
      {unreadBadge > 0 && <span className={styles.tabBadge}>{unreadBadge}</span>}
    </button>
  )
}

// S2b5691-2 [AC-S2b5691-2-3]: informational chip on an agent tab whose hook
// mechanism cannot drive the full Activity Inbox vocabulary. Renders nothing
// for capabilities.notify === "full" (claude, opencode) and for non-agent
// tab types (bash/files/git/sprint), so it never adds noise to the common
// case — only turn_end (codex-like: can only say "your turn", never "waiting
// for permission") and none (a user-defined agent with no notify mechanism
// at all) get a visible marker, preventing "no notification = all good"
// misreads.
function NotifyCapabilityBadge({ tabType }: { tabType: string }) {
  const caps = agentCapabilities(tabType)
  if (!caps || caps.notify === 'full') return null
  const label = caps.notify === 'turn_end' ? '通知は turn 完了のみ' : '通知なし'
  return (
    <span
      data-testid="notify-capability-badge"
      data-notify-level={caps.notify}
      className={styles.notifyBadge}
      title={label}
      aria-label={label}
    >
      🔕
    </span>
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
  // For Bash tabs the rename targets the tmux window suffix (so the
  // editor should default to `dev-server`, not `Bash dev-server`). For
  // Claude tabs the rename writes a free-form display label, so we
  // start with whatever the user already sees.
  if (t.type === 'bash' && t.id.includes(':')) {
    return t.id.split(':')[1] ?? t.name
  }
  return t.name
}

function capitalise(s: string): string {
  return s.length === 0 ? s : s[0].toUpperCase() + s.slice(1)
}

function sameSequence(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false
  }
  return true
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
      // S2b5691-2: any other registry agent kind (codex, opencode, a
      // user-defined generic agent, …) gets a generic icon instead of the
      // bare bullet used for non-agent tab types.
      return isAgentTab(type) ? '🤖' : '•'
  }
}
