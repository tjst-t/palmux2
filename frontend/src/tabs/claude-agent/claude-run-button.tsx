/**
 * claude-run-button.tsx — S031-3
 *
 * Persistent ▶ Run dropdown in the Claude tab header.
 * Reads /api/.../commands, groups by source, click → resolveBashTarget + send.
 */

import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'

import { api } from '../../lib/api'
import { resolveBashTarget } from '../../lib/bash-target'
import { terminalManager } from '../../lib/terminal-manager'
import { usePalmuxStore, selectBranchById, selectRepoById } from '../../stores/palmux-store'

import styles from './claude-run-button.module.css'

interface DetectedCommand {
  name: string
  source: string
  command: string
}

interface Props {
  repoId: string
  branchId: string
}

export function ClaudeRunButton({ repoId, branchId }: Props) {
  const [open, setOpen] = useState(false)
  const [commands, setCommands] = useState<DetectedCommand[]>([])
  const [loading, setLoading] = useState(false)
  const buttonRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()

  const addTab = usePalmuxStore((s) => s.addTab)
  const activeRepo = usePalmuxStore((s) => selectRepoById(repoId)(s))
  const activeBranch = usePalmuxStore((s) => selectBranchById(repoId, branchId)(s))

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    void api
      .get<DetectedCommand[]>(
        `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/commands`,
      )
      .then((cs) => {
        if (!cancelled) setCommands(cs)
      })
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [repoId, branchId])

  // Close on outside click
  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      const target = e.target as Node
      if (
        !buttonRef.current?.contains(target) &&
        !dropdownRef.current?.contains(target)
      ) {
        setOpen(false)
      }
    }
    window.addEventListener('mousedown', handler)
    return () => window.removeEventListener('mousedown', handler)
  }, [open])

  if (!loading && commands.length === 0) return null

  const runCommand = async (command: string) => {
    if (!activeRepo || !activeBranch) return
    setOpen(false)
    const target = await resolveBashTarget(
      repoId,
      branchId,
      activeBranch.tabSet.tabs,
      addTab,
    )
    if (!target) return
    // Navigate to the bash tab using target.tabId directly (avoids stale
    // closure when auto-create just created a tab not yet in tabs[] snapshot).
    const search = searchParams.toString() ? `?${searchParams.toString()}` : ''
    navigate(
      `/${encodeURIComponent(repoId)}/${encodeURIComponent(branchId)}/${encodeURIComponent(target.tabId)}${search}`,
    )
    terminalManager.sendInput(target.termKey, command + '\r')
    terminalManager.focus(target.termKey)
  }

  // Group commands by source
  const groups: Record<string, DetectedCommand[]> = {}
  for (const c of commands) {
    ;(groups[c.source] ??= []).push(c)
  }
  const sourceOrder = Object.keys(groups).sort()

  return (
    <div className={styles.wrap} data-testid="run-btn-wrap">
      <button
        ref={buttonRef}
        type="button"
        className={`${styles.btn}${open ? ` ${styles.btnOpen}` : ''}`}
        onClick={() => setOpen((v) => !v)}
        title="Run a Make/npm command in a Bash tab"
        data-testid="run-btn"
        disabled={loading}
      >
        <span className={styles.triangle}>▶</span>
        <span className={styles.label}>Run</span>
        <span className={styles.caret}>▾</span>
      </button>
      {open && (
        <div ref={dropdownRef} className={styles.dropdown} data-testid="run-dropdown">
          {sourceOrder.map((source) => (
            <div key={source}>
              <div className={styles.groupHeader}>{source}</div>
              {groups[source].map((c) => (
                <button
                  key={c.name}
                  type="button"
                  className={styles.item}
                  onClick={() => void runCommand(c.command)}
                  data-testid={`run-item-${c.name}`}
                >
                  <span className={styles.itemLabel}>{c.name}</span>
                  <span className={styles.itemBadge}>{source}</span>
                </button>
              ))}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
