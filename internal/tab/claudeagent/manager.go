package claudeagent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// BranchResolver yields the on-disk worktree path for a branch. Implemented
// by *store.Store; passed in as an interface so this package can stay free of
// store imports.
type BranchResolver interface {
	WorktreePath(repoID, branchID string) (string, error)
}

// EventPublisher is the subset of *store.EventHub claudeagent needs to
// fan branch-scoped state changes out to Drawer / Activity Inbox / etc.
// Implemented by main.go via a small adapter so we don't import store.
type EventPublisher interface {
	Publish(eventType, repoID, branchID string, payload any)
}

// Config bundles long-lived settings the Manager needs.
type Config struct {
	Binary             string
	DefaultModel       string
	DefaultPermissionMode string
	ExtraArgs          []string
}

// NotificationSink is the subset of notify.Hub claudeagent uses to surface
// permission requests / errors in the global Activity Inbox. Implemented
// by main.go's adapter; nil-safe.
type NotificationSink interface {
	IngestInternal(repoID, branchID string, n InternalNotification)
	ClearByRequestID(repoID, branchID, requestID string)
}

// InternalNotification is the shape claudeagent publishes to NotificationSink.
// Mirrors notify.IngestRequest + a stable request_id so the sink can
// dedupe/clear when the underlying request resolves.
type InternalNotification struct {
	RequestID string                       `json:"requestId,omitempty"`
	Type      string                       `json:"type"`              // "urgent" | "warning" | "info"
	Title     string                       `json:"title,omitempty"`
	Message   string                       `json:"message,omitempty"`
	Detail    string                       `json:"detail,omitempty"`
	Actions   []InternalNotificationAction `json:"actions,omitempty"`
}

// InternalNotificationAction is one inline button on a notification.
type InternalNotificationAction struct {
	Label  string `json:"label"`
	Action string `json:"action"`
}

// Manager owns one Agent per (repoID, branchID). It is the single entry
// point used by the WS handler and the REST handlers.
type Manager struct {
	cfg     Config
	store   *Store
	branches BranchResolver
	events  EventPublisher
	notify  NotificationSink
	logger  *slog.Logger

	mu     sync.Mutex
	agents map[string]*Agent
}

// NewManager constructs a Manager. `cfg.Binary == ""` falls back to "claude".
// `events` and `notify` are optional — Manager falls back to per-WS-only
// broadcast when nil (handy for tests).
func NewManager(cfg Config, store *Store, branches BranchResolver, events EventPublisher, notify NotificationSink, logger *slog.Logger) *Manager {
	if cfg.Binary == "" {
		cfg.Binary = "claude"
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = ""
	}
	if cfg.DefaultPermissionMode == "" {
		cfg.DefaultPermissionMode = "acceptEdits"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		cfg:     cfg,
		store:   store,
		branches: branches,
		events:  events,
		notify:  notify,
		logger:  logger,
		agents:  map[string]*Agent{},
	}
}

func (m *Manager) key(repoID, branchID string) string { return repoID + "/" + branchID }

// Get returns the existing Agent for the branch, or nil.
func (m *Manager) Get(repoID, branchID string) *Agent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.agents[m.key(repoID, branchID)]
}

// EnsureAgent returns the existing Agent or creates a fresh one. The Client
// is not spawned yet — the caller decides when (lazy on first message).
func (m *Manager) EnsureAgent(repoID, branchID string) (*Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(repoID, branchID)
	if existing, ok := m.agents[k]; ok {
		return existing, nil
	}
	worktree, err := m.branches.WorktreePath(repoID, branchID)
	if err != nil {
		return nil, err
	}
	resumeID := m.store.ActiveFor(repoID, branchID)
	model := ""
	if meta, ok := m.store.Get(resumeID); ok {
		model = meta.Model
	}
	if model == "" {
		model = m.cfg.DefaultModel
	}
	a := newAgent(agentDeps{
		repoID:        repoID,
		branchID:      branchID,
		worktree:      worktree,
		model:         model,
		permissionMode: m.cfg.DefaultPermissionMode,
		resumeID:      resumeID,
		manager:       m,
		logger:        m.logger,
	})
	// Seed the session with whatever init payload we cached from the
	// previous run — gives us the slash-command popup / model list / agent
	// list on first paint, before this agent's CLI has been spawned.
	if cached := m.store.LastInit(); len(cached.Commands) > 0 || len(cached.Models) > 0 {
		a.session.SetInitInfo(cached)
	}
	m.agents[k] = a
	return a, nil
}

// KillBranch terminates the branch's Agent, if any. Used from
// Provider.OnBranchClose.
func (m *Manager) KillBranch(_ context.Context, repoID, branchID string) error {
	m.mu.Lock()
	a, ok := m.agents[m.key(repoID, branchID)]
	delete(m.agents, m.key(repoID, branchID))
	m.mu.Unlock()
	if !ok {
		return nil
	}
	a.Shutdown()
	return nil
}

// Shutdown stops every Agent. Called on server shutdown.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	agents := m.agents
	m.agents = map[string]*Agent{}
	m.mu.Unlock()
	for _, a := range agents {
		a.Shutdown()
	}
}

// Store exposes the underlying persistence store (used by REST handlers).
func (m *Manager) Store() *Store { return m.store }

// Config exposes the runtime config (binary path, defaults).
func (m *Manager) Config() Config { return m.cfg }

// ──────────── Agent ────────────────────────────────────────────────────────

type agentDeps struct {
	repoID, branchID string
	worktree         string
	model            string
	permissionMode   string
	resumeID         string
	manager          *Manager
	logger           *slog.Logger
}

// Agent is one branch's stateful pairing of (Session, optional Client). The
// WS handler subscribes to its broadcast channel; the Manager owns its
// lifecycle.
type Agent struct {
	deps agentDeps

	mu      sync.Mutex
	client  *Client
	session *Session
	starting bool

	// fan-out: each WS connection registers a channel; all events get cloned
	// into every channel using oldest-drop on overflow.
	subsMu sync.RWMutex
	subs   map[chan AgentEvent]struct{}

	// CLI permission requests block on a per-permission channel until the
	// browser answers. The map lives behind a.mu.
	permWaiters map[string]chan canUseToolResponse

	// intentionalRespawn is flipped on by respawnClient before it kills the
	// current Client. watchClient checks it to suppress the "Claude CLI
	// exited" error event that would otherwise alarm the user.
	intentionalRespawn bool

	// pendingFork is set by ForkSession; consumed by the next EnsureClient
	// to spawn the CLI with --fork-session.
	pendingFork bool
}

func newAgent(deps agentDeps) *Agent {
	return &Agent{
		deps: deps,
		session: NewSession(deps.repoID, deps.branchID, deps.resumeID, deps.model, deps.permissionMode),
		subs: map[chan AgentEvent]struct{}{},
	}
}

// Subscribe registers a new WS connection and returns the receive channel
// plus an unsubscribe func. The buffer is small but we drop oldest on
// overflow so a slow client can't block the broadcaster.
func (a *Agent) Subscribe() (<-chan AgentEvent, func()) {
	ch := make(chan AgentEvent, 64)
	a.subsMu.Lock()
	a.subs[ch] = struct{}{}
	a.subsMu.Unlock()
	return ch, func() {
		a.subsMu.Lock()
		if _, ok := a.subs[ch]; ok {
			delete(a.subs, ch)
			close(ch)
		}
		a.subsMu.Unlock()
	}
}

func (a *Agent) broadcast(ev AgentEvent) {
	a.subsMu.RLock()
	defer a.subsMu.RUnlock()
	for ch := range a.subs {
		select {
		case ch <- ev:
		default:
			// drop oldest, then push
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

func (a *Agent) broadcastMany(evs []AgentEvent) {
	for _, ev := range evs {
		a.broadcast(ev)
	}
}

// Snapshot returns a SessionInitPayload for the current state.
func (a *Agent) Snapshot() SessionInitPayload {
	return a.session.Snapshot()
}

// SetAuthStatus is called by the WS handler before snapshotting so the UI
// knows whether to show the setup hint.
func (a *Agent) SetAuthStatus(s AuthStatus) {
	a.session.SetAuthStatus(s)
}

// EnsureClient lazily spawns the CLI subprocess on first user message. The
// session_id, model, and permission_mode used come from the Session.
func (a *Agent) EnsureClient(ctx context.Context) error {
	a.mu.Lock()
	if a.client != nil {
		a.mu.Unlock()
		return nil
	}
	if a.starting {
		a.mu.Unlock()
		return errors.New("claudeagent: client is already starting")
	}
	a.starting = true
	a.mu.Unlock()

	a.session.SetStatus(StatusStarting)
	a.broadcastStatus(StatusStarting)

	resumeID := a.session.SessionID()
	if resumeID == "" {
		resumeID = a.deps.manager.store.ActiveFor(a.deps.repoID, a.deps.branchID)
	}
	model := a.session.Model()
	if model == "" {
		model = a.deps.manager.cfg.DefaultModel
	}

	a.mu.Lock()
	fork := a.pendingFork
	a.pendingFork = false
	a.mu.Unlock()
	cli, err := NewClient(ctx, ClientOptions{
		Binary:         a.deps.manager.cfg.Binary,
		Cwd:            a.deps.worktree,
		SessionID:      resumeID,
		Model:          model,
		PermissionMode: a.session.PermissionMode(),
		Effort:         a.session.Effort(),
		Fork:           fork,
		ExtraArgs:      a.deps.manager.cfg.ExtraArgs,
		Logger:         a.deps.logger,
	}, a.handleStreamMsg, a.handleCanUseTool, a)
	if err != nil {
		a.mu.Lock()
		a.starting = false
		a.mu.Unlock()
		a.session.SetStatus(StatusError)
		a.broadcastStatus(StatusError)
		a.broadcastError("Failed to start Claude CLI", err.Error())
		return err
	}

	a.mu.Lock()
	a.client = cli
	a.starting = false
	a.mu.Unlock()

	go a.watchClient(cli)

	initCtx, cancel := context.WithTimeout(ctx, controlRequestTimeout)
	defer cancel()
	resp, err := cli.Initialize(initCtx)
	if err != nil {
		a.deps.logger.Warn("claudeagent: initialize failed", "err", err)
	} else if len(resp) > 0 {
		info := parseInitInfo(resp)
		a.session.SetInitInfo(info)
		// Push to any connected WS client so the slash popup updates
		// without waiting for a fresh snapshot.
		if ev, e := makeEvent(EvInitInfo, info); e == nil {
			a.broadcast(ev)
		}
		// Persist for the next agent (potentially across restarts) so
		// the slash menu is non-empty before the lazy spawn.
		if err := a.deps.manager.store.SetLastInit(info); err != nil {
			a.deps.logger.Warn("claudeagent: SetLastInit failed", "err", err)
		}
	}
	// Flip out of "starting" — the CLI is now ready for input. (Stream
	// events from a real turn will subsequently set thinking / tool_running
	// / etc.) If the user is mid-turn we don't clobber the status, but in
	// practice EnsureClient is only entered when no client existed so
	// idle is the right resting state.
	if a.session.Status() == StatusStarting {
		a.session.SetStatus(StatusIdle)
		a.broadcastStatus(StatusIdle)
	}
	return nil
}

func (a *Agent) watchClient(cli *Client) {
	<-cli.Done()
	a.mu.Lock()
	if a.client == cli {
		a.client = nil
	}
	intentional := a.intentionalRespawn
	a.intentionalRespawn = false
	a.mu.Unlock()
	a.session.SetStatus(StatusIdle)
	a.broadcastStatus(StatusIdle)

	// Stale --resume session_id: the CLI emitted "No conversation found
	// with session ID" and exited 1. Drop the active pointer so the next
	// EnsureClient starts fresh (no --resume), surface a less-alarming
	// notice, and kick off the respawn ourselves so the user doesn't have
	// to type a message just to recover.
	if cli.InvalidResume() {
		oldID := a.session.SessionID()
		a.session.Reset()
		_ = a.deps.manager.store.ClearActive(a.deps.repoID, a.deps.branchID)
		a.deps.logger.Info("claudeagent: stored session_id is stale, dropping active pointer",
			"branch", a.deps.branchID, "stale_session", oldID)
		if ev, err := makeEvent(EvSessionReplaced, SessionReplacedPayload{OldSessionID: oldID, NewSessionID: ""}); err == nil {
			a.broadcast(ev)
		}
		a.publishEvent(EventClaudeSessionReplaced, map[string]any{
			"oldSessionId": oldID,
			"newSessionId": "",
		})
		// Kick a fresh spawn. If this also fails (e.g. auth issue) the
		// regular error path takes over.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := a.EnsureClient(ctx); err != nil {
				a.deps.logger.Warn("claudeagent: fresh spawn after stale resume failed", "err", err)
			}
		}()
		return
	}

	if err := cli.ExitErr(); err != nil && !intentional {
		a.broadcastError("Claude CLI exited", err.Error())
	}
}

func (a *Agent) handleStreamMsg(msg streamMsg) {
	if msg.Type == "system" && msg.Subtype == "init" && msg.SessionID != "" {
		// Persist the new session id for resume.
		_ = a.deps.manager.store.SetActive(a.deps.repoID, a.deps.branchID, msg.SessionID, a.session.Model())
	}
	beforeStatus := a.session.Status()
	evs := processStreamMessage(a.session, msg)
	a.broadcastMany(evs)
	// processStreamMessage flips status via session.SetStatus directly
	// (e.g. message_start → thinking, content_block_start tool_use →
	// tool_running, result → idle). Per-WS subscribers see those via the
	// EvStatusChange events returned above, but the global EventHub
	// (Drawer pip / Activity Inbox) needs an explicit publish — without
	// it, branches stay stuck on "thinking" after a turn completes.
	if afterStatus := a.session.Status(); afterStatus != beforeStatus {
		a.publishEvent(string(EventClaudeStatus), map[string]any{
			"status": string(afterStatus),
		})
	}

	if msg.Type == "result" {
		_ = a.deps.manager.store.UpdateMeta(a.session.SessionID(), func(m *SessionMeta) {
			m.LastActivityAt = time.Now().UTC()
			m.TurnCount++
			m.TotalCostUSD += msg.TotalCostUSD
		})
		// Cross-tab notification: turn-end carries cost / duration / error
		// flag so the Drawer / Inbox can react. The full per-block stream
		// stays on the WS only.
		a.publishEvent(EventClaudeTurnEnd, map[string]any{
			"isError":      msg.IsError,
			"totalCostUsd": msg.TotalCostUSD,
			"durationMs":   msg.DurationMs,
		})
		// Activity-Inbox notification — "Claude is ready". Lets the user
		// switch tabs / contexts during a long turn and come back when
		// it's done. The Inbox auto-clears as soon as the Claude tab is
		// focused, so users actively reading the conversation don't see
		// stale entries pile up.
		if !msg.IsError {
			a.publishNotification(InternalNotification{
				Type:    "info",
				Title:   "Claude is ready",
				Message: turnEndPreview(msg.Result),
			})
		}
	}
}

// turnEndPreview compresses the assistant's final reply into a short
// one-line preview for the Activity Inbox / browser notification.
func turnEndPreview(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Collapse whitespace.
	out := make([]rune, 0, len(s))
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		if r == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		out = append(out, r)
	}
	if len(out) > 100 {
		return string(out[:99]) + "…"
	}
	return string(out)
}

func (a *Agent) handleCanUseTool(_ context.Context, req canUseToolRequest, cliRequestID string) (canUseToolResponse, error) {
	if a.session.IsAllowedThisSession(req.ToolName) {
		return canUseToolResponse{Behavior: "allow"}, nil
	}
	permID, _, _ := a.session.AddPermissionRequest(cliRequestID, req.ToolName, req.Input)
	a.session.SetStatus(StatusAwaitingPermission)
	a.broadcastStatus(StatusAwaitingPermission)
	if ev, err := makeEvent(EvPermissionRequest, PermissionRequestPayload{
		PermissionID: permID,
		ToolName:     req.ToolName,
		Input:        req.Input,
	}); err == nil {
		a.broadcast(ev)
	}
	resp, err := a.awaitPermission(permID)
	a.session.SetStatus(StatusThinking)
	a.broadcastStatus(StatusThinking)
	return resp, err
}

// RequestPermission satisfies PermissionRequester. The MCP `tools/call`
// handler calls this when the CLI asks whether a tool may run; the flow
// mirrors handleCanUseTool but returns the MCP-shaped permissionResponse.
// Session-scoped allow-listing is honoured here too.
func (a *Agent) RequestPermission(_ context.Context, toolName string, input json.RawMessage, toolUseID string) (permissionResponse, error) {
	if a.session.IsAllowedThisSession(toolName) {
		return permissionResponse{Behavior: "allow"}, nil
	}
	permID, _, _ := a.session.AddPermissionRequest(toolUseID, toolName, input)
	a.session.SetStatus(StatusAwaitingPermission)
	a.broadcastStatus(StatusAwaitingPermission)
	if ev, err := makeEvent(EvPermissionRequest, PermissionRequestPayload{
		PermissionID: permID,
		ToolName:     toolName,
		Input:        input,
	}); err == nil {
		a.broadcast(ev)
	}
	// Cross-tab fan-out: Drawer / Inbox / etc.
	a.publishEvent(EventClaudePermissionRequest, map[string]any{
		"permissionId": permID,
		"toolName":     toolName,
		"input":        json.RawMessage(input),
	})
	a.publishNotification(InternalNotification{
		RequestID: permID,
		Type:      "urgent",
		Title:     "Tool permission needed",
		Message:   summariseToolForNotification(toolName, input),
		Actions: []InternalNotificationAction{
			{Label: "Allow", Action: "claude.permission.allow:" + permID},
			{Label: "Deny", Action: "claude.permission.deny:" + permID},
		},
	})
	resp, err := a.awaitPermission(permID)
	// The notification (if any) is no longer actionable — clear it so the
	// Inbox doesn't keep nagging.
	a.clearNotification(permID)
	a.publishEvent(EventClaudePermissionResolved, map[string]any{
		"permissionId": permID,
		"decision":     resp.Behavior,
	})
	// Status flips back when the next stream event arrives; if the user
	// chose deny the CLI may immediately produce a tool_result and a fresh
	// thinking pass, so we don't try to predict it here.
	a.broadcastStatus(a.session.Status())
	if err != nil {
		return permissionResponse{Behavior: "deny", Message: err.Error()}, nil
	}
	out := permissionResponse{Behavior: resp.Behavior, Message: resp.Message}
	if len(resp.UpdatedInput) > 0 {
		out.UpdatedInput = resp.UpdatedInput
	}
	return out, nil
}

// summariseToolForNotification compresses a tool name + input into one
// readable line for the Inbox. Mirrors toolSummary on the frontend.
func summariseToolForNotification(toolName string, input json.RawMessage) string {
	var obj map[string]any
	if err := json.Unmarshal(input, &obj); err != nil {
		return toolName
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := obj[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
		return ""
	}
	switch toolName {
	case "Bash":
		s := pick("command")
		if len(s) > 80 {
			s = s[:79] + "…"
		}
		return "Bash: " + s
	case "Edit", "Write", "NotebookEdit":
		return toolName + ": " + pick("file_path")
	case "Read":
		return "Read: " + pick("file_path")
	case "Glob", "Grep":
		return toolName + ": " + pick("pattern", "glob")
	case "WebFetch", "WebSearch":
		return toolName + ": " + pick("url", "query")
	case "Task":
		return "Task: " + pick("description", "subagent_type")
	}
	return toolName
}

func (a *Agent) awaitPermission(permID string) (canUseToolResponse, error) {
	a.mu.Lock()
	if a.permWaiters == nil {
		a.permWaiters = map[string]chan canUseToolResponse{}
	}
	ch, ok := a.permWaiters[permID]
	if !ok {
		ch = make(chan canUseToolResponse, 1)
		a.permWaiters[permID] = ch
	}
	a.mu.Unlock()

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(15 * time.Minute):
		// Reasonable upper bound — the user might be away from the keyboard;
		// erring on the side of patience here costs the CLI a turn.
		a.mu.Lock()
		delete(a.permWaiters, permID)
		a.mu.Unlock()
		return canUseToolResponse{Behavior: "deny", Message: "permission request timed out"}, nil
	}
}

// SendUserMessage relays text to the CLI, after lazily spawning if needed.
func (a *Agent) SendUserMessage(ctx context.Context, content string) error {
	if err := a.EnsureClient(ctx); err != nil {
		return err
	}
	a.mu.Lock()
	cli := a.client
	a.mu.Unlock()
	if cli == nil {
		return errors.New("claudeagent: client unavailable")
	}
	turnID := a.session.AppendUserTurn(content)
	if ev, err := makeEvent(EvUserMessage, UserMessagePayload{TurnID: turnID, Content: content}); err == nil {
		a.broadcast(ev)
	}
	a.session.SetStatus(StatusThinking)
	a.broadcastStatus(StatusThinking)
	return cli.SendUserMessage(content)
}

// Interrupt aborts the in-flight assistant turn.
func (a *Agent) Interrupt(ctx context.Context) error {
	a.mu.Lock()
	cli := a.client
	a.mu.Unlock()
	if cli == nil {
		return nil
	}
	return cli.Interrupt(ctx)
}

// SetModel sends a control_request to swap models.
func (a *Agent) SetModel(ctx context.Context, model string) error {
	a.mu.Lock()
	cli := a.client
	a.mu.Unlock()
	a.session.SetModel(model)
	if cli == nil {
		return nil
	}
	return cli.SetModel(ctx, model)
}

// SetEffort swaps the `--effort` level. There's no documented
// control_request for this, so we always respawn with the new flag. The
// session_id carries the conversation forward.
func (a *Agent) SetEffort(ctx context.Context, effort string) error {
	a.session.SetEffort(effort)
	a.mu.Lock()
	cli := a.client
	a.mu.Unlock()
	if cli == nil {
		return nil
	}
	a.deps.logger.Info("claudeagent: respawning CLI to apply effort", "effort", effort)
	return a.respawnClient(ctx)
}

// SetPermissionMode swaps the permission policy. The CLI's
// `set_permission_mode` control_request is best-effort — empirically the
// running CLI doesn't always honour the change for tool execution. To get
// reliable behaviour we kill and re-spawn with `--permission-mode <mode>`
// and `--resume <session_id>` so the conversation survives the swap.
func (a *Agent) SetPermissionMode(ctx context.Context, mode string) error {
	a.session.SetPermissionMode(mode)
	a.mu.Lock()
	cli := a.client
	a.mu.Unlock()
	if cli == nil {
		// No running CLI; the next spawn will pick up the new mode.
		return nil
	}
	// Try the in-band control_request first — cheaper if it works.
	if err := cli.SetPermissionMode(ctx, mode); err != nil {
		a.deps.logger.Warn("claudeagent: set_permission_mode control request failed, will respawn",
			"err", err, "mode", mode)
	}
	a.deps.logger.Info("claudeagent: respawning CLI to apply permission mode", "mode", mode)
	return a.respawnClient(ctx)
}

// respawnClient kills the current claude process (if any) and starts a new
// one. The new process resumes the existing session_id so the conversation
// continues seamlessly. Subscribers stay connected — broadcast goes through
// Agent, not Client.
//
// `intentionalRespawn` is set so watchClient knows the kill was deliberate
// and should not surface a noisy "Claude CLI exited" error event.
func (a *Agent) respawnClient(ctx context.Context) error {
	a.mu.Lock()
	if a.client != nil {
		a.intentionalRespawn = true
		a.client.Close()
		a.client = nil
	}
	a.mu.Unlock()
	return a.EnsureClient(ctx)
}

// AlwaysAllowTool writes a permission rule for `toolName` into the
// worktree's `.claude/settings.json` permissions.allow array, then resolves
// the current pending permission as allow. Future tool calls of the same
// name will skip our prompt because the CLI honours the rule itself.
func (a *Agent) AlwaysAllowTool(frame PermissionRespondFrame) error {
	pattern := a.session.ToolNameForPermission(frame.PermissionID)
	if pattern == "" {
		return errors.New("unknown permission_id")
	}
	if err := addToProjectAllowList(a.deps.worktree, pattern); err != nil {
		return err
	}
	a.session.AddSessionAllow(pattern) // also short-circuit during this session
	return a.AnswerPermission(PermissionRespondFrame{
		PermissionID: frame.PermissionID,
		Decision:     "allow",
		Scope:        "session",
		UpdatedInput: frame.UpdatedInput,
	})
}

// AnswerPermission resolves the pending permission and forwards the response
// to the CLI.
func (a *Agent) AnswerPermission(frame PermissionRespondFrame) error {
	cliRequestID, ok := a.session.ResolvePermission(frame.PermissionID, frame.Decision)
	if !ok {
		return errors.New("unknown permission_id")
	}
	if frame.Decision == "allow" && frame.Scope == "session" {
		if tool := a.session.ToolNameForPermission(frame.PermissionID); tool != "" {
			a.session.AddSessionAllow(tool)
		}
	}
	_ = cliRequestID
	resp := canUseToolResponse{Behavior: frame.Decision}
	if frame.Decision == "deny" && frame.Reason != "" {
		resp.Message = frame.Reason
	}
	if frame.Decision == "allow" && len(frame.UpdatedInput) > 0 {
		resp.UpdatedInput = frame.UpdatedInput
	}
	a.mu.Lock()
	ch, ok := a.permWaiters[frame.PermissionID]
	if ok {
		delete(a.permWaiters, frame.PermissionID)
	}
	a.mu.Unlock()
	if ok {
		ch <- resp
	}
	return nil
}

// markIntentionalKill flips intentionalRespawn so watchClient won't surface
// the "Claude CLI exited" error event for the next CLI death. Used by
// /clear and by respawnClient — anywhere we deliberately tear down.
func (a *Agent) markIntentionalKill() {
	a.mu.Lock()
	a.intentionalRespawn = true
	a.mu.Unlock()
}

// ForkSession spawns a fresh CLI with `--resume <baseId> --fork-session`
// so the new conversation starts from baseId's history but writes to a
// new session_id. UI can call this from the history popup to branch off
// a past session.
func (a *Agent) ForkSession(ctx context.Context, baseSessionID string) error {
	if baseSessionID == "" {
		return errors.New("claudeagent: empty base session id")
	}
	a.session.Reset()
	a.session.SetSessionID(baseSessionID)
	// Mark the next spawn as fork. Since EnsureClient picks Resume from
	// session.SessionID() and Fork from a flag, store the fork bit on the
	// agent and clear it after one successful spawn.
	a.mu.Lock()
	a.pendingFork = true
	a.mu.Unlock()
	if err := a.deps.manager.store.SetActive(a.deps.repoID, a.deps.branchID, baseSessionID, a.session.Model()); err != nil {
		a.deps.logger.Warn("claudeagent: SetActive on fork failed", "err", err)
	}
	if ev, err := makeEvent(EvSessionReplaced, SessionReplacedPayload{OldSessionID: baseSessionID, NewSessionID: ""}); err == nil {
		a.broadcast(ev)
	}
	a.publishEvent(EventClaudeSessionReplaced, map[string]any{
		"oldSessionId": baseSessionID,
		"newSessionId": "",
	})
	return a.respawnClient(ctx)
}

// ResumeSession swaps the active session_id to the given id, kills the
// current CLI (if any) and respawns. The CLI's `--resume <id>` reloads
// the transcript from disk so the conversation continues seamlessly.
//
// In-memory turns are wiped before respawn — they're re-emitted via
// stream events as the resumed CLI replays history; the snapshot the
// new client gets via session.init won't have local turns until the CLI
// catches up. (CLI replays differ between versions; we trust the disk.)
func (a *Agent) ResumeSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("claudeagent: empty session id")
	}
	old := a.session.Reset()
	a.session.SetSessionID(sessionID)
	if err := a.deps.manager.store.SetActive(a.deps.repoID, a.deps.branchID, sessionID, a.session.Model()); err != nil {
		a.deps.logger.Warn("claudeagent: SetActive on resume failed", "err", err)
	}
	if ev, err := makeEvent(EvSessionReplaced, SessionReplacedPayload{OldSessionID: old, NewSessionID: sessionID}); err == nil {
		a.broadcast(ev)
	}
	a.publishEvent(EventClaudeSessionReplaced, map[string]any{
		"oldSessionId": old,
		"newSessionId": sessionID,
	})
	return a.respawnClient(ctx)
}

// Clear is /clear: kill the current CLI, drop the active session pointer,
// reset in-memory state, and notify subscribers. The transcript on disk
// remains.
func (a *Agent) Clear() {
	a.mu.Lock()
	if a.client != nil {
		a.intentionalRespawn = true
		a.client.Close()
		a.client = nil
	}
	a.mu.Unlock()
	old := a.session.Reset()
	_ = a.deps.manager.store.ClearActive(a.deps.repoID, a.deps.branchID)
	if ev, err := makeEvent(EvSessionReplaced, SessionReplacedPayload{OldSessionID: old}); err == nil {
		a.broadcast(ev)
	}
	a.publishEvent(EventClaudeSessionReplaced, map[string]any{
		"oldSessionId": old,
		"newSessionId": "",
	})
	a.broadcastStatus(StatusIdle)
}

// Shutdown is the manager-driven teardown.
func (a *Agent) Shutdown() {
	a.mu.Lock()
	if a.client != nil {
		a.client.Close()
		a.client = nil
	}
	a.mu.Unlock()
}

func (a *Agent) broadcastStatus(s AgentStatus) {
	if ev, err := makeEvent(EvStatusChange, StatusChangePayload{Status: s}); err == nil {
		a.broadcast(ev)
	}
	a.publishEvent(string(EventClaudeStatus), map[string]any{
		"status": string(s),
	})
}

func (a *Agent) broadcastError(msg, detail string) {
	if ev, err := makeEvent(EvError, ErrorPayload{Message: msg, Detail: detail}); err == nil {
		a.broadcast(ev)
	}
	a.publishEvent(string(EventClaudeError), map[string]any{
		"message": msg,
		"detail":  detail,
	})
}

// publishEvent fans a branch-scoped state change out to the global
// EventHub so non-active UI (Drawer pip / Activity Inbox) can react.
func (a *Agent) publishEvent(eventType string, payload any) {
	if a.deps.manager == nil || a.deps.manager.events == nil {
		return
	}
	a.deps.manager.events.Publish(eventType, a.deps.repoID, a.deps.branchID, payload)
}

// publishNotification surfaces an actionable item in the Notify Hub for
// the Activity Inbox. Idempotent on RequestID — sink decides dedupe.
func (a *Agent) publishNotification(n InternalNotification) {
	if a.deps.manager == nil || a.deps.manager.notify == nil {
		return
	}
	a.deps.manager.notify.IngestInternal(a.deps.repoID, a.deps.branchID, n)
}

// clearNotification tells the sink to remove (or mark resolved) a
// previously-published notification. Used when a permission gets answered
// from any path so the Inbox stops showing the prompt.
func (a *Agent) clearNotification(requestID string) {
	if a.deps.manager == nil || a.deps.manager.notify == nil {
		return
	}
	a.deps.manager.notify.ClearByRequestID(a.deps.repoID, a.deps.branchID, requestID)
}

// EventType constants from the store package, redeclared here to avoid the
// import cycle. Keep in lockstep with internal/store/events.go.
const (
	EventClaudeStatus             = "claude.status"
	EventClaudePermissionRequest  = "claude.permission_request"
	EventClaudePermissionResolved = "claude.permission_resolved"
	EventClaudeError              = "claude.error"
	EventClaudeTurnEnd            = "claude.turn_end"
	EventClaudeSessionReplaced    = "claude.session_replaced"
)
