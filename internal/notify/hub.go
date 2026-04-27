// Package notify is the in-memory hub for Claude Code notifications. Hooks
// (e.g. Stop, Notification events) POST a payload here; the hub stores the
// most recent message + unread count per branch and republishes through the
// store's EventHub so connected browsers see badges instantly.
package notify

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// Notification is one inbox entry.
type Notification struct {
	Type      string               `json:"type"`
	Message   string               `json:"message,omitempty"`
	Title     string               `json:"title,omitempty"`
	Detail    string               `json:"detail,omitempty"`
	CreatedAt time.Time            `json:"createdAt"`
	// RequestID is set by IngestInternal so the Inbox can dedupe on
	// republish and so ClearByRequestID can target a specific entry.
	RequestID string               `json:"requestId,omitempty"`
	Actions   []NotificationAction `json:"actions,omitempty"`
	// Resolved flips true when ClearByRequestID is called. The Inbox
	// hides resolved entries by default but can show them on demand.
	Resolved bool `json:"resolved,omitempty"`
}

// NotificationAction is one inline button on a notification.
type NotificationAction struct {
	Label  string `json:"label"`
	Action string `json:"action"`
}

// BranchState aggregates the last-N notifications + an unread count for a
// single branch. Unread resets to zero when the user focuses the Claude tab
// (frontend calls Clear).
type BranchState struct {
	UnreadCount   int            `json:"unreadCount"`
	LastMessage   string         `json:"lastMessage,omitempty"`
	LastType      string         `json:"lastType,omitempty"`
	LastAt        time.Time      `json:"lastAt,omitzero"`
	Notifications []Notification `json:"notifications"`
}

// Publisher is the subset of *store.EventHub we need. Defined as an interface
// so the package doesn't import store and create a cycle.
type Publisher interface {
	Publish(eventType string, repoID, branchID string, payload any)
}

// SessionResolver maps a tmux session name back to {repoId, branchId}.
// notify never imports the Store directly — main.go wires this in.
type SessionResolver func(sessionName string) (repoID, branchID string, ok bool)

// Hub holds notifications keyed by "{repoId}/{branchId}" plus by tmux session
// (for inbound POSTs that only know the session). Maximum 50 notifications
// per branch.
type Hub struct {
	mu       sync.RWMutex
	byBranch map[string]*BranchState

	resolver  SessionResolver
	publisher Publisher
}

// New returns a Hub with the given resolver + publisher. Either may be nil
// during tests; the hub then becomes a passive store.
func New(resolver SessionResolver, publisher Publisher) *Hub {
	return &Hub{
		byBranch:  map[string]*BranchState{},
		resolver:  resolver,
		publisher: publisher,
	}
}

const maxPerBranch = 50

// IngestRequest is the payload accepted by POST /api/notify.
type IngestRequest struct {
	TmuxSession string `json:"tmuxSession"`
	RepoID      string `json:"repoId,omitempty"`
	BranchID    string `json:"branchId,omitempty"`
	Type        string `json:"type"`
	Message     string `json:"message,omitempty"`
	Title       string `json:"title,omitempty"`
}

// Ingest records a notification. Returns the resolved repoID/branchID or an
// error. If both TmuxSession and (RepoID,BranchID) are missing, fails.
func (h *Hub) Ingest(req IngestRequest) (string, string, error) {
	repoID, branchID := strings.TrimSpace(req.RepoID), strings.TrimSpace(req.BranchID)
	if repoID == "" || branchID == "" {
		if req.TmuxSession == "" {
			return "", "", errors.New("notify: must include tmuxSession or repoId+branchId")
		}
		if h.resolver == nil {
			return "", "", errors.New("notify: no session resolver configured")
		}
		var ok bool
		repoID, branchID, ok = h.resolver(req.TmuxSession)
		if !ok {
			return "", "", errors.New("notify: unknown tmux session: " + req.TmuxSession)
		}
	}
	t := strings.TrimSpace(req.Type)
	if t == "" {
		t = "info"
	}
	n := Notification{
		Type:      t,
		Message:   req.Message,
		Title:     req.Title,
		CreatedAt: time.Now().UTC(),
	}

	key := repoID + "/" + branchID
	h.mu.Lock()
	state, ok := h.byBranch[key]
	if !ok {
		state = &BranchState{}
		h.byBranch[key] = state
	}
	state.Notifications = append(state.Notifications, n)
	if len(state.Notifications) > maxPerBranch {
		state.Notifications = state.Notifications[len(state.Notifications)-maxPerBranch:]
	}
	state.UnreadCount++
	state.LastMessage = n.Message
	state.LastType = n.Type
	state.LastAt = n.CreatedAt
	snapshot := *state
	h.mu.Unlock()

	if h.publisher != nil {
		h.publisher.Publish("notification", repoID, branchID, snapshot)
	}
	return repoID, branchID, nil
}

// InternalRequest is the in-process publish path used by Claude tab
// (permission requests, errors, turn-end summaries). Skips the
// session-resolver step since the caller already knows {repoID, branchID}.
type InternalRequest struct {
	RequestID string
	Type      string // "urgent" | "warning" | "info"
	Title     string
	Message   string
	Detail    string
	Actions   []NotificationAction
}

// IngestInternal records a notification that originates from inside the
// process (rather than via /api/notify POST). If RequestID matches an
// existing notification on the same branch, that entry is updated in place
// rather than duplicated — useful when permission requests get re-emitted
// during reconnects.
func (h *Hub) IngestInternal(repoID, branchID string, req InternalRequest) {
	if repoID == "" || branchID == "" {
		return
	}
	t := strings.TrimSpace(req.Type)
	if t == "" {
		t = "info"
	}
	n := Notification{
		Type:      t,
		Message:   req.Message,
		Title:     req.Title,
		Detail:    req.Detail,
		CreatedAt: time.Now().UTC(),
		RequestID: req.RequestID,
		Actions:   req.Actions,
	}

	key := repoID + "/" + branchID
	h.mu.Lock()
	state, ok := h.byBranch[key]
	if !ok {
		state = &BranchState{}
		h.byBranch[key] = state
	}
	if req.RequestID != "" {
		// Dedupe / replace by requestID.
		for i := range state.Notifications {
			if state.Notifications[i].RequestID == req.RequestID && !state.Notifications[i].Resolved {
				state.Notifications[i] = n
				state.LastMessage = n.Message
				state.LastType = n.Type
				state.LastAt = n.CreatedAt
				snapshot := *state
				h.mu.Unlock()
				if h.publisher != nil {
					h.publisher.Publish("notification", repoID, branchID, snapshot)
				}
				return
			}
		}
	}
	state.Notifications = append(state.Notifications, n)
	if len(state.Notifications) > maxPerBranch {
		state.Notifications = state.Notifications[len(state.Notifications)-maxPerBranch:]
	}
	state.UnreadCount++
	state.LastMessage = n.Message
	state.LastType = n.Type
	state.LastAt = n.CreatedAt
	snapshot := *state
	h.mu.Unlock()

	if h.publisher != nil {
		h.publisher.Publish("notification", repoID, branchID, snapshot)
	}
}

// ClearByRequestID flips the matching notification's Resolved flag and
// re-broadcasts. Used when a permission request gets answered (from any
// path) so the Inbox stops showing the prompt.
func (h *Hub) ClearByRequestID(repoID, branchID, requestID string) {
	if requestID == "" {
		return
	}
	key := repoID + "/" + branchID
	h.mu.Lock()
	state, ok := h.byBranch[key]
	if !ok {
		h.mu.Unlock()
		return
	}
	changed := false
	for i := range state.Notifications {
		if state.Notifications[i].RequestID == requestID && !state.Notifications[i].Resolved {
			state.Notifications[i].Resolved = true
			state.Notifications[i].Actions = nil
			changed = true
		}
	}
	if !changed {
		h.mu.Unlock()
		return
	}
	snapshot := *state
	h.mu.Unlock()
	if h.publisher != nil {
		h.publisher.Publish("notification", repoID, branchID, snapshot)
	}
}

// Clear resets the unread count for a branch. Returns the post-clear state.
func (h *Hub) Clear(repoID, branchID string) BranchState {
	key := repoID + "/" + branchID
	h.mu.Lock()
	state, ok := h.byBranch[key]
	if !ok {
		state = &BranchState{}
		h.byBranch[key] = state
	}
	state.UnreadCount = 0
	snapshot := *state
	h.mu.Unlock()
	if h.publisher != nil {
		h.publisher.Publish("notification.cleared", repoID, branchID, snapshot)
	}
	return snapshot
}

// Snapshot returns the state for a branch (zero-value if none).
func (h *Hub) Snapshot(repoID, branchID string) BranchState {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if s, ok := h.byBranch[repoID+"/"+branchID]; ok {
		return *s
	}
	return BranchState{}
}

// All returns a map keyed "{repoId}/{branchId}" of every known branch state.
func (h *Hub) All() map[string]BranchState {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]BranchState, len(h.byBranch))
	for k, v := range h.byBranch {
		out[k] = *v
	}
	return out
}
