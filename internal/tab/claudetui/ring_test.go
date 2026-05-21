package claudetui

import (
	"bytes"
	"sync"
	"testing"
)

// TestRingWrite verifies basic FIFO write + Bytes() round-trip.
func TestRingWrite(t *testing.T) {
	r := NewRing(16)
	data := []byte("hello, world!")
	if _, err := r.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, want := r.Bytes(), data; !bytes.Equal(got, want) {
		t.Fatalf("Bytes() = %q, want %q", got, want)
	}
}

// TestRingWrap verifies that the ring correctly evicts the oldest bytes when
// the buffer fills up.
func TestRingWrap(t *testing.T) {
	r := NewRing(8)
	// Write 5 bytes.
	if _, err := r.Write([]byte("abcde")); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	// Write 5 more bytes, which wraps the buffer and evicts 'a','b'.
	if _, err := r.Write([]byte("fghij")); err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	got := r.Bytes()
	want := []byte("cdefghij")
	if !bytes.Equal(got, want) {
		t.Fatalf("Bytes() = %q, want %q", got, want)
	}
}

// TestRingOversizedWrite verifies that a write larger than capacity keeps only
// the tail.
func TestRingOversizedWrite(t *testing.T) {
	r := NewRing(4)
	if _, err := r.Write([]byte("abcdefgh")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := r.Bytes()
	want := []byte("efgh")
	if !bytes.Equal(got, want) {
		t.Fatalf("Bytes() = %q, want %q", got, want)
	}
}

// TestSnapshotAndSubscribeAtomic is the Fix 3 race test.
//
// N concurrent writers write to the ring while a subscriber watches.
// The subscriber uses SnapshotAndSubscribe atomically so no bytes written
// after subscribe-time can be missed.  The invariant checked:
//   - snapshot length == total pre-subscribe writes
//   - live bytes received are a whole multiple of the write unit (no partial
//     writes slipped through)
func TestSnapshotAndSubscribeAtomic(t *testing.T) {
	const (
		numWriters   = 8
		writesEach   = 200
		payloadSize  = 10
		ringCapacity = 1 << 20 // 1MiB — big enough to hold all data
	)

	r := NewRing(ringCapacity)

	// Phase 1: write a known prefix before subscribing.
	prefix := bytes.Repeat([]byte("X"), payloadSize)
	for i := 0; i < writesEach; i++ {
		if _, err := r.Write(prefix); err != nil {
			t.Fatalf("pre-subscribe write: %v", err)
		}
	}

	// Phase 2: atomically snapshot + subscribe (Fix 3).
	snapshot, sub := r.SnapshotAndSubscribe()

	// The snapshot must be non-empty (we wrote to the ring above).
	if len(snapshot) == 0 {
		r.Unsubscribe(sub)
		t.Fatal("snapshot should be non-empty")
	}

	// Phase 3: run N concurrent writers while we are subscribed.
	var wg sync.WaitGroup
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chunk := bytes.Repeat([]byte("Y"), payloadSize)
			for i := 0; i < writesEach; i++ {
				if _, err := r.Write(chunk); err != nil {
					t.Errorf("concurrent write: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	// Unsubscribe once (closes sub.Done); drain Ch after.
	r.Unsubscribe(sub)

	var liveBytes int
drainLoop:
	for {
		select {
		case chunk := <-sub.Ch:
			liveBytes += len(chunk)
		default:
			break drainLoop
		}
	}

	// The important invariant: no byte written BEFORE subscribe is absent
	// from the snapshot.
	preBytes := writesEach * payloadSize
	if len(snapshot) != preBytes {
		t.Fatalf("snapshot length = %d, want %d (pre-subscribe bytes)", len(snapshot), preBytes)
	}
	// Post-subscribe writes may be dropped under backpressure; that is fine.
	// What must NOT happen: partial payloads (partial writes).
	if liveBytes%payloadSize != 0 {
		t.Fatalf("liveBytes %d not a multiple of payload size %d (partial write?)", liveBytes, payloadSize)
	}
	t.Logf("snapshot=%d bytes, live=%d bytes (drops allowed)", len(snapshot), liveBytes)
}

// TestRingLen verifies Len() tracks the number of stored bytes.
func TestRingLen(t *testing.T) {
	r := NewRing(64)
	if r.Len() != 0 {
		t.Fatalf("initial Len() = %d, want 0", r.Len())
	}
	r.Write([]byte("hello"))
	if r.Len() != 5 {
		t.Fatalf("Len() = %d, want 5", r.Len())
	}
}

// TestRingConcurrentWriteRead exercises the ring under concurrent writers and
// concurrent Bytes() readers (no assertions on content — just asserts no data
// race, suitable for -race).
func TestRingConcurrentWriteRead(t *testing.T) {
	r := NewRing(512)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Write([]byte("data"))
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = r.Bytes()
			}
		}()
	}
	wg.Wait()
}
