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
  | 'hook'

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
  /** Set on kind:"plan" blocks once the user has clicked Approve or
   *  Keep planning. "approved" / "rejected" / undefined. Replayed on
   *  reload so the action row stays hidden. */
  planDecision?: 'approved' | 'rejected'
  /** Set on kind:"plan" blocks once the user approved with a chosen
   *  permission mode (e.g. "auto"). Used to render the post-approval
   *  status label. */
  planTargetMode?: string

  // ──────────── kind:"hook" fields (S005) ────────────
  // Populated only when --include-hook-events is enabled. Mirrors the
  // CLI's system/hook_started + system/hook_response envelopes.
  /** CLI-minted UUID pairing a hook_started with its hook_response. */
  hookId?: string
  /** Lifecycle event: "PreToolUse" / "PostToolUse" / "Stop" / etc. */
  hookEvent?: string
  /** Matcher-qualified header label, e.g. "PreToolUse:Bash". */
  hookName?: string
  hookStdout?: string
  hookStderr?: string
  hookOutput?: string
  hookExitCode?: number
  hookOutcome?: string
  /** Optional CLI-modified tool input payload (when the hook rewrote
   *  the input). Stored as JSON-compatible value so the UI can pretty-
   *  print. */
  hookPayload?: unknown
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
  role: 'user' | 'assistant' | 'tool' | 'hook'
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

/** Per-server MCP connection status, mirrored from the CLI's
 *  `system/init` payload (`mcp_servers[]`). The Status string is left
 *  as raw text because the CLI's vocabulary has expanded over time
 *  (`connected`, `connecting`, `failed`, `needs-auth`, ...) — the UI
 *  classifies it in a single place (statusTone) and degrades unknown
 *  values to a neutral pill rather than dropping them. */
export interface MCPServerInfo {
  name: string
  status: string
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
  /** MCP server connection statuses populated from system/init. May be
   *  absent (older snapshots) or empty (no MCP servers configured). */
  mcpServers?: MCPServerInfo[]
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
