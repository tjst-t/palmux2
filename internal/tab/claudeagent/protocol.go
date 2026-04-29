package claudeagent

import "encoding/json"

// streamMsg is the wire-format envelope for messages exchanged with the
// claude CLI when run with --input-format stream-json --output-format
// stream-json. Every JSON line is a complete object whose `type` discriminates
// the shape of the rest of the fields.
//
// The CLI's stream-json schema is not a stable public API. We keep all
// nested structures as json.RawMessage so unknown fields pass through
// transparently — a CLI version bump that adds a field will not crash us.
type streamMsg struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`
	Event   json.RawMessage `json:"event,omitempty"`

	SessionID string `json:"session_id,omitempty"`

	// ParentToolUseID is non-empty (and not "null") when this envelope was
	// emitted by a sub-agent the CLI spawned via the Task tool. Its value
	// is the tool_use_id of the Task block in the parent conversation.
	// Top-level (user-driven) envelopes leave this empty / null.
	//
	// Wire-confirmed against claude CLI 2.1.123: the SDK schema declares
	// this field on `user`, `assistant`, `stream_event`, and `tool_progress`
	// messages.
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`

	// control_request / control_response
	RequestID string          `json:"request_id,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`
	Response  json.RawMessage `json:"response,omitempty"`

	// system / init payload (--verbose)
	Model           string          `json:"model,omitempty"`
	Cwd             string          `json:"cwd,omitempty"`
	Tools           json.RawMessage `json:"tools,omitempty"`
	MCPServers      []MCPServerInfo `json:"mcp_servers,omitempty"`
	PermissionMode  string          `json:"permission_mode,omitempty"`
	APIKeySource    string          `json:"apiKeySource,omitempty"`

	// result / turn-end payload
	TotalCostUSD     float64         `json:"total_cost_usd,omitempty"`
	DurationMs       int             `json:"duration_ms,omitempty"`
	NumTurns         int             `json:"num_turns,omitempty"`
	IsError          bool            `json:"is_error,omitempty"`
	Result           string          `json:"result,omitempty"`
	Usage            json.RawMessage `json:"usage,omitempty"`
}

// MCPServerInfo is the per-server status the CLI reports in system/init.
type MCPServerInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "connected" | "needs-auth" | "failed" | ...
}

// assistantMessage / userMessage are the bodies inside `message`.
//
// The Anthropic Messages API content shape (text / thinking / tool_use /
// tool_result) is what the CLI emits.
type chatMessage struct {
	ID      string         `json:"id,omitempty"`
	Role    string         `json:"role"`
	Model   string         `json:"model,omitempty"`
	Content rawContentList `json:"content"`
	StopReason string      `json:"stop_reason,omitempty"`
	Usage   json.RawMessage `json:"usage,omitempty"`
}

// rawContentList accepts both a plain string (user text) and an array of
// block objects (assistant or structured user content). Normalised to the
// array form on unmarshal.
type rawContentList []json.RawMessage

func (r *rawContentList) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		// plain string body — wrap as a single text block
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		obj := struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "text", Text: s}
		raw, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		*r = rawContentList{raw}
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	*r = arr
	return nil
}

// contentBlock is just enough of a block's shape to dispatch on; the rest of
// the JSON travels as RawMessage so we can surface unknown blocks verbatim.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// streamEvent is the body of a "stream_event" message. Used to render
// assistant output incrementally when --include-partial-messages is on.
type streamEvent struct {
	Type    string          `json:"type"`
	Index   int             `json:"index,omitempty"`
	Delta   json.RawMessage `json:"delta,omitempty"`
	Block   json.RawMessage `json:"content_block,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`
	Usage   json.RawMessage `json:"usage,omitempty"`
}

// streamDelta carries the actual delta text/thinking/tool input.
type streamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

// canUseToolRequest is the body of a control_request with subtype
// "can_use_tool" — the CLI asking permission to run a tool.
type canUseToolRequest struct {
	Subtype  string          `json:"subtype"`
	ToolName string          `json:"tool_name"`
	Input    json.RawMessage `json:"input"`
}

// canUseToolResponse is what we send back. Behavior is "allow" or "deny";
// when "allow" we may rewrite the tool input via UpdatedInput.
type canUseToolResponse struct {
	Subtype      string          `json:"subtype"`
	Behavior     string          `json:"behavior"` // "allow" | "deny"
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	Message      string          `json:"message,omitempty"`
}

// initializeRequest is the first control_request we send after spawn. It
// declares the SDK-typed MCP servers Palmux owns; without this list, the
// CLI rejects --permission-prompt-tool references because the server isn't
// in its dynamically-managed set even when --mcp-config carries the entry.
//
// Optional fields the SDK includes (hooks, jsonSchema, systemPrompt, …) are
// omitted — Palmux doesn't override any of them today.
type initializeRequest struct {
	Subtype       string   `json:"subtype"`
	SDKMCPServers []string `json:"sdkMcpServers,omitempty"`
}

// interruptRequest is the control_request that aborts the in-flight turn.
type interruptRequest struct {
	Subtype string `json:"subtype"`
}

// setModelRequest swaps the model mid-session.
type setModelRequest struct {
	Subtype string `json:"subtype"`
	Model   string `json:"model"`
}

// setPermissionModeRequest swaps the permission mode mid-session.
type setPermissionModeRequest struct {
	Subtype string `json:"subtype"`
	Mode    string `json:"mode"`
}

// setMCPServersRequest replaces the set of dynamically-managed MCP servers
// the CLI knows about. The SDK uses this to register `type:"sdk"` servers
// that exist only in-process — `--mcp-config` alone does not suffice.
type setMCPServersRequest struct {
	Subtype string                  `json:"subtype"`
	Servers map[string]mcpServerRef `json:"servers"`
}

// mcpServerRef is the JSON shape under setMCPServersRequest.Servers. The
// CLI accepts the same union as `--mcp-config`; we only ever send sdk-type.
type mcpServerRef struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// controlResponseInner is the nested object inside a control_response
// envelope. CLI uses asymmetric shapes for control_request vs
// control_response: requests put `request_id` at top level, responses bury
// it one level deeper inside the union of success/error variants.
type controlResponseInner struct {
	Subtype   string          `json:"subtype"` // "success" | "error"
	RequestID string          `json:"request_id"`
	Response  json.RawMessage `json:"response,omitempty"` // success
	Error     string          `json:"error,omitempty"`    // error
}
