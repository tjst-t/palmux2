import { useEffect, useMemo, useRef, useState } from 'react'

import { api } from '../../lib/api'
import type { TabViewProps } from '../../lib/tab-registry'

import { BlockView } from './blocks'
import styles from './claude-agent-view.module.css'
import { Composer } from './composer'
import { HistoryPopup } from './history-popup'
import type { AgentStatus, Turn } from './types'
import { useAgent } from './use-agent'

// Fallback list — only used until /api/claude/modes responds. The labels
// mirror the order we ask the server for: safest → most permissive.
const FALLBACK_PERMISSION_MODES: PermissionModesResp = {
  modes: ['default', 'plan', 'acceptEdits', 'auto', 'bypassPermissions'],
  default: 'acceptEdits',
  source: 'fallback',
}

interface PermissionModesResp {
  modes: string[]
  default: string
  source: 'cli' | 'fallback'
}

export function ClaudeAgentView({ repoId, branchId }: TabViewProps) {
  const { state, connState, send } = useAgent(repoId, branchId)
  const conversationRef = useRef<HTMLDivElement>(null)
  const historyButtonRef = useRef<HTMLButtonElement | null>(null)
  const [modes, setModes] = useState<PermissionModesResp>(FALLBACK_PERMISSION_MODES)
  const [autoFollow, setAutoFollow] = useState(true)
  const [historyOpen, setHistoryOpen] = useState(false)

  // ⌘H / Ctrl+H opens the session history popup.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === 'h' || e.key === 'H')) {
        // Only intercept when not typing in a text field.
        const target = e.target as HTMLElement | null
        if (target?.tagName === 'TEXTAREA' || target?.tagName === 'INPUT') return
        e.preventDefault()
        setHistoryOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // Fetch CLI-supported permission modes once on mount.
  useEffect(() => {
    let cancelled = false
    api
      .get<PermissionModesResp>('/api/claude/modes')
      .then((data) => {
        if (!cancelled && data?.modes?.length) setModes(data)
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  // Auto-scroll to bottom unless the user scrolled up.
  useEffect(() => {
    if (!autoFollow) return
    const el = conversationRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [state.turns, state.status, autoFollow])

  useEffect(() => {
    const el = conversationRef.current
    if (!el) return
    const onScroll = () => {
      const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
      setAutoFollow(atBottom)
    }
    el.addEventListener('scroll', onScroll)
    return () => el.removeEventListener('scroll', onScroll)
  }, [])

  // y / n shortcut for pending permission, only when composer doesn't have focus.
  useEffect(() => {
    if (!state.pendingPermission) return
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null
      if (target && (target.tagName === 'TEXTAREA' || target.tagName === 'INPUT')) return
      if (e.key === 'y' || e.key === 'Y') {
        e.preventDefault()
        send.permissionRespond(state.pendingPermission!.permissionId, 'allow', 'once')
      } else if (e.key === 'n' || e.key === 'N' || e.key === 'Escape') {
        e.preventDefault()
        send.permissionRespond(state.pendingPermission!.permissionId, 'deny', 'once')
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [state.pendingPermission, send])

  const isStreaming =
    state.status === 'thinking' ||
    state.status === 'tool_running' ||
    state.status === 'starting'

  const respondPermission = useMemo(
    () => (
      permissionId: string,
      decision: 'allow' | 'deny',
      scope: 'once' | 'session' | 'always',
      reason?: string,
      updatedInput?: unknown,
    ) => {
      send.permissionRespond(permissionId, decision, scope, updatedInput, reason)
    },
    [send],
  )

  return (
    <div className={styles.wrap}>
      <TopBar
        status={state.status}
        totalCostUsd={state.totalCostUsd}
        contextPct={contextPercent(state.lastUsage)}
        mcpServers={[]}
        connState={connState}
        onClear={() => send.sessionClear()}
        canInterrupt={isStreaming}
        onInterrupt={() => send.interrupt()}
        onOpenHistory={() => setHistoryOpen((v) => !v)}
        historyButtonRef={historyButtonRef}
      />
      {historyOpen && (
        <div style={{ position: 'relative' }}>
          <HistoryPopup
            repoId={repoId}
            branchId={branchId}
            currentSessionId={state.sessionId}
            open={historyOpen}
            onClose={() => setHistoryOpen(false)}
            onResume={(id) => send.sessionResume(id)}
            onFork={(id) => send.sessionFork(id)}
            anchorRef={historyButtonRef}
          />
        </div>
      )}

      {!state.authOk && state.authMessage && (
        <pre className={styles.authError}>{state.authMessage}</pre>
      )}

      <div className={styles.conversation} ref={conversationRef}>
        <div className={styles.conversationInner}>
          {state.errors.slice(-3).map((e) => (
            <div key={e.id} className={styles.errorBanner}>
              {e.message}
              {e.detail && <small>{e.detail}</small>}
            </div>
          ))}

          {state.turns.length === 0 ? (
            <div className={styles.empty}>
              <p>Start a conversation. Try “Summarise this repo” or “Open package.json”.</p>
              <p style={{ marginTop: 12, fontSize: 11, color: 'var(--color-fg-dim)' }}>
                Slash commands: <code>/clear</code> for a fresh session, <code>/model &lt;name&gt;</code> to switch.
              </p>
            </div>
          ) : (
            state.turns.map((turn) => (
              <TurnView
                key={turn.id}
                turn={turn}
                onRespondPermission={respondPermission}
              />
            ))
          )}
        </div>
      </div>

      {isStreaming && (
        <div className={styles.streaming}>
          <div className={styles.inner}>
            <span className={styles.dots}>
              <span /><span /><span />
            </span>
            <span>{labelForStatus(state.status)}</span>
          </div>
        </div>
      )}

      <Composer
        repoId={repoId}
        branchId={branchId}
        onSend={(c) => send.userMessage(c)}
        onInterrupt={() => send.interrupt()}
        isStreaming={isStreaming}
        disabled={!state.authOk}
        connState={connState}
        model={state.model}
        effort={state.effort}
        permissionMode={state.permissionMode}
        permissionModes={modes.modes}
        onModelChange={(m) => send.setModel(m)}
        onEffortChange={(e) => send.setEffort(e)}
        onPermissionModeChange={(m) => send.setPermissionMode(m)}
        initInfo={state.initInfo}
      />
    </div>
  )
}

type RespondPermissionFn = (
  permissionId: string,
  decision: 'allow' | 'deny',
  scope: 'once' | 'session' | 'always',
  reason?: string,
  updatedInput?: unknown,
) => void

function TurnView({
  turn,
  onRespondPermission,
}: {
  turn: Turn
  onRespondPermission: RespondPermissionFn
}) {
  if (turn.role === 'user') {
    return (
      <div className={styles.turnUser}>
        <div className={styles.userBubble}>
          {turn.blocks.map((b) => (
            <BlockView key={b.id} block={b} />
          ))}
        </div>
      </div>
    )
  }
  // tool turns and assistant turns share the same prose-flow layout.
  const cls = turn.role === 'tool' ? styles.turnTool : styles.turnAssistant
  return (
    <div className={cls}>
      {turn.blocks.map((b) => {
        const handlers =
          b.kind === 'permission' && !b.decision && b.permissionId
            ? {
                onAllow: (scope: 'once' | 'session' | 'always', updatedInput?: unknown) =>
                  onRespondPermission(b.permissionId!, 'allow', scope, undefined, updatedInput),
                onDeny: (reason?: string) =>
                  onRespondPermission(b.permissionId!, 'deny', 'once', reason),
              }
            : undefined
        return <BlockView key={b.id} block={b} permissionHandlers={handlers} />
      })}
    </div>
  )
}

interface TopBarProps {
  status: AgentStatus
  totalCostUsd: number
  contextPct?: number
  mcpServers: { name: string; status: string }[]
  connState: 'connecting' | 'open' | 'closed' | 'closing'
  canInterrupt: boolean
  onInterrupt: () => void
  onClear: () => void
  onOpenHistory: () => void
  historyButtonRef?: React.RefObject<HTMLButtonElement | null>
}

function TopBar(props: TopBarProps) {
  return (
    <div className={styles.topBar}>
      <span className={`${styles.statusPip} ${pipClass(props.status)}`} aria-hidden />
      <span className={styles.statusText}>{labelForStatus(props.status)}</span>

      <span className={styles.spacer} />

      {props.contextPct != null && (
        <span className={styles.topBarItem} title="context window used">
          {props.contextPct.toFixed(0)}% ctx
        </span>
      )}

      {props.totalCostUsd > 0 && (
        <span className={styles.topBarItem} title="total session cost (USD)">
          ${props.totalCostUsd.toFixed(4)}
        </span>
      )}

      {props.canInterrupt && (
        <button
          type="button"
          className={styles.iconBtn}
          onClick={props.onInterrupt}
          title="Interrupt (Esc)"
        >
          stop
        </button>
      )}

      <button
        ref={props.historyButtonRef}
        type="button"
        className={styles.iconBtn}
        onClick={props.onOpenHistory}
        title="History (⌘H)"
      >
        history
      </button>

      <button
        type="button"
        className={styles.iconBtn}
        onClick={props.onClear}
        title="/clear — start a fresh session"
      >
        /clear
      </button>

      {props.connState !== 'open' && (
        <span className={styles.connBanner}>{props.connState}…</span>
      )}
    </div>
  )
}

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

function labelForStatus(s: AgentStatus): string {
  switch (s) {
    case 'idle':                return 'idle'
    case 'starting':            return 'starting…'
    case 'thinking':            return 'thinking…'
    case 'tool_running':        return 'running tool…'
    case 'awaiting_permission': return 'awaiting permission'
    case 'error':               return 'error'
  }
}

function contextPercent(usage?: import('./agent-state').AgentUsage): number | undefined {
  if (!usage || !usage.contextWindow) return undefined
  const consumed =
    (usage.inputTokens ?? 0) +
    (usage.cacheReadInputTokens ?? 0) +
    (usage.cacheCreationInputTokens ?? 0)
  if (consumed <= 0) return undefined
  return Math.min(100, (consumed / usage.contextWindow) * 100)
}
