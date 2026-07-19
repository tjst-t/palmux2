package vt

import "testing"

// TestDECSTBM_BottomMarginBeyondBufferHeight_ClampedNoPanic is the
// code-review-cycle-1 regression test for the S2b5691 DECSTBM (CSI r) crash
// fix — see handlers.go's 'r' CSI handler doc comment and screen.go's
// setVerticalMargins doc comment. Before the fix, a bottom margin beyond the
// terminal's actual buffer height could make the next scroll/reverse-index
// operation panic with "index out of range" and kill the WHOLE palmux2
// process (not just the one terminal session) — reproduced live against
// real codex-cli 0.144.1's normal startup redraw on a stock 80x24 terminal
// inside a real incus container while implementing S2b5691.
//
// This test reproduces the same failure shape directly against this
// package: CSI 1;30r on an 80x24 screen requests top=1 (row 0, 0-indexed),
// bottom=30 — beyond the 24-row buffer — then ESC M (reverse index) with the
// cursor at the scroll region's top margin triggers
// Screen.ScrollDown -> Screen.InsertLine -> ultraviolet.Buffer.InsertLineArea,
// which indexes b.Lines[i] for i up to area.Max.Y-1 with no bounds check
// beyond "y >= b.Height()" on y itself. Asserts both that this sequence
// does NOT panic, and that setVerticalMargins' defense-in-depth clamp
// actually took effect (Max.Y clamped to the real buffer height) — a test
// that merely swallowed a panic via recover() without checking the clamped
// value could pass even if the clamp were silently removed, as long as
// nothing downstream happened to dereference the out-of-range Y in THIS
// particular sequence.
func TestDECSTBM_BottomMarginBeyondBufferHeight_ClampedNoPanic(t *testing.T) {
	const width, height = 80, 24
	e := NewEmulator(width, height)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v (a DECSTBM bottom margin beyond the buffer height must be clamped, not panic the process)", r)
		}
	}()

	// CSI 1;30r — DECSTBM: top=1, bottom=30 (1-indexed), bottom exceeds the
	// 24-row buffer height.
	if _, err := e.Write([]byte("\x1b[1;30r")); err != nil {
		t.Fatalf("Write(CSI 1;30r): %v", err)
	}

	if got := e.scr.ScrollRegion(); got.Max.Y != height {
		t.Errorf("scroll region Max.Y = %d after CSI 1;30r on a %d-row screen, want clamped to %d (setVerticalMargins clamp did not take effect)",
			got.Max.Y, height, height)
	}

	// ESC M (reverse index) with the cursor at the scroll region's top
	// margin (row 0, set by the DECSTBM sequence above) triggers
	// reverseIndex -> Screen.ScrollDown -> Buffer.InsertLineArea — the exact
	// call that panicked before this fix, when Max.Y was left at 30 against
	// a 24-line buffer.
	if _, err := e.Write([]byte("\x1bM")); err != nil {
		t.Fatalf("Write(ESC M): %v", err)
	}
}

// TestDECSTBM_NormalMarginUnaffected is a sanity companion: a bottom margin
// that is already within the buffer height must be honoured exactly (the
// clamp must be a no-op in the common case), so the fix doesn't silently
// break ordinary scroll-region usage.
func TestDECSTBM_NormalMarginUnaffected(t *testing.T) {
	const width, height = 80, 24
	e := NewEmulator(width, height)

	// CSI 3;20r — top=3, bottom=20, both well within the 24-row buffer.
	if _, err := e.Write([]byte("\x1b[3;20r")); err != nil {
		t.Fatalf("Write(CSI 3;20r): %v", err)
	}

	got := e.scr.ScrollRegion()
	if got.Min.Y != 2 || got.Max.Y != 20 {
		t.Errorf("scroll region = {Min.Y:%d Max.Y:%d}, want {Min.Y:2 Max.Y:20} (CSI 3;20r is [rect Min,Max) with a 0-indexed top margin)",
			got.Min.Y, got.Max.Y)
	}
}
