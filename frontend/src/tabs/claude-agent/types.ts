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

export interface SlashCommand {
  name: string
  description?: string
  argumentHint?: string
  aliases?: string[]
}

export interface NamedItem {
  name: string
  description?: string
  model?: string
}

export interface ModelDescriptor {
  value: string
  displayName?: string
  description?: string
  supportsEffort?: boolean
  supportedEffortLevels?: string[]
  supportsAdaptiveThinking?: boolean
  supportsAutoMode?: boolean
}

export interface InitInfo {
  commands?: SlashCommand[]
  agents?: NamedItem[]
  models?: ModelDescriptor[]
  outputStyle?: string
  availableOutputStyles?: string[]
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
  initInfo?: InitInfo
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
