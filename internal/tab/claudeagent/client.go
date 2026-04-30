package claudeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ClientOptions configures one CLI subprocess.
type ClientOptions struct {
	Binary         string   // "claude" by default
	Cwd            string   // worktree path
	SessionID      string   // resume target ("" = new session)
	Model          string   // --model
	PermissionMode string   // --permission-mode
	Effort         string   // --effort (low | medium | high | xhigh | max)
	Fork           bool     // when true, --fork-session: use session_id as base but start a fresh id
	// IncludeHookEvents adds --include-hook-events to the CLI invocation so
	// PreToolUse / PostToolUse / Stop / etc. hooks emit lifecycle envelopes
	// (system/hook_started + system/hook_response) on stdout. Opt-in: the
	// flag is omitted by default to keep stream volume low and to honour
	// "CLI is truth" — Palmux never invents hook activity, only mirrors
	// what the CLI emits.
	IncludeHookEvents bool
	// AddDirs are absolute filesystem paths passed to the CLI as
	// `--add-dir <path>` (repeatable). The flag teaches Claude that
	// these directories are within its allowed scope so tools
	// (Read/Edit/etc.) don't bounce on the worktree boundary.
	// Wire-confirmed against claude CLI 2.1.123: the help text reads
	//   --add-dir <directories...>  Additional directories to allow tool access to
	// and the flag is repeatable / space-separated. Like
	// --include-hook-events, this is a startup-only flag — adding a new
	// dir mid-session requires a respawn (handled in agent.go via
	// respawnClient when the AddDirs set grows between user.message
	// frames).
	AddDirs        []string
	ExtraArgs      []string // user-supplied flags from settings.json
	Logger         *slog.Logger
}

// CanUseToolHandler is invoked when the CLI asks permission to run a tool
// via the legacy `can_use_tool` control_request. Modern Claude Code routes
// permission checks through `--permission-prompt-tool` + MCP instead, so
// this handler is rarely (if ever) hit; we keep it as a defence-in-depth
// fallback. The handler must answer with allow/deny; an error → deny.
type CanUseToolHandler func(ctx context.Context, req canUseToolRequest, requestID string) (canUseToolResponse, error)

// MessageHandler is the upstream callback for every (non-control) line the
// CLI emits. The callee receives the raw envelope so it can dispatch on Type.
type MessageHandler func(msg streamMsg)

// Client owns one claude CLI subprocess. It is a thin transport: it spawns,
// pumps JSON-lines I/O, and exposes Send / Interrupt / SetModel / Close. All
// stream-json normalisation and conversation state lives one layer up
// (manager.go / normalize.go).
type Client struct {
	opts        ClientOptions
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	mux         *controlMux
	onMessage   MessageHandler
	onCanUseTool CanUseToolHandler
	mcp         *mcpServer
	logger      *slog.Logger

	writeMu sync.Mutex
	closeOnce sync.Once

	doneCh chan struct{} // closed when the subprocess exits
	exitErr error

	// invalidResume is set by pumpStderr when the CLI emits the
	// "No conversation found with session ID" line — a sign that the
	// --resume target is stale and the Agent should retry without it.
	invalidResumeMu sync.Mutex
	invalidResume   bool
}

// NewClient spawns the CLI and starts pumping its stdout in a goroutine.
// Pass `permission` to wire the MCP-based permission prompt — without it
// the CLI will deny tool calls in stream-json mode, since `bypass`/`auto`
// modes still gate dangerous operations through the prompt tool. The
// caller MUST eventually call Close to reap the process.
func NewClient(ctx context.Context, opts ClientOptions, onMessage MessageHandler, onCanUseTool CanUseToolHandler, permission PermissionRequester) (*Client, error) {
	if opts.Binary == "" {
		opts.Binary = "claude"
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--setting-sources", "project,user",
	}
	if permission != nil {
		// Tell the CLI which tool to ask for permission. The SDK server
		// itself is declared via `sdkMcpServers` inside the `initialize`
		// control_request — passing it through --mcp-config too registers
		// the entry twice and triggers a duplicate initialize handshake.
		args = append(args,
			"--permission-prompt-tool", "mcp__"+MCPServerName+"__"+PermissionToolName,
		)
	}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
		if opts.Fork {
			args = append(args, "--fork-session")
		}
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", opts.PermissionMode)
	}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	if opts.IncludeHookEvents {
		// Wire-confirmed against claude CLI 2.1.123: emits
		// `{"type":"system","subtype":"hook_started",...}` followed by
		// `{"type":"system","subtype":"hook_response","output":"...","stdout":"...","stderr":"...","exit_code":N,"outcome":"success|...","hook_event":"PreToolUse|PostToolUse|...","hook_id":"...","hook_name":"PreToolUse:Bash"}`
		// — handled by normalize.go's handleHookEvent path.
		args = append(args, "--include-hook-events")
	}
	for _, d := range opts.AddDirs {
		if d == "" {
			continue
		}
		// CLI 2.1.123 wire-confirmed: `--add-dir <directories...>` accepts
		// repeated flag occurrences. We pass them as separate `--add-dir`
		// invocations rather than space-separated to avoid shell-quoting
		// surprises if a path contains a space.
		args = append(args, "--add-dir", d)
	}
	args = append(args, opts.ExtraArgs...)

	// Run with a long-lived ctx so SIGTERM isn't delivered the moment the
	// HTTP request that triggered the spawn finishes. The Manager kills the
	// process explicitly on shutdown.
	cmd := exec.CommandContext(context.Background(), opts.Binary, args...)
	cmd.Dir = opts.Cwd

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("claudeagent: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claudeagent: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("claudeagent: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claudeagent: spawn %s: %w", opts.Binary, err)
	}

	c := &Client{
		opts:        opts,
		cmd:         cmd,
		stdin:       stdin,
		mux:         newControlMux(),
		onMessage:   onMessage,
		onCanUseTool: onCanUseTool,
		logger:      opts.Logger,
		doneCh:      make(chan struct{}),
	}
	if permission != nil {
		c.mcp = newMCPServer(permission)
	}

	go c.pumpStdout(stdout)
	go c.pumpStderr(stderr)
	go c.waitProcess()

	return c, nil
}

// Done returns a channel closed once the subprocess has exited. ExitErr
// captures the cause; nil for clean exits.
func (c *Client) Done() <-chan struct{} { return c.doneCh }
func (c *Client) ExitErr() error        { return c.exitErr }

func (c *Client) waitProcess() {
	err := c.cmd.Wait()
	c.exitErr = err
	close(c.doneCh)
	c.mux.closeAll()
}

// pumpStdout reads JSON-lines off the CLI's stdout. Bufio's default 64 KiB
// max-token-size is too small for big tool_result blocks; bump it to 4 MiB.
const stdoutLineBudget = 4 << 20

func (c *Client) pumpStdout(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), stdoutLineBudget)
	consecutiveErrors := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg streamMsg
		if err := json.Unmarshal(line, &msg); err != nil {
			consecutiveErrors++
			c.logger.Warn("claudeagent: bad json line", "err", err, "snippet", truncate(string(line), 200))
			if consecutiveErrors >= 5 {
				c.logger.Error("claudeagent: too many bad json lines, killing CLI")
				_ = c.cmd.Process.Kill()
				return
			}
			continue
		}
		consecutiveErrors = 0

		switch {
		case msg.Type == "control_response":
			// Spec shape: {response: {subtype, request_id, response|error}}.
			// We reach into the nested object — request_id is NOT the
			// envelope-level field for responses.
			var inner controlResponseInner
			if err := json.Unmarshal(msg.Response, &inner); err != nil {
				c.logger.Warn("claudeagent: malformed control_response", "err", err)
				continue
			}
			c.mux.resolveResponse(inner)
		case msg.Type == "control_request" && msg.RequestID != "" && len(msg.Request) > 0:
			go c.handleControlRequest(msg)
		case msg.Type == "control_cancel_request":
			// CLI-side cancellation. We don't track inflight handlers granularly
			// — the caller's ctx will fire a regular timeout. Acknowledge by
			// dropping the message.
		default:
			if c.onMessage != nil {
				c.onMessage(msg)
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		c.logger.Warn("claudeagent: stdout scanner", "err", err)
	}
}

func (c *Client) pumpStderr(stderr io.Reader) {
	r := bufio.NewReader(stderr)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			c.logger.Info("claude.stderr", "line", line)
			// Surface known fatal patterns to anyone watching. These are
			// hints for the Manager — e.g. a stale --resume session_id
			// makes the CLI exit immediately, so we want to clear the
			// active pointer before respawning.
			if strings.Contains(line, "No conversation found with session ID") {
				c.flagInvalidResume()
			}
		}
		if err != nil {
			return
		}
	}
}

// flagInvalidResume marks this client as having tried to resume a session
// that no longer exists on disk. The Agent's watchClient checks the flag
// after the process exits and clears the persisted active session_id so
// the next spawn starts fresh.
func (c *Client) flagInvalidResume() {
	c.invalidResumeMu.Lock()
	c.invalidResume = true
	c.invalidResumeMu.Unlock()
}

// InvalidResume reports whether stderr matched the "no conversation found"
// pattern at any point during this client's lifetime.
func (c *Client) InvalidResume() bool {
	c.invalidResumeMu.Lock()
	defer c.invalidResumeMu.Unlock()
	return c.invalidResume
}

// handleControlRequest processes CLI-initiated control_requests. Runs on
// its own goroutine so a slow permission decision doesn't stall stdout.
func (c *Client) handleControlRequest(msg streamMsg) {
	var head struct {
		Subtype string `json:"subtype"`
	}
	_ = json.Unmarshal(msg.Request, &head)
	switch head.Subtype {
	case "mcp_message":
		c.handleMCPMessage(msg)
	case "can_use_tool":
		var req canUseToolRequest
		if err := json.Unmarshal(msg.Request, &req); err != nil {
			c.respondControl(msg.RequestID, canUseToolResponse{
				Subtype: "can_use_tool", Behavior: "deny",
				Message: "malformed canUseTool request",
			})
			return
		}
		if c.onCanUseTool == nil {
			c.respondControl(msg.RequestID, canUseToolResponse{
				Subtype: "can_use_tool", Behavior: "deny",
				Message: "no permission handler configured",
			})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), controlRequestTimeout)
		defer cancel()
		resp, err := c.onCanUseTool(ctx, req, msg.RequestID)
		if err != nil {
			c.respondControl(msg.RequestID, canUseToolResponse{
				Subtype: "can_use_tool", Behavior: "deny",
				Message: err.Error(),
			})
			return
		}
		resp.Subtype = "can_use_tool"
		c.respondControl(msg.RequestID, resp)
	default:
		c.logger.Warn("claudeagent: unknown control_request subtype", "subtype", head.Subtype)
	}
}

// handleMCPMessage unwraps the JSON-RPC message inside an mcp_message
// control_request, dispatches it to the in-process MCP server, and packages
// the JSON-RPC response back inside an mcp_message control_response so the
// CLI's MCP client picks it up. Notification-style requests yield no
// response.
func (c *Client) handleMCPMessage(msg streamMsg) {
	if c.mcp == nil {
		c.respondControl(msg.RequestID, map[string]any{
			"subtype": "mcp_message",
			"mcp_response": map[string]any{
				"jsonrpc": "2.0",
				"error":   map[string]any{"code": -32601, "message": "MCP not configured"},
			},
		})
		return
	}
	var inner struct {
		Subtype    string          `json:"subtype"`
		ServerName string          `json:"server_name"`
		Message    json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(msg.Request, &inner); err != nil {
		c.logger.Warn("claudeagent: malformed mcp_message", "err", err)
		return
	}
	if inner.ServerName != "" && inner.ServerName != MCPServerName {
		c.logger.Warn("claudeagent: mcp_message for unknown server", "server", inner.ServerName)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	resp, hasResp := c.mcp.handle(ctx, inner.Message)
	// The CLI tracks the OUTER control_request on its own — every envelope
	// it sends needs a control_response, even when the inner MCP message
	// is a notification with no JSON-RPC response. We send a stub
	// `{mcp_response: null}` body in that case so the CLI's pending map
	// can clear and the conversation can proceed.
	body := map[string]any{}
	if hasResp {
		body["mcp_response"] = resp
	} else {
		body["mcp_response"] = nil
	}
	c.respondControl(msg.RequestID, body)
}

// respondControl writes a success-shaped control_response with the given
// request_id and payload. The wire format is asymmetric: outgoing requests
// carry request_id at the envelope level, but responses nest it one deeper
// alongside `subtype: "success"` (see GG4 schema in CLI).
func (c *Client) respondControl(requestID string, body any) {
	frame, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   body,
		},
	})
	if err != nil {
		c.logger.Warn("claudeagent: marshal control envelope", "err", err)
		return
	}
	if err := c.writeLine(frame); err != nil {
		c.logger.Warn("claudeagent: write control response", "err", err)
	}
}

// respondControlError writes an error-shaped control_response.
func (c *Client) respondControlError(requestID, message string) {
	frame, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "error",
			"request_id": requestID,
			"error":      message,
		},
	})
	if err != nil {
		c.logger.Warn("claudeagent: marshal control envelope", "err", err)
		return
	}
	if err := c.writeLine(frame); err != nil {
		c.logger.Warn("claudeagent: write control error", "err", err)
	}
}

func (c *Client) writeLine(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(data); err != nil {
		return err
	}
	if _, err := c.stdin.Write([]byte("\n")); err != nil {
		return err
	}
	return nil
}

// SendUserMessage sends a plain text user message into the CLI's stdin.
func (c *Client) SendUserMessage(content string) error {
	body, err := json.Marshal(map[string]any{
		"role":    "user",
		"content": content,
	})
	if err != nil {
		return err
	}
	frame, err := json.Marshal(streamMsg{
		Type:    "user",
		Message: body,
	})
	if err != nil {
		return err
	}
	return c.writeLine(frame)
}

// Initialize sends the initialize control request. Declares the SDK-typed
// MCP servers up-front (just `palmux` for now). The CLI responds with a
// big payload describing its commands / agents / models / account; we
// hand the raw response back to the caller so the manager can extract the
// pieces the UI needs (commands list, model menu, etc).
func (c *Client) Initialize(ctx context.Context) (json.RawMessage, error) {
	req := initializeRequest{Subtype: "initialize"}
	if c.mcp != nil {
		req.SDKMCPServers = []string{MCPServerName}
	}
	return c.controlCall(ctx, req)
}

// Interrupt aborts the in-flight assistant turn.
func (c *Client) Interrupt(ctx context.Context) error {
	_, err := c.controlCall(ctx, interruptRequest{Subtype: "interrupt"})
	return err
}

// SetModel changes the model mid-session.
func (c *Client) SetModel(ctx context.Context, model string) error {
	_, err := c.controlCall(ctx, setModelRequest{Subtype: "set_model", Model: model})
	return err
}

// SetPermissionMode swaps the permission policy.
func (c *Client) SetPermissionMode(ctx context.Context, mode string) error {
	_, err := c.controlCall(ctx, setPermissionModeRequest{Subtype: "set_permission_mode", Mode: mode})
	return err
}

// RegisterSDKMCPServer announces an in-process MCP server to the CLI via
// `mcp_set_servers`. Static `--mcp-config` is necessary but not sufficient
// for SDK-typed servers; the CLI ignores them at tool-resolution time
// unless they're also in the dynamically-managed set.
func (c *Client) RegisterSDKMCPServer(ctx context.Context, name string) error {
	_, err := c.controlCall(ctx, setMCPServersRequest{
		Subtype: "mcp_set_servers",
		Servers: map[string]mcpServerRef{
			name: {Type: "sdk", Name: name},
		},
	})
	return err
}

// Close kills the subprocess and waits for the pumps to drain. Safe to call
// multiple times.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		_ = c.stdin.Close()
		<-c.doneCh
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
