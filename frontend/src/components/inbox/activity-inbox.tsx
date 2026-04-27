// Activity Inbox — header bell that surfaces every branch's
// agent notifications with inline actions.
//
// The store already aggregates per-branch state via /api/notifications and
// the events WS; this component renders that and adds inline action
// buttons. Claude permission requests use the dedicated REST endpoint to
// answer without opening the Claude WS, so the user can allow/deny from
// any tab.

import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'

import { api } from '../../lib/api'
import { terminalManager } from '../../lib/terminal-manager'
import { urlForTab } from '../../lib/tab-nav'
import {
  type NotificationItem,
  selectBranchNotifications,
  usePalmuxStore,
} from '../../stores/palmux-store'

import styles from './inbox.module.css'

interface InboxRow {
  repoId: string
  branchId: string
  branchName: string
  repoLabel: string
  unreadCount: number
  lastMessage?: string
  lastType?: string
  lastAt?: string
  hasClaudeTab: boolean
  claudeTabId?: string
  claudeTabIsTerminal: boolean
  /** Most-recent unresolved notification from this branch (e.g. a pending
   *  permission). Drives the Allow / Deny inline buttons. */
  pendingItem?: NotificationItem
}

export function ActivityInbox() {
  const [open, setOpen] = useState(false)
  const repos = usePalmuxStore((s) => s.repos)
  const notifications = usePalmuxStore((s) => s.notifications)
  const clearBranchNotifications = usePalmuxStore((s) => s.clearBranchNotifications)
  const navigate = useNavigate()
  const location = useLocation()
  const ref = useRef<HTMLDivElement | null>(null)

  // Collapse rows: every branch that has any notification (read or unread).
  const rows = useMemo<InboxRow[]>(() => {
    const out: InboxRow[] = []
    for (const repo of repos) {
      for (const branch of repo.openBranches) {
        const key = `${repo.id}/${branch.id}`
        const state = notifications[key]
        if (!state || (!state.lastMessage && state.unreadCount === 0)) continue
        const claudeTab = branch.tabSet.tabs.find((t) => t.type === 'claude')
        const items = state.notifications ?? []
        const pendingItem = items
          .slice()
          .reverse()
          .find((n) => !n.resolved && (n.actions?.length ?? 0) > 0)
        out.push({
          repoId: repo.id,
          branchId: branch.id,
          branchName: branch.name,
          repoLabel: repoDisplay(repo.ghqPath),
          unreadCount: state.unreadCount,
          lastMessage: state.lastMessage,
          lastType: state.lastType,
          lastAt: state.lastAt,
          hasClaudeTab: !!claudeTab,
          claudeTabId: claudeTab?.id,
          claudeTabIsTerminal: !!claudeTab?.windowName,
          pendingItem,
        })
      }
    }
    out.sort((a, b) => {
      // Pending actionable items first.
      if (!!a.pendingItem !== !!b.pendingItem) return a.pendingItem ? -1 : 1
      if (a.unreadCount !== b.unreadCount) return b.unreadCount - a.unreadCount
      return (b.lastAt ?? '').localeCompare(a.lastAt ?? '')
    })
    return out
  }, [repos, notifications])

  const totalUnread = rows.reduce((sum, r) => sum + r.unreadCount, 0)
  const totalPending = rows.filter((r) => r.pendingItem).length

  // Click-outside / Esc closes the popover.
  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      if (!ref.current) return
      if (!ref.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('pointerdown', onPointerDown, true)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown, true)
      window.removeEventListener('keydown', onKey)
    }
  }, [open])

  const goTo = (row: InboxRow) => {
    if (!row.claudeTabId) return
    const search = location.search
    navigate(urlForTab(row.repoId, row.branchId, row.claudeTabId) + search)
    setOpen(false)
  }

  return (
    <div ref={ref} className={styles.bellWrap}>
      <button
        className={styles.bellBtn}
        onClick={() => setOpen((v) => !v)}
        title={
          totalPending
            ? `${totalPending} pending request${totalPending > 1 ? 's' : ''}`
            : totalUnread
              ? `${totalUnread} unread notifications`
              : 'Activity inbox'
        }
        aria-label="Activity inbox"
      >
        🔔
        {(totalPending > 0 || totalUnread > 0) && (
          <span className={styles.badge}>
            {totalPending > 0 ? totalPending : totalUnread > 99 ? '99+' : totalUnread}
          </span>
        )}
      </button>
      {open && (
        <div className={styles.popover} role="dialog" aria-label="Activity inbox">
          <header className={styles.head}>
            <h3 className={styles.title}>Activity Inbox</h3>
            {rows.some((r) => r.unreadCount > 0) && (
              <button
                className={styles.clearAll}
                onClick={() => {
                  for (const r of rows) {
                    if (r.unreadCount > 0) void clearBranchNotifications(r.repoId, r.branchId)
                  }
                }}
              >
                Mark all read
              </button>
            )}
          </header>
          {rows.length === 0 ? (
            <p className={styles.empty}>No activity yet.</p>
          ) : (
            <ul className={styles.list}>
              {rows.map((row) => (
                <InboxRowView
                  key={`${row.repoId}/${row.branchId}`}
                  row={row}
                  onOpen={() => goTo(row)}
                  onClose={() => setOpen(false)}
                />
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}

function InboxRowView({
  row,
  onOpen,
  onClose,
}: {
  row: InboxRow
  onOpen: () => void
  onClose: () => void
}) {
  const clearBranchNotifications = usePalmuxStore((s) => s.clearBranchNotifications)

  // Live read so the row updates without remounting when the underlying
  // notification flips to resolved.
  const live = usePalmuxStore(selectBranchNotifications(row.repoId, row.branchId))
  const pendingItem = useMemo(() => {
    const items = live?.notifications ?? []
    return items
      .slice()
      .reverse()
      .find((n) => !n.resolved && (n.actions?.length ?? 0) > 0)
  }, [live])

  const respondPermission = async (decision: 'allow' | 'deny') => {
    if (!pendingItem?.requestId) return
    try {
      await api.post(
        `/api/repos/${encodeURIComponent(row.repoId)}/branches/${encodeURIComponent(row.branchId)}/tabs/claude/permission/${encodeURIComponent(pendingItem.requestId)}`,
        { decision, scope: 'once' },
      )
    } catch {
      // best-effort; the store will resync on the next event
    }
  }

  // Legacy tmux-tab fallback: send keystrokes through terminalManager.
  const sendKey = (data: string) => {
    if (!row.claudeTabId || !row.claudeTabIsTerminal) return
    const key = `${row.repoId}/${row.branchId}/${row.claudeTabId}`
    if (!terminalManager.sendInput(key, data)) {
      onOpen()
      window.setTimeout(() => terminalManager.sendInput(key, data), 200)
      return
    }
    void clearBranchNotifications(row.repoId, row.branchId)
  }

  const isMcpPermission = !!pendingItem?.actions?.some((a) =>
    a.action.startsWith('claude.permission.'),
  )

  return (
    <li className={row.unreadCount > 0 ? `${styles.row} ${styles.unread}` : styles.row}>
      <div className={styles.rowHead} onClick={onOpen}>
        <span className={styles.branchName}>
          {row.repoLabel} / {row.branchName}
        </span>
        {pendingItem && <span className={styles.unreadCount}>!</span>}
        {!pendingItem && row.unreadCount > 0 && (
          <span className={styles.unreadCount}>{row.unreadCount}</span>
        )}
      </div>
      {(pendingItem?.message ?? row.lastMessage) && (
        <p className={styles.message} onClick={onOpen}>
          {pendingItem?.title ? <strong>{pendingItem.title}: </strong> : null}
          {pendingItem?.message ?? row.lastMessage}
        </p>
      )}
      <div className={styles.rowHead}>
        <span className={styles.timestamp}>{row.lastAt ? formatRelative(row.lastAt) : ''}</span>
      </div>
      {isMcpPermission && pendingItem ? (
        <div className={styles.actions}>
          <button
            className={styles.action}
            data-kind="yes"
            onClick={() => {
              void respondPermission('allow')
              onClose()
            }}
          >
            Allow
          </button>
          <button
            className={styles.action}
            data-kind="no"
            onClick={() => {
              void respondPermission('deny')
              onClose()
            }}
          >
            Deny
          </button>
          <button className={styles.action} onClick={onOpen}>
            Open
          </button>
        </div>
      ) : row.claudeTabIsTerminal ? (
        <div className={styles.actions}>
          <button className={styles.action} data-kind="yes" onClick={() => sendKey('y\r')}>
            y
          </button>
          <button className={styles.action} data-kind="no" onClick={() => sendKey('n\r')}>
            n
          </button>
          <button className={styles.action} onClick={() => sendKey('/resume\r')}>
            Resume
          </button>
          <button className={styles.action} onClick={onOpen}>
            Open
          </button>
        </div>
      ) : row.hasClaudeTab ? (
        <div className={styles.actions}>
          <button className={styles.action} onClick={onOpen}>
            Open
          </button>
        </div>
      ) : null}
    </li>
  )
}

function repoDisplay(ghqPath: string): string {
  const parts = ghqPath.split('/')
  return parts.slice(1).join('/') || ghqPath
}

function formatRelative(iso: string): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const delta = Date.now() - t
  if (delta < 60_000) return 'just now'
  if (delta < 3_600_000) return `${Math.floor(delta / 60_000)}m ago`
  if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)}h ago`
  return new Date(iso).toLocaleDateString()
}
