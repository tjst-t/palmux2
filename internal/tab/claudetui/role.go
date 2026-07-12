// Package claudetui provides the Claude TUI tab implementation.
package claudetui

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Role constants identify a subscriber's current role.  Use these instead of
// magic strings throughout the package (priority_rule 7).
const (
	// RoleActive is the role of the subscriber that can send input to the PTY.
	RoleActive = "active"
	// RoleViewer is the role of any subscriber that is not currently active.
	RoleViewer = "viewer"
)

// RoleEvent is the JSON text frame sent to all subscribers when the active
// subscriber changes.  It is emitted on both raw-mode and grid-mode connections
// as a text frame (never binary), regardless of WS mode.
//
// Wire shape:
//
//	{"type":"role","role":"active"|"viewer","since":<unix-millis>}
type RoleEvent struct {
	Type  string `json:"type"`  // always "role"
	Role  string `json:"role"`  // RoleActive or RoleViewer
	Since int64  `json:"since"` // unix milliseconds
}

// subscriber represents a single connected WebSocket client.
// It carries a monotonic ID, the current role, and a channel for out-of-band
// control events (role transitions) that need to be delivered to the client
// irrespective of the primary PTY-output channel.
type subscriber struct {
	// id is a monotonically increasing per-Daemon counter assigned at subscribe
	// time.  It uniquely identifies this client within a single Daemon lifetime.
	id int64

	// roleCh delivers RoleEvent JSON (text frames) to the pump goroutine for
	// this subscriber.  It is buffered so the broadcaster never blocks.
	roleCh chan []byte

	// done is closed when this subscriber disconnects (mirrors the ioCtx Done
	// signal but available without a context reference).
	done chan struct{}
}

// newSubscriber allocates a subscriber with the given id.
func newSubscriber(id int64) *subscriber {
	return &subscriber{
		id:     id,
		roleCh: make(chan []byte, 8),
		done:   make(chan struct{}),
	}
}

// close signals that this subscriber has disconnected.
func (s *subscriber) close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// sendRole enqueues a JSON-encoded RoleEvent on the subscriber's roleCh.
// It is non-blocking: if the channel is full the event is silently dropped.
// This should be extremely rare in practice given the small buffer size and the
// low frequency of role events.
func (s *subscriber) sendRole(ev []byte) {
	select {
	case s.roleCh <- ev:
	default:
		// Subscriber is slow; drop this role event rather than blocking.
	}
}

// ─── Role coordinator ─────────────────────────────────────────────────────────

// roleCoordinator manages multi-client role assignment for a single Daemon.
// It is embedded in Daemon and provides the OnSubscribe / OnUnsubscribe /
// TakeActive methods required by the AttachHandler.
//
// Invariants:
//   - At most one subscriber is active at a time.
//   - activeID == 0 means no active subscriber (idle).
//   - A newly connecting client gets active if the list was empty; otherwise
//     it gets viewer.
//   - When the active disconnects, the first remaining subscriber is promoted.
//   - When any client sends input, TakeActive transfers the active role to it.
type roleCoordinator struct {
	mu       sync.Mutex
	subs     []*subscriber
	nextID   int64 // protected by mu
	activeID atomic.Int64
	logger   *slog.Logger
}

// newRoleCoordinator allocates a roleCoordinator.
func newRoleCoordinator(logger *slog.Logger) *roleCoordinator {
	if logger == nil {
		logger = slog.Default()
	}
	return &roleCoordinator{logger: logger}
}

// OnSubscribe registers a new subscriber and returns its initial role.
// If the coordinator has no active subscriber, this one becomes active;
// otherwise it becomes a viewer.
//
// The initial role event is NOT sent here — the caller is responsible for
// sending it after any connection preamble (grid.init / ring replay) so the
// client can correlate its role with the initial state in one read pass.
func (rc *roleCoordinator) OnSubscribe(sub *subscriber) string {
	rc.mu.Lock()
	rc.nextID++
	sub.id = rc.nextID
	rc.subs = append(rc.subs, sub)
	currentActive := rc.activeID.Load()
	var role string
	if currentActive == 0 {
		rc.activeID.Store(sub.id)
		role = RoleActive
	} else {
		role = RoleViewer
	}
	// Capture the count under the lock — rc.subs may be concurrently
	// mutated (e.g. a racing OnUnsubscribe reassigning the slice header)
	// the instant we unlock, so reading len(rc.subs) below would be an
	// unsynchronized, data-raced read of that header (AC-S64c835-1-2).
	total := len(rc.subs)
	rc.mu.Unlock()

	rc.logger.Debug("claudetui: subscriber joined",
		"sub_id", sub.id,
		"role", role,
		"total", total,
	)
	return role
}

// OnUnsubscribe removes a subscriber.  If the departing subscriber was active,
// the first remaining subscriber is promoted and a role event is broadcast.
func (rc *roleCoordinator) OnUnsubscribe(sub *subscriber) {
	rc.mu.Lock()
	// Remove from list.
	newSubs := rc.subs[:0]
	for _, s := range rc.subs {
		if s.id != sub.id {
			newSubs = append(newSubs, s)
		}
	}
	rc.subs = newSubs

	wasActive := rc.activeID.Load() == sub.id
	var promoted *subscriber
	if wasActive {
		if len(rc.subs) > 0 {
			promoted = rc.subs[0]
			rc.activeID.Store(promoted.id)
		} else {
			rc.activeID.Store(0)
		}
	}
	// Capture the count under the lock, same rationale as OnSubscribe above
	// — this was the exact data race reported for AC-S64c835-1-2: two
	// concurrent OnUnsubscribe calls (one client's write at "rc.subs =
	// newSubs" racing another's unguarded len(rc.subs) read here after
	// Unlock).
	remaining := len(rc.subs)
	rc.mu.Unlock()

	rc.logger.Debug("claudetui: subscriber left",
		"sub_id", sub.id,
		"was_active", wasActive,
		"remaining", remaining,
	)

	if promoted != nil {
		// Broadcast the promotion to all remaining subscribers.
		rc.broadcastRole(promoted)
	}
}

// TakeActive promotes sub to active if it is not already.
// If sub was a viewer and becomes active, the previous active subscriber
// (if any) gets a viewer role event and sub gets an active role event.
//
// Returns true if the role changed (i.e., sub was a viewer).
func (rc *roleCoordinator) TakeActive(sub *subscriber) bool {
	if rc.activeID.Load() == sub.id {
		return false // already active; nothing to do
	}

	rc.mu.Lock()
	prev := rc.activeID.Load()
	if prev == sub.id {
		rc.mu.Unlock()
		return false
	}
	rc.activeID.Store(sub.id)
	// Collect all current subs for broadcast (snapshot under lock).
	snapshot := make([]*subscriber, len(rc.subs))
	copy(snapshot, rc.subs)
	rc.mu.Unlock()

	rc.logger.Debug("claudetui: active transferred",
		"from_id", prev,
		"to_id", sub.id,
	)

	// Build and send role events to each subscriber.
	ts := time.Now().UnixMilli()
	for _, s := range snapshot {
		var role string
		if s.id == sub.id {
			role = RoleActive
		} else {
			role = RoleViewer
		}
		ev, err := json.Marshal(RoleEvent{
			Type:  "role",
			Role:  role,
			Since: ts,
		})
		if err != nil {
			rc.logger.Error("claudetui: marshal role event", "err", err)
			continue
		}
		s.sendRole(ev)
	}
	return true
}

// broadcastRole sends a RoleActive event to the promoted subscriber and a
// RoleViewer event to all others.  Called under no locks.
func (rc *roleCoordinator) broadcastRole(promoted *subscriber) {
	rc.mu.Lock()
	snapshot := make([]*subscriber, len(rc.subs))
	copy(snapshot, rc.subs)
	rc.mu.Unlock()

	ts := time.Now().UnixMilli()
	for _, s := range snapshot {
		var role string
		if s.id == promoted.id {
			role = RoleActive
		} else {
			role = RoleViewer
		}
		ev, err := json.Marshal(RoleEvent{
			Type:  "role",
			Role:  role,
			Since: ts,
		})
		if err != nil {
			rc.logger.Error("claudetui: marshal role event for broadcast", "err", err)
			continue
		}
		s.sendRole(ev)
	}
}

// initialRoleEvent builds a JSON RoleEvent for the given role.
func initialRoleEvent(role string) ([]byte, error) {
	return json.Marshal(RoleEvent{
		Type:  "role",
		Role:  role,
		Since: time.Now().UnixMilli(),
	})
}

// sendRoleFrame writes a role JSON text frame on conn.
// It is safe to call from any goroutine; errors are logged and swallowed.
func sendRoleFrame(ctx context.Context, conn *websocket.Conn, ev []byte) error {
	return conn.Write(ctx, websocket.MessageText, ev)
}
