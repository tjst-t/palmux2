// Package claudeagent — PermissionState consolidates the permission-id
// bookkeeping that was historically spread across two structs:
//
//   - Agent.permWaiters: map[permission_id] → chan canUseToolResponse
//     (the channel the MCP permission_prompt RPC blocks on while
//     awaiting the user's decision)
//   - Session.pendingPermissions: map[permission_id] → CLI request_id
//   - Session.allowList: map[tool_name] → "allow for this session"
//   - Session.askPermissions: map[permission_id] → tool_use_id (Ask)
//   - Session.planPermissions: map[permission_id] → tool_use_id (Plan)
//   - Session.hookBlocks: map[hook_id] → (turn_id, block_id)
//
// S43cfb1-6 introduces this type as the single source of truth for
// permission-id state. Behaviour is **invariant-preserving** — the
// existing Agent / Session callers continue to access their per-map
// fields directly; PermissionState is the safe, well-tested data
// structure new code should target, and the migration of the
// existing fields proceeds incrementally (see backlog: "migrate
// Agent.permWaiters / Session permission maps to PermissionState").
//
// All methods are safe for concurrent use. Callers MUST NOT touch the
// internal maps directly.
package claudeagent

import (
	"errors"
	"sync"
)

// ErrWaiterAlreadyRegistered is returned by RegisterWaiter when a
// permission_id is registered twice. Silent overwrite would leak the
// original waiter's channel; callers must explicitly re-key or
// resolve the prior waiter first.
var ErrWaiterAlreadyRegistered = errors.New("permstate: waiter already registered for this permission_id")

// PermissionState is the consolidated permission-id bookkeeping for a
// single Agent + Session pair. All fields are guarded by mu — never
// touch them outside accessor methods.
type PermissionState struct {
	mu sync.Mutex

	// waiters: permission_id → channel the MCP permission_prompt RPC
	// is blocked on. Resolved when the user clicks Allow / Deny.
	waiters map[string]chan canUseToolResponse

	// pendingByID: permission_id → CLI request_id. The control RPC
	// loop uses this to send the canUseToolResponse back to the
	// correct CLI request.
	pendingByID map[string]string

	// allowList: tool_name → "allow for this session". Skips the
	// permission_prompt entirely on subsequent calls.
	allowList map[string]struct{}

	// askByPermissionID: permission_id → tool_use_id, for
	// AskUserQuestion blocks. The view's `ask.respond` handler uses
	// this to short-circuit the generic permission resolution path.
	askByPermissionID map[string]string

	// planByPermissionID: permission_id → tool_use_id, for
	// ExitPlanMode (Plan) blocks. Same shape as askByPermissionID.
	planByPermissionID map[string]string

	// hookBlocks: hook_id → (turn_id, block_id) for kind:"hook"
	// blocks. Used to update an existing hook block when a later
	// hook_response envelope arrives.
	hookBlocks map[string]hookBlockRef
}

// NewPermissionState returns a fully-initialised PermissionState
// safe for concurrent use.
func NewPermissionState() *PermissionState {
	return &PermissionState{
		waiters:            map[string]chan canUseToolResponse{},
		pendingByID:        map[string]string{},
		allowList:          map[string]struct{}{},
		askByPermissionID:  map[string]string{},
		planByPermissionID: map[string]string{},
		hookBlocks:         map[string]hookBlockRef{},
	}
}

// ────────────────── waiter lifecycle ────────────────────────────

// RegisterWaiter installs a channel the canUseToolResponse will be
// posted to once the user makes a decision. Returns the channel.
//
// If a waiter for `permID` already exists this returns
// ErrWaiterAlreadyRegistered — silent overwrite would leak the
// existing channel. Callers MUST clear the old waiter (via
// ResolveWaiter or DropWaiter) first.
func (p *PermissionState) RegisterWaiter(permID string) (chan canUseToolResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.waiters[permID]; exists {
		return nil, ErrWaiterAlreadyRegistered
	}
	ch := make(chan canUseToolResponse, 1)
	p.waiters[permID] = ch
	return ch, nil
}

// ResolveWaiter delivers `resp` to the waiter for `permID` and
// removes the registration. Returns false when no waiter is
// registered (e.g. the agent was Closed mid-flight or the
// permission_id is unknown).
//
// Send is non-blocking: the channel is buffered (cap=1), so a single
// Resolve always succeeds. A second Resolve for the same permID
// after this call returns false (no waiter).
func (p *PermissionState) ResolveWaiter(permID string, resp canUseToolResponse) bool {
	p.mu.Lock()
	ch, ok := p.waiters[permID]
	if ok {
		delete(p.waiters, permID)
	}
	p.mu.Unlock()
	if !ok {
		return false
	}
	// Non-blocking send because cap=1 + we're the only sender per
	// permID (RegisterWaiter rejects duplicates).
	select {
	case ch <- resp:
	default:
		// Should not happen with single-sender + cap=1, but handle
		// gracefully so a lock-step bug elsewhere can't deadlock us.
	}
	return true
}

// DropWaiter removes a waiter without delivering a response. Used
// when the agent is being torn down — the channel goes unblocked,
// causing the caller's recv to return the zero value (default
// "deny" semantics in the generic recv path).
func (p *PermissionState) DropWaiter(permID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.waiters, permID)
}

// HasWaiter returns true when a waiter is currently registered.
// Used by tests + diagnostics.
func (p *PermissionState) HasWaiter(permID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.waiters[permID]
	return ok
}

// WaiterCount returns the number of currently-registered waiters.
// Used by tests + leak diagnostics.
func (p *PermissionState) WaiterCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.waiters)
}

// ────────────────── pending request lookup ──────────────────────

// SetPending records the CLI request_id that originated `permID`.
// Overwrites any prior value (re-asking for the same permission is
// supported — the prior request was already drained or abandoned).
func (p *PermissionState) SetPending(permID, cliRequestID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pendingByID[permID] = cliRequestID
}

// TakePending returns the CLI request_id for `permID` and removes
// it from the map. Returns ("", false) when no pending entry exists.
func (p *PermissionState) TakePending(permID string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.pendingByID[permID]
	if ok {
		delete(p.pendingByID, permID)
	}
	return v, ok
}

// ────────────────── allow list ──────────────────────────────────

// AddAllow marks `toolName` as "allow for this session" so subsequent
// calls skip the permission_prompt round-trip.
func (p *PermissionState) AddAllow(toolName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowList[toolName] = struct{}{}
}

// IsAllowed returns true when `toolName` was previously AddAllow'd.
func (p *PermissionState) IsAllowed(toolName string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.allowList[toolName]
	return ok
}

// ────────────────── ask / plan permission tracking ──────────────

func (p *PermissionState) RegisterAsk(permID, toolUseID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.askByPermissionID[permID] = toolUseID
}

func (p *PermissionState) TakeAsk(permID string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.askByPermissionID[permID]
	if ok {
		delete(p.askByPermissionID, permID)
	}
	return v, ok
}

func (p *PermissionState) HasAsk(permID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.askByPermissionID[permID]
	return ok
}

func (p *PermissionState) RegisterPlan(permID, toolUseID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.planByPermissionID[permID] = toolUseID
}

func (p *PermissionState) TakePlan(permID string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.planByPermissionID[permID]
	if ok {
		delete(p.planByPermissionID, permID)
	}
	return v, ok
}

func (p *PermissionState) HasPlan(permID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.planByPermissionID[permID]
	return ok
}

// ────────────────── hook block registry ─────────────────────────

func (p *PermissionState) RegisterHook(hookID string, ref hookBlockRef) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hookBlocks[hookID] = ref
}

func (p *PermissionState) LookupHook(hookID string) (hookBlockRef, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ref, ok := p.hookBlocks[hookID]
	return ref, ok
}

func (p *PermissionState) DropHook(hookID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.hookBlocks, hookID)
}

// ────────────────── tear-down ───────────────────────────────────

// Reset clears all per-session permission state. Used on session
// swap / fork / clear so the new session doesn't inherit the prior
// session's allow list or pending requests. Pending waiters are
// dropped — they will recv the zero value (caller's recv interprets
// as deny).
func (p *PermissionState) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waiters = map[string]chan canUseToolResponse{}
	p.pendingByID = map[string]string{}
	p.allowList = map[string]struct{}{}
	p.askByPermissionID = map[string]string{}
	p.planByPermissionID = map[string]string{}
	p.hookBlocks = map[string]hookBlockRef{}
}

// CloseAndDropAllWaiters drains every registered waiter without
// delivering a response. Used at agent teardown so RegisterWaiter
// callers can unblock and return.
func (p *PermissionState) CloseAndDropAllWaiters() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waiters = map[string]chan canUseToolResponse{}
}
