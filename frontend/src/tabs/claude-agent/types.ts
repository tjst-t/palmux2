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
  | 'plan'
  | 'ask'

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
  /** Upstream Anthropic tool_use_id for tool_use blocks. Distinct from
   *  `id` (the Palmux-minted local identifier). Sub-agent turns spawned
   *  by a Task tool block point to this via `parentToolUseId`. */
  toolUseId?: string
  /** Set on kind:"ask" blocks once the user has chosen option(s).
   *  Wire-shape mirrors the Go backend's `[][]string` — one inner array
   *  per question, holding the chosen option labels. The frontend uses
   *  this to render the "decided" view on reconnect. */
  askAnswers?: string[][]
}

/** Shape of the input payload on a kind:"ask" block. Mirrors the
 *  AskUserQuestion CLI tool input. */
export interface AskQuestion {
  question: string
  header?: string
  multiSelect?: boolean
  options: AskOption[]
}

export interface AskOption {
  label: string
  description?: string
}

export interface AskInput {
  questions: AskQuestion[]
}

export interface Turn {
  role: 'user' | 'assistant' | 'tool'
  id: string
  blocks: Block[]
  /** Set when this turn was produced by a sub-agent the CLI spawned via
   *  the Task tool. Value = the parent Task block's toolUseId. Used by
   *  the renderer to nest sub-agent turns underneath their parent. */
  parentToolUseId?: string
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
