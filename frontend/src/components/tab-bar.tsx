import { useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'

import { useLongPress } from '../hooks/use-long-press'
import { api } from '../lib/api'
import type { Branch, Tab } from '../lib/api'
import { selectBranchNotifications, usePalmuxStore } from '../stores/palmux-store'

import { confirmDialog } from './context-menu/confirm-dialog'
import { promptDialog } from './context-menu/prompt-dialog'
import { selectDialog } from './context-menu/select-dialog'
import { useContextMenu } from './context-menu/store'
import styles from './tab-bar.module.css'

interface Props {
  branch: Branch
}

export function TabBar({ branch }: Props) {
  const { repoId } = useParams()
  const { tabId } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  const addTab = usePalmuxStore((s) => s.addTab)
  const removeTab = usePalmuxStore((s) => s.removeTab)
  const renameTab = usePalmuxStore((s) => s.renameTab)
  const notifs = usePalmuxStore(
    repoId ? selectBranchNotifications(repoId, branch.id) : () => undefined,
  )
  const claudeUnread = notifs?.unreadCount ?? 0
  const [adding, setAdding] = useState(false)
  const showContextMenu = useContextMenu()

  if (!repoId) return null

  const goToTab = (id: string) => {
    navigate(`/${repoId}/${branch.id}/${encodeURIComponent(id)}${location.search}`)
  }

  const onSelect = (id: string) => goToTab(id)

  const onAddBash = async () => {
    setAdding(true)
    try {
      const t = await addTab(repoId, branch.id, 'bash')
      goToTab(t.id)
    } finally {
      setAdding(false)
    }
  }

  // Drag-to-scroll the tab strip with the pointer. We swallow the synthetic
  // click that follows a real drag so the tab under the pointer doesn't get
  // accidentally activated.
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const dragRef = useRef<{ startX: number; startScroll: number; moved: boolean } | null>(null)
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
      // If a drag actually moved, kill the click that follows.
      // dragRef is already null here (pointerup cleared it); we cache the
      // last-moved state on the element.
      const el = scrollRef.current
      if (el?.dataset.justDragged === '1') {
        e.preventDefault()
        e.stopPropagation()
        delete el.dataset.justDragged
      }
    },
  }
  // Mark "we just dragged" after movement so onClickCapture can suppress.
  const wrappedPointerUp = (e: React.PointerEvent) => {
    const drag = dragRef.current
    if (drag?.moved && scrollRef.current) {
      scrollRef.current.dataset.justDragged = '1'
    }
    dragHandlers.onPointerUp()
    e.currentTarget.releasePointerCapture?.(e.pointerId)
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
        {branch.tabSet.tabs.map((t) => (
          <TabRow
            key={t.id}
            tab={t}
            active={t.id === decodeURIComponent(tabId ?? '')}
            unreadBadge={t.type === 'claude' && claudeUnread > 0 ? claudeUnread : 0}
            onSelect={() => onSelect(t.id)}
            onContext={(x, y) => openContext(t, x, y)}
          />
        ))}
      </div>
      <button className={styles.addBtn} onClick={onAddBash} disabled={adding} title="New Bash tab">
        +
      </button>
    </div>
  )

  function openContext(t: Tab, x: number, y: number) {
    const claudeItems =
      t.type === 'claude'
        ? [
            {
              label: 'Restart Claude…',
              onClick: async () => {
                const model = await selectDialog.ask({
                  title: 'Restart Claude',
                  message: 'Pick a model. Default uses whatever the CLI defaults to.',
                  options: [
                    { label: 'Default', value: '' },
                    {
                      label: 'Opus 4.7',
                      value: 'claude-opus-4-7',
                      detail: 'most capable',
                    },
                    {
                      label: 'Sonnet 4.6',
                      value: 'claude-sonnet-4-6',
                      detail: 'balanced',
                    },
                    {
                      label: 'Haiku 4.5',
                      value: 'claude-haiku-4-5-20251001',
                      detail: 'fast',
                    },
                  ],
                })
                if (model == null) return
                await api.post(
                  `/api/repos/${encodeURIComponent(repoId!)}/branches/${encodeURIComponent(branch.id)}/claude/restart`,
                  { model },
                )
              },
            },
            {
              label: 'Resume Claude',
              onClick: async () => {
                await api.post(
                  `/api/repos/${encodeURIComponent(repoId!)}/branches/${encodeURIComponent(branch.id)}/claude/resume`,
                )
              },
            },
            { type: 'separator' as const },
          ]
        : []
    showContextMenu(
      [
        { type: 'heading', label: t.name },
        ...claudeItems,
        {
          label: 'Rename…',
          disabled: t.protected,
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
          label: 'Close tab',
          danger: true,
          disabled: t.protected,
          onClick: async () => {
            const ok = await confirmDialog.ask({
              title: 'Close tab?',
              message: `Close tab "${t.name}"? The tmux window will be killed.`,
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
      aria-selected={active}
      className={active ? `${styles.tab} ${styles.tabActive}` : styles.tab}
      onClick={onSelect}
      onContextMenu={(e) => {
        e.preventDefault()
        onContext(e.clientX, e.clientY)
      }}
      title={tab.protected ? `${tab.name} (protected)` : `${tab.name} — Right-click / long-press for actions`}
      {...longPress}
    >
      <span className={styles.tabIcon}>{iconFor(tab.type)}</span>
      <span className={styles.tabLabel}>{tab.name}</span>
      {unreadBadge > 0 && <span className={styles.tabBadge}>{unreadBadge}</span>}
    </button>
  )
}

function extractName(t: Tab): string {
  if (!t.id.includes(':')) return t.name
  return t.id.split(':')[1] ?? t.name
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
