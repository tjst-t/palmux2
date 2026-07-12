package ptyhost

import "sync"

// DefaultRingSize is the default ring buffer capacity (1 MiB).
const DefaultRingSize = 1 << 20

// Chunk is one piece of live data delivered to a [Subscription]. Offset is
// the absolute offset (since the ring was created) of Data[0].
type Chunk struct {
	Offset int64
	Data   []byte
}

// Subscription is a live-data channel returned by [Ring.SnapshotAndSubscribe].
// The caller receives ring output on Ch; it must call [Ring.Unsubscribe] when
// done to avoid goroutine and memory leaks.
type Subscription struct {
	// Ch carries chunks broadcast after the snapshot was taken. Buffered (256
	// slots); a full channel silently drops that chunk for this subscriber
	// only — the ring buffer itself still retains it up to its capacity.
	Ch chan Chunk
	// Done is closed by [Ring.Unsubscribe] to signal any pump goroutine
	// draining Ch that it should stop.
	Done chan struct{}
}

// Ring is a fixed-capacity FIFO byte ring that retains up to capacity bytes
// of the most recent output, while tracking an ABSOLUTE, monotonically
// increasing offset for every byte ever written. Offsets are never reused —
// once a byte is evicted by wrap-around, its offset simply becomes
// unavailable for replay (below the ring's current "oldest offset"); it is
// never reassigned to a different byte.
//
// This is the ptyhost-side analogue of internal/tab/claudetui.Ring, extended
// with absolute-offset replay so a reconnecting palmux2 client can resume
// from wherever it left off (see docs/no-halt-agent-design.md §2/§3).
//
// All methods are safe for concurrent use.
type Ring struct {
	mu   sync.Mutex
	buf  []byte // underlying storage, always len == cap
	head int    // next write position (wraps around)
	used int    // bytes currently stored (<= cap)

	total int64 // total bytes ever written; also the "next write" absolute offset

	subs map[*Subscription]struct{}
}

// NewRing allocates a ring buffer with the given capacity in bytes.
// A non-positive capacity falls back to [DefaultRingSize].
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultRingSize
	}
	return &Ring{
		buf:  make([]byte, capacity),
		subs: make(map[*Subscription]struct{}),
	}
}

// Write appends p to the ring buffer, advances the absolute offset counter by
// len(p), and broadcasts the chunk (with its absolute starting offset) to all
// current subscribers. If p is larger than the buffer capacity, only the
// tail len(p) bytes are retained for future replay — but the FULL p is still
// broadcast to live subscribers with its true starting offset, since replay
// capacity and live delivery are independent concerns.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(p)
	if n == 0 {
		return 0, nil
	}
	startOffset := r.total
	capN := len(r.buf)

	if n >= capN {
		tail := p[n-capN:]
		copy(r.buf, tail)
		r.head = 0
		r.used = capN
	} else {
		first := capN - r.head
		if n <= first {
			copy(r.buf[r.head:], p)
		} else {
			copy(r.buf[r.head:], p[:first])
			copy(r.buf, p[first:])
		}
		r.head = (r.head + n) % capN
		r.used += n
		if r.used > capN {
			r.used = capN
		}
	}
	r.total += int64(n)

	if len(r.subs) > 0 {
		chunk := make([]byte, n)
		copy(chunk, p)
		for sub := range r.subs {
			select {
			case sub.Ch <- Chunk{Offset: startOffset, Data: chunk}:
			default:
				// Subscriber is lagging; drop this chunk for it only.
			}
		}
	}

	return n, nil
}

// snapshot returns the current contents (oldest -> newest) WITHOUT acquiring
// the lock. Callers MUST hold r.mu.
func (r *Ring) snapshot() []byte {
	if r.used == 0 {
		return nil
	}
	capN := len(r.buf)
	out := make([]byte, r.used)
	if r.used < capN {
		copy(out, r.buf[:r.used])
	} else {
		tail := capN - r.head
		copy(out, r.buf[r.head:])
		copy(out[tail:], r.buf[:r.head])
	}
	return out
}

// readFromLocked returns the bytes available starting at offset (clamped to
// the currently retained window) and the actual absolute offset of the first
// returned byte. Callers MUST hold r.mu.
func (r *Ring) readFromLocked(offset int64) ([]byte, int64) {
	oldest := r.total - int64(r.used)
	if offset < 0 || offset < oldest {
		offset = oldest
	}
	if offset > r.total {
		offset = r.total
	}
	full := r.snapshot()
	skip := offset - oldest
	if skip < 0 {
		skip = 0
	}
	if int(skip) > len(full) {
		skip = int64(len(full))
	}
	out := make([]byte, len(full)-int(skip))
	copy(out, full[skip:])
	return out, offset
}

// ReadFrom returns a copy of the bytes available from offset (inclusive)
// onward, plus the actual absolute offset of the first returned byte.
//
//   - offset < 0 means "from the oldest byte still retained" (ring head).
//   - offset older than the oldest retained byte is clamped up to the
//     oldest retained byte (the requested range was evicted).
//   - offset beyond the newest written byte is clamped down to
//     TotalWritten() (nothing to replay yet; caller should rely on a live
//     subscription for anything newer).
func (r *Ring) ReadFrom(offset int64) ([]byte, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readFromLocked(offset)
}

// SnapshotAndSubscribe atomically captures replay data starting at offset and
// registers a live subscriber — all under the same lock, so no byte written
// between the snapshot and the subscription registration can be lost.
//
// The caller must call [Ring.Unsubscribe] on the returned [Subscription] when
// it is no longer needed.
func (r *Ring) SnapshotAndSubscribe(offset int64) ([]byte, int64, *Subscription) {
	sub := &Subscription{
		Ch:   make(chan Chunk, 256),
		Done: make(chan struct{}),
	}
	r.mu.Lock()
	data, start := r.readFromLocked(offset)
	r.subs[sub] = struct{}{}
	r.mu.Unlock()
	return data, start, sub
}

// Unsubscribe removes a subscriber registered via [Ring.SnapshotAndSubscribe]
// and closes its Done channel. In-flight chunks already enqueued in Ch remain
// readable.
func (r *Ring) Unsubscribe(sub *Subscription) {
	r.mu.Lock()
	delete(r.subs, sub)
	r.mu.Unlock()
	close(sub.Done)
}

// Len returns the number of bytes currently retained (<= capacity).
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.used
}

// TotalWritten returns the absolute offset one past the last byte ever
// written (i.e. the offset the NEXT Write call will start at).
func (r *Ring) TotalWritten() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

// OldestOffset returns the absolute offset of the oldest byte still
// retained in the ring (i.e. TotalWritten() - Len()).
func (r *Ring) OldestOffset() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total - int64(r.used)
}
