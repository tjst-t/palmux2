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
  /** Active AskUserQuestion permission IDs, keyed by permissionId, value =
   *  the corresponding kind:"ask" block id so the renderer can wire the
   *  action row. The entry is removed when the user submits an answer
   *  (or on session.replaced / session.init). */
  pendingAskByBlock: Record<string, string>
  /** Active ExitPlanMode permission IDs, keyed by permissionId, value =
   *  the corresponding kind:"plan" block id. Mirrors pendingAskByBlock —
   *  used by claude-agent-view's planHandlersFor to decide whether to
   *  show the action row on a plan block. */
  pendingPlanByBlock: Record<string, string>
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
  pendingAskByBlock: {},
  pendingPlanByBlock: {},
  errors: [],
}

export type AgentAction =
  | { kind: 'reset' }
  | { kind: 'restore'; state: AgentState }
  | { kind: 'init'; payload: SessionInit }
  | { kind: 'event'; ev: { type: string; ts: string; payload?: unknown } }

let errorCounter = 0

export function reduce(state: AgentState, action: AgentAction): AgentState {
  switch (action.kind) {
    case 'reset':
      return initialState
    case 'restore':
      return action.state
    case 'init': {
      const p = action.payload
      // If the agent is mid-permission when we reconnect, the snapshot
      // includes the unresolved permission block. Reconstruct
      // pendingPermission from it so the Allow/Deny buttons keep working
      // — otherwise they'd disappear and the user couldn't answer.
      const pending = findPendingPermissionInTurns(p.turns ?? [])
      // Same for AskUserQuestion: walk the turns and re-build the
      // permissionId → blockId map so the AskQuestionBlock action row
      // is enabled for any unanswered question that survived a reload.
      const pendingAskByBlock = findPendingAsksInTurns(p.turns ?? [])
      const pendingPlanByBlock = findPendingPlansInTurns(p.turns ?? [])
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
        pendingAskByBlock,
        pendingPlanByBlock,
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
    case 'init.info': {
      next.initInfo = ev.payload as InitInfo
      return next
    }
    case 'session.replaced': {
      next.turns = []
      next.sessionId = ((ev.payload as { newSessionId?: string }).newSessionId) ?? ''
      next.totalCostUsd = 0
      next.pendingPermission = undefined
      next.pendingAskByBlock = {}
      next.pendingPlanByBlock = {}
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
      const p = ev.payload as { turnId: string; role: 'user' | 'assistant'; parentToolUseId?: string }
      next.turns = [
        ...next.turns,
        {
          id: p.turnId,
          role: p.role,
          blocks: [],
          ...(p.parentToolUseId ? { parentToolUseId: p.parentToolUseId } : {}),
        },
      ]
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
      const p = ev.payload as { turnId: string; block: Block; parentToolUseId?: string }
      next.turns = upsertTurn(
        next.turns,
        p.turnId,
        'assistant',
        (turn) => ({
          ...turn,
          // If a turn already exists, do not clobber its parent linkage
          // — turn.start is the canonical source. But if block.start
          // creates the turn implicitly (no preceding message_start),
          // capture the parent here.
          ...(p.parentToolUseId && !turn.parentToolUseId
            ? { parentToolUseId: p.parentToolUseId }
            : {}),
          blocks: [...turn.blocks, { ...p.block }],
        }),
        p.parentToolUseId,
      )
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
    case 'ask.question': {
      const p = ev.payload as {
        permissionId: string
        blockId?: string
        turnId?: string
        toolUseId?: string
        questions?: unknown
      }
      // Stamp permissionId on the matching kind:"ask" block (matching
      // by toolUseId when it's available, falling back to blockId, and
      // finally to the most recent ask block) so the renderer can wire
      // the action row. Also keep `questions` fresh in case the block
      // arrived only via permission_prompt without a prior content
      // block_start (some CLI flows skip the stream_event).
      const turns = stampAskPermission(next.turns, p.permissionId, {
        blockId: p.blockId,
        toolUseId: p.toolUseId,
        questions: p.questions,
      })
      const blockId = findAskBlockIdForPermission(turns, p.permissionId)
      const nextAsk = { ...next.pendingAskByBlock }
      if (blockId) nextAsk[p.permissionId] = blockId
      next.turns = turns
      next.pendingAskByBlock = nextAsk
      return next
    }
    case 'ask.decided': {
      const p = ev.payload as {
        permissionId: string
        blockId?: string
        turnId?: string
        answers?: unknown
      }
      const answers = parseAnswers(p.answers)
      next.turns = applyAskAnswers(next.turns, p.permissionId, answers, p.blockId)
      const nextAsk = { ...next.pendingAskByBlock }
      delete nextAsk[p.permissionId]
      next.pendingAskByBlock = nextAsk
      return next
    }
    case 'plan.question': {
      const p = ev.payload as {
        permissionId: string
        blockId?: string
        turnId?: string
        toolUseId?: string
      }
      // Stamp permissionId on the matching kind:"plan" block — by
      // toolUseId when known, falling back to blockId, then to the most
      // recent plan block. Also rebuilds pendingPlanByBlock so the
      // renderer can show the action row.
      const turns = stampPlanPermission(next.turns, p.permissionId, {
        blockId: p.blockId,
        toolUseId: p.toolUseId,
      })
      const blockId = findPlanBlockIdForPermission(turns, p.permissionId)
      const nextPlan = { ...next.pendingPlanByBlock }
      if (blockId) nextPlan[p.permissionId] = blockId
      next.turns = turns
      next.pendingPlanByBlock = nextPlan
      return next
    }
    case 'plan.decided': {
      const p = ev.payload as {
        permissionId: string
        blockId?: string
        turnId?: string
        decision: 'approved' | 'rejected'
        targetMode?: string
      }
      next.turns = applyPlanDecision(next.turns, p.permissionId, p.decision, p.targetMode, p.blockId)
      const nextPlan = { ...next.pendingPlanByBlock }
      delete nextPlan[p.permissionId]
      next.pendingPlanByBlock = nextPlan
      return next
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
  parentToolUseId?: string,
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
    out.push(
      mutate({
        id: turnId,
        role,
        blocks: [],
        ...(parentToolUseId ? { parentToolUseId } : {}),
      }),
    )
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

// findPendingAsksInTurns rebuilds the permissionId → blockId map from a
// snapshot. Any kind:"ask" block that has a permissionId stamped but no
// askAnswers is treated as still-pending — the user can answer it after
// reconnect / reload.
function findPendingAsksInTurns(turns: Turn[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const t of turns) {
    for (const b of t.blocks) {
      if (b.kind !== 'ask' || !b.permissionId) continue
      const decided = Array.isArray(b.askAnswers) && b.askAnswers.length > 0
      if (decided) continue
      out[b.permissionId] = b.id
    }
  }
  return out
}

// stampAskPermission attaches a freshly-issued permissionId to the
// matching kind:"ask" block. Matching priority: explicit blockId →
// toolUseId → last ask block in the turns. Also lets us replace the
// block's `input` if the server shipped fresh `questions` so a client
// that skipped content_block_start can still render the question.
function stampAskPermission(
  turns: Turn[],
  permissionId: string,
  match: { blockId?: string; toolUseId?: string; questions?: unknown },
): Turn[] {
  let stamped = false
  const out = turns.map((t) => ({
    ...t,
    blocks: t.blocks.map((b) => {
      if (stamped) return b
      if (b.kind !== 'ask') return b
      if (match.blockId && b.id !== match.blockId) return b
      if (!match.blockId && match.toolUseId && b.toolUseId !== match.toolUseId) return b
      stamped = true
      const next: Block = { ...b, permissionId }
      if (match.questions != null) {
        // Wrap the questions list in the AskInput shape the renderer
        // expects. We splice in fresh questions only when the existing
        // block doesn't already have any (avoid clobbering streamed
        // question text). Detection is best-effort.
        const existing = parseAskInputObject(b.input)
        const hasExisting = !!existing && Array.isArray(existing.questions) && existing.questions.length > 0
        if (!hasExisting) {
          next.input = { questions: match.questions }
        }
      }
      return next
    }),
  }))
  if (stamped) return out
  // Fallback: walk newest-first manually, finding the most recent ask
  // block irrespective of toolUseId. If none, the event is a no-op
  // (the block.start envelope hasn't landed yet — should be rare).
  for (let i = turns.length - 1; i >= 0; i--) {
    const t = turns[i]
    for (let j = t.blocks.length - 1; j >= 0; j--) {
      const b = t.blocks[j]
      if (b.kind !== 'ask') continue
      const newBlocks = t.blocks.slice()
      const next: Block = { ...b, permissionId }
      if (match.questions != null) {
        const existing = parseAskInputObject(b.input)
        const hasExisting = !!existing && Array.isArray(existing.questions) && existing.questions.length > 0
        if (!hasExisting) next.input = { questions: match.questions }
      }
      newBlocks[j] = next
      const newTurn = { ...t, blocks: newBlocks }
      const newTurns = turns.slice()
      newTurns[i] = newTurn
      return newTurns
    }
  }
  return turns
}

function findAskBlockIdForPermission(turns: Turn[], permissionId: string): string | undefined {
  for (let i = turns.length - 1; i >= 0; i--) {
    for (const b of turns[i].blocks) {
      if (b.kind === 'ask' && b.permissionId === permissionId) return b.id
    }
  }
  return undefined
}

function applyAskAnswers(
  turns: Turn[],
  permissionId: string,
  answers: string[][],
  blockId?: string,
): Turn[] {
  return turns.map((t) => ({
    ...t,
    blocks: t.blocks.map((b) => {
      if (b.kind !== 'ask') return b
      if (blockId && b.id !== blockId) return b
      if (!blockId && b.permissionId !== permissionId) return b
      return { ...b, askAnswers: answers, done: true }
    }),
  }))
}

function parseAnswers(raw: unknown): string[][] {
  if (Array.isArray(raw)) {
    return raw.map((arr) => Array.isArray(arr) ? arr.filter((s): s is string => typeof s === 'string') : [])
  }
  if (typeof raw === 'string') {
    try {
      const parsed = JSON.parse(raw) as unknown
      return parseAnswers(parsed)
    } catch { return [] }
  }
  return []
}

function parseAskInputObject(input: unknown): { questions?: unknown[] } | null {
  if (input == null) return null
  if (typeof input === 'object') return input as { questions?: unknown[] }
  if (typeof input === 'string') {
    try { return JSON.parse(input) as { questions?: unknown[] } } catch { return null }
  }
  return null
}

// findPendingPlansInTurns mirrors findPendingAsksInTurns: walks the
// snapshot for any kind:"plan" block that has a permissionId stamped
// but no planDecision yet. Used to rebuild pendingPlanByBlock on
// reconnect / page reload so the action row stays live.
function findPendingPlansInTurns(turns: Turn[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const t of turns) {
    for (const b of t.blocks) {
      if (b.kind !== 'plan' || !b.permissionId) continue
      if (b.planDecision) continue
      out[b.permissionId] = b.id
    }
  }
  return out
}

// stampPlanPermission attaches a freshly-issued permissionId to the
// matching kind:"plan" block. Mirrors stampAskPermission. Matching
// priority: blockId → toolUseId → most recent plan block.
function stampPlanPermission(
  turns: Turn[],
  permissionId: string,
  match: { blockId?: string; toolUseId?: string },
): Turn[] {
  let stamped = false
  const out = turns.map((t) => ({
    ...t,
    blocks: t.blocks.map((b) => {
      if (stamped) return b
      if (b.kind !== 'plan') return b
      if (match.blockId && b.id !== match.blockId) return b
      if (!match.blockId && match.toolUseId && b.toolUseId !== match.toolUseId) return b
      stamped = true
      const next: Block = { ...b, permissionId }
      return next
    }),
  }))
  if (stamped) return out
  // Fallback: walk newest-first manually.
  for (let i = turns.length - 1; i >= 0; i--) {
    const t = turns[i]
    for (let j = t.blocks.length - 1; j >= 0; j--) {
      const b = t.blocks[j]
      if (b.kind !== 'plan') continue
      const newBlocks = t.blocks.slice()
      newBlocks[j] = { ...b, permissionId }
      const newTurn = { ...t, blocks: newBlocks }
      const newTurns = turns.slice()
      newTurns[i] = newTurn
      return newTurns
    }
  }
  return turns
}

function findPlanBlockIdForPermission(turns: Turn[], permissionId: string): string | undefined {
  for (let i = turns.length - 1; i >= 0; i--) {
    for (const b of turns[i].blocks) {
      if (b.kind === 'plan' && b.permissionId === permissionId) return b.id
    }
  }
  return undefined
}

// applyPlanDecision stamps the user's decision (and target mode) onto
// the kind:"plan" block matching permissionId. Mirrors applyAskAnswers.
function applyPlanDecision(
  turns: Turn[],
  permissionId: string,
  decision: 'approved' | 'rejected',
  targetMode: string | undefined,
  blockId?: string,
): Turn[] {
  return turns.map((t) => ({
    ...t,
    blocks: t.blocks.map((b) => {
      if (b.kind !== 'plan') return b
      if (blockId && b.id !== blockId) return b
      if (!blockId && b.permissionId !== permissionId) return b
      return {
        ...b,
        planDecision: decision,
        planTargetMode: targetMode,
        done: true,
      }
    }),
  }))
}
