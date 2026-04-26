// Activity Inbox — header bell that surfaces every branch's
// agent notifications with inline actions.
//
// The store already aggregates per-branch state via /api/notifications and
// the events WS; this component just renders that and adds the y/n/Resume
// shortcuts.

import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'

import { terminalManager } from '../../lib/terminal-manager'
import { usePalmuxStore } from '../../stores/palmux-store'

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
        })
      }
    }
    out.sort((a, b) => {
      if (a.unreadCount !== b.unreadCount) return b.unreadCount - a.unreadCount
      return (b.lastAt ?? '').localeCompare(a.lastAt ?? '')
    })
    return out
  }, [repos, notifications])

  const totalUnread = rows.reduce((sum, r) => sum + r.unreadCount, 0)

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
    navigate(
      `/${encodeURIComponent(row.repoId)}/${encodeURIComponent(row.branchId)}/${encodeURIComponent(row.claudeTabId)}${search}`,
    )
    setOpen(false)
  }

  const sendKey = (row: InboxRow, data: string) => {
    if (!row.claudeTabId) return
    const key = `${row.repoId}/${row.branchId}/${row.claudeTabId}`
    if (!terminalManager.sendInput(key, data)) {
      // The terminal isn't mounted; navigate there first so it mounts, then
      // resend on next tick. Best-effort.
      goTo(row)
      window.setTimeout(() => terminalManager.sendInput(key, data), 200)
      return
    }
    void clearBranchNotifications(row.repoId, row.branchId)
  }

  return (
    <div ref={ref} className={styles.bellWrap}>
      <button
        className={styles.bellBtn}
        onClick={() => setOpen((v) => !v)}
        title={totalUnread ? `${totalUnread} unread notifications` : 'Activity inbox'}
        aria-label="Activity inbox"
      >
        🔔
        {totalUnread > 0 && <span className={styles.badge}>{totalUnread > 99 ? '99+' : totalUnread}</span>}
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
                <li
                  key={`${row.repoId}/${row.branchId}`}
                  className={row.unreadCount > 0 ? `${styles.row} ${styles.unread}` : styles.row}
                >
                  <div className={styles.rowHead} onClick={() => goTo(row)}>
                    <span className={styles.branchName}>
                      {row.repoLabel} / {row.branchName}
                    </span>
                    {row.unreadCount > 0 && (
                      <span className={styles.unreadCount}>{row.unreadCount}</span>
                    )}
                  </div>
                  {row.lastMessage && (
                    <p className={styles.message} onClick={() => goTo(row)}>
                      {row.lastMessage}
                    </p>
                  )}
                  <div className={styles.rowHead}>
                    <span className={styles.timestamp}>
                      {row.lastAt ? formatRelative(row.lastAt) : ''}
                    </span>
                  </div>
                  {row.hasClaudeTab && (
                    <div className={styles.actions}>
                      <button
                        className={styles.action}
                        data-kind="yes"
                        onClick={() => sendKey(row, 'y\r')}
                      >
                        y
                      </button>
                      <button
                        className={styles.action}
                        data-kind="no"
                        onClick={() => sendKey(row, 'n\r')}
                      >
                        n
                      </button>
                      <button className={styles.action} onClick={() => sendKey(row, '/resume\r')}>
                        Resume
                      </button>
                      <button className={styles.action} onClick={() => goTo(row)}>
                        Open
                      </button>
                    </div>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
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

