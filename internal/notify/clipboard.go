package notify

import "time"

// CopyEvent is emitted when a terminal subprocess sends an OSC 52
// clipboard-write sequence.  Subscribers on the notify hub can act on this
// event (e.g. forward the text to a connected browser via the event WebSocket).
//
// This event is distinct from the generic Notification pipeline: it is
// lightweight, carries no unread count, and is never stored in BranchState.
// Story 2 will expose it over the grid WebSocket; for now it is observable to
// any Hub subscriber via Hub.SubscribeCopy.
type CopyEvent struct {
	// Text is the UTF-8 decoded clipboard payload.
	Text string
	// RepoID and BranchID identify the workspace the event originated from.
	RepoID   string
	BranchID string
	// At is the wall-clock time the OSC 52 sequence was parsed.
	At time.Time
}

// copySubscriber is one subscription registered by [Hub.SubscribeCopy].
type copySubscriber struct {
	ch   chan CopyEvent
	done chan struct{}
}

// SubscribeCopy returns a channel that receives [CopyEvent] values published
// via [Hub.PublishCopy].  The caller must call the returned cancel function
// when it no longer needs events; this prevents goroutine and channel leaks.
//
// The returned channel is buffered (64 slots).  A slow consumer causes events
// to be dropped silently — the same drop policy used by the Ring subscriber.
func (h *Hub) SubscribeCopy() (<-chan CopyEvent, func()) {
	sub := &copySubscriber{
		ch:   make(chan CopyEvent, 64),
		done: make(chan struct{}),
	}
	h.copyMu.Lock()
	h.copySubs = append(h.copySubs, sub)
	h.copyMu.Unlock()
	cancel := func() {
		h.copyMu.Lock()
		for i, s := range h.copySubs {
			if s == sub {
				h.copySubs = append(h.copySubs[:i], h.copySubs[i+1:]...)
				break
			}
		}
		h.copyMu.Unlock()
		close(sub.done)
	}
	return sub.ch, cancel
}

// PublishCopy broadcasts ev to all current [SubscribeCopy] subscribers.
// Slow subscribers are skipped (drop policy: never block the PTY read loop).
// PublishCopy is safe for concurrent use.
func (h *Hub) PublishCopy(ev CopyEvent) {
	h.copyMu.RLock()
	subs := h.copySubs
	h.copyMu.RUnlock()
	for _, sub := range subs {
		select {
		case sub.ch <- ev:
		default:
			// Subscriber is lagging; drop this event for it.
		}
	}
}
