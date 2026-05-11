/**
 * claude-run-button.tsx — S031-3
 *
 * Persistent ▶ Run dropdown in the Claude tab header.
 * Reads /api/.../commands, groups by source, click → resolveBashTarget + send.
 */

import { useEffect, useReducer, useRef, useState } from 'react'
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

// S13b16a-4: useReducer-based fetch state for the Run dropdown's
// command list. Combining `loading` + `commands` into a single reducer
// (a) eliminates the inline `setLoading(true)` that
// `react-hooks/set-state-in-effect` flagged, and (b) lets us atomically
// reset to "loading" when the route changes — the React 19 recommended
// pattern for mount-fetch effects.
type FetchAction =
  | { type: 'start' }
  | { type: 'ok'; data: DetectedCommand[] }
  | { type: 'err' }

interface FetchState {
  loading: boolean
  commands: DetectedCommand[]
}

const fetchInitial: FetchState = { loading: true, commands: [] }

function fetchReducer(_s: FetchState, a: FetchAction): FetchState {
  switch (a.type) {
    case 'start': return { loading: true, commands: [] }
    case 'ok':    return { loading: false, commands: a.data }
    case 'err':   return { loading: false, commands: [] }
  }
}

export function ClaudeRunButton({ repoId, branchId }: Props) {
  const [open, setOpen] = useState(false)
  const [{ loading, commands }, dispatch] = useReducer(fetchReducer, fetchInitial)
  const buttonRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()

  const addTab = usePalmuxStore((s) => s.addTab)
  const activeRepo = usePalmuxStore((s) => selectRepoById(repoId)(s))
  const activeBranch = usePalmuxStore((s) => selectBranchById(repoId, branchId)(s))

  useEffect(() => {
    let cancelled = false
    dispatch({ type: 'start' })
    void api
      .get<DetectedCommand[]>(
        `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/commands`,
      )
      .then((cs) => {
        if (!cancelled) dispatch({ type: 'ok', data: cs })
      })
      .catch(() => {
        if (!cancelled) dispatch({ type: 'err' })
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
