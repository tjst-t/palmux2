package claudetui

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	vt "github.com/charmbracelet/x/vt"

	"github.com/tjst-t/palmux2/internal/notify"
)

// Grid is a snapshot of the emulator's visible grid at a point in time.
// It is intended for consumption by the grid WebSocket mode added in Story 2.
type Grid struct {
	// Cols is the terminal width in columns.
	Cols int `json:"cols"`
	// Rows is the terminal height in rows.
	Rows int `json:"rows"`
	// Cursor is the cursor position (0-based x, y).
	Cursor struct {
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"cursor"`
	// AltScreen is true when the alternate screen buffer is active.
	AltScreen bool `json:"altScreen"`
	// Lines contains one GridRow per terminal row.  Rows is always
	// len(Lines) == Grid.Rows.
	Lines []GridRow `json:"lines"`
}

// GridRow is one row of the visible grid.
type GridRow struct {
	// Y is the 0-based row index.
	Y int `json:"y"`
	// Cells contains one GridCell per column.
	Cells []GridCell `json:"cells"`
}

// GridCell is one cell in the visible grid.
type GridCell struct {
	// Ch is the displayed character.  Space (' ') for empty cells.
	Ch rune `json:"ch"`
	// FG is the foreground colour as a packed 0x00RRGGBB value, or 0 for the
	// terminal default.  The colour is resolved from the emulator palette when
	// an indexed colour is in use.
	FG uint32 `json:"fg"`
	// BG is the background colour in the same format.
	BG uint32 `json:"bg"`
	// Attrs encodes character attributes.  The bit layout matches
	// [github.com/charmbracelet/ultraviolet.AttrBold] et al.; callers may
	// compare directly against those constants.
	Attrs uint8 `json:"attrs"`
}

// Emulator maintains a server-side headless terminal emulator that consumes
// PTY output and keeps an up-to-date grid (cells × style) representation.
//
// Feed is called synchronously from the PTY read loop — it must be fast.
// GridSnapshot may be called from any goroutine.
//
// # OSC 52
//
// Whenever the subprocess emits an OSC 52 clipboard-write sequence, the
// Emulator decodes the base64 payload and calls [notify.Hub.PublishCopy] so
// that Story 2's grid WebSocket — or any other subscriber — can forward the
// text to the browser.
type Emulator struct {
	// em is the thread-safe charmbracelet/x/vt emulator.
	em *vt.SafeEmulator

	// hub is the notify hub used to publish CopyEvents.  May be nil in tests
	// that do not need clipboard forwarding.
	hub *notify.Hub

	// repoID and branchID identify the workspace; they are stamped onto
	// CopyEvents.
	repoID, branchID string

	// logger is used for diagnostics.
	logger *slog.Logger
}

// NewEmulator creates an Emulator with the given initial terminal dimensions
// (cols × rows), wired to hub for OSC 52 clipboard forwarding.
//
// hub may be nil — the emulator will function normally but clipboard events
// will not be published.  repoID and branchID are stamped onto every
// [notify.CopyEvent]; they may be empty in unit tests.
func NewEmulator(cols, rows int, hub *notify.Hub, repoID, branchID string) *Emulator {
	em := vt.NewSafeEmulator(cols, rows)
	e := &Emulator{
		em:       em,
		hub:      hub,
		repoID:   repoID,
		branchID: branchID,
		logger:   slog.Default(),
	}
	// Register OSC 52 handler — the library passes the full payload after
	// stripping the "52" command number, so the data is "c;<base64>" or just
	// "<base64>".  We split on ';' to find the b64 part.
	em.RegisterOscHandler(52, func(data []byte) bool {
		e.handleOsc52(data)
		return true // handled; suppress library's "unhandled" log
	})
	return e
}

// Feed writes p to the underlying terminal emulator.  It is called from the
// Daemon PTY read loop and must be fast.  Any error is logged and ignored —
// the PTY read loop must not block.
//
// Feed is safe for concurrent use (SafeEmulator locks internally).
func (e *Emulator) Feed(p []byte) {
	if len(p) == 0 {
		return
	}
	if _, err := e.em.Write(p); err != nil {
		e.logger.Debug("claudetui emulator: feed write error", "err", err)
	}
}

// GridSnapshot returns a consistent snapshot of the current visible grid.
// It is safe for concurrent use.
func (e *Emulator) GridSnapshot() Grid {
	cols := e.em.Width()
	rows := e.em.Height()
	pos := e.em.CursorPosition()
	altScreen := e.em.IsAltScreen()

	lines := make([]GridRow, rows)
	for y := 0; y < rows; y++ {
		cells := make([]GridCell, cols)
		for x := 0; x < cols; x++ {
			c := e.em.CellAt(x, y)
			cells[x] = cellToGridCell(c)
		}
		lines[y] = GridRow{Y: y, Cells: cells}
	}

	g := Grid{
		Cols:      cols,
		Rows:      rows,
		AltScreen: altScreen,
		Lines:     lines,
	}
	g.Cursor.X = pos.X
	g.Cursor.Y = pos.Y
	return g
}

// Resize changes the terminal dimensions.  It is called from Daemon.Resize so
// the emulator stays in sync with the PTY size.
func (e *Emulator) Resize(cols, rows int) {
	e.em.Resize(cols, rows)
}

// Close releases resources held by the emulator.
func (e *Emulator) Close() {
	_ = e.em.Close()
}

// handleOsc52 is called by the OSC 52 handler.  It decodes the clipboard
// payload and publishes a CopyEvent to the notify hub.
//
// The charmbracelet/x/vt library strips the leading "52" command number and
// passes the remainder.  The format is "c;<base64>" where "c" is the clipboard
// target; we support that and also the bare "<base64>" form in case the library
// evolves.
func (e *Emulator) handleOsc52(data []byte) {
	b64 := extractB64(data)
	if b64 == nil {
		e.logger.Debug("claudetui emulator: osc52: empty payload")
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(string(b64))
	if err != nil {
		// Some terminals omit padding; try RawStdEncoding as fallback.
		decoded, err = base64.RawStdEncoding.DecodeString(string(b64))
		if err != nil {
			e.logger.Debug("claudetui emulator: osc52: base64 decode error", "err", err)
			return
		}
	}
	text := string(decoded)
	e.logger.Debug("claudetui emulator: osc52: clipboard write",
		"repo", e.repoID, "branch", e.branchID, "len", len(text))
	if e.hub != nil {
		e.hub.PublishCopy(notify.CopyEvent{
			Text:     text,
			RepoID:   e.repoID,
			BranchID: e.branchID,
			At:       time.Now().UTC(),
		})
	}
}

// extractB64 returns the base64 portion of an OSC 52 payload.
//
// The payload may be:
//   - "c;<base64>"  — standard; we take the part after the last ';'
//   - "<base64>"    — bare; we return the full slice
//
// Returns nil when the payload is empty after stripping whitespace.
func extractB64(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	// Find the last ';' separator.  OSC 52 format: "c;<b64>" — the 'c' is the
	// clipboard target identifier.
	if idx := bytes.LastIndexByte(data, ';'); idx >= 0 {
		return data[idx+1:]
	}
	return data
}

// cellToGridCell converts a *uv.Cell to a GridCell.  Nil cells (empty/default)
// produce a space with zero attributes.
func cellToGridCell(c *uv.Cell) GridCell {
	if c == nil {
		return GridCell{Ch: ' '}
	}
	ch := ' '
	if c.Content != "" {
		runes := []rune(c.Content)
		if len(runes) > 0 {
			ch = runes[0]
		}
	}
	return GridCell{
		Ch:    ch,
		FG:    colorToRGB(c.Style.Fg),
		BG:    colorToRGB(c.Style.Bg),
		Attrs: c.Style.Attrs,
	}
}

// colorToRGB converts a color.Color (nil = terminal default) to a packed
// 0x00RRGGBB uint32.  Returns 0 for nil (terminal default).
func colorToRGB(c uvColor) uint32 {
	if c == nil {
		return 0
	}
	r, g, b, _ := c.RGBA()
	// RGBA() returns values in the [0, 65535] range; scale to [0, 255].
	return (uint32(r>>8) << 16) | (uint32(g>>8) << 8) | uint32(b>>8)
}

// uvColor is a local alias for image/color.Color, used to accept nil colours
// from uv.Style without importing image/color directly.
type uvColor interface {
	RGBA() (r, g, b, a uint32)
}

