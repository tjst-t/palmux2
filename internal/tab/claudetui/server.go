package claudetui

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"
)

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
func AttachHandler(d *Daemon) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		// Use the request context for WS I/O only.  The subprocess lives
		// under daemonCtx, not reqCtx (Fix 7).
		reqCtx := r.Context()

		// Lazy spawn — priority_rule 4.
		if err := d.EnsureStarted(reqCtx); err != nil {
			slog.Error("claudetui: ensure started", "err", err)
			conn.Close(websocket.StatusInternalError, "daemon error")
			return
		}

		d.attachedCount.Add(1)
		defer d.attachedCount.Add(-1)

		// Fix 3: SnapshotAndSubscribe is atomic — no live bytes can slip
		// between the snapshot and the subscribe call.
		snapshot, sub := d.ring.SnapshotAndSubscribe()
		defer d.ring.Unsubscribe(sub)

		// Replay ring buffer to the new client.
		if len(snapshot) > 0 {
			if wErr := conn.Write(reqCtx, websocket.MessageBinary, snapshot); wErr != nil {
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
					if wErr := conn.Write(reqCtx, websocket.MessageBinary, chunk); wErr != nil {
						return
					}
				case <-sub.Done:
					return
				case <-reqCtx.Done():
					return
				}
			}
		}()

		// Pump WS → PTY (blocking; exits when the WS connection closes or the
		// request context is cancelled).
		for {
			_, msg, err := conn.Read(reqCtx)
			if err != nil {
				break
			}
			if wErr := d.WriteInput(reqCtx, msg); wErr != nil {
				slog.Warn("claudetui: write input", "err", wErr)
				break
			}
		}

		<-pumpDone
	})
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
