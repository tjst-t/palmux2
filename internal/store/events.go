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
	// S1e8d02: emitted when sync_worktree observes that the same worktree
	// path now points at a different branch (= the user ran `git checkout`
	// in-place). This is the path-identity domain event that REPLACES the
	// old `branch.closed + branch.opened` pair for in-place rename. The
	// payload is `{ oldBranch, newBranch, worktreePath }`. The branch's
	// ID, tmux session, Claude agent, tab list and Drawer position all
	// remain unchanged — only the display label updates.
	EventBranchHeadChanged EventType = "branch.head_changed"
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

	// Worktree lifecycle events (S021). Fired when stale subagent
	// worktrees are removed via the cleanup endpoint. The payload is the
	// full `SubagentCleanupResult` so other clients can refresh both
	// their Drawer and any in-progress cleanup dialogs. There is no
	// separate `branch.promoted` event because the existing
	// `branch.categoryChanged` already carries the after-promote
	// category.
	EventWorktreeCleaned EventType = "worktree.cleaned"

	// S023: emitted when a repo's `last_active_branch` is mutated. The
	// payload is `{ branch: "<name>" }` (empty string means "cleared").
	// Other clients use this to refresh their Drawer's collapsed-repo
	// header click target without forcing a full /api/repos refetch.
	EventBranchLastActiveChanged EventType = "branch.lastActiveChanged"

	// S8478ca-5: emitted when a workspace's runtime view changes (kind,
	// state, address, error). Payload is the RuntimeView object. The FE
	// uses this to update the header chip and drawer badge in real time
	// without a full /api/repos reload.
	EventBranchRuntimeChanged EventType = "branch.runtimeChanged"

	// S8478ca-4: emitted when the port-scan loop detects the current set of
	// listening ports inside an incus-container workspace. Payload is
	// incus.PortsDetectedEvent {inst, ports:[{port,proto}]}. The FE uses
	// this to surface per-workspace service URLs without polling.
	EventBranchPortsDetected EventType = "branch.portsDetected"

	// See8bd4-3: emitted by the port-scan loop with the full user-facing
	// Ports view (listening ports + exposure state). Payload is
	// `{runtimeKind, ports: []runtime.PortView}`. The Ports tab subscribes to
	// this to refresh its list and published-URL state without polling.
	EventBranchPortsChanged EventType = "branch.portsChanged"

	// S7364e3: emitted when the scan loop detects a change in a workspace
	// container's image-drift status (stale ⇄ fresh). Payload is
	// `{stale: bool}`. The FE updates the "update available" badge on the
	// header runtime chip and drawer entry without a full /api/repos reload.
	EventBranchRuntimeDrift EventType = "branch.runtimeDrift"

	// S6ab0ed: emitted by the self-update poller when the set of
	// update-available managed components transitions. Payload is the full
	// selfupdate.Snapshot (components current→latest + overall available +
	// nixManaged). The FE drives the top-right "更新あり" badge + update panel
	// from this (app-global, like settings.updated — no repo/branch fields).
	EventAppUpdateAvailable EventType = "app.updateAvailable"
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
