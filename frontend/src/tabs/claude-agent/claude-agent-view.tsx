import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { confirmDialog } from '../../components/context-menu/confirm-dialog'
import { api } from '../../lib/api'
import type { TabViewProps } from '../../lib/tab-registry'

import styles from './claude-agent-view.module.css'
import { ClaudeRunButton } from './claude-run-button'
import { Composer } from './composer'
import { ConversationExportDialog } from './conversation-export'
import {
  ConversationList,
  type ConversationListHandle,
} from './conversation-list'
import { useScrollAutoFollow } from './hooks/use-scroll-auto-follow'
import { usePermissionHandlers } from './hooks/use-permission-handlers'
import { useTurnTree } from './hooks/use-turn-tree'
import {
  ConversationSearchBar,
  useConversationSearch,
} from './conversation-search'
import { ClaudeSearchProvider } from './search-context'
import { HistoryPopup } from './history-popup'
import { MCPPopup } from './mcp-popup'
import { rollupTone, statusTone, type MCPStatusTone } from './mcp-status'
import { SettingsPopup } from './settings-popup'
import { TurnView } from './turn-view'
import type { AgentStatus, MCPServerInfo } from './types'
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

export function ClaudeAgentView({ repoId, branchId, tabId }: TabViewProps) {
  // S009: pass tabId through so multiple Claude tabs on the same branch
  // each get their own WS / state cache. Empty / legacy `claude` folds
  // to the canonical id inside useAgent.
  const { state, connState, send } = useAgent(repoId, branchId, tabId)
  const listHandleRef = useRef<ConversationListHandle | null>(null)
  const historyButtonRef = useRef<HTMLButtonElement | null>(null)
  const mcpButtonRef = useRef<HTMLButtonElement | null>(null)
  const [modes, setModes] = useState<PermissionModesResp>(FALLBACK_PERMISSION_MODES)

  const [historyOpen, setHistoryOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [mcpOpen, setMcpOpen] = useState(false)
  const [exportOpen, setExportOpen] = useState(false)
  // S019 / hotfix: lift the user-turn-edit "editing" flag above the
  // virtualised conversation list so it survives the row unmounting
  // when it scrolls out of view. Single string is enough — the user
  // edits one turn at a time.
  const [editingTurnId, setEditingTurnId] = useState<string | null>(null)
  const onEditingChange = useCallback((turnId: string, editing: boolean) => {
    setEditingTurnId((prev) => {
      if (editing) return turnId
      return prev === turnId ? null : prev
    })
  }, [])

  // Top-level turns + parent→children map. Sub-agent (Task) turns
  // aren't virtualised separately; they nest inline via TaskTreeBlock.
  //
  // S019: when the user is viewing an archived version of a user turn,
  // we splice the archived turn's subsequentTurnIds into the list AT
  // the position of that user turn (replacing the live tail) so the
  // version arrow effectively scrolls back to the abandoned thread.
  // S43cfb1-2: extracted into useTurnTree.
  const { topLevelTurns, childrenByParent } = useTurnTree({
    turns: state.turns,
    archivedTurnsById: state.archivedTurnsById,
    activeVersionByTurnId: state.activeVersionByTurnId,
  })

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

  // S018: in-conversation search (Cmd+F / Ctrl+F). Scrolls to the
  // matching row through the imperative List API so virtualisation
  // (S017) plays nicely — the row is realised before being centred.
  const search = useConversationSearch(topLevelTurns, (idx) => {
    listHandleRef.current?.scrollToRow(idx, { align: 'center', behavior: 'smooth' })
  })
  // The search captures Cmd+F **before** the browser; we only do this
  // when the Claude tab's wrapper currently contains the focused
  // element. Outside, the user's normal browser Find still works.
  const wrapRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey)) return
      if (e.key !== 'f' && e.key !== 'F') return
      // Inside the Claude tab? If wrapRef contains the active element,
      // we own this shortcut.
      const wrap = wrapRef.current
      if (!wrap) return
      const active = document.activeElement
      const inside = wrap.contains(active) || active === document.body
      if (!inside) return
      e.preventDefault()
      search.open()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [search])

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

  // S43cfb1-2: scroll auto-follow + scroll-restore wiring extracted
  // into useScrollAutoFollow. The hook owns autoFollow state, the
  // user-input timestamp ref, the scroll-to-bottom effect, the
  // containerRef polling effect, and the visibility gate.
  const {
    autoFollow,
    onListScroll,
    onUserInput,
    restoreVisible,
    scrollToLatest,
  } = useScrollAutoFollow({
    repoId,
    branchId,
    tabId,
    sessionId: state.sessionId,
    contentSeq: state.contentSeq,
    hasTurns: topLevelTurns.length > 0,
    listHandleRef,
  })

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

  // S43cfb1-2: plan + ask permission handler factories extracted into
  // usePermissionHandlers. The hook owns planDecisions optimistic
  // state, planAuthority lookup, and the per-block handler builders.
  const { planHandlersFor, askHandlersFor } = usePermissionHandlers({
    turns: state.turns,
    pendingPlanByBlock: state.pendingPlanByBlock,
    pendingAskByBlock: state.pendingAskByBlock,
    modes,
    send,
  })

  const activeBlockId =
    search.state.matches[search.state.active]?.blockId

  // Memoise the per-row renderer so ConversationList's `rowProps` doesn't
  // invalidate every parent render. An inline closure here ages out the
  // memoised `rowProps` in conversation-list.tsx whenever any unrelated
  // state (typing, hover, scroll throttle) re-runs the parent — which
  // forces react-window to re-render every row and can interact badly
  // with `useDynamicRowHeight`'s ResizeObserver pass.
  const renderTurn = useCallback(
    (turn: import('./types').Turn) => (
      <div className={styles.virtualTurnRow}>
        <TurnView
          turn={turn}
          activeVersionIndex={state.activeVersionByTurnId[turn.id] ?? -1}
          onSetVersion={(idx) => send.rewindSetVersion(turn.id, idx)}
          onRewind={send.rewind}
          onRewindApplyLocal={send.rewindApplyLocal}
          editingTurnId={editingTurnId}
          onEditingChange={onEditingChange}
          onRespondPermission={respondPermission}
          planHandlersFor={planHandlersFor}
          askHandlersFor={askHandlersFor}
          childrenByParent={childrenByParent}
        />
      </div>
    ),
    [
      state.activeVersionByTurnId,
      send,
      editingTurnId,
      onEditingChange,
      respondPermission,
      planHandlersFor,
      askHandlersFor,
      childrenByParent,
    ],
  )

  return (
    <div className={styles.wrap} ref={wrapRef}>
      <TopBar
        status={state.status}
        totalCostUsd={state.totalCostUsd}
        contextPct={contextPercent(state.lastUsage)}
        mcpServers={state.mcpServers}
        mcpOpen={mcpOpen}
        onToggleMcp={() => setMcpOpen((v) => !v)}
        mcpButtonRef={mcpButtonRef}
        connState={connState}
        onClear={async () => {
          // Match Claude Code CLI behaviour: /clear wipes the conversation
          // context, which is destructive — require explicit confirmation.
          const ok = await confirmDialog.ask({
            title: 'Clear conversation context?',
            message: 'This starts a fresh session. The current conversation will not be visible in this tab anymore (the on-disk transcript stays under ~/.claude/projects/ and remains accessible from the History popup).',
            confirmLabel: 'Clear',
            cancelLabel: 'Cancel',
            danger: true,
          })
          if (ok) send.sessionClear()
        }}
        canInterrupt={isStreaming}
        onInterrupt={() => send.interrupt()}
        onOpenHistory={() => setHistoryOpen((v) => !v)}
        onOpenSettings={() => setSettingsOpen(true)}
        onOpenSearch={search.open}
        onOpenExport={() => setExportOpen(true)}
        historyButtonRef={historyButtonRef}
        repoId={repoId}
        branchId={branchId}
      />
      <ConversationSearchBar
        state={search.state}
        setQuery={search.setQuery}
        onNext={search.next}
        onPrev={search.prev}
        onClose={search.close}
        inputRef={search.inputRef}
      />
      <ConversationExportDialog
        open={exportOpen}
        onClose={() => setExportOpen(false)}
        turns={state.turns}
        branchId={branchId}
        repoId={repoId}
        sessionId={state.sessionId}
        model={state.model}
      />
      <SettingsPopup
        repoId={repoId}
        branchId={branchId}
        open={settingsOpen}
        onClose={() => setSettingsOpen(false)}
      />
      <div style={{ position: 'relative' }}>
        <MCPPopup
          servers={state.mcpServers}
          open={mcpOpen}
          onClose={() => setMcpOpen(false)}
          anchorRef={mcpButtonRef}
        />
      </div>
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

      <div className={styles.conversation} data-testid="claude-conversation">
        {state.errors.slice(-3).length > 0 && (
          <div className={styles.errorBannerStack}>
            {state.errors.slice(-3).map((e) => (
              <div key={e.id} className={styles.errorBanner}>
                {e.message}
                {e.detail && <small>{e.detail}</small>}
              </div>
            ))}
          </div>
        )}
        {state.compacting && (
          <div className={styles.compactSpinner} data-testid="compacting-spinner">
            <span className={styles.dots}><span /><span /><span /></span>
            <span>Compacting conversation…</span>
          </div>
        )}

        {state.turns.length === 0 ? (
          <div className={styles.empty}>
            <p>Start a conversation. Try “Summarise this repo” or “Open package.json”.</p>
            <p style={{ marginTop: 12, fontSize: 11, color: 'var(--color-fg-dim)' }}>
              Slash commands: <code>/clear</code> for a fresh session, <code>/model &lt;name&gt;</code> to switch.
            </p>
          </div>
        ) : (
          // visibility:hidden (not display:none) keeps the list laid
          // out so react-window can measure rows while the user
          // doesn't see the pre-restore scrollTop=0 state. We flip
          // to visible from the onSettled callback the moment the
          // first scroll adjustment lands (or, for atBottom / no-record
          // paths, immediately).
          <div
            style={{
              flex: 1,
              minHeight: 0,
              minWidth: 0,
              display: 'flex',
              flexDirection: 'column',
              visibility: restoreVisible ? 'visible' : 'hidden',
            }}
          >
            <ClaudeSearchProvider
              query={search.state.query}
              openedBlocks={search.state.openedBlocks}
              activeBlockId={activeBlockId}
            >
              <ConversationList
                ref={listHandleRef}
                turns={topLevelTurns}
                onScroll={onListScroll}
                onUserInput={onUserInput}
                renderTurn={renderTurn}
              />
            </ClaudeSearchProvider>
          </div>
        )}
        {!autoFollow && state.turns.length > 0 && (
          <button
            type="button"
            className={styles.scrollToBottomBtn}
            data-testid="scroll-to-bottom"
            aria-label="Scroll to latest"
            title="Scroll to latest"
            onClick={() => scrollToLatest('smooth')}
          >
            <svg
              width="20"
              height="20"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2.4"
              strokeLinecap="round"
              strokeLinejoin="round"
              aria-hidden
            >
              <path d="M12 5v14" />
              <path d="m6 13 6 6 6-6" />
            </svg>
          </button>
        )}
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
        tabId={tabId}
        onSend={(c, addDirs) => send.userMessage(c, addDirs)}
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

// TurnView and RespondPermissionFn moved to ./turn-view.tsx in S43cfb1-2.

// applyVersionView / splitTurnTree / findActivePlan / findPlanBlockById
// were moved to ./hooks/use-turn-tree.ts and
// ./hooks/use-permission-handlers.ts in S43cfb1-2.

interface TopBarProps {
  status: AgentStatus
  totalCostUsd: number
  contextPct?: number
  mcpServers: MCPServerInfo[]
  /** True when the MCP popup is currently open. Used to render the
   *  trigger button in an active state and announce expansion to AT. */
  mcpOpen: boolean
  /** Toggles the MCP popup. Owned by the parent so the popup itself
   *  doesn't have to track its own visibility. */
  onToggleMcp: () => void
  mcpButtonRef?: React.RefObject<HTMLButtonElement | null>
  connState: 'connecting' | 'open' | 'closed' | 'closing'
  canInterrupt: boolean
  onInterrupt: () => void
  onClear: () => void
  onOpenHistory: () => void
  onOpenSettings: () => void
  /** S018 — opens the in-conversation Cmd+F search bar. Owned by the
   *  parent so the search hook lives there. */
  onOpenSearch: () => void
  /** S018 — opens the export dialog. */
  onOpenExport: () => void
  historyButtonRef?: React.RefObject<HTMLButtonElement | null>
  /** S031-3 — repo/branch context for the Run button. */
  repoId?: string
  branchId?: string
}

function TopBar(props: TopBarProps) {
  const tone = rollupTone(props.mcpServers)
  const okCount = props.mcpServers.filter((s) => statusToneAgree(s.status, 'ok')).length
  const total = props.mcpServers.length
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

      {/* S031-3: persistent ▶ Run button */}
      {props.repoId && props.branchId && (
        <ClaudeRunButton repoId={props.repoId} branchId={props.branchId} />
      )}

      <button
        type="button"
        className={styles.iconBtn}
        onClick={props.onOpenSearch}
        title="Find in conversation (⌘F)"
        data-testid="topbar-search-btn"
      >
        find
      </button>

      <button
        type="button"
        className={styles.iconBtn}
        onClick={props.onOpenExport}
        title="Export conversation"
        data-testid="topbar-export-btn"
      >
        export
      </button>

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
        onClick={props.onOpenSettings}
        title="Open .claude/settings.json viewer"
      >
        settings
      </button>

      <button
        ref={props.mcpButtonRef}
        type="button"
        className={`${styles.iconBtn} ${styles.mcpBtn}`}
        onClick={props.onToggleMcp}
        title={mcpTitle}
        aria-haspopup="dialog"
        aria-expanded={props.mcpOpen}
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

function contextPercent(usage?: import('./agent-state').AgentUsage): number | undefined {
  if (!usage || !usage.contextWindow) return undefined
  const consumed =
    (usage.inputTokens ?? 0) +
    (usage.cacheReadInputTokens ?? 0) +
    (usage.cacheCreationInputTokens ?? 0)
  if (consumed <= 0) return undefined
  return Math.min(100, (consumed / usage.contextWindow) * 100)
}
