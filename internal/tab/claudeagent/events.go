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
	EvStatusChange      EventType = "status.change"
	EvUserMessage       EventType = "user.message"
	EvError             EventType = "error"
)

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
	Kind     string          `json:"kind"` // text | thinking | tool_use | tool_result | todo | permission
	Index    int             `json:"index"`
	Text     string          `json:"text,omitempty"`
	Name     string          `json:"name,omitempty"`     // tool_use name
	Input    json.RawMessage `json:"input,omitempty"`    // tool_use input (may be partial during stream)
	Output   string          `json:"output,omitempty"`   // tool_result content
	IsError  bool            `json:"isError,omitempty"`
	Done     bool            `json:"done,omitempty"`
	Todos    json.RawMessage `json:"todos,omitempty"`    // TodoWrite payload (latest replaces)

	// Permission-block fields
	PermissionID string          `json:"permissionId,omitempty"`
	ToolName     string          `json:"toolName,omitempty"`
	Decision     string          `json:"decision,omitempty"` // "allow" | "deny" | ""
}

// Turn is one user→assistant exchange in the cached snapshot.
type Turn struct {
	Role   string  `json:"role"` // "user" | "assistant"
	ID     string  `json:"id"`
	Blocks []Block `json:"blocks"`
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
	TurnID string `json:"turnId"`
	Role   string `json:"role"`
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
