import { useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'

import type { Branch, Tab } from '../lib/api'
import { selectBranchNotifications, usePalmuxStore } from '../stores/palmux-store'

import { confirmDialog } from './context-menu/confirm-dialog'
import { promptDialog } from './context-menu/prompt-dialog'
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

  const onContext = (e: React.MouseEvent, t: Tab) => {
    e.preventDefault()
    showContextMenu(
      [
        { type: 'heading', label: t.name },
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
              await renameTab(repoId, branch.id, t.id, trimmed)
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
            if (ok) await removeTab(repoId, branch.id, t.id)
          },
        },
      ],
      e.clientX,
      e.clientY,
    )
  }

  return (
    <div className={styles.bar} role="tablist">
      <div className={styles.scroll}>
        {branch.tabSet.tabs.map((t) => {
          const active = t.id === decodeURIComponent(tabId ?? '')
          return (
            <button
              key={t.id}
              role="tab"
              aria-selected={active}
              className={active ? `${styles.tab} ${styles.tabActive}` : styles.tab}
              onClick={() => onSelect(t.id)}
              onContextMenu={(e) => onContext(e, t)}
              title={t.protected ? `${t.name} (protected)` : `${t.name} — Right-click for actions`}
            >
              <span className={styles.tabIcon}>{iconFor(t.type)}</span>
              <span className={styles.tabLabel}>{t.name}</span>
              {t.type === 'claude' && claudeUnread > 0 && (
                <span className={styles.tabBadge}>{claudeUnread}</span>
              )}
            </button>
          )
        })}
      </div>
      <button className={styles.addBtn} onClick={onAddBash} disabled={adding} title="New Bash tab">
        +
      </button>
    </div>
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
