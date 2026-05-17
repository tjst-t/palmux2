package pty

import (
	"bytes"
	"testing"
)

func TestRingBuffer_Basic(t *testing.T) {
	r := NewRingBuffer(10)
	if r.Len() != 0 {
		t.Fatalf("want 0 got %d", r.Len())
	}
	r.Write([]byte("hello"))
	if r.Len() != 5 {
		t.Fatalf("want 5 got %d", r.Len())
	}
	if !bytes.Equal(r.Bytes(), []byte("hello")) {
		t.Fatalf("want 'hello' got %q", r.Bytes())
	}
}

func TestRingBuffer_Wrap(t *testing.T) {
	r := NewRingBuffer(5)
	r.Write([]byte("abcde")) // fills buffer
	r.Write([]byte("fg"))    // overwrites first 2 bytes
	// Oldest = cdefg? No: after wrap head is at 2, used=5.
	// data[2:5]="cde", data[0:2]="fg" → "cdefg"
	got := r.Bytes()
	if !bytes.Equal(got, []byte("cdefg")) {
		t.Fatalf("want 'cdefg' got %q", got)
	}
}

func TestRingBuffer_LargerThanCap(t *testing.T) {
	r := NewRingBuffer(4)
	r.Write([]byte("abcdefgh")) // 8 bytes > cap 4
	// Only last 4 retained: "efgh"
	got := r.Bytes()
	if !bytes.Equal(got, []byte("efgh")) {
		t.Fatalf("want 'efgh' got %q", got)
	}
}

func TestRingBuffer_Empty(t *testing.T) {
	r := NewRingBuffer(8)
	if r.Bytes() != nil {
		t.Fatalf("empty ring should return nil")
	}
}
