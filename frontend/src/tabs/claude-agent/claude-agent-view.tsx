import { useEffect, useMemo, useRef, useState } from 'react'

import { api } from '../../lib/api'
import type { TabViewProps } from '../../lib/tab-registry'

import { BlockView } from './blocks'
import styles from './claude-agent-view.module.css'
import type { AgentStatus, Turn } from './types'
import { useAgent } from './use-agent'

const MODEL_OPTIONS = [
  { value: '', label: 'default' },
  { value: 'sonnet', label: 'sonnet' },
  { value: 'opus', label: 'opus' },
  { value: 'haiku', label: 'haiku' },
]

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

function modeLabel(mode: string): string {
  switch (mode) {
    case 'default':           return 'default'
    case 'acceptEdits':       return 'accept edits'
    case 'plan':              return 'plan'
    case 'auto':              return 'auto'
    case 'dontAsk':           return "don't ask"
    case 'bypassPermissions': return 'bypass'
    default:                  return mode
  }
}

export function ClaudeAgentView({ repoId, branchId }: TabViewProps) {
  const { state, connState, send } = useAgent(repoId, branchId)
  const conversationRef = useRef<HTMLDivElement>(null)
  const [modes, setModes] = useState<PermissionModesResp>(FALLBACK_PERMISSION_MODES)
  const [autoFollow, setAutoFollow] = useState(true)

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
      scope: 'once' | 'session',
      reason?: string,
    ) => {
      send.permissionRespond(permissionId, decision, scope, undefined, reason)
    },
    [send],
  )

  return (
    <div className={styles.wrap}>
      <TopBar
        status={state.status}
        totalCostUsd={state.totalCostUsd}
        connState={connState}
        onClear={() => send.sessionClear()}
        canInterrupt={isStreaming}
        onInterrupt={() => send.interrupt()}
      />

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
        onSend={(c) => send.userMessage(c)}
        onInterrupt={() => send.interrupt()}
        isStreaming={isStreaming}
        disabled={!state.authOk}
        connState={connState}
        model={state.model}
        permissionMode={state.permissionMode}
        permissionModes={modes.modes}
        onModelChange={(m) => send.setModel(m)}
        onPermissionModeChange={(m) => send.setPermissionMode(m)}
      />
    </div>
  )
}

type RespondPermissionFn = (
  permissionId: string,
  decision: 'allow' | 'deny',
  scope: 'once' | 'session',
  reason?: string,
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
                onAllow: (scope: 'once' | 'session') =>
                  onRespondPermission(b.permissionId!, 'allow', scope),
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
  connState: 'connecting' | 'open' | 'closed' | 'closing'
  canInterrupt: boolean
  onInterrupt: () => void
  onClear: () => void
}

function TopBar(props: TopBarProps) {
  return (
    <div className={styles.topBar}>
      <span className={`${styles.statusPip} ${pipClass(props.status)}`} aria-hidden />
      <span className={styles.statusText}>{labelForStatus(props.status)}</span>

      <span className={styles.spacer} />

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

interface ComposerProps {
  onSend: (content: string) => void
  onInterrupt: () => void
  isStreaming: boolean
  disabled: boolean
  connState: 'connecting' | 'open' | 'closed' | 'closing'
  model: string
  permissionMode: string
  permissionModes: string[]
  onModelChange: (model: string) => void
  onPermissionModeChange: (mode: string) => void
}

function Composer({
  onSend,
  onInterrupt,
  isStreaming,
  disabled,
  connState,
  model,
  permissionMode,
  permissionModes,
  onModelChange,
  onPermissionModeChange,
}: ComposerProps) {
  const [value, setValue] = useState('')
  const [composing, setComposing] = useState(false)

  const submit = () => {
    if (!value.trim() || isStreaming || disabled) return
    onSend(value)
    setValue('')
  }

  const handleKey = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (composing) return
    if (e.key === 'Escape' && isStreaming) {
      e.preventDefault()
      onInterrupt()
      return
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submit()
    }
  }

  const placeholder = disabled
    ? 'Authenticate Claude Code first'
    : 'Message Claude…  (Enter to send, Shift+Enter for newline)'

  return (
    <div className={styles.composer}>
      <div className={styles.composerInner}>
        <textarea
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onCompositionStart={() => setComposing(true)}
          onCompositionEnd={() => setComposing(false)}
          onKeyDown={handleKey}
          placeholder={placeholder}
          rows={1}
          disabled={disabled}
        />
        <div className={styles.composerFooter}>
          <span className={styles.modePill}>
            <select
              aria-label="Model"
              value={model}
              onChange={(e) => onModelChange(e.target.value)}
            >
              {MODEL_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </span>
          <span className={styles.modePill}>
            <select
              aria-label="Permission mode"
              value={permissionMode}
              onChange={(e) => onPermissionModeChange(e.target.value)}
            >
              {permissionModes.map((m) => (
                <option key={m} value={m}>{modeLabel(m)}</option>
              ))}
            </select>
          </span>

          <span className={styles.composerFooterSpacer} />

          {connState !== 'open' && (
            <span className={styles.connBanner}>{connState}…</span>
          )}

          {isStreaming ? (
            <button
              type="button"
              className={`${styles.sendBtn} ${styles.interrupt}`}
              onClick={onInterrupt}
              title="Esc to interrupt"
              aria-label="Interrupt"
            >
              ■
            </button>
          ) : (
            <button
              type="button"
              className={styles.sendBtn}
              onClick={submit}
              disabled={!value.trim() || disabled}
              title="Send (Enter)"
              aria-label="Send"
            >
              ↑
            </button>
          )}
        </div>
      </div>
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
