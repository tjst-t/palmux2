// Package pty provides the PTY daemon internals for the Track B PoC.
// This package is intentionally separate from the production internal/tmux
// package — it is throwaway PoC code that will NOT be promoted to
// production without a dedicated Sprint.
package pty

import "sync"

// RingBuffer is a fixed-capacity FIFO byte ring that retains up to
// `cap` bytes of the most recent PTY output.  When the buffer is full,
// the oldest bytes are overwritten (tail eviction).
// All methods are safe for concurrent use.
type RingBuffer struct {
	mu   sync.RWMutex
	buf  []byte // underlying storage, always len == cap
	head int    // next write position (wraps around)
	used int    // bytes currently stored (<= cap)
}

// NewRingBuffer allocates a ring buffer with the given capacity in bytes.
// capacity must be > 0.
func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{buf: make([]byte, capacity)}
}

// Write appends p to the ring buffer.  If p is larger than the buffer
// capacity only the tail len(p) bytes are kept.
func (r *RingBuffer) Write(p []byte) (int, error) {
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
		return n, nil
	}
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
	return n, nil
}

// Bytes returns a copy of the current ring buffer contents in insertion
// order (oldest → newest).
func (r *RingBuffer) Bytes() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
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

// Len returns the number of bytes currently stored.
func (r *RingBuffer) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.used
}
