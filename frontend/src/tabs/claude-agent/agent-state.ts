// Reducer-driven local state for one ClaudeAgent tab. The shape mirrors
// SessionInit + the streaming events defined in internal/tab/claudeagent.
//
// Kept deliberately self-contained (no Zustand) — one Agent tab = one
// instance of this state, lifetime equal to the React component.

import type { AgentStatus, Block, InitInfo, SessionInit, Turn } from './types'

export interface AgentState {
  ready: boolean
  authOk: boolean
  authMessage?: string
  sessionId: string
  model: string
  effort: string
  permissionMode: string
  status: AgentStatus
  totalCostUsd: number
  turns: Turn[]
  pendingPermission?: { permissionId: string; toolName: string; input: unknown }
  errors: { id: number; message: string; detail?: string }[]
  /** CLI-reported capabilities (commands list, models, agents). */
  initInfo?: InitInfo
  /** Latest usage info from a turn.end (input/cache/output tokens). */
  lastUsage?: AgentUsage
  /** ISO timestamp of the most-recently-applied event — debug only. */
  lastEventTs?: string
}

export interface AgentUsage {
  inputTokens?: number
  outputTokens?: number
  cacheReadInputTokens?: number
  cacheCreationInputTokens?: number
  contextWindow?: number
  maxOutputTokens?: number
}

export const initialState: AgentState = {
  ready: false,
  authOk: false,
  sessionId: '',
  model: '',
  effort: '',
  permissionMode: 'acceptEdits',
  status: 'idle',
  totalCostUsd: 0,
  turns: [],
  errors: [],
}

export type AgentAction =
  | { kind: 'reset' }
  | { kind: 'init'; payload: SessionInit }
  | { kind: 'event'; ev: { type: string; ts: string; payload?: unknown } }

let errorCounter = 0

export function reduce(state: AgentState, action: AgentAction): AgentState {
  switch (action.kind) {
    case 'reset':
      return initialState
    case 'init': {
      const p = action.payload
      // If the agent is mid-permission when we reconnect, the snapshot
      // includes the unresolved permission block. Reconstruct
      // pendingPermission from it so the Allow/Deny buttons keep working
      // — otherwise they'd disappear and the user couldn't answer.
      const pending = findPendingPermissionInTurns(p.turns ?? [])
      return {
        ...state,
        ready: true,
        authOk: p.authOk,
        authMessage: p.authMessage,
        sessionId: p.sessionId,
        model: p.model,
        effort: (p as { effort?: string }).effort ?? '',
        permissionMode: p.permissionMode,
        status: p.status,
        totalCostUsd: p.totalCostUsd,
        turns: p.turns ?? [],
        pendingPermission: pending,
        errors: [],
        initInfo: p.initInfo,
      }
    }
    case 'event':
      return applyEvent(state, action.ev)
  }
}

function applyEvent(state: AgentState, ev: { type: string; ts: string; payload?: unknown }): AgentState {
  const next: AgentState = { ...state, lastEventTs: ev.ts }
  switch (ev.type) {
    case 'session.init': {
      // Re-init: trust the snapshot.
      const p = ev.payload as SessionInit
      return reduce(initialState, { kind: 'init', payload: p })
    }
    case 'session.replaced': {
      next.turns = []
      next.sessionId = ((ev.payload as { newSessionId?: string }).newSessionId) ?? ''
      next.totalCostUsd = 0
      next.pendingPermission = undefined
      return next
    }
    case 'status.change': {
      const p = ev.payload as { status: AgentStatus }
      next.status = p.status
      if (p.status !== 'awaiting_permission') {
        next.pendingPermission = undefined
      }
      return next
    }
    case 'turn.start': {
      const p = ev.payload as { turnId: string; role: 'user' | 'assistant' }
      next.turns = [...next.turns, { id: p.turnId, role: p.role, blocks: [] }]
      return next
    }
    case 'turn.end': {
      const p = ev.payload as {
        turnId: string
        totalCostUsd?: number
        usage?: Partial<AgentUsage> & {
          input_tokens?: number
          output_tokens?: number
          cache_read_input_tokens?: number
          cache_creation_input_tokens?: number
          contextWindow?: number
          maxOutputTokens?: number
        }
      }
      if (typeof p.totalCostUsd === 'number') {
        next.totalCostUsd = next.totalCostUsd + p.totalCostUsd
      }
      if (p.usage) {
        const u = p.usage
        next.lastUsage = {
          inputTokens: u.inputTokens ?? u.input_tokens,
          outputTokens: u.outputTokens ?? u.output_tokens,
          cacheReadInputTokens: u.cacheReadInputTokens ?? u.cache_read_input_tokens,
          cacheCreationInputTokens: u.cacheCreationInputTokens ?? u.cache_creation_input_tokens,
          contextWindow: u.contextWindow,
          maxOutputTokens: u.maxOutputTokens,
        }
      }
      return next
    }
    case 'block.start': {
      const p = ev.payload as { turnId: string; block: Block }
      next.turns = upsertTurn(next.turns, p.turnId, 'assistant', (turn) => ({
        ...turn,
        blocks: [...turn.blocks, { ...p.block }],
      }))
      return next
    }
    case 'block.delta': {
      const p = ev.payload as {
        turnId: string
        blockId: string
        index?: number
        kind?: 'text' | 'thinking' | 'tool_input'
        text?: string
        partial?: string
      }
      next.turns = mutateBlock(next.turns, p.turnId, p.blockId, (b) => {
        if (p.kind === 'text' || p.kind === 'thinking') {
          return { ...b, text: (b.text ?? '') + (p.text ?? '') }
        }
        if (p.kind === 'tool_input') {
          return { ...b, text: (b.text ?? '') + (p.partial ?? '') }
        }
        return b
      })
      return next
    }
    case 'block.end': {
      const p = ev.payload as { turnId: string; blockId: string; final?: unknown }
      next.turns = mutateBlock(next.turns, p.turnId, p.blockId, (b) => ({
        ...b,
        done: true,
        ...(p.final && b.kind === 'tool_use'
          ? { input: p.final, text: '' }
          : {}),
      }))
      return next
    }
    case 'tool.result': {
      const p = ev.payload as { turnId: string; toolUseId?: string; output: string; isError: boolean }
      next.turns = [
        ...next.turns,
        {
          id: p.turnId,
          role: 'tool',
          blocks: [{
            id: `${p.turnId}-result`,
            kind: 'tool_result',
            output: p.output,
            isError: p.isError,
            done: true,
          }],
        },
      ]
      return next
    }
    case 'permission.request': {
      const p = ev.payload as { permissionId: string; toolName: string; input: unknown }
      next.pendingPermission = p
      return appendPermissionBlock(next, p)
    }
    case 'user.message': {
      const p = ev.payload as { turnId: string; content: string }
      // We optimistically appended a user turn on submit; replace if the
      // server-assigned ID came through.
      if (next.turns.length > 0) {
        const last = next.turns[next.turns.length - 1]
        if (last.role === 'user' && last.id !== p.turnId && last.blocks[0]?.text === p.content) {
          const replaced = { ...last, id: p.turnId }
          next.turns = [...next.turns.slice(0, -1), replaced]
          return next
        }
      }
      // Otherwise just append (covers cross-tab sync from another browser).
      next.turns = [
        ...next.turns,
        {
          id: p.turnId,
          role: 'user',
          blocks: [{ id: `${p.turnId}-user`, kind: 'text', text: p.content, done: true }],
        },
      ]
      return next
    }
    case 'error': {
      const p = ev.payload as { message: string; detail?: string }
      errorCounter += 1
      next.errors = [...next.errors, { id: errorCounter, message: p.message, detail: p.detail }]
      return next
    }
    default:
      return next
  }
}

function upsertTurn(
  turns: Turn[],
  turnId: string,
  role: 'user' | 'assistant' | 'tool',
  mutate: (t: Turn) => Turn,
): Turn[] {
  let found = false
  const out = turns.map((t) => {
    if (t.id === turnId) {
      found = true
      return mutate(t)
    }
    return t
  })
  if (!found) {
    out.push(mutate({ id: turnId, role, blocks: [] }))
  }
  return out
}

function mutateBlock(
  turns: Turn[],
  turnId: string,
  blockId: string,
  mutate: (b: Block) => Block,
): Turn[] {
  return turns.map((t) => {
    if (t.id !== turnId) return t
    return {
      ...t,
      blocks: t.blocks.map((b) => (b.id === blockId ? mutate(b) : b)),
    }
  })
}

function appendPermissionBlock(state: AgentState, payload: { permissionId: string; toolName: string; input: unknown }): AgentState {
  const turns = [...state.turns]
  // Attach to the most-recent assistant turn — that's what the server
  // already did when it added the permission block to its session cache.
  for (let i = turns.length - 1; i >= 0; i--) {
    if (turns[i].role === 'assistant') {
      const existing = turns[i].blocks.find((b) => b.permissionId === payload.permissionId)
      if (existing) return { ...state, turns }
      turns[i] = {
        ...turns[i],
        blocks: [
          ...turns[i].blocks,
          {
            id: `${payload.permissionId}-block`,
            kind: 'permission',
            permissionId: payload.permissionId,
            toolName: payload.toolName,
            input: payload.input,
          },
        ],
      }
      return { ...state, turns }
    }
  }
  // No assistant turn yet — make a new one.
  turns.push({
    id: `pending-${payload.permissionId}`,
    role: 'assistant',
    blocks: [
      {
        id: `${payload.permissionId}-block`,
        kind: 'permission',
        permissionId: payload.permissionId,
        toolName: payload.toolName,
        input: payload.input,
      },
    ],
  })
  return { ...state, turns }
}

// findPendingPermissionInTurns scans the turns snapshot for an undecided
// permission block. Used when re-hydrating from session.init so the
// Allow/Deny buttons keep working after a WS reconnect or page reload.
function findPendingPermissionInTurns(
  turns: Turn[],
): { permissionId: string; toolName: string; input: unknown } | undefined {
  for (let i = turns.length - 1; i >= 0; i--) {
    for (const b of turns[i].blocks) {
      if (b.kind === 'permission' && !b.decision && b.permissionId) {
        return {
          permissionId: b.permissionId,
          toolName: b.toolName ?? 'tool',
          input: b.input,
        }
      }
    }
  }
  return undefined
}
