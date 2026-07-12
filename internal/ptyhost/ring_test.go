package ptyhost

import (
	"bytes"
	"testing"
	"time"
)

// TestRing_ReadFrom_BasicOffsets covers writing N chunks and replaying from
// an arbitrary offset O, asserting the returned bytes equal the tail of the
// written stream from O onward and the returned start offset equals O
// (AC-S3f2658-1-1).
func TestRing_ReadFrom_BasicOffsets(t *testing.T) {
	r := NewRing(1024)

	chunks := [][]byte{
		[]byte("hello "),
		[]byte("world "),
		[]byte("foo bar baz"),
	}
	var all []byte
	for _, c := range chunks {
		if _, err := r.Write(c); err != nil {
			t.Fatalf("Write: %v", err)
		}
		all = append(all, c...)
	}

	total := r.TotalWritten()
	if total != int64(len(all)) {
		t.Fatalf("TotalWritten = %d, want %d", total, len(all))
	}

	// Replay from every offset and assert exact tail match.
	for offset := int64(0); offset <= total; offset++ {
		data, start := r.ReadFrom(offset)
		if start != offset {
			t.Fatalf("ReadFrom(%d): start = %d, want %d", offset, start, offset)
		}
		want := all[offset:]
		if !bytes.Equal(data, want) {
			t.Fatalf("ReadFrom(%d) = %q, want %q", offset, data, want)
		}
	}
}

// TestRing_ReadFrom_NegativeOffsetIsRingHead asserts offset=-1 replays from
// the oldest byte still retained (the ring head).
func TestRing_ReadFrom_NegativeOffsetIsRingHead(t *testing.T) {
	r := NewRing(1024)
	_, _ = r.Write([]byte("abc"))
	_, _ = r.Write([]byte("def"))

	data, start := r.ReadFrom(-1)
	if start != 0 {
		t.Fatalf("start = %d, want 0 (nothing evicted yet)", start)
	}
	if string(data) != "abcdef" {
		t.Fatalf("data = %q, want %q", data, "abcdef")
	}
}

// TestRing_WrapEviction_AdvancesOldestOffset_NoReuse writes more than the
// ring capacity and asserts: (1) the oldest available offset advances past
// the evicted bytes, (2) replay from an evicted offset clamps up to the new
// oldest offset rather than returning stale/wrong bytes, (3) offsets are
// never reused for different bytes as the ring wraps repeatedly.
func TestRing_WrapEviction_AdvancesOldestOffset_NoReuse(t *testing.T) {
	const capacity = 16
	r := NewRing(capacity)

	// Write 40 bytes in 4-byte chunks (10 writes) — capacity wraps 2.5x over.
	var all []byte
	for i := 0; i < 10; i++ {
		chunk := []byte{byte('A' + i), byte('A' + i), byte('A' + i), byte('A' + i)}
		if _, err := r.Write(chunk); err != nil {
			t.Fatalf("Write: %v", err)
		}
		all = append(all, chunk...)
	}

	total := r.TotalWritten()
	if total != int64(len(all)) {
		t.Fatalf("TotalWritten = %d, want %d", total, len(all))
	}
	oldest := r.OldestOffset()
	wantOldest := total - capacity
	if oldest != wantOldest {
		t.Fatalf("OldestOffset = %d, want %d (total=%d, cap=%d)", oldest, wantOldest, total, capacity)
	}

	// Replay from offset 0 (long evicted) must clamp to oldest, not panic or
	// return bytes from a reused/incorrect position.
	data, start := r.ReadFrom(0)
	if start != oldest {
		t.Fatalf("ReadFrom(0): start = %d, want clamp to oldest %d", start, oldest)
	}
	want := all[oldest:]
	if !bytes.Equal(data, want) {
		t.Fatalf("ReadFrom(0) after eviction = %q, want %q", data, want)
	}
	if len(data) != capacity {
		t.Fatalf("len(data) = %d, want ring capacity %d", len(data), capacity)
	}

	// Replay from an offset squarely inside the retained window still works
	// byte-exactly (proves offsets are monotone/absolute, not rebased to 0
	// on wrap).
	mid := oldest + 3
	data2, start2 := r.ReadFrom(mid)
	if start2 != mid {
		t.Fatalf("ReadFrom(mid): start = %d, want %d", start2, mid)
	}
	if !bytes.Equal(data2, all[mid:]) {
		t.Fatalf("ReadFrom(mid) = %q, want %q", data2, all[mid:])
	}
}

// TestRing_ReadFrom_BeyondTotal_ClampsToTotal asserts requesting an offset
// past everything written so far returns no data and clamps start to
// TotalWritten (nothing to replay; caller should rely on live delivery).
func TestRing_ReadFrom_BeyondTotal_ClampsToTotal(t *testing.T) {
	r := NewRing(1024)
	_, _ = r.Write([]byte("abc"))
	data, start := r.ReadFrom(1000)
	if start != r.TotalWritten() {
		t.Fatalf("start = %d, want TotalWritten() = %d", start, r.TotalWritten())
	}
	if len(data) != 0 {
		t.Fatalf("data = %q, want empty", data)
	}
}

// TestRing_SnapshotAndSubscribe_NoLostBytes exercises the atomic
// snapshot+subscribe path: a write racing the subscribe call must be
// observed exactly once, either in the snapshot or in the live channel, and
// never dropped.
func TestRing_SnapshotAndSubscribe_NoLostBytes(t *testing.T) {
	r := NewRing(1 << 16)
	_, _ = r.Write([]byte("before-subscribe"))

	data, start, sub := r.SnapshotAndSubscribe(-1)
	defer r.Unsubscribe(sub)
	if start != 0 {
		t.Fatalf("start = %d, want 0", start)
	}
	if string(data) != "before-subscribe" {
		t.Fatalf("snapshot data = %q", data)
	}

	_, _ = r.Write([]byte("after-subscribe"))

	select {
	case chunk := <-sub.Ch:
		if string(chunk.Data) != "after-subscribe" {
			t.Fatalf("live chunk = %q, want %q", chunk.Data, "after-subscribe")
		}
		if chunk.Offset != int64(len("before-subscribe")) {
			t.Fatalf("live chunk offset = %d, want %d", chunk.Offset, len("before-subscribe"))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for live chunk")
	}
}

// TestRing_Unsubscribe_ClosesDone asserts Unsubscribe closes Done so any pump
// goroutine draining Ch can stop.
func TestRing_Unsubscribe_ClosesDone(t *testing.T) {
	r := NewRing(1024)
	_, _, sub := r.SnapshotAndSubscribe(-1)
	r.Unsubscribe(sub)
	select {
	case <-sub.Done:
	default:
		t.Fatal("Done channel not closed after Unsubscribe")
	}
}

// TestRing_LargerThanCapacityWrite_KeepsTail asserts a single write larger
// than the ring capacity retains only the tail for replay, with a correctly
// advanced oldest offset.
func TestRing_LargerThanCapacityWrite_KeepsTail(t *testing.T) {
	r := NewRing(8)
	big := []byte("0123456789ABCDEF") // 16 bytes, cap is 8
	_, _ = r.Write(big)

	if r.TotalWritten() != int64(len(big)) {
		t.Fatalf("TotalWritten = %d, want %d", r.TotalWritten(), len(big))
	}
	data, start := r.ReadFrom(-1)
	wantStart := int64(len(big) - 8)
	if start != wantStart {
		t.Fatalf("start = %d, want %d", start, wantStart)
	}
	if string(data) != "89ABCDEF" {
		t.Fatalf("data = %q, want %q", data, "89ABCDEF")
	}
}
