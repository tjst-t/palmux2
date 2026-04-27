// Wire-format types shared by the WS client and the rendering layer.
// Mirror the Go structs at internal/tab/claudeagent/events.go.

export type AgentStatus =
  | 'idle'
  | 'starting'
  | 'thinking'
  | 'tool_running'
  | 'awaiting_permission'
  | 'error'

export type BlockKind =
  | 'text'
  | 'thinking'
  | 'tool_use'
  | 'tool_result'
  | 'todo'
  | 'permission'

export interface Block {
  id: string
  kind: BlockKind
  index?: number
  text?: string
  name?: string
  input?: unknown
  output?: string
  isError?: boolean
  done?: boolean
  todos?: unknown
  permissionId?: string
  toolName?: string
  decision?: 'allow' | 'deny' | ''
}

export interface Turn {
  role: 'user' | 'assistant' | 'tool'
  id: string
  blocks: Block[]
}

export interface SessionInit {
  sessionId: string
  branchId: string
  repoId: string
  model: string
  permissionMode: string
  status: AgentStatus
  turns: Turn[]
  totalCostUsd: number
  authOk: boolean
  authMessage?: string
}

export interface AgentEvent<T = unknown> {
  type: string
  ts: string
  payload?: T
}

export interface AuthStatus {
  ok: boolean
  source?: string
  message?: string
}
