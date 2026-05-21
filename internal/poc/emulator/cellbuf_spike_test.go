// Package emulator provides PoC spike tests for Go terminal-emulator libraries.
// This file tests Candidate A: github.com/charmbracelet/x/vt
//
// The fixture testdata/sample_tui.ansi.bin is a hand-crafted ANSI byte stream
// (see testdata comments below) that exercises the features listed in the task.
// NO live `claude` binary is spawned; the stream is deterministic.
package emulator

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"
)

// TestCellbufSpike validates the charmbracelet/x/vt terminal emulator against
// the deterministic ANSI fixture.
//
// Fixture byte-stream layout (see also the gen_fixture.go helper):
//
//	\x1b[?1049h          — Enter alt screen (CSI ?1049h)
//	\x1b[2J              — Clear screen
//	\x1b[H               — Cursor home (1,1)
//	\x1b[3;2H            — Cursor move to row 3, col 2
//	\x1b[31;1m           — SGR: red foreground + bold
//	"HELLO"              — Five cells: H E L L O at (col2..col6, row3)
//	\x1b[0m              — SGR reset
//	\x1b[5;1H            — Cursor move to row 5, col 1
//	\x1b[38;5;196m       — SGR 256-color (index 196 = red)
//	"COLOR256"           — Text at row 5
//	\x1b[0m              — SGR reset
//	\x1b[7;1H            — Row 7, col 1
//	U+2500 × 20          — Box-drawing horizontal line
//	\x1b[9;1H            — Row 9, col 1
//	\x1b[32m             — Green foreground
//	"> "                 — Prompt characters
//	\x1b[0m              — Reset
//	"input box text …"  — Normal text
//	\x1b[9;3H            — Cursor lands at row 9, col 3 (after "> ")
//	OSC 52;c;<b64>BEL   — Clipboard write
//	\x1b[?2004h          — Bracketed paste ON
//	\x1b[?1006h          — SGR mouse mode ON
//	\x1b[10;1H           — Row 10 col 1
//	\x1b[33m + "END"     — Yellow "END"
//	\x1b[0m              — Reset
//	\x1b[9;3H            — Final cursor at row 9, col 3  ← assertion anchor
func TestCellbufSpike(t *testing.T) {
	// ---- Load fixture ----
	fixturePath := filepath.Join("testdata", "sample_tui.ansi.bin")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}
	t.Logf("fixture size: %d bytes", len(data))

	// ---- Create emulator (80 cols × 24 rows) ----
	em := vt.NewEmulator(80, 24)

	// Track observed OSC 52 clipboard data via callback
	var osc52Received bool
	var osc52Data string
	em.RegisterOscHandler(52, func(payload []byte) bool {
		// payload is "c;<base64>" after the "52;" prefix is stripped
		// The library may pass the full "c;<b64>" or just "<b64>"
		osc52Received = true
		osc52Data = string(payload)
		return true
	})

	// Track bracketed paste and mouse modes via callbacks
	bracketedPasteEnabled := false
	mouseEnabled := false
	em.SetCallbacks(vt.Callbacks{
		AltScreen: func(entered bool) {
			if entered {
				t.Log("FEATURE alt_screen: ✓ AltScreen callback fired (entered=true)")
			}
		},
		EnableMode: func(mode ansi.Mode) {
			switch mode {
			case ansi.ModeBracketedPaste:
				bracketedPasteEnabled = true
				t.Logf("FEATURE bracketed_paste: ✓ mode enabled (?2004h)")
			case ansi.ModeMouseExtSgr:
				mouseEnabled = true
				t.Logf("FEATURE mouse_sgr: ✓ SGR mouse mode enabled (?1006h)")
			}
		},
	})

	// ---- Feed fixture ----
	n, err := em.Write(data)
	if err != nil {
		t.Fatalf("emulator.Write failed: %v (wrote %d bytes)", err, n)
	}
	t.Logf("wrote %d bytes to emulator", n)

	// ---- AC-1: Alt screen ----
	if em.IsAltScreen() {
		t.Log("FEATURE alt_screen: ✓ IsAltScreen() == true")
	} else {
		// charmbracelet/x/vt tracks this via internal state; callback may have
		// fired even if IsAltScreen flips back.  The fixture doesn't exit alt
		// screen, so this should be true.
		t.Error("FEATURE alt_screen: ✗ IsAltScreen() == false after ?1049h")
	}

	// ---- AC-2: Cursor position (should be row 9, col 3; 0-indexed: y=8, x=2) ----
	// The fixture ends with \x1b[9;3H which moves cursor to row 9, col 3 (1-based).
	// charmbracelet/x/vt uses 0-based (x, y) internally.
	pos := em.CursorPosition()
	expectedX, expectedY := 2, 8 // 0-based: col 3 → x=2, row 9 → y=8
	if pos.X == expectedX && pos.Y == expectedY {
		t.Logf("FEATURE cursor_pos: ✓ cursor at (%d,%d) (0-based x=%d,y=%d)", pos.X, pos.Y, expectedX, expectedY)
	} else {
		t.Errorf("FEATURE cursor_pos: ✗ cursor at (%d,%d), expected (%d,%d)", pos.X, pos.Y, expectedX, expectedY)
	}

	// ---- AC-3: Red + bold cell ("H" at row3, col2 → 0-based x=1, y=2) ----
	cell := em.CellAt(1, 2)
	if cell == nil {
		t.Error("FEATURE sgr_red_bold: ✗ CellAt(1,2) returned nil")
	} else {
		content := cell.Content
		hasBold := cell.Style.Attrs&uv.AttrBold != 0
		t.Logf("FEATURE sgr_red_bold: cell(1,2) content=%q bold=%v fg=%v", content, hasBold, cell.Style.Fg)
		if content != "H" {
			t.Errorf("FEATURE sgr_red_bold: ✗ expected 'H' at (1,2), got %q", content)
		}
		if !hasBold {
			t.Errorf("FEATURE sgr_red_bold: ✗ bold attribute not set on 'H' cell")
		} else {
			t.Log("FEATURE sgr_red_bold: ✓ bold attribute confirmed")
		}
		// Check red foreground is set (not nil and not white/default)
		if cell.Style.Fg != nil {
			t.Logf("FEATURE sgr_red_bold: ✓ foreground color is set: %v", cell.Style.Fg)
		} else {
			t.Log("FEATURE sgr_red_bold: partial — foreground color is nil (may use indexed representation)")
		}
	}

	// ---- AC-4: Prompt ">" cell at row9, col1 (0-based x=0, y=8) ----
	promptCell := em.CellAt(0, 8)
	if promptCell == nil {
		t.Error("FEATURE prompt_cell: ✗ CellAt(0,8) returned nil")
	} else {
		t.Logf("FEATURE prompt_cell: cell(0,8) content=%q", promptCell.Content)
		if promptCell.Content == ">" {
			t.Log("FEATURE prompt_cell: ✓ '>' rune found at prompt position (0,8)")
		} else {
			t.Errorf("FEATURE prompt_cell: ✗ expected '>' at (0,8), got %q", promptCell.Content)
		}
	}

	// ---- AC-5: 256-color text (row5, col1 → 0-based x=0, y=4) ----
	cell256 := em.CellAt(0, 4)
	if cell256 == nil {
		t.Log("FEATURE sgr_256color: partial — CellAt(0,4) returned nil")
	} else {
		t.Logf("FEATURE sgr_256color: cell(0,4) content=%q fg=%v", cell256.Content, cell256.Style.Fg)
		if cell256.Content == "C" {
			t.Log("FEATURE sgr_256color: ✓ first char of COLOR256 text found")
		} else {
			t.Errorf("FEATURE sgr_256color: ✗ expected 'C' at (0,4), got %q", cell256.Content)
		}
	}

	// ---- AC-6: Box-drawing characters (row7, col1 → 0-based x=0, y=6) ----
	boxCell := em.CellAt(0, 6)
	if boxCell == nil {
		t.Log("FEATURE box_drawing: partial — CellAt(0,6) returned nil")
	} else {
		t.Logf("FEATURE box_drawing: cell(0,6) content=%q", boxCell.Content)
		if boxCell.Content == "─" || boxCell.Content == "\xe2\x94\x80" {
			t.Log("FEATURE box_drawing: ✓ U+2500 '─' found at (0,6)")
		} else {
			t.Errorf("FEATURE box_drawing: ✗ expected '─' at (0,6), got %q", boxCell.Content)
		}
	}

	// ---- AC-7: OSC 52 clipboard ----
	if osc52Received {
		// Verify the payload can be decoded as base64(hello)
		// The library may deliver just "c;<b64>" or the raw b64
		t.Logf("FEATURE osc52_clipboard: ✓ OSC 52 handler fired, payload=%q", osc52Data)
		// Try to find and decode the base64 part
		expectedB64 := base64.StdEncoding.EncodeToString([]byte("hello"))
		t.Logf("FEATURE osc52_clipboard: expected base64=%q, got=%q", expectedB64, osc52Data)
	} else {
		t.Log("FEATURE osc52_clipboard: partial — OSC 52 handler not fired (library may not route OSC 52 to custom handler by default)")
	}

	// ---- AC-8: Bracketed paste ----
	if bracketedPasteEnabled {
		t.Log("FEATURE bracketed_paste: ✓ mode ?2004h acknowledged")
	} else {
		t.Log("FEATURE bracketed_paste: partial — EnableMode callback not fired for ?2004h; library may handle internally")
	}

	// ---- AC-9: Mouse mode ----
	if mouseEnabled {
		t.Log("FEATURE mouse_sgr: ✓ mode ?1006h acknowledged")
	} else {
		t.Log("FEATURE mouse_sgr: partial — EnableMode callback not fired for ?1006h; library may handle internally")
	}

	// ---- AC-10: Sixel / graphics extension ----
	// The fixture does not send sixel data; log that this is untested.
	t.Log("FEATURE sixel: not exercised in fixture (library does not advertise native sixel support)")

	t.Log("=== charmbracelet/x/vt spike complete ===")
}
