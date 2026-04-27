package claudeagent

import (
	"context"
	"encoding/json"
	"fmt"
)

// In-process MCP server. Handles just enough of the Model Context Protocol
// for Claude Code to use Palmux as a `--permission-prompt-tool` provider.
//
// We expose exactly one tool: `permission_prompt`. The CLI calls it whenever
// it wants to run a tool that requires permission (the alternative — built-
// in TTY prompts — fails closed in stream-json mode). The tool delegates to
// the host's PermissionRequester (UI-driven Allow / Deny / Allow-for-session).
//
// Wire layout — three nesting levels at play:
//
//	stream-json envelope          (control_request type=mcp_message)
//	  └── mcp_message wrapper     (server_name + JSON-RPC 2.0 message)
//	        └── JSON-RPC method   (initialize / tools/list / tools/call)
//
// We don't validate against the full MCP schema — only what claude-code
// actually sends.

// MCPServerName is the name we expose to the CLI. The fully-qualified tool
// reference becomes `mcp__<MCPServerName>__permission_prompt`.
const MCPServerName = "palmux"

// PermissionToolName is the single tool exposed by the SDK MCP server.
const PermissionToolName = "permission_prompt"

// permissionResponse is what we return from the permission tool. The CLI
// parses the *string* in the first content block as JSON and looks for
// `behavior` / `updatedInput` / `message`.
type permissionResponse struct {
	Behavior     string          `json:"behavior"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	Message      string          `json:"message,omitempty"`
}

// PermissionRequester is the bridge from the MCP layer to the host UI. The
// implementation is on Agent — the same handleCanUseTool path Palmux already
// uses for the (now-legacy) direct can_use_tool control flow.
type PermissionRequester interface {
	RequestPermission(ctx context.Context, toolName string, input json.RawMessage, toolUseID string) (permissionResponse, error)
}

// mcpServer is the JSON-RPC 2.0 dispatcher. Stateless aside from the wired
// requester; one instance per Agent.
type mcpServer struct {
	requester PermissionRequester
}

func newMCPServer(req PermissionRequester) *mcpServer {
	return &mcpServer{requester: req}
}

// jsonRPCRequest mirrors the subset of JSON-RPC 2.0 we need. `id` may be a
// number, string, or null — we keep it as RawMessage and echo verbatim.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// handle dispatches one JSON-RPC request to the appropriate method handler.
// Notifications (no id) are dropped silently — we never originate them, and
// the CLI doesn't send any to permission tools.
func (s *mcpServer) handle(ctx context.Context, raw json.RawMessage) (jsonRPCResponse, bool) {
	var req jsonRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return jsonRPCResponse{
			JSONRPC: "2.0",
			Error:   &jsonRPCError{Code: -32700, Message: "parse error"},
		}, true
	}
	// Notification — id absent; no response required.
	if len(req.ID) == 0 {
		return jsonRPCResponse{}, false
	}
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &initParams)
		resp.Result = s.initializeResult(initParams.ProtocolVersion)
	case "tools/list":
		resp.Result = s.toolsListResult()
	case "tools/call":
		out, jerr := s.toolsCall(ctx, req.Params)
		if jerr != nil {
			resp.Error = jerr
		} else {
			resp.Result = out
		}
	case "notifications/initialized", "notifications/cancelled":
		// Notification — no response. Shouldn't appear with id, but ignore.
		return jsonRPCResponse{}, false
	default:
		resp.Error = &jsonRPCError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp, true
}

// initializeResult mirrors a minimal MCP server initialize response. We
// echo whichever protocolVersion the CLI advertised so we don't have to
// track every revision; this keeps us forward-compatible.
func (s *mcpServer) initializeResult(clientProto string) map[string]any {
	if clientProto == "" {
		clientProto = "2025-06-18"
	}
	return map[string]any{
		"protocolVersion": clientProto,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    MCPServerName,
			"version": "0.1.0",
		},
	}
}

// toolsListResult advertises the single permission_prompt tool. Schema is
// permissive — the CLI doesn't validate strictly here.
func (s *mcpServer) toolsListResult() map[string]any {
	return map[string]any{
		"tools": []map[string]any{
			{
				"name":        PermissionToolName,
				"description": "Ask the Palmux user whether the agent may run this tool.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"tool_name":   map[string]any{"type": "string"},
						"input":       map[string]any{"type": "object"},
						"tool_use_id": map[string]any{"type": "string"},
					},
					"required": []string{"tool_name"},
				},
			},
		},
	}
}

// toolsCallParams is the payload of a JSON-RPC tools/call request.
type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type permissionArgs struct {
	ToolName  string          `json:"tool_name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
}

// toolsCall is the only method that actually does work. Anything other than
// permission_prompt yields a JSON-RPC 'method not found' style error.
func (s *mcpServer) toolsCall(ctx context.Context, raw json.RawMessage) (map[string]any, *jsonRPCError) {
	var p toolsCallParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	if p.Name != PermissionToolName {
		return nil, &jsonRPCError{Code: -32601, Message: fmt.Sprintf("unknown tool %q", p.Name)}
	}
	var args permissionArgs
	if err := json.Unmarshal(p.Arguments, &args); err != nil {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid arguments: " + err.Error()}
	}
	if s.requester == nil {
		return permissionToolResult(permissionResponse{Behavior: "deny", Message: "no permission requester"}), nil
	}

	resp, err := s.requester.RequestPermission(ctx, args.ToolName, args.Input, args.ToolUseID)
	if err != nil {
		return permissionToolResult(permissionResponse{Behavior: "deny", Message: err.Error()}), nil
	}
	// CLI's allow-variant schema requires updatedInput. If the host didn't
	// rewrite anything, pass the original input through unchanged. Without
	// this the response trips zod's invalid_union check and the tool call
	// fails with a `<tool_use_error>` instead of running.
	if resp.Behavior == "allow" && len(resp.UpdatedInput) == 0 {
		if len(args.Input) > 0 {
			resp.UpdatedInput = args.Input
		} else {
			resp.UpdatedInput = json.RawMessage(`{}`)
		}
	}
	if resp.Behavior == "deny" && resp.Message == "" {
		resp.Message = "Denied by user."
	}
	return permissionToolResult(resp), nil
}

// permissionToolResult packages the permission decision into the MCP content
// shape the CLI expects: a single text block whose body is a JSON-encoded
// `{behavior, updatedInput?, message?}` document. The CLI parses that string
// to drive its allow/deny path.
func permissionToolResult(resp permissionResponse) map[string]any {
	body, _ := json.Marshal(resp)
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(body)},
		},
		"isError": resp.Behavior == "deny",
	}
}
