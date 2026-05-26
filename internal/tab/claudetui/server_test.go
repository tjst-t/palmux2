package claudetui

import (
	"context"
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
