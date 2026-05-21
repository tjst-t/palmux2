package claudetui

import "sync"

// DefaultRingSize is the default ring buffer capacity (1 MiB).
const DefaultRingSize = 1 << 20

// Subscription is a live-data channel returned by [Ring.SnapshotAndSubscribe].
// The caller receives PTY output chunks on Ch; it must call [Ring.Unsubscribe]
// when done to avoid goroutine and memory leaks.
type Subscription struct {
	// Ch carries live PTY output chunks broadcast after the snapshot was taken.
	// The channel is buffered (256 slots).  Full slots silently drop that chunk
	// for this subscriber only; the ring buffer retains it.
	Ch chan []byte
	// Done is closed by [Ring.Unsubscribe] to signal the pump goroutine.
	Done chan struct{}
}

// Ring is a fixed-capacity FIFO byte ring that retains up to cap bytes of the
// most recent PTY output.  When the buffer is full, the oldest bytes are
// overwritten (tail eviction).
//
// All methods are safe for concurrent use.
//
// The key improvement over the PoC [internal/poc/pty.RingBuffer] is
// [Ring.SnapshotAndSubscribe]: it atomically captures the current contents and
// registers a live subscriber under a single lock, preventing any bytes that
// arrive between a snapshot read and a subscribe call from being lost (Fix 3).
type Ring struct {
	mu   sync.Mutex
	buf  []byte // underlying storage, always len == cap_
	head int    // next write position (wraps around)
	used int    // bytes currently stored (<= cap_)

	// subscribers is the fan-out set for live data.
	subs map[*Subscription]struct{}
}

// NewRing allocates a ring buffer with the given capacity in bytes.
// capacity must be > 0.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultRingSize
	}
	return &Ring{
		buf:  make([]byte, capacity),
		subs: make(map[*Subscription]struct{}),
	}
}

// Write appends p to the ring buffer and broadcasts the chunk to all current
// subscribers.  If p is larger than the buffer capacity only the tail
// len(p) bytes are kept.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cap_ := len(r.buf)
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	// If p exceeds buffer capacity, discard everything except the tail.
	if n >= cap_ {
		p = p[n-cap_:]
		n = cap_
		copy(r.buf, p)
		r.head = 0
		r.used = cap_
	} else {
		// Copy in two segments if we wrap around.
		first := cap_ - r.head
		if n <= first {
			copy(r.buf[r.head:], p)
		} else {
			copy(r.buf[r.head:], p[:first])
			copy(r.buf, p[first:])
		}
		r.head = (r.head + n) % cap_
		r.used += n
		if r.used > cap_ {
			r.used = cap_
		}
	}

	// Fan-out to subscribers under the same lock.
	if len(r.subs) > 0 {
		// Make a copy of p for safe broadcasting (p may be a slice of a
		// reused read buffer — callers like readLoop already copy).
		chunk := make([]byte, n)
		copy(chunk, p)
		for sub := range r.subs {
			select {
			case sub.Ch <- chunk:
			default:
				// Subscriber is lagging; drop this chunk for it only.
			}
		}
	}

	return n, nil
}

// Bytes returns a copy of the current ring buffer contents in insertion order
// (oldest → newest).  The snapshot is taken under the ring lock; it is
// consistent with respect to concurrent Write calls.
func (r *Ring) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot()
}

// snapshot returns the current contents WITHOUT acquiring the lock.
// Callers MUST hold r.mu.
func (r *Ring) snapshot() []byte {
	if r.used == 0 {
		return nil
	}
	cap_ := len(r.buf)
	out := make([]byte, r.used)
	if r.used < cap_ {
		// Buffer has never wrapped: data starts at index 0.
		copy(out, r.buf[:r.used])
	} else {
		// Buffer is full: oldest byte is at r.head.
		tail := cap_ - r.head
		copy(out, r.buf[r.head:])
		copy(out[tail:], r.buf[:r.head])
	}
	return out
}

// SnapshotAndSubscribe atomically captures the current ring contents and
// registers a live subscriber — all under the same lock.  This prevents any
// bytes that arrive between a snapshot read and a subscribe call from being
// silently lost (Fix 3 — D3 decision from S1d2278).
//
// The caller must call [Ring.Unsubscribe] on the returned [Subscription] when
// it is no longer needed.
func (r *Ring) SnapshotAndSubscribe() ([]byte, *Subscription) {
	sub := &Subscription{
		Ch:   make(chan []byte, 256),
		Done: make(chan struct{}),
	}
	r.mu.Lock()
	data := r.snapshot()
	r.subs[sub] = struct{}{}
	r.mu.Unlock()
	return data, sub
}

// Unsubscribe removes a subscriber registered via [Ring.SnapshotAndSubscribe].
// After this call the subscription's Done channel is closed; in-flight chunks
// already enqueued in Ch are still readable.
func (r *Ring) Unsubscribe(sub *Subscription) {
	r.mu.Lock()
	delete(r.subs, sub)
	r.mu.Unlock()
	close(sub.Done)
}

// Len returns the number of bytes currently stored.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.used
}
