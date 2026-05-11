// S4b9df4-2: TopBar extracted from claude-agent-view.tsx into its own
// module. Helpers that were only called from TopBar (pipClass /
// labelForStatus / mcpPipClass / contextPercent / statusToneAgree)
// moved with it. The 14 props the original TopBar accepted are now
// grouped into 3 buckets to make the call site readable and to make
// adding new buttons / status pills less cumbersome:
//
//   - status:  AgentStatus + cost + ctx% + connState + mcpServers
//   - actions: per-button event handlers (now an `actions: { ... }`
//              object so adding a new button doesn't require a fresh
//              top-level prop on the parent)
//   - refs/ctx: button refs the parent owns (history/mcp anchors), the
//              repo/branch context the Run button needs.
//
// The MCP popup open-state and toggle still live with the parent —
// they're shared with the popup itself, so keeping them at the top
// level avoids prop-drilling them through TopBar twice.
//
// User-facing behaviour: unchanged. All data-testid attributes
// preserved verbatim so the Story 1 E2E coverage holds.

import type React from 'react'

import { ClaudeRunButton } from './claude-run-button'
import styles from './claude-agent-view.module.css'
import { rollupTone, statusTone, type MCPStatusTone } from './mcp-status'
import type { AgentStatus, MCPServerInfo } from './types'
import type { AgentUsage } from './agent-state'

/** Live agent state the TopBar mirrors as pip / cost / context. */
export interface TopBarStatus {
  status: AgentStatus
  totalCostUsd: number
  /** Pre-computed percent (0..100). Pass `undefined` to hide the chip. */
  contextPct?: number
  mcpServers: MCPServerInfo[]
  connState: 'connecting' | 'open' | 'closed' | 'closing'
  /** True when the agent is currently busy (thinking / running tool /
   *  starting). Drives the visibility of the Interrupt button. */
  canInterrupt: boolean
}

/** Per-button handlers. Keeping them in an `actions` object means
 *  adding a new button is a one-liner here + the JSX, not a fresh
 *  prop on the call site. */
export interface TopBarActions {
  onClear: () => void
  onInterrupt: () => void
  onOpenHistory: () => void
  onOpenSettings: () => void
  onOpenSearch: () => void
  onOpenExport: () => void
}

/** Refs / context the TopBar can't compute on its own. */
export interface TopBarContext {
  mcpButtonRef?: React.RefObject<HTMLButtonElement | null>
  historyButtonRef?: React.RefObject<HTMLButtonElement | null>
  /** S031-3 — repo/branch passed to the persistent ▶ Run button. */
  repoId?: string
  branchId?: string
}

export interface TopBarProps {
  state: TopBarStatus
  actions: TopBarActions
  ctx: TopBarContext
  /** True when the MCP popup is currently open. Stays as a top-level
   *  prop because it's shared with the popup itself (parent state). */
  mcpOpen: boolean
  /** Toggles the MCP popup. Same reasoning as `mcpOpen`. */
  onToggleMcp: () => void
}

export function TopBar({ state, actions, ctx, mcpOpen, onToggleMcp }: TopBarProps) {
  const tone = rollupTone(state.mcpServers)
  const okCount = state.mcpServers.filter((s) => statusToneAgree(s.status, 'ok')).length
  const total = state.mcpServers.length
  const mcpSummary = total === 0 ? '—' : `${okCount}/${total}`
  const mcpTitle =
    total === 0
      ? 'MCP — no servers configured'
      : tone === 'err'
      ? `MCP — ${total - okCount} of ${total} not connected`
      : tone === 'warn'
      ? `MCP — ${total - okCount} of ${total} pending`
      : `MCP — ${okCount}/${total} connected`
  return (
    <div className={styles.topBar} data-testid="claude-topbar">
      <span
        className={`${styles.statusPip} ${pipClass(state.status)}`}
        aria-hidden
        data-testid="topbar-status-pip"
        data-status={state.status}
      />
      <span className={styles.statusText} data-testid="topbar-status-text">
        {labelForStatus(state.status)}
      </span>

      <span className={styles.spacer} />

      {state.contextPct != null && (
        <span className={styles.topBarItem} title="context window used">
          {state.contextPct.toFixed(0)}% ctx
        </span>
      )}

      {state.totalCostUsd > 0 && (
        <span className={styles.topBarItem} title="total session cost (USD)">
          ${state.totalCostUsd.toFixed(4)}
        </span>
      )}

      {state.canInterrupt && (
        <button
          type="button"
          className={styles.iconBtn}
          onClick={actions.onInterrupt}
          title="Interrupt (Esc)"
          data-testid="topbar-interrupt-btn"
        >
          stop
        </button>
      )}

      {/* S031-3: persistent ▶ Run button */}
      {ctx.repoId && ctx.branchId && (
        <ClaudeRunButton repoId={ctx.repoId} branchId={ctx.branchId} />
      )}

      <button
        type="button"
        className={styles.iconBtn}
        onClick={actions.onOpenSearch}
        title="Find in conversation (⌘F)"
        data-testid="topbar-search-btn"
      >
        find
      </button>

      <button
        type="button"
        className={styles.iconBtn}
        onClick={actions.onOpenExport}
        title="Export conversation"
        data-testid="topbar-export-btn"
      >
        export
      </button>

      <button
        ref={ctx.historyButtonRef}
        type="button"
        className={styles.iconBtn}
        onClick={actions.onOpenHistory}
        title="History (⌘H)"
        data-testid="topbar-history-btn"
      >
        history
      </button>

      <button
        type="button"
        className={styles.iconBtn}
        onClick={actions.onOpenSettings}
        title="Open .claude/settings.json viewer"
        data-testid="topbar-settings-btn"
      >
        settings
      </button>

      <button
        // eslint-disable-next-line react-hooks/refs -- pre-React-19 latest-closure ref pattern (no useEffectEvent yet)
        ref={ctx.mcpButtonRef}
        type="button"
        className={`${styles.iconBtn} ${styles.mcpBtn}`}
        onClick={onToggleMcp}
        title={mcpTitle}
        aria-haspopup="dialog"
        aria-expanded={mcpOpen}
        data-testid="mcp-topbar-btn"
      >
        <span
          className={`${styles.mcpPip} ${mcpPipClass(tone)}`}
          aria-hidden
          data-testid="mcp-topbar-pip"
          data-tone={tone}
        />
        <span data-testid="mcp-topbar-summary">mcp {mcpSummary}</span>
      </button>

      <button
        type="button"
        className={styles.iconBtn}
        onClick={actions.onClear}
        title="/clear — start a fresh session"
        data-testid="topbar-clear-btn"
      >
        /clear
      </button>

      {state.connState !== 'open' && (
        <span className={styles.connBanner}>{state.connState}…</span>
      )}
    </div>
  )
}

// ── helpers ─────────────────────────────────────────────────────────
// All previously private to claude-agent-view.tsx. Kept private to
// this module — TopBar is the only consumer.

function pipClass(s: AgentStatus): string {
  switch (s) {
    case 'idle':                return styles.statusPipIdle
    case 'thinking':            return styles.statusPipThinking
    case 'tool_running':        return styles.statusPipTool
    case 'awaiting_permission': return styles.statusPipPerm
    case 'error':               return styles.statusPipErr
    case 'starting':            return styles.statusPipStart
    default:                    return ''
  }
}

/** Label for the status pip. Exported because the streaming overlay
 *  (parent component) reuses the same human label. */
// eslint-disable-next-line react-refresh/only-export-components -- helper coupled to component (HMR-only concern, no runtime impact)
export function labelForStatus(s: AgentStatus): string {
  switch (s) {
    case 'idle':                return 'idle'
    case 'starting':            return 'starting…'
    case 'thinking':            return 'thinking…'
    case 'tool_running':        return 'running tool…'
    case 'awaiting_permission': return 'awaiting permission'
    case 'error':               return 'error'
  }
}

function mcpPipClass(tone: MCPStatusTone): string {
  switch (tone) {
    case 'ok':      return styles.mcpPipOk
    case 'warn':    return styles.mcpPipWarn
    case 'err':     return styles.mcpPipErr
    case 'unknown': return styles.mcpPipUnknown
  }
}

// statusToneAgree returns true iff the raw CLI status maps to the same
// tone as `target`. Thin wrapper over mcp-popup.statusTone so the TopBar
// can count "connected" servers without re-implementing classification.
function statusToneAgree(raw: string, target: MCPStatusTone): boolean {
  return statusTone(raw) === target
}

/** Compute the % of context window used given a usage object.
 *  Exported because parent (claude-agent-view) gathers usage from
 *  agent-state and feeds the result back as `state.contextPct`. */
// eslint-disable-next-line react-refresh/only-export-components -- helper coupled to component (HMR-only concern, no runtime impact)
export function contextPercent(usage?: AgentUsage): number | undefined {
  if (!usage || !usage.contextWindow) return undefined
  const consumed =
    (usage.inputTokens ?? 0) +
    (usage.cacheReadInputTokens ?? 0) +
    (usage.cacheCreationInputTokens ?? 0)
  if (consumed <= 0) return undefined
  return Math.min(100, (consumed / usage.contextWindow) * 100)
}
