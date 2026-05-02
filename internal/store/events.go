package store

import (
	"sync"
	"sync/atomic"
)

// EventType enumerates all event names emitted by the Store. Frontend
// subscribers (Phase 3 onward) match on these strings.
type EventType string

const (
	EventRepoOpened    EventType = "repo.opened"
	EventRepoClosed    EventType = "repo.closed"
	EventRepoStarred   EventType = "repo.starred"
	EventRepoUnstarred EventType = "repo.unstarred"

	EventBranchOpened EventType = "branch.opened"
	EventBranchClosed EventType = "branch.closed"
	// S015: emitted when a branch's drawer category changes (currently
	// only my ↔ unmanaged via promote/demote; subagent is derived from
	// path patterns and changes implicitly with settings updates). Payload
	// is `{ category: "user" | "unmanaged" | "subagent" }`.
	EventBranchCategoryChanged EventType = "branch.categoryChanged"

	EventTabAdded     EventType = "tab.added"
	EventTabRemoved   EventType = "tab.removed"
	EventTabRenamed   EventType = "tab.renamed"
	EventTabReordered EventType = "tab.reordered" // S020 — payload `{order: TabID[]}`

	EventNotification EventType = "notification"
	EventSettings     EventType = "settings.updated"

	// Claude tab events. Carry per-branch state changes so non-active UI
	// (Drawer pip, Activity Inbox) can react in real time. The payloads
	// are claude-tab specific; consumers filter by Type.
	EventClaudeStatus             EventType = "claude.status"
	EventClaudePermissionRequest  EventType = "claude.permission_request"
	EventClaudePermissionResolved EventType = "claude.permission_resolved"
	EventClaudeError              EventType = "claude.error"
	EventClaudeTurnEnd            EventType = "claude.turn_end"
	EventClaudeSessionReplaced    EventType = "claude.session_replaced"

	// Git tab events (S012). Fired by the per-branch worktree watcher
	// (debounced 1s) and by direct write endpoints (commit, pull, branch
	// switch) so connected browsers refresh the status view in real time.
	EventGitStatusChanged    EventType = "git.statusChanged"
	EventGitCredentialPrompt EventType = "git.credentialRequest"

	// Sprint Dashboard events (S016). Fired when the per-branch
	// worktreewatch sees a change to docs/ROADMAP.md, docs/sprint-logs/*
	// or .claude/autopilot-*.lock. The payload is
	// `{ files: [paths], scopes: ["overview"|"sprintDetail"|"dependencies"|"decisions"|"refine"] }`
	// so the FE can refetch only the affected views.
	EventSprintChanged EventType = "sprint.changed"
)

// Event is one broadcastable change.
type Event struct {
	Type     EventType `json:"type"`
	RepoID   string    `json:"repoId,omitempty"`
	BranchID string    `json:"branchId,omitempty"`
	TabID    string    `json:"tabId,omitempty"`
	Payload  any       `json:"payload,omitempty"`
}

// EventHub is a fan-out broadcaster. Each subscriber receives every event on
// its own buffered channel. Slow subscribers cause oldest events to drop —
// the design assumption is that clients re-fetch full state via REST after
// a reconnect, so transient losses are recoverable.
type EventHub struct {
	mu          sync.Mutex
	subscribers map[uint64]chan Event
	nextID      uint64
}

// NewEventHub returns a ready-to-use hub.
func NewEventHub() *EventHub {
	return &EventHub{subscribers: map[uint64]chan Event{}}
}

// Subscribe registers a new subscriber. Returns its channel and an
// unsubscribe function the caller must invoke when done.
func (h *EventHub) Subscribe() (<-chan Event, func()) {
	id := atomic.AddUint64(&h.nextID, 1)
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.subscribers[id] = ch
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if c, ok := h.subscribers[id]; ok {
			delete(h.subscribers, id)
			close(c)
		}
		h.mu.Unlock()
	}
}

// Publish fan-outs the event to every subscriber, dropping events on
// subscribers whose channel is full (oldest-drop semantics).
func (h *EventHub) Publish(evt Event) {
	h.mu.Lock()
	subs := make([]chan Event, 0, len(h.subscribers))
	for _, c := range h.subscribers {
		subs = append(subs, c)
	}
	h.mu.Unlock()
	for _, c := range subs {
		select {
		case c <- evt:
		default:
			// Drop oldest, push newest.
			select {
			case <-c:
			default:
			}
			select {
			case c <- evt:
			default:
			}
		}
	}
}

// SubscriberCount returns the current subscriber count (test/diagnostics).
func (h *EventHub) SubscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subscribers)
}
