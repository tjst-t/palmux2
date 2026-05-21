// Package emulator provides PoC spike tests for Go terminal-emulator libraries.
// This file tests Candidate B: github.com/hinshun/vt10x
//
// The fixture testdata/sample_tui.ansi.bin is the same hand-crafted ANSI
// byte stream used in cellbuf_spike_test.go. NO live `claude` binary is
// spawned; the stream is deterministic.
package emulator

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/hinshun/vt10x"
)

// attrBold is the internal bit mask for bold in vt10x's Glyph.Mode field.
// vt10x does not export this constant, but from state.go:
//
//	attrReverse  = 1 << iota  // bit 0
//	attrUnderline              // bit 1
//	attrBold                   // bit 2
const attrBoldVT10x = 1 << 2 // same as int16(4)

// TestAltSpike validates the hinshun/vt10x terminal emulator against the
// same deterministic ANSI fixture used in TestCellbufSpike.
//
// Fixture layout is documented in cellbuf_spike_test.go.
func TestAltSpike(t *testing.T) {
	// ---- Load fixture ----
	fixturePath := filepath.Join("testdata", "sample_tui.ansi.bin")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}
	t.Logf("fixture size: %d bytes", len(data))

	// ---- Create terminal (80 cols × 24 rows) ----
	term := vt10x.New(vt10x.WithSize(80, 24))

	// ---- Feed fixture using Parse() ----
	reader := bufio.NewReader(bytes.NewReader(data))
	if err := term.Parse(reader); err != nil {
		t.Logf("FEATURE parse: partial — Parse returned error: %v (emulator may still have processed partial state)", err)
	} else {
		t.Log("FEATURE parse: ✓ Parse() returned nil error")
	}

	term.Lock()
	defer term.Unlock()

	// ---- AC-1: Alt screen ----
	mode := term.Mode()
	if mode&vt10x.ModeAltScreen != 0 {
		t.Log("FEATURE alt_screen: ✓ ModeAltScreen bit is set")
	} else {
		t.Error("FEATURE alt_screen: ✗ ModeAltScreen bit not set after ?1049h")
	}

	// ---- AC-2: Cursor position (row9, col3 → 0-based x=2, y=8) ----
	cursor := term.Cursor()
	expectedX, expectedY := 2, 8
	if cursor.X == expectedX && cursor.Y == expectedY {
		t.Logf("FEATURE cursor_pos: ✓ cursor at (x=%d, y=%d)", cursor.X, cursor.Y)
	} else {
		t.Errorf("FEATURE cursor_pos: ✗ cursor at (x=%d, y=%d), expected (%d, %d)", cursor.X, cursor.Y, expectedX, expectedY)
	}

	// ---- AC-3: Red + bold "H" at row3, col2 (0-based x=1, y=2) ----
	glyph := term.Cell(1, 2)
	t.Logf("FEATURE sgr_red_bold: cell(1,2) char=%q mode=0x%04x fg=%v", glyph.Char, glyph.Mode, glyph.FG)
	if glyph.Char == 'H' {
		t.Log("FEATURE sgr_red_bold: ✓ 'H' rune found at (1,2)")
	} else {
		t.Errorf("FEATURE sgr_red_bold: ✗ expected 'H', got %q", glyph.Char)
	}
	if glyph.Mode&attrBoldVT10x != 0 {
		t.Log("FEATURE sgr_red_bold: ✓ bold attribute confirmed")
	} else {
		t.Errorf("FEATURE sgr_red_bold: ✗ bold attribute not set (mode=0x%04x)", glyph.Mode)
	}
	// Red foreground: CSI 31m → vt10x color index 1 (Red)
	if glyph.FG == vt10x.Red {
		t.Logf("FEATURE sgr_red_bold: ✓ foreground is Red (index %d)", glyph.FG)
	} else {
		// Bold + red may shift to bright red (index 9) in some emulators
		t.Logf("FEATURE sgr_red_bold: partial — FG=%d (expected Red=%d; may be BrightRed=%d due to bold-bright shift)", glyph.FG, vt10x.Red, vt10x.Red+8)
	}

	// ---- AC-4: Prompt ">" at row9, col1 (0-based x=0, y=8) ----
	promptGlyph := term.Cell(0, 8)
	t.Logf("FEATURE prompt_cell: cell(0,8) char=%q", promptGlyph.Char)
	if promptGlyph.Char == '>' {
		t.Log("FEATURE prompt_cell: ✓ '>' rune found at prompt position (0,8)")
	} else {
		t.Errorf("FEATURE prompt_cell: ✗ expected '>' at (0,8), got %q", promptGlyph.Char)
	}

	// ---- AC-5: 256-color text (row5, col1 → 0-based x=0, y=4) ----
	glyph256 := term.Cell(0, 4)
	t.Logf("FEATURE sgr_256color: cell(0,4) char=%q fg=%v", glyph256.Char, glyph256.FG)
	if glyph256.Char == 'C' {
		t.Log("FEATURE sgr_256color: ✓ first char of COLOR256 text found")
	} else {
		t.Errorf("FEATURE sgr_256color: ✗ expected 'C' at (0,4), got %q", glyph256.Char)
	}

	// ---- AC-6: Box-drawing character (row7, col1 → 0-based x=0, y=6) ----
	boxGlyph := term.Cell(0, 6)
	t.Logf("FEATURE box_drawing: cell(0,6) char=%q (U+%04X)", boxGlyph.Char, boxGlyph.Char)
	if boxGlyph.Char == '─' {
		t.Log("FEATURE box_drawing: ✓ U+2500 '─' found at (0,6)")
	} else {
		t.Errorf("FEATURE box_drawing: ✗ expected U+2500 '─', got %q (U+%04X)", boxGlyph.Char, boxGlyph.Char)
	}

	// ---- AC-7: OSC 52 clipboard ----
	// vt10x does not provide a hook/callback for OSC sequences.
	// The library silently ignores OSC 52 — document this limitation.
	t.Log("FEATURE osc52_clipboard: ✗ vt10x does not expose an OSC callback; OSC 52 is silently ignored (no API for clipboard events)")

	// ---- AC-8: Bracketed paste (?2004h) ----
	// vt10x does not expose a ModeFlag for ?2004 (bracketed paste).
	// Check if the library has any matching mode bit:
	// ModeWrap, ModeInsert, ModeAppKeypad, ModeAltScreen, ModeCRLF,
	// ModeMouseButton, ModeMouseMotion, ModeSGRMouse, ModeMouseX10,
	// ModeMouseMany, ModeFocus, ModeUT8 are the known exported flags.
	// ?2004 is not among them.
	t.Log("FEATURE bracketed_paste: partial — vt10x has no exported ModeFlag for ?2004h; mode is parsed but cannot be queried via public API")

	// ---- AC-9: Mouse mode (?1006h SGR) ----
	if mode&vt10x.ModeMouseSgr != 0 {
		t.Log("FEATURE mouse_sgr: ✓ ModeMouseSgr bit set after ?1006h")
	} else {
		t.Log("FEATURE mouse_sgr: partial — ModeMouseSgr bit not set; library may not map ?1006h to this flag")
	}

	// ---- AC-10: Sixel / graphics extension ----
	t.Log("FEATURE sixel: not exercised in fixture (vt10x does not advertise sixel support)")

	t.Log("=== hinshun/vt10x spike complete ===")
}
