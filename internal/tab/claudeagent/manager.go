package claudeagent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// BranchResolver yields the on-disk worktree path for a branch. Implemented
// by *store.Store; passed in as an interface so this package can stay free of
// store imports.
type BranchResolver interface {
	WorktreePath(repoID, branchID string) (string, error)
}

// Config bundles long-lived settings the Manager needs.
type Config struct {
	Binary             string
	DefaultModel       string
	DefaultPermissionMode string
	ExtraArgs          []string
}

// Manager owns one Agent per (repoID, branchID). It is the single entry
// point used by the WS handler and the REST handlers.
type Manager struct {
	cfg     Config
	store   *Store
	branches BranchResolver
	logger  *slog.Logger

	mu     sync.Mutex
	agents map[string]*Agent
}

// NewManager constructs a Manager. `cfg.Binary == ""` falls back to "claude".
func NewManager(cfg Config, store *Store, branches BranchResolver, logger *slog.Logger) *Manager {
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

	cli, err := NewClient(ctx, ClientOptions{
		Binary:         a.deps.manager.cfg.Binary,
		Cwd:            a.deps.worktree,
		SessionID:      resumeID,
		Model:          model,
		PermissionMode: a.session.PermissionMode(),
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
	if err := cli.Initialize(initCtx); err != nil {
		a.deps.logger.Warn("claudeagent: initialize failed", "err", err)
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
	if err := cli.ExitErr(); err != nil && !intentional {
		a.broadcastError("Claude CLI exited", err.Error())
	}
}

func (a *Agent) handleStreamMsg(msg streamMsg) {
	if msg.Type == "system" && msg.Subtype == "init" && msg.SessionID != "" {
		// Persist the new session id for resume.
		_ = a.deps.manager.store.SetActive(a.deps.repoID, a.deps.branchID, msg.SessionID, a.session.Model())
	}
	evs := processStreamMessage(a.session, msg)
	a.broadcastMany(evs)

	if msg.Type == "result" {
		_ = a.deps.manager.store.UpdateMeta(a.session.SessionID(), func(m *SessionMeta) {
			m.LastActivityAt = time.Now().UTC()
			m.TurnCount++
			m.TotalCostUSD += msg.TotalCostUSD
		})
	}
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
	resp, err := a.awaitPermission(permID)
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
}

func (a *Agent) broadcastError(msg, detail string) {
	if ev, err := makeEvent(EvError, ErrorPayload{Message: msg, Detail: detail}); err == nil {
		a.broadcast(ev)
	}
}
