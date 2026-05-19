package claudetui

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

// gridCoalesceInterval is the maximum rate at which grid.diff frames are sent
// to the client. 50 ms = 20 fps (AC-S0fd64b-2-3).
const gridCoalesceInterval = 50 * time.Millisecond

// attachMode is the WS frame format selected by the ?mode= query parameter.
type attachMode int

const (
	attachModeRaw  attachMode = iota // default — raw binary PTY bytes
	attachModeGrid                   // JSON text frames with grid diffs
)

// gridInitMsg is the first JSON message sent in grid mode.
//
// Wire shape:
//
//	{"type":"grid.init","cols":80,"rows":24,"cursor":{"x":0,"y":0},"altScreen":false,"rows":[...]}
//
// Note: JSON does not allow duplicate keys.  The "rows" key in the wire format
// refers to the row array; the integer terminal height is inferred from
// len(rows).  The "cols" integer is included separately.  This matches the
// endpoint contract in gui-spec-S0fd64b-4.json.
//
// MarshalJSON is implemented manually to emit both the integer "rows" count
// AND the "rows" array under a single key by using the name "rows" only for
// the array (the integer terminal height is available as len(rows)).  We also
// emit "cols" as the integer column count for parity.  If a consumer needs the
// integer count it reads len(rows).
type gridInitMsg struct {
	Type      string    `json:"type"`
	Cols      int       `json:"cols"`
	Cursor    gridPoint `json:"cursor"`
	AltScreen bool      `json:"altScreen"`
	Lines     []gridRow `json:"rows"`
}

// gridDiffMsg carries only the rows that changed since the last sent snapshot.
type gridDiffMsg struct {
	Type      string    `json:"type"`
	Cursor    gridPoint `json:"cursor"`
	AltScreen bool      `json:"altScreen"`
	Lines     []gridRow `json:"rows"`
}

// gridPoint is a JSON-serialisable cursor position.
type gridPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// gridRow is a JSON-serialisable terminal row.
type gridRow struct {
	Y     int        `json:"y"`
	Cells []gridCell `json:"cells"`
}

// gridCell is a JSON-serialisable terminal cell.
// Ch is always emitted as a single UTF-8 string ("a", " ", etc.).
// FG / BG / Attrs use omitempty so zero values are elided.
type gridCell struct {
	// Ch is serialised as a string by MarshalJSON.
	Ch    rune   `json:"-"`
	FG    uint32 `json:"fg,omitempty"`
	BG    uint32 `json:"bg,omitempty"`
	Attrs uint8  `json:"attrs,omitempty"`
}

// MarshalJSON encodes gridCell as {"ch":"x","fg":N,"bg":N,"attrs":N}.
// FG/BG/Attrs are omitted when zero.  Ch == 0 is emitted as a space.
func (c gridCell) MarshalJSON() ([]byte, error) {
	ch := c.Ch
	if ch == 0 {
		ch = ' '
	}
	// Encode the rune as a minimal JSON string.
	buf := make([]byte, 0, utf8.RuneLen(ch)+2) // '"' + char + '"'
	buf = append(buf, '"')
	buf = utf8.AppendRune(buf, ch)
	buf = append(buf, '"')
	chJSON := buf

	// Build the rest of the object manually to honour omitempty on FG/BG/Attrs.
	type cellRest struct {
		FG    uint32 `json:"fg,omitempty"`
		BG    uint32 `json:"bg,omitempty"`
		Attrs uint8  `json:"attrs,omitempty"`
	}
	rest, err := json.Marshal(cellRest{FG: c.FG, BG: c.BG, Attrs: c.Attrs})
	if err != nil {
		return nil, err
	}
	// rest is "{}" or "{\"fg\":...}" etc.  We want {"ch":"x",...} so we splice.
	if string(rest) == "{}" {
		return append(append([]byte(`{"ch":`), chJSON...), '}'), nil
	}
	// rest[0] == '{', rest[1:] == "\"fg\":..."
	out := append([]byte(`{"ch":`), chJSON...)
	out = append(out, ',')
	out = append(out, rest[1:]...) // strip leading '{'
	return out, nil
}

// gridFromSnapshot converts a Grid snapshot into the wire types used by both
// gridInitMsg and gridDiffMsg.
func gridRowsFromSnapshot(g Grid) []gridRow {
	rows := make([]gridRow, len(g.Lines))
	for i, line := range g.Lines {
		cells := make([]gridCell, len(line.Cells))
		for j, c := range line.Cells {
			cells[j] = gridCell{
				Ch:    c.Ch,
				FG:    c.FG,
				BG:    c.BG,
				Attrs: c.Attrs,
			}
		}
		rows[i] = gridRow{Y: line.Y, Cells: cells}
	}
	return rows
}

// diffRows returns the subset of rows from cur that differ from prev.
// A row is "changed" if any cell within it differs from the same row in prev.
func diffRows(prev, cur Grid) []gridRow {
	// If dimensions changed, treat all rows as changed.
	if prev.Cols != cur.Cols || prev.Rows != cur.Rows ||
		len(prev.Lines) != len(cur.Lines) {
		return gridRowsFromSnapshot(cur)
	}
	var changed []gridRow
	for i, curLine := range cur.Lines {
		prevLine := prev.Lines[i]
		if rowChanged(prevLine, curLine) {
			cells := make([]gridCell, len(curLine.Cells))
			for j, c := range curLine.Cells {
				cells[j] = gridCell{
					Ch:    c.Ch,
					FG:    c.FG,
					BG:    c.BG,
					Attrs: c.Attrs,
				}
			}
			changed = append(changed, gridRow{Y: curLine.Y, Cells: cells})
		}
	}
	return changed
}

// rowChanged returns true if any cell in a and b differs.
func rowChanged(a, b GridRow) bool {
	if len(a.Cells) != len(b.Cells) {
		return true
	}
	for i, ca := range a.Cells {
		cb := b.Cells[i]
		if ca.Ch != cb.Ch || ca.FG != cb.FG || ca.BG != cb.BG || ca.Attrs != cb.Attrs {
			return true
		}
	}
	return false
}

// parseAttachMode maps the "mode" query parameter to an attachMode value.
// Unknown values default to attachModeRaw so the handler is forward-compatible.
func parseAttachMode(r *http.Request) attachMode {
	switch r.URL.Query().Get("mode") {
	case "grid":
		return attachModeGrid
	default:
		return attachModeRaw
	}
}

// AttachHandler returns an http.Handler that upgrades the connection to
// WebSocket and attaches to daemon d.
//
// The handler:
//  1. Lazily spawns the subprocess (EnsureStarted — priority_rule 4).
//  2. Atomically snapshots the ring buffer and subscribes to live output
//     (Fix 3 — SnapshotAndSubscribe).
//  3. Replays the snapshot to the client.
//  4. Pumps live PTY output to the client and client input to the PTY.
//
// The daemon context (daemonCtx) governs the subprocess lifetime; the request
// context governs only the WebSocket I/O.  A WS disconnect does NOT kill the
// subprocess (Fix 7 — daemonCtx isolation).
//
// When ?mode=grid is supplied the server switches to JSON text-frame delivery
// (grid.init + periodic grid.diff) instead of raw binary PTY bytes.  Client →
// server input is always raw binary frames regardless of mode.
func AttachHandler(d *Daemon) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := parseAttachMode(r)

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Origin checks are the caller's responsibility (palmux2 auth
			// middleware runs before this handler in production).
			InsecureSkipVerify: true,
		})
		if err != nil {
			slog.Error("claudetui: ws accept", "err", err)
			return
		}
		defer conn.CloseNow()

		// Use a cancellable context derived from the request context for WS
		// I/O only.  The subprocess lives under daemonCtx, not ioCtx (Fix 7).
		//
		// ioCancel is called after the read loop exits so the pump goroutine
		// (PTY → WS) can observe ioCtx.Done() and unblock even when the ring
		// subscription channel is quiet.  Without this the pump goroutine
		// would block on <-sub.Ch indefinitely and attachedCount would never
		// reach zero.
		ioCtx, ioCancel := context.WithCancel(r.Context())
		defer ioCancel()

		// Lazy spawn — priority_rule 4.
		if err := d.EnsureStarted(ioCtx); err != nil {
			slog.Error("claudetui: ensure started", "err", err)
			conn.Close(websocket.StatusInternalError, "daemon error")
			return
		}

		d.attachedCount.Add(1)
		defer d.attachedCount.Add(-1)

		if mode == attachModeGrid {
			serveGridMode(ioCtx, ioCancel, conn, d)
			return
		}

		// ── Raw mode (default) ────────────────────────────────────────────────

		// Fix 3: SnapshotAndSubscribe is atomic — no live bytes can slip
		// between the snapshot and the subscribe call.
		snapshot, sub := d.ring.SnapshotAndSubscribe()
		defer d.ring.Unsubscribe(sub)

		// Replay ring buffer to the new client.
		if len(snapshot) > 0 {
			if wErr := conn.Write(ioCtx, websocket.MessageBinary, snapshot); wErr != nil {
				slog.Warn("claudetui: ring replay write error", "err", wErr)
				return
			}
		}

		// Pump PTY → WS in a background goroutine.
		pumpDone := make(chan struct{})
		go func() {
			defer close(pumpDone)
			for {
				select {
				case chunk, ok := <-sub.Ch:
					if !ok {
						return
					}
					if wErr := conn.Write(ioCtx, websocket.MessageBinary, chunk); wErr != nil {
						return
					}
				case <-sub.Done:
					return
				case <-ioCtx.Done():
					return
				}
			}
		}()

		// Pump WS → PTY (blocking; exits when the WS connection closes or the
		// I/O context is cancelled).
		for {
			_, msg, err := conn.Read(ioCtx)
			if err != nil {
				break
			}
			if wErr := d.WriteInput(ioCtx, msg); wErr != nil {
				slog.Warn("claudetui: write input", "err", wErr)
				break
			}
		}

		// Cancel the I/O context so the pump goroutine unblocks on ioCtx.Done()
		// if it is waiting for PTY data.  The defer above guarantees this runs
		// even on early returns, but calling it explicitly here makes the
		// ordering clear: read loop exits → pump goroutine exits → pumpDone
		// closes → handler returns → defer attachedCount.Add(-1).
		ioCancel()
		<-pumpDone
	})
}

// serveGridMode runs the grid-mode WS loop:
//  1. Sends grid.init with the full current grid snapshot.
//  2. Subscribes to the ring for PTY-output change signals.
//  3. Runs a 50 ms coalescing ticker; on each tick it compares the new grid
//     snapshot against the last sent one and, if any rows changed, sends a
//     grid.diff containing only the changed rows plus the updated cursor /
//     altScreen state.
//  4. Forwards client → server binary frames to daemon.WriteInput unchanged.
//
// serveGridMode returns when ioCtx is cancelled (WS disconnect).
func serveGridMode(
	ioCtx context.Context,
	ioCancel context.CancelFunc,
	conn *websocket.Conn,
	d *Daemon,
) {
	// ── Step 1: send grid.init ────────────────────────────────────────────────
	lastSnap := d.GridSnapshot()
	initMsg := gridInitMsg{
		Type: "grid.init",
		Cols: lastSnap.Cols,
		Cursor: gridPoint{
			X: lastSnap.Cursor.X,
			Y: lastSnap.Cursor.Y,
		},
		AltScreen: lastSnap.AltScreen,
		Lines:     gridRowsFromSnapshot(lastSnap),
	}
	initBytes, err := json.Marshal(initMsg)
	if err != nil {
		slog.Error("claudetui: grid.init marshal", "err", err)
		return
	}
	if wErr := conn.Write(ioCtx, websocket.MessageText, initBytes); wErr != nil {
		slog.Warn("claudetui: grid.init write", "err", wErr)
		return
	}

	// ── Step 2: subscribe to ring for change signals ──────────────────────────
	// We only use sub.Ch to wake up the ticker loop faster when PTY output
	// arrives; we do not relay individual chunks (grid diff is coalesced).
	_, sub := d.ring.SnapshotAndSubscribe()
	defer d.ring.Unsubscribe(sub)

	// ── Step 3: coalescing diff loop ──────────────────────────────────────────
	ticker := time.NewTicker(gridCoalesceInterval)
	defer ticker.Stop()

	diffDone := make(chan struct{})
	go func() {
		defer close(diffDone)
		for {
			select {
			case <-ioCtx.Done():
				return

			case <-sub.Done:
				return

			case <-ticker.C:
				cur := d.GridSnapshot()
				changedRows := diffRows(lastSnap, cur)
				cursorChanged := cur.Cursor != lastSnap.Cursor
				altScreenChanged := cur.AltScreen != lastSnap.AltScreen

				if len(changedRows) == 0 && !cursorChanged && !altScreenChanged {
					continue // nothing changed; skip this tick
				}

				diffMsg := gridDiffMsg{
					Type: "grid.diff",
					Cursor: gridPoint{
						X: cur.Cursor.X,
						Y: cur.Cursor.Y,
					},
					AltScreen: cur.AltScreen,
					Lines:     changedRows,
				}
				diffBytes, merr := json.Marshal(diffMsg)
				if merr != nil {
					slog.Error("claudetui: grid.diff marshal", "err", merr)
					return
				}
				if wErr := conn.Write(ioCtx, websocket.MessageText, diffBytes); wErr != nil {
					// Client disconnected.
					return
				}
				lastSnap = cur
			}
		}
	}()

	// ── Step 4: pump WS → PTY ─────────────────────────────────────────────────
	for {
		_, msg, rErr := conn.Read(ioCtx)
		if rErr != nil {
			break
		}
		if wErr := d.WriteInput(ioCtx, msg); wErr != nil {
			slog.Warn("claudetui: grid mode write input", "err", wErr)
			break
		}
	}

	ioCancel()
	<-diffDone
}

// StatsHandler returns an http.Handler that serves a JSON Stats snapshot.
func StatsHandler(d *Daemon) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats := d.CurrentStats()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(stats); err != nil {
			slog.Error("claudetui: stats encode", "err", err)
		}
	})
}
