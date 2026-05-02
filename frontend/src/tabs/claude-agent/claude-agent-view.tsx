import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { confirmDialog } from '../../components/context-menu/confirm-dialog'
import { api } from '../../lib/api'
import type { TabViewProps } from '../../lib/tab-registry'

import { BlockView } from './blocks'
import styles from './claude-agent-view.module.css'
import { Composer } from './composer'
import { ConversationExportDialog } from './conversation-export'
import {
  ConversationList,
  type ConversationListHandle,
  scrollStorageKey,
  usePersistScroll,
  useScrollRestore,
} from './conversation-list'
import {
  ConversationSearchBar,
  useConversationSearch,
} from './conversation-search'
import { ClaudeSearchProvider } from './search-context'
import { HistoryPopup } from './history-popup'
import { MCPPopup } from './mcp-popup'
import { rollupTone, statusTone, type MCPStatusTone } from './mcp-status'
import { SettingsPopup } from './settings-popup'
import type { AgentStatus, MCPServerInfo, Turn } from './types'
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
  const [autoFollow, setAutoFollow] = useState(true)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [mcpOpen, setMcpOpen] = useState(false)
  const [exportOpen, setExportOpen] = useState(false)

  // S017: virtualisation. Resolve the inner scroll container from the
  // List's imperative API so scroll-restore / persist hooks can hang
  // listeners off it. The element only exists after the first render
  // of List, so we re-resolve on every render — cheap.
  const containerRef = useRef<HTMLDivElement | null>(null)
  // Top-level turns + parent→children map. Sub-agent (Task) turns
  // aren't virtualised separately; they nest inline via TaskTreeBlock.
  const { topLevelTurns, childrenByParent } = useMemo(
    () => splitTurnTree(state.turns),
    [state.turns],
  )
  // Stable storage key for scroll restoration. tabId can be empty in
  // legacy URLs — fold to a constant so the key shape is stable.
  const storageKey = scrollStorageKey(repoId, branchId, tabId || 'claude')
  // planDecisions tracks the optimistic UI flip on click. The server
  // echoes plan.decided afterwards which makes the decision durable
  // (block.planDecision) — the optimistic state is only there to hide
  // the action row immediately on click while the WS round-trip
  // happens. Reset on unmount; cleared as soon as durable state lands.
  const [planDecisions, setPlanDecisions] = useState<
    Record<string, { decided: 'approved' | 'rejected'; targetMode?: string }>
  >({})

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

  // S017: auto-scroll routes through the ConversationList imperative
  // API. We can't just bump scrollTop on the wrapper because the
  // wrapper isn't the scroll container any more — react-window owns
  // the scroller and only it knows the precomputed total height.
  useEffect(() => {
    if (!autoFollow) return
    const handle = listHandleRef.current
    if (handle) handle.scrollToBottom('instant')
  }, [state.turns, state.status, autoFollow])

  // Bridge scroll events from List → autoFollow flag.
  const onListScroll = useCallback(
    (scrollTop: number, scrollHeight: number, clientHeight: number) => {
      const atBottom = scrollHeight - scrollTop - clientHeight < 32
      setAutoFollow(atBottom)
      // Also keep containerRef in sync so the persist/restore hooks
      // resolve the live element each render.
      const el = listHandleRef.current?.element() ?? null
      containerRef.current = el
    },
    [],
  )

  // Resolve containerRef on mount (List's element() lazy returns null
  // until the first paint).
  useEffect(() => {
    const el = listHandleRef.current?.element() ?? null
    containerRef.current = el
  }, [state.sessionId])

  // S017: scroll position persistence. Reload-resilient via
  // localStorage keyed by sessionId so a session swap doesn't
  // accidentally restore the wrong offset.
  useScrollRestore({
    sessionId: state.sessionId,
    storageKey,
    containerRef,
    hasTurns: topLevelTurns.length > 0,
  })
  usePersistScroll({
    sessionId: state.sessionId,
    storageKey,
    containerRef,
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

  // Resolve the active plan block — the most recent kind:"plan" block
  // that has a permission_id stamped (from plan.question) and is not
  // already decided. Earlier plans, plans without a permission_id (CLI
  // hasn't routed the permission_prompt through to us yet), and plans
  // already decided are all read-only. We no longer condition on
  // state.permissionMode == "plan" because the moment the user clicks
  // Approve we issue a SetPermissionMode and that flips the mode
  // before the plan.decided event lands — see S001-refine docs.
  const planAuthority = useMemo(
    () => findActivePlan(state.turns, state.pendingPlanByBlock),
    [state.turns, state.pendingPlanByBlock],
  )

  // resolveDefaultMode picks the dropdown's initial value: "auto" if
  // the CLI advertises it, otherwise the CLI default (excluding
  // "plan"), otherwise "auto" as a last resort.
  const resolveDefaultMode = (): string => {
    const supports = (m: string) => modes.modes.includes(m)
    if (supports('auto')) return 'auto'
    if (modes.default && modes.default !== 'plan') return modes.default
    return 'auto'
  }

  const planHandlersFor = (blockId: string | undefined): PlanHandlersForView | undefined => {
    if (!blockId) return undefined
    const optimistic = planDecisions[blockId]
    const isActive = blockId === planAuthority?.blockId
    const permissionId = planAuthority?.permissionId
    const decisionFromBlock = findPlanBlockById(state.turns, blockId)?.planDecision
    const targetModeFromBlock = findPlanBlockById(state.turns, blockId)?.planTargetMode
    const decided = optimistic?.decided ?? decisionFromBlock
    const targetMode = optimistic?.targetMode ?? targetModeFromBlock
    return {
      decided,
      targetMode,
      canActOnPlan: isActive && !decided && !!permissionId,
      modes: modes.modes.filter((m) => m !== 'plan'),
      defaultMode: resolveDefaultMode(),
      onApprove: (mode: string, editedPlan?: string) => {
        if (!permissionId) return
        setPlanDecisions((prev) => ({
          ...prev,
          [blockId]: { decided: 'approved', targetMode: mode },
        }))
        send.planRespond(permissionId, 'approve', {
          targetMode: mode,
          editedPlan: editedPlan ?? '',
        })
      },
      onReject: () => {
        if (!permissionId) return
        setPlanDecisions((prev) => ({
          ...prev,
          [blockId]: { decided: 'rejected' },
        }))
        send.planRespond(permissionId, 'reject')
      },
    }
  }

  // askHandlersFor returns the AskUserQuestion action wiring for one
  // kind:"ask" block. Looks up the active permission_id by block id and
  // — if found — exposes onRespond which ships the chosen labels to the
  // server. Blocks without a registered permission (already answered or
  // not yet permission_prompt'd) get canRespond:false so the action row
  // is disabled.
  const askHandlersFor = (blockId: string | undefined): AskHandlersForView | undefined => {
    if (!blockId) return undefined
    const entry = Object.entries(state.pendingAskByBlock).find(([, bid]) => bid === blockId)
    if (!entry) return { canRespond: false, onRespond: () => {} }
    const [permissionId] = entry
    return {
      canRespond: true,
      onRespond: (answers) => send.askRespond(permissionId, answers),
    }
  }

  const activeBlockId =
    search.state.matches[search.state.active]?.blockId

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
          <ClaudeSearchProvider
            query={search.state.query}
            openedBlocks={search.state.openedBlocks}
            activeBlockId={activeBlockId}
          >
            <ConversationList
              ref={listHandleRef}
              turns={topLevelTurns}
              sessionKey={state.sessionId}
              onScroll={onListScroll}
              renderTurn={(turn) => (
                <div className={styles.virtualTurnRow}>
                  <TurnView
                    turn={turn}
                    onRespondPermission={respondPermission}
                    planHandlersFor={planHandlersFor}
                    askHandlersFor={askHandlersFor}
                    childrenByParent={childrenByParent}
                  />
                </div>
              )}
            />
          </ClaudeSearchProvider>
        )}
        {!autoFollow && state.turns.length > 0 && (
          <button
            type="button"
            className={styles.scrollToBottomBtn}
            data-testid="scroll-to-bottom"
            aria-label="Scroll to latest"
            title="Scroll to latest"
            onClick={() => {
              listHandleRef.current?.scrollToBottom('smooth')
              setAutoFollow(true)
            }}
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

type RespondPermissionFn = (
  permissionId: string,
  decision: 'allow' | 'deny',
  scope: 'once' | 'session' | 'always',
  reason?: string,
  updatedInput?: unknown,
) => void

interface PlanHandlersForView {
  decided?: 'approved' | 'rejected'
  targetMode?: string
  canActOnPlan: boolean
  onApprove: (mode: string, editedPlan?: string) => void
  onReject: () => void
  modes: string[]
  defaultMode: string
}

interface AskHandlersForView {
  canRespond: boolean
  onRespond: (answers: string[][]) => void
}

function TurnView({
  turn,
  onRespondPermission,
  planHandlersFor,
  askHandlersFor,
  childrenByParent,
}: {
  turn: Turn
  onRespondPermission: RespondPermissionFn
  planHandlersFor: (blockId: string | undefined) => PlanHandlersForView | undefined
  askHandlersFor: (blockId: string | undefined) => AskHandlersForView | undefined
  /** Map of toolUseId → child turns produced by sub-agents the CLI
   *  spawned via that Task tool block. When a block in this turn has
   *  a non-empty entry in this map, it is rendered as a TaskTree with
   *  the children inlined underneath. */
  childrenByParent?: Map<string, Turn[]>
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
  // tool / hook / assistant turns share the same prose-flow layout — hook
  // turns are visually similar enough to tool result groups that we
  // reuse the same chrome rather than introducing a third style. The
  // HookBlock itself is what gives the row its distinct identity.
  const cls =
    turn.role === 'tool' || turn.role === 'hook'
      ? styles.turnTool
      : styles.turnAssistant
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
        const planHandlers = b.kind === 'plan' ? planHandlersFor(b.id) : undefined
        const askHandlers = b.kind === 'ask' ? askHandlersFor(b.id) : undefined
        // Sub-agent child turns: only relevant for tool_use blocks
        // (today only `Task` spawns sub-agents, but the linkage is
        // generic so any future tool that emits sub-agents nests too).
        const childTurns =
          b.kind === 'tool_use' && b.toolUseId
            ? childrenByParent?.get(b.toolUseId)
            : undefined
        const renderTaskChildren =
          childTurns && childTurns.length > 0
            ? () =>
                childTurns.map((child) => (
                  <TurnView
                    key={child.id}
                    turn={child}
                    onRespondPermission={onRespondPermission}
                    planHandlersFor={planHandlersFor}
                    askHandlersFor={askHandlersFor}
                    childrenByParent={childrenByParent}
                  />
                ))
            : undefined
        return (
          <BlockView
            key={b.id}
            block={b}
            permissionHandlers={handlers}
            planHandlers={planHandlers}
            askHandlers={askHandlers}
            renderTaskChildren={renderTaskChildren}
          />
        )
      })}
    </div>
  )
}

// splitTurnTree partitions the flat turns list into top-level turns
// (the units of virtualisation) and a parent→children map for
// sub-agent (Task) turns. Children render inline inside TaskTreeBlock
// rather than as their own virtual rows because the parent Task
// block already owns the collapsing chrome and child transcripts are
// typically short — splitting them across rows would couple row
// heights in a way that defeats clean ResizeObserver measurement.
function splitTurnTree(turns: Turn[]): {
  topLevelTurns: Turn[]
  childrenByParent: Map<string, Turn[]>
} {
  const childrenByParent = new Map<string, Turn[]>()
  const topLevelTurns: Turn[] = []
  for (const t of turns) {
    if (t.parentToolUseId) {
      const arr = childrenByParent.get(t.parentToolUseId) ?? []
      arr.push(t)
      childrenByParent.set(t.parentToolUseId, arr)
    } else {
      topLevelTurns.push(t)
    }
  }
  return { topLevelTurns, childrenByParent }
}

// findActivePlan walks the turns newest-first and returns the most
// recent kind:"plan" block that:
//   - has a permission_id stamped (= the backend has issued a
//     plan.question and is awaiting our reply), and
//   - has not yet been decided.
// That block — and only that block — is the one whose action row we
// enable. The map argument is `pendingPlanByBlock` from agent-state.
function findActivePlan(
  turns: Turn[],
  pendingPlanByBlock: Record<string, string>,
): { blockId: string; permissionId: string } | undefined {
  // Build a reverse index for O(1) blockId → permissionId lookup.
  const byBlock = new Map<string, string>()
  for (const [permId, blockId] of Object.entries(pendingPlanByBlock)) {
    byBlock.set(blockId, permId)
  }
  for (let i = turns.length - 1; i >= 0; i--) {
    const t = turns[i]
    for (let j = t.blocks.length - 1; j >= 0; j--) {
      const b = t.blocks[j]
      if (b.kind !== 'plan') continue
      if (b.planDecision) continue
      const permId = byBlock.get(b.id) ?? b.permissionId
      if (!permId) continue
      return { blockId: b.id, permissionId: permId }
    }
  }
  return undefined
}

// findPlanBlockById returns the kind:"plan" Block whose id matches, or
// undefined. Used to read planDecision/planTargetMode for the read-only
// post-decision label.
function findPlanBlockById(turns: Turn[], blockId: string) {
  for (const t of turns) {
    for (const b of t.blocks) {
      if (b.id === blockId && b.kind === 'plan') return b
    }
  }
  return undefined
}

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
