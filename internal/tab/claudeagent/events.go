package claudeagent

import (
	"encoding/json"
	"sync/atomic"
	"time"
)

// AgentEvent is the WS frame Palmux sends to the browser. Stream-json shapes
// are normalised into this small, stable schema so the frontend doesn't
// follow CLI version drift.
type AgentEvent struct {
	Type    string          `json:"type"`
	TS      time.Time       `json:"ts"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// EventType enumerates every server→client event we emit.
type EventType string

const (
	EvSessionInit       EventType = "session.init"
	EvSessionReplaced   EventType = "session.replaced"
	EvTurnStart         EventType = "turn.start"
	EvTurnEnd           EventType = "turn.end"
	EvBlockStart        EventType = "block.start"
	EvBlockDelta        EventType = "block.delta"
	EvBlockEnd          EventType = "block.end"
	EvToolResult        EventType = "tool.result"
	EvPermissionRequest EventType = "permission.request"
	EvAskQuestion       EventType = "ask.question"
	EvAskDecided        EventType = "ask.decided"
	EvPlanQuestion      EventType = "plan.question"
	EvPlanDecided       EventType = "plan.decided"
	EvStatusChange      EventType = "status.change"
	EvUserMessage       EventType = "user.message"
	EvError             EventType = "error"
	// Pushed when the CLI's initialize control_response lands, so the
	// frontend can refresh its slash-command popup / model list / MCP
	// server view without re-fetching the snapshot.
	EvInitInfo EventType = "init.info"
	// Pushed when permission_mode changes server-side without a
	// CLI respawn (e.g. plan approve flow). Lets the frontend update
	// its mode pill without requiring a heavy session.init.
	EvPermissionModeChange EventType = "permission_mode.change"
)

// PermissionModeChangePayload notifies the frontend of a server-side
// permission_mode change.
type PermissionModeChangePayload struct {
	Mode string `json:"mode"`
}

// AgentStatus is the high-level UI state pip.
type AgentStatus string

const (
	StatusIdle              AgentStatus = "idle"
	StatusStarting          AgentStatus = "starting"
	StatusThinking          AgentStatus = "thinking"
	StatusToolRunning       AgentStatus = "tool_running"
	StatusAwaitingPermission AgentStatus = "awaiting_permission"
	StatusError             AgentStatus = "error"
)

// Block is the cached block shape stored on the session and replayed on
// reconnect.
type Block struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"` // text | thinking | tool_use | tool_result | todo | permission | plan | ask | hook
	Index    int             `json:"index"`
	Text     string          `json:"text,omitempty"`
	Name     string          `json:"name,omitempty"`     // tool_use name
	Input    json.RawMessage `json:"input,omitempty"`    // tool_use input (may be partial during stream)
	Output   string          `json:"output,omitempty"`   // tool_result content
	IsError  bool            `json:"isError,omitempty"`
	Done     bool            `json:"done,omitempty"`
	Todos    json.RawMessage `json:"todos,omitempty"`    // TodoWrite payload (latest replaces)

	// ──────────── kind:"hook" fields (S005) ────────────
	// Populated only when the CLI runs with --include-hook-events. Each
	// hook fires twice in the stream: a `hook_started` envelope opens the
	// block, and a `hook_response` envelope closes it with the actual
	// stdout/stderr/exit_code. Until the response lands we render the
	// block in a "running" state (Done=false). All fields except HookID
	// are absent on the started envelope.

	// HookID is the CLI-minted UUID that pairs hook_started with its
	// hook_response. Used to upsert the block on the response.
	HookID string `json:"hookId,omitempty"`
	// HookEvent is the lifecycle event name: "PreToolUse" / "PostToolUse"
	// / "Stop" / "Notification" etc.
	HookEvent string `json:"hookEvent,omitempty"`
	// HookName is the matcher-qualified name from the CLI: e.g.
	// "PreToolUse:Bash". Used as the block header label.
	HookName string `json:"hookName,omitempty"`
	// HookStdout / HookStderr / HookOutput / HookExitCode mirror the
	// `hook_response` envelope. HookOutput is the combined output the CLI
	// reports; the split stdout/stderr fields are kept separate so the
	// UI can colour stderr lines.
	HookStdout   string `json:"hookStdout,omitempty"`
	HookStderr   string `json:"hookStderr,omitempty"`
	HookOutput   string `json:"hookOutput,omitempty"`
	HookExitCode int    `json:"hookExitCode,omitempty"`
	// HookOutcome is the CLI's high-level verdict: "success" /
	// "blocked" / "modified_payload" etc. Surfaced as a tone pip on the
	// header. Distinct from HookExitCode because a hook can succeed
	// (exit 0) yet still be reported as having modified the payload.
	HookOutcome string `json:"hookOutcome,omitempty"`
	// HookPayload is the (optionally CLI-modified) JSON payload the CLI
	// reports as the post-hook input. Only populated when the hook
	// rewrote the input. Stored as raw JSON so the UI can pretty-print.
	HookPayload json.RawMessage `json:"hookPayload,omitempty"`

	// ToolUseID is the upstream Anthropic tool_use_id for tool_use blocks.
	// Distinct from Block.ID, which is the Palmux-minted local identifier.
	// Needed so the frontend can match sub-agent turns (whose envelopes
	// carry parent_tool_use_id) to the parent Task block that spawned
	// them. Empty for non-tool blocks.
	ToolUseID string `json:"toolUseId,omitempty"`

	// Permission-block fields. PermissionID is also stamped onto ask
	// blocks (kind:"ask") once the matching permission_prompt arrives,
	// so the UI can route ask.respond frames using that id.
	PermissionID string          `json:"permissionId,omitempty"`
	ToolName     string          `json:"toolName,omitempty"`
	Decision     string          `json:"decision,omitempty"` // "allow" | "deny" | ""

	// AskAnswers is set on kind:"ask" blocks once the user has chosen
	// option(s). The shape is the array of arrays (one inner array per
	// question, each holding the chosen labels) we ship to the CLI.
	// Used by the frontend to switch to the "decided" view on reload.
	AskAnswers json.RawMessage `json:"askAnswers,omitempty"`

	// PlanDecision records the user's answer to a kind:"plan" block once
	// they've clicked Approve or Keep planning. "approved" / "rejected" /
	// "" — empty means the plan is still actionable. Replayed on reload
	// so the action row stays hidden after the user has decided.
	PlanDecision string `json:"planDecision,omitempty"`
	// PlanTargetMode is set on kind:"plan" blocks once the user has approved
	// the plan with a chosen permission mode (e.g. "auto" / "acceptEdits").
	// The frontend uses this to render the "Approved — switching to mode X"
	// label after reload.
	PlanTargetMode string `json:"planTargetMode,omitempty"`
}

// Turn is one user→assistant exchange in the cached snapshot.
type Turn struct {
	Role   string  `json:"role"` // "user" | "assistant" | "tool"
	ID     string  `json:"id"`
	Blocks []Block `json:"blocks"`
	// ParentToolUseID is non-empty when this turn was produced by a
	// sub-agent the CLI spawned via the Task tool. Its value is the
	// tool_use_id of the parent Task block in the calling conversation.
	// The frontend uses this to render sub-agent turns nested underneath
	// their parent Task block instead of flat in the timeline.
	ParentToolUseID string `json:"parentToolUseId,omitempty"`
	// streamCovered is set true when stream_event envelopes (message_start
	// or content_block_start) populated this turn. The trailing `assistant`
	// envelope from the CLI then becomes a no-op — its purpose is to be a
	// fallback when stream events were missing, but firing it on top of an
	// already-streamed turn duplicates ask/plan blocks. Unexported so it
	// stays out of the snapshot wire format.
	streamCovered bool `json:"-"`
}

// SessionInitPayload is what the server writes immediately on WS connect.
type SessionInitPayload struct {
	SessionID      string      `json:"sessionId"`
	BranchID       string      `json:"branchId"`
	RepoID         string      `json:"repoId"`
	Model          string      `json:"model"`
	Effort         string      `json:"effort,omitempty"`
	PermissionMode string      `json:"permissionMode"`
	Status         AgentStatus `json:"status"`
	Turns          []Turn      `json:"turns"`
	TotalCostUSD   float64     `json:"totalCostUsd"`
	AuthOK         bool        `json:"authOk"`
	AuthMessage    string      `json:"authMessage,omitempty"`
	/** CLI-reported capabilities (commands list, models, etc). */
	InitInfo       InitInfo    `json:"initInfo"`
	MCPServers     []MCPServerInfo `json:"mcpServers,omitempty"`
}

// TurnStartPayload signals the start of an assistant turn.
type TurnStartPayload struct {
	TurnID          string `json:"turnId"`
	Role            string `json:"role"`
	ParentToolUseID string `json:"parentToolUseId,omitempty"`
}

// TurnEndPayload signals turn completion.
type TurnEndPayload struct {
	TurnID       string          `json:"turnId"`
	IsError      bool            `json:"isError"`
	TotalCostUSD float64         `json:"totalCostUsd"`
	DurationMs   int             `json:"durationMs"`
	Usage        json.RawMessage `json:"usage,omitempty"`
}

// BlockStartPayload begins a new block in the current turn.
type BlockStartPayload struct {
	TurnID string `json:"turnId"`
	Block  Block  `json:"block"`
	// ParentToolUseID is the parent_tool_use_id of the envelope this
	// block came from. The frontend uses it to attach the implicit turn
	// (the one block.start created when no message_start preceded it)
	// to the correct sub-agent ancestor.
	ParentToolUseID string `json:"parentToolUseId,omitempty"`
}

// BlockDeltaPayload appends to an in-progress block.
type BlockDeltaPayload struct {
	TurnID  string `json:"turnId"`
	BlockID string `json:"blockId"`
	Index   int    `json:"index"`
	Kind    string `json:"kind"`              // "text" | "thinking" | "tool_input"
	Text    string `json:"text,omitempty"`    // for text/thinking
	Partial string `json:"partial,omitempty"` // for tool_use input partial json
}

// BlockEndPayload finalizes a block (no more deltas).
type BlockEndPayload struct {
	TurnID  string          `json:"turnId"`
	BlockID string          `json:"blockId"`
	Final   json.RawMessage `json:"final,omitempty"` // optional final, well-formed input/text
}

// ToolResultPayload is emitted when a tool_result block lands. The result is
// attached to the most-recent user turn (or starts one).
type ToolResultPayload struct {
	TurnID    string `json:"turnId"`
	ToolUseID string `json:"toolUseId"`
	Output    string `json:"output"`
	IsError   bool   `json:"isError"`
}

// PermissionRequestPayload mirrors a CLI canUseTool request.
type PermissionRequestPayload struct {
	PermissionID string          `json:"permissionId"`
	ToolName     string          `json:"toolName"`
	Input        json.RawMessage `json:"input"`
}

// AskQuestionPayload announces an active AskUserQuestion request to the
// frontend. The frontend matches the permissionId against the kind:"ask"
// block to enable its action row (otherwise the buttons stay disabled
// because we wouldn't know how to answer).
type AskQuestionPayload struct {
	PermissionID string          `json:"permissionId"`
	BlockID      string          `json:"blockId,omitempty"`
	TurnID       string          `json:"turnId,omitempty"`
	ToolUseID    string          `json:"toolUseId,omitempty"`
	Questions    json.RawMessage `json:"questions,omitempty"`
}

// AskDecidedPayload notifies the frontend that an AskUserQuestion has
// been answered. The frontend uses it to flip the corresponding
// kind:"ask" block to the "decided" view (chosen option highlighted,
// buttons disabled) so all connected clients stay in sync.
type AskDecidedPayload struct {
	PermissionID string          `json:"permissionId"`
	BlockID      string          `json:"blockId,omitempty"`
	TurnID       string          `json:"turnId,omitempty"`
	Answers      json.RawMessage `json:"answers,omitempty"`
}

// PlanQuestionPayload announces an active ExitPlanMode permission request to
// the frontend. The frontend matches the permissionId against the kind:"plan"
// block to enable its action row (Approve / Edit / Keep planning) — without
// it, buttons stay hidden because no plan.respond would be answerable.
type PlanQuestionPayload struct {
	PermissionID string `json:"permissionId"`
	BlockID      string `json:"blockId,omitempty"`
	TurnID       string `json:"turnId,omitempty"`
	ToolUseID    string `json:"toolUseId,omitempty"`
}

// PlanDecidedPayload notifies the frontend that a plan permission has been
// answered. Decision is "approved" or "rejected"; TargetMode is the new
// permission mode (e.g. "auto") when approved. Mirrors AskDecidedPayload —
// useful for cross-tab sync so a second tab flips the action row off too.
type PlanDecidedPayload struct {
	PermissionID string `json:"permissionId"`
	BlockID      string `json:"blockId,omitempty"`
	TurnID       string `json:"turnId,omitempty"`
	Decision     string `json:"decision"` // "approved" | "rejected"
	TargetMode   string `json:"targetMode,omitempty"`
}

// StatusChangePayload is a single AgentStatus update.
type StatusChangePayload struct {
	Status AgentStatus `json:"status"`
}

// SessionReplacedPayload — the user did /clear or a fresh resume failed.
type SessionReplacedPayload struct {
	OldSessionID string `json:"oldSessionId"`
	NewSessionID string `json:"newSessionId"`
}

// UserMessagePayload echoes the user-side message back so all connected
// clients see the same conversation.
type UserMessagePayload struct {
	TurnID  string `json:"turnId"`
	Content string `json:"content"`
}

// ErrorPayload is fatal-or-transient error info.
type ErrorPayload struct {
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// ──────────── Client → Server frames ───────────────────────────────────────

// ClientFrame is the discriminated union from the browser.
type ClientFrame struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// UserMessageFrame — a new message to send to the agent.
type UserMessageFrame struct {
	Content string `json:"content"`
}

// PermissionRespondFrame — the user's answer to a permission.request.
type PermissionRespondFrame struct {
	PermissionID string          `json:"permissionId"`
	Decision     string          `json:"decision"` // "allow" | "deny"
	Scope        string          `json:"scope"`    // "once" | "session"
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	Reason       string          `json:"reason,omitempty"`
}

// AskRespondFrame — the user's answer to an AskUserQuestion. Answers is
// a slice of slices, one inner slice per question (so multi-select
// questions can ship multiple labels). Empty inner slice ⇒ "no answer"
// for that question (aka skip / dismiss). The CLI is informed via the
// underlying permission_prompt MCP request; see Agent.AnswerAskQuestion.
type AskRespondFrame struct {
	PermissionID string     `json:"permissionId"`
	Answers      [][]string `json:"answers"`
}

// PlanRespondFrame — the user's answer to an ExitPlanMode permission. The
// CLI's ExitPlanMode tool routes through the same MCP permission_prompt
// path as any other tool, but Palmux re-routes its UI to a kind:"plan"
// block (see manager.requestPlanResponse). This frame is what the
// frontend ships back when the user clicks Approve / Keep planning.
//
// Decision == "approve" ⇒ behavior:"allow" goes back to the CLI; the
// agent (optionally) switches to TargetMode and the EditedPlan, if
// non-empty, is shipped as updatedInput.plan so the executing CLI sees
// the user's edits.
//
// Decision == "reject" ⇒ behavior:"deny" with a "User chose to keep
// planning" message; the agent stays in plan mode.
type PlanRespondFrame struct {
	PermissionID string `json:"permissionId"`
	Decision     string `json:"decision"`     // "approve" | "reject"
	TargetMode   string `json:"targetMode,omitempty"`
	EditedPlan   string `json:"editedPlan,omitempty"`
}

// SetModelFrame — change models.
type SetModelFrame struct {
	Model string `json:"model"`
}

// SetPermissionModeFrame — switch permission mode.
type SetPermissionModeFrame struct {
	Mode string `json:"mode"`
}

// SetEffortFrame — switch CLI --effort level.
type SetEffortFrame struct {
	Effort string `json:"effort"`
}

// SessionResumeFrame — switch active session_id and respawn.
type SessionResumeFrame struct {
	SessionID string `json:"sessionId"`
}

// SessionForkFrame — fork off baseSessionId into a fresh session.
type SessionForkFrame struct {
	BaseSessionID string `json:"baseSessionId"`
}

// ──────────── helpers ──────────────────────────────────────────────────────

var idCounter atomic.Uint64

func newID(prefix string) string {
	n := idCounter.Add(1)
	return prefix + "_" + uintHex(n)
}

func uintHex(n uint64) string {
	const hex = "0123456789abcdef"
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = hex[n&0xF]
		n >>= 4
	}
	return string(buf[i:])
}

func makeEvent(typ EventType, payload any) (AgentEvent, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return AgentEvent{}, err
	}
	return AgentEvent{Type: string(typ), TS: time.Now().UTC(), Payload: b}, nil
}
