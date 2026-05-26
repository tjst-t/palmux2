package claudetui

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"
)

// AttachHandler returns an http.Handler that upgrades the connection to
// WebSocket and attaches to daemon d in raw mode.
//
// The handler:
//  1. Lazily spawns the subprocess (EnsureStarted — priority_rule 4).
//  2. Atomically snapshots the ring buffer and subscribes to live output
//     (Fix 3 — SnapshotAndSubscribe).
//  3. Replays the snapshot to the client.
//  4. Pumps live PTY output to the client and client input to the PTY.
//  5. Registers a [subscriber] with [Daemon.roles] and delivers role events
//     as text frames interspersed with the PTY byte stream (Story 3).
//
// The daemon context (daemonCtx) governs the subprocess lifetime; the request
// context governs only the WebSocket I/O.  A WS disconnect does NOT kill the
// subprocess (Fix 7 — daemonCtx isolation).
//
// Note: the ?mode=grid query parameter (from Sprint B) is silently ignored —
// all connections use raw binary PTY framing (Sprint C Story 1 simplification).
func AttachHandler(d *Daemon) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Origin checks are the caller's responsibility (palmux2 auth
			// middleware runs before this handler in production).
			InsecureSkipVerify: true,
			// Disable permessage-deflate for consistent behaviour across
			// Chromium builds.
			CompressionMode: websocket.CompressionDisabled,
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

		// Story 3: register with the role coordinator before replaying the ring
		// so the initial role event is sent after the ring snapshot replay.
		roleSub := newSubscriber(0) // id assigned by OnSubscribe
		initialRole := d.roles.OnSubscribe(roleSub)
		defer func() {
			roleSub.close()
			d.roles.OnUnsubscribe(roleSub)
		}()

		// Fix 3: SnapshotAndSubscribe is atomic — no live bytes can slip
		// between the snapshot and the subscribe call.
		snapshot, ringSub := d.ring.SnapshotAndSubscribe()
		defer d.ring.Unsubscribe(ringSub)

		// Replay ring buffer to the new client.
		if len(snapshot) > 0 {
			if wErr := conn.Write(ioCtx, websocket.MessageBinary, snapshot); wErr != nil {
				slog.Warn("claudetui: ring replay write error", "err", wErr)
				return
			}
		}

		// Send initial role event as a text frame after the ring replay.
		if roleEv, evErr := initialRoleEvent(initialRole); evErr == nil {
			if wErr := sendRoleFrame(ioCtx, conn, roleEv); wErr != nil {
				slog.Warn("claudetui: initial role frame write", "err", wErr)
				return
			}
		}

		// Pump PTY bytes + role events → WS in a background goroutine.
		// Binary frames carry PTY bytes; text frames carry role events.
		pumpDone := make(chan struct{})
		go func() {
			defer close(pumpDone)
			for {
				select {
				case chunk, ok := <-ringSub.Ch:
					if !ok {
						return
					}
					if wErr := conn.Write(ioCtx, websocket.MessageBinary, chunk); wErr != nil {
						return
					}
				case ev, ok := <-roleSub.roleCh:
					if !ok {
						return
					}
					if wErr := conn.Write(ioCtx, websocket.MessageText, ev); wErr != nil {
						return
					}
				case <-ringSub.Done:
					return
				case <-roleSub.done:
					return
				case <-ioCtx.Done():
					return
				}
			}
		}()

		// Pump WS → PTY (blocking; exits when the WS connection closes or the
		// I/O context is cancelled).
		// Story 3: any input from this client transfers the active role to it
		// ("last-typed-wins" semantics).
		for {
			_, msg, err := conn.Read(ioCtx)
			if err != nil {
				break
			}
			// Transfer active role to this client before writing input.
			d.roles.TakeActive(roleSub)
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
