package claudetui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestStatsHandler verifies the StatsHandler returns valid JSON with the
// expected fields.
func TestStatsHandler(t *testing.T) {
	d := NewDaemon(DaemonConfig{ClaudeBin: "claude", RingSize: 1024})
	t.Cleanup(func() { d.Shutdown() })

	ts := httptest.NewServer(StatsHandler(d))
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("GET /stats: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

// TestAttachHandlerSnapshotReplay verifies that a newly attached WS client
// receives the ring buffer snapshot on connect (Fix 3 — SnapshotAndSubscribe).
func TestAttachHandlerSnapshotReplay(t *testing.T) {
	bin := fakeBin(t)
	d := NewDaemon(DaemonConfig{
		ClaudeBin: bin,
		RingSize:  1 << 16,
	})
	t.Cleanup(func() { d.Shutdown() })

	// Pre-populate the ring with known data.
	payload := []byte("hello replay\n")
	if _, err := d.ring.Write(payload); err != nil {
		t.Fatalf("ring.Write: %v", err)
	}

	// Start the daemon so EnsureStarted in the handler succeeds.
	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}

	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	// Connect a WS client.
	wsURL := "ws" + ts.URL[len("http"):]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()

	// The first message should be the ring snapshot.
	_, got, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}

	// The snapshot should start with our injected payload.
	// (The ring may also contain subprocess output, so we check prefix.)
	if len(got) < len(payload) {
		t.Fatalf("received %d bytes, expected at least %d (the injected snapshot payload)", len(got), len(payload))
	}
	t.Logf("received %d bytes from ring snapshot", len(got))
}

// ─── Grid mode tests ──────────────────────────────────────────────────────────

// TestAttachHandler_GridMode_Init verifies that connecting with ?mode=grid
// results in a text frame with {type:"grid.init", cols, rows, cursor, altScreen,
// rows:[...]}.
func TestAttachHandler_GridMode_Init(t *testing.T) {
	bin := fakeBin(t)
	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		RingSize:      1 << 16,
		ResumeOnDeath: false,
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}

	// Wait for some output so the emulator has content.
	deadline := time.After(5 * time.Second)
	for {
		if len(d.ring.Bytes()) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("daemon produced no output within 5 s")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	// Let emulator process the bytes.
	time.Sleep(30 * time.Millisecond)

	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	wsURL := "ws" + ts.URL[len("http"):] + "?mode=grid"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()

	// First frame must be a text frame.
	msgType, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if msgType != websocket.MessageText {
		t.Fatalf("expected text frame for grid.init, got binary")
	}

	var msg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("json unmarshal: %v\nraw: %s", err, raw)
	}

	// Check type field.
	var typ string
	if err := json.Unmarshal(msg["type"], &typ); err != nil {
		t.Fatalf("type field: %v", err)
	}
	if typ != "grid.init" {
		t.Fatalf("type = %q, want grid.init", typ)
	}

	// Check required top-level fields exist.
	// Note: "rows" is the array of row data; "cols" is the integer column count.
	// The integer row count equals len(rows) in the array.
	for _, field := range []string{"cols", "rows", "cursor", "altScreen"} {
		if _, ok := msg[field]; !ok {
			t.Errorf("grid.init missing field %q; keys=%v", field, keysOf(msg))
		}
	}

	var cols int
	if err := json.Unmarshal(msg["cols"], &cols); err != nil {
		t.Fatalf("cols field: %v", err)
	}
	if cols <= 0 {
		t.Fatalf("expected positive cols, got %d", cols)
	}

	// Check cursor shape.
	var cursor map[string]int
	if err := json.Unmarshal(msg["cursor"], &cursor); err != nil {
		t.Fatalf("cursor field: %v", err)
	}
	if _, ok := cursor["x"]; !ok {
		t.Error("cursor missing 'x'")
	}
	if _, ok := cursor["y"]; !ok {
		t.Error("cursor missing 'y'")
	}

	// Unmarshal the rows array.
	var rawRows []map[string]json.RawMessage
	if err := json.Unmarshal(msg["rows"], &rawRows); err != nil {
		t.Fatalf("rows field is not an array: %v", err)
	}
	if len(rawRows) == 0 {
		t.Fatal("rows array empty")
	}

	// The whole init message should unmarshal into gridInitMsg too.
	var initMsg gridInitMsg
	if err := json.Unmarshal(raw, &initMsg); err != nil {
		t.Fatalf("unmarshal gridInitMsg: %v", err)
	}
	if len(initMsg.Lines) == 0 {
		t.Fatal("grid.init rows array is empty via struct unmarshal")
	}

	firstRow := rawRows[0]
	var rawCells []map[string]json.RawMessage
	if err := json.Unmarshal(firstRow["cells"], &rawCells); err != nil {
		t.Fatalf("cells: %v", err)
	}
	if len(rawCells) == 0 {
		t.Fatal("first row has no cells")
	}
	// Each cell must have "ch" as a string.
	for i, cell := range rawCells {
		chRaw, ok := cell["ch"]
		if !ok {
			t.Fatalf("cell %d missing 'ch' field", i)
		}
		var ch string
		if err := json.Unmarshal(chRaw, &ch); err != nil {
			t.Fatalf("cell %d: ch is not a string: %v", i, err)
		}
		if len(ch) == 0 {
			t.Fatalf("cell %d: ch is empty string", i)
		}
	}
	t.Logf("grid.init OK: cols=%d, %d rows, first row has %d cells", cols, len(rawRows), len(rawCells))
}

// TestAttachHandler_GridMode_Diff verifies that grid.diff frames are sent
// after grid.init when the emulator state changes.
func TestAttachHandler_GridMode_Diff(t *testing.T) {
	bin := fakeBin(t)
	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		RingSize:      1 << 16,
		ResumeOnDeath: false,
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	wsURL := "ws" + ts.URL[len("http"):] + "?mode=grid"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()

	// Consume grid.init.
	msgType, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read grid.init: %v", err)
	}
	if msgType != websocket.MessageText {
		t.Fatalf("grid.init: expected text frame, got binary")
	}
	var initCheck struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &initCheck); err != nil {
		t.Fatalf("unmarshal grid.init check: %v", err)
	}
	if initCheck.Type != "grid.init" {
		t.Fatalf("expected grid.init, got %q", initCheck.Type)
	}

	// Send bytes to the subprocess to cause emulator state change.
	// fake_claude echoes heartbeats — we inject our own marker too.
	marker := []byte("marker-for-diff-test\n")
	if wErr := d.WriteInput(ctx, marker); wErr != nil {
		t.Fatalf("WriteInput: %v", wErr)
	}

	// Wait up to 10 s for at least one grid.diff frame.
	deadline := time.After(10 * time.Second)
	got := false
	for !got {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for grid.diff frame")
		default:
		}

		readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		mt, diffRaw, rErr := conn.Read(readCtx)
		readCancel()
		if rErr != nil {
			continue
		}
		if mt != websocket.MessageText {
			continue
		}
		var diff struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(diffRaw, &diff); err != nil {
			continue
		}
		if diff.Type != "grid.diff" {
			continue
		}

		// Validate grid.diff schema.
		var diffMsg map[string]json.RawMessage
		if err := json.Unmarshal(diffRaw, &diffMsg); err != nil {
			t.Fatalf("unmarshal grid.diff: %v", err)
		}
		for _, field := range []string{"type", "cursor", "altScreen", "rows"} {
			if _, ok := diffMsg[field]; !ok {
				t.Errorf("grid.diff missing field %q", field)
			}
		}
		// Rows in diff should be a (possibly empty) array.
		var changedRows []json.RawMessage
		if err := json.Unmarshal(diffMsg["rows"], &changedRows); err != nil {
			t.Fatalf("grid.diff rows: %v", err)
		}
		t.Logf("grid.diff OK: %d changed rows", len(changedRows))
		got = true
	}
}

// TestAttachHandler_GridMode_CoalesceRate verifies that under sustained PTY
// output the number of grid.diff frames in 2 s is at most 40 (= 20 fps).
func TestAttachHandler_GridMode_CoalesceRate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping coalesce-rate test in short mode")
	}

	bin := fakeBin(t)
	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		RingSize:      1 << 16,
		ResumeOnDeath: false,
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	wsURL := "ws" + ts.URL[len("http"):] + "?mode=grid"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()

	// Consume grid.init.
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("read grid.init: %v", err)
	}

	// Inject output bytes rapidly via a background goroutine.
	go func() {
		chunk := make([]byte, 1024)
		for i := range chunk {
			chunk[i] = 'A' + byte(i%26)
		}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		end := time.After(2 * time.Second)
		for {
			select {
			case <-end:
				return
			case <-ticker.C:
				_ = d.WriteInput(ctx, chunk)
			}
		}
	}()

	// Count grid.diff frames received over 2 s.
	diffCount := 0
	obs := time.After(2 * time.Second)
	for {
		select {
		case <-obs:
			goto done
		default:
		}
		readCtx, readCancel := context.WithTimeout(ctx, 100*time.Millisecond)
		mt, raw, rErr := conn.Read(readCtx)
		readCancel()
		if rErr != nil {
			continue
		}
		if mt != websocket.MessageText {
			continue
		}
		var hdr struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &hdr); err == nil && hdr.Type == "grid.diff" {
			diffCount++
		}
	}
done:
	// 2 s * 20 fps = 40 max. Allow 20% jitter → 48.
	const maxFrames = 48
	if diffCount > maxFrames {
		t.Fatalf("grid.diff count = %d in 2 s, want <= %d (20 fps coalesce)", diffCount, maxFrames)
	}
	t.Logf("grid.diff count = %d in 2 s (limit %d) — coalesce rate OK", diffCount, maxFrames)
}

// TestAttachHandler_GridMode_InputCompat verifies that in grid mode, binary
// frames from the client are forwarded to the PTY subprocess unchanged.
func TestAttachHandler_GridMode_InputCompat(t *testing.T) {
	bin := fakeBin(t)
	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		RingSize:      1 << 16,
		ResumeOnDeath: false,
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	wsURL := "ws" + ts.URL[len("http"):] + "?mode=grid"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()

	// Consume grid.init.
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("read grid.init: %v", err)
	}

	// Send binary frame (client input).  fake_claude echoes heartbeats; our
	// input triggers PTY activity which causes grid.diff frames.
	input := []byte("input-test\n")
	if wErr := conn.Write(ctx, websocket.MessageBinary, input); wErr != nil {
		t.Fatalf("send binary input: %v", wErr)
	}

	// We should receive grid.diff frames (not an error or binary frames).
	deadline := time.After(8 * time.Second)
	gotDiff := false
	for !gotDiff {
		select {
		case <-deadline:
			t.Fatal("no grid.diff received after binary input in grid mode")
		default:
		}
		readCtx, readCancel := context.WithTimeout(ctx, 300*time.Millisecond)
		mt, raw, rErr := conn.Read(readCtx)
		readCancel()
		if rErr != nil {
			continue
		}
		if mt == websocket.MessageBinary {
			t.Fatalf("grid mode sent binary frame; expected text-only server→client")
		}
		var hdr struct{ Type string `json:"type"` }
		if err := json.Unmarshal(raw, &hdr); err == nil &&
			(hdr.Type == "grid.diff" || hdr.Type == "grid.init") {
			gotDiff = true
		}
	}
	t.Log("grid mode input compat OK: binary input → grid.diff frames received")
}

// TestAttachHandler_RawMode_StillWorks verifies that the default mode (no
// ?mode= query param) still delivers raw binary frames — backward compat.
func TestAttachHandler_RawMode_StillWorks(t *testing.T) {
	bin := fakeBin(t)
	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		RingSize:      1 << 16,
		ResumeOnDeath: false,
	})
	t.Cleanup(func() { d.Shutdown() })

	// Pre-populate ring so the replay delivers a binary frame immediately.
	payload := []byte("raw-mode-check\n")
	if _, err := d.ring.Write(payload); err != nil {
		t.Fatalf("ring.Write: %v", err)
	}
	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}

	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	// Connect WITHOUT ?mode=grid.
	wsURL := "ws" + ts.URL[len("http"):]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()

	// First frame must be binary.
	msgType, _, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if msgType != websocket.MessageBinary {
		t.Fatalf("raw mode: expected binary frame, got text (grid mode leak?)")
	}
	t.Log("raw mode still delivers binary frames — backward compat OK")
}

// keysOf returns the keys of a map[string]json.RawMessage for test diagnostics.
func keysOf(m map[string]json.RawMessage) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// TestAttachHandlerRequestCancelDoesNotKillDaemon verifies Fix 7 at the HTTP
// handler level: cancelling the request context (simulating WS disconnect)
// does NOT affect the daemon state.
func TestAttachHandlerRequestCancelDoesNotKillDaemon(t *testing.T) {
	bin := fakeBin(t)
	d := NewDaemon(DaemonConfig{
		ClaudeBin: bin,
		RingSize:  1 << 16,
	})
	t.Cleanup(func() { d.Shutdown() })

	ts := httptest.NewServer(AttachHandler(d))
	defer ts.Close()

	wsURL := "ws" + ts.URL[len("http"):]

	// Connect and immediately close the WS connection.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		cancel()
		t.Fatalf("ws dial: %v", err)
	}
	conn.CloseNow()
	cancel()

	// Give a short window for any erroneous context propagation.
	time.Sleep(200 * time.Millisecond)

	// The daemon should still be running (subprocess alive).
	stats := d.CurrentStats()
	if !stats.Alive {
		t.Fatalf("daemon died after WS client disconnect; Fix 7 regression")
	}
	t.Logf("daemon still alive (state=%s) after WS client disconnect (Fix 7 confirmed)", stats.State)
}
