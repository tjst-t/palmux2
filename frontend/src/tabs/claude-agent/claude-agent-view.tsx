import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { confirmDialog } from '../../components/context-menu/confirm-dialog'
import { api } from '../../lib/api'
import type { TabViewProps } from '../../lib/tab-registry'

import styles from './claude-agent-view.module.css'
import { Composer } from './composer'
import { ConversationExportDialog } from './conversation-export'
import {
  ConversationList,
  type ConversationListHandle,
} from './conversation-list'
import { useClaudeShortcuts } from './hooks/use-claude-shortcuts'
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
import { SettingsPopup } from './settings-popup'
// S4b9df4-2: TopBar + helpers (pipClass / labelForStatus / mcpPipClass /
// statusToneAgree / contextPercent) extracted into ./top-bar.tsx.
import { TopBar, contextPercent, labelForStatus } from './top-bar'
import { TurnView } from './turn-view'
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
  // S4b9df4-4: editingTurnId lift-up removed. The S019 lift-up was
  // a workaround for react-window unmounting rows; commit bed812b
  // dropped react-window so rows never unmount. UserTurnEditor now
  // owns its own editing state.

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

  // S018: in-conversation search (Cmd+F / Ctrl+F). Scrolls to the
  // matching row through the imperative List API so virtualisation
  // (S017) plays nicely — the row is realised before being centred.
  const search = useConversationSearch(topLevelTurns, (idx) => {
    listHandleRef.current?.scrollToRow(idx, { align: 'center', behavior: 'smooth' })
  })
  // wrapRef is required by useClaudeShortcuts to gate Cmd+F to the
  // currently-focused tab so the browser's Find still works elsewhere.
  const wrapRef = useRef<HTMLDivElement | null>(null)

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

  // S4b9df4-3: the three keyboard shortcuts (⌘H toggle history,
  // ⌘F open search, y/n/Escape respond to pending permission) are
  // all dispatched by useClaudeShortcuts. Single keydown listener,
  // shared textarea/input focus guard.
  useClaudeShortcuts({
    onToggleHistory: () => setHistoryOpen((v) => !v),
    onOpenSearch: search.open,
    wrapRef,
    pendingPermission: state.pendingPermission,
    send,
  })

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
      respondPermission,
      planHandlersFor,
      askHandlersFor,
      childrenByParent,
    ],
  )

  // S4b9df4-2: TopBar props grouped into 3 buckets so the call site
  // is short and adding a new button doesn't grow the parent's prop
  // list. The /clear handler is destructive, hence the explicit
  // confirm dialog inline (matches Claude Code CLI behaviour).
  const onClear = useCallback(async () => {
    const ok = await confirmDialog.ask({
      title: 'Clear conversation context?',
      message:
        'This starts a fresh session. The current conversation will not be visible in this tab anymore (the on-disk transcript stays under ~/.claude/projects/ and remains accessible from the History popup).',
      confirmLabel: 'Clear',
      cancelLabel: 'Cancel',
      danger: true,
    })
    if (ok) send.sessionClear()
  }, [send])

  return (
    <div className={styles.wrap} ref={wrapRef}>
      <TopBar
        state={{
          status: state.status,
          totalCostUsd: state.totalCostUsd,
          contextPct: contextPercent(state.lastUsage),
          mcpServers: state.mcpServers,
          connState,
          canInterrupt: isStreaming,
        }}
        actions={{
          onClear,
          onInterrupt: () => send.interrupt(),
          onOpenHistory: () => setHistoryOpen((v) => !v),
          onOpenSettings: () => setSettingsOpen(true),
          onOpenSearch: search.open,
          onOpenExport: () => setExportOpen(true),
        }}
        ctx={{
          mcpButtonRef,
          historyButtonRef,
          repoId,
          branchId,
        }}
        mcpOpen={mcpOpen}
        onToggleMcp={() => setMcpOpen((v) => !v)}
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
      <div className={styles.popupAnchor}>
        <MCPPopup
          servers={state.mcpServers}
          open={mcpOpen}
          onClose={() => setMcpOpen(false)}
          anchorRef={mcpButtonRef}
        />
      </div>
      {historyOpen && (
        <div className={styles.popupAnchor}>
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
            <p className={styles.emptyHint}>
              Slash commands: <code>/clear</code> for a fresh session, <code>/model &lt;name&gt;</code> to switch.
            </p>
          </div>
        ) : (
          // visibility:hidden (not display:none) keeps the list laid
          // out so the parent flex container can compute heights
          // before the scroll-restore finishes. We flip to visible
          // from the onSettled callback the moment the first scroll
          // adjustment lands (or, for atBottom / no-record paths,
          // immediately). The flex/min layout itself is in
          // .listShell — only the visibility flip stays inline.
          <div
            className={styles.listShell}
            style={{ visibility: restoreVisible ? 'visible' : 'hidden' }}
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

// S4b9df4-2: TopBarProps / TopBar / pipClass / labelForStatus /
// mcpPipClass / statusToneAgree / contextPercent moved into
// ./top-bar.tsx. labelForStatus + contextPercent are re-exported
// from there because the streaming overlay (this file) and parent
// composer still consume them.
