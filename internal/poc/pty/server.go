package pty

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"
)

// Server wraps a Daemon with HTTP + WebSocket endpoints.
type Server struct {
	daemon *Daemon
	mux    *http.ServeMux
}

// NewServer creates an HTTP server wired to daemon.
func NewServer(daemon *Daemon) *Server {
	s := &Server{daemon: daemon, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("GET /poc/pty/stats", s.handleStats)
	s.mux.HandleFunc("GET /poc/pty/attach", s.handleAttach)
	// Also support WS upgrade on the same path regardless of Go 1.22
	// pattern subtleties by registering without method prefix as fallback.
	return s
}

// Handler returns the http.Handler for use with http.ListenAndServe.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// handleIndex serves a minimal stub page.  Story 3 replaces this with
// the real xterm.js HTML.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>poc-pty</title></head>
<body>
<p data-testid="pty-poc-status">connecting</p>
<div data-testid="pty-poc-terminal"></div>
<button data-testid="pty-poc-reconnect-btn" style="display:none">Reconnect</button>
<p>poc-pty stub — Story 3 replaces this with xterm.js</p>
</body>
</html>`)
}

// handleStats serves JSON stats for the daemon.
// [AC-S1d2278-2-4] alive:false is observable here.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := s.daemon.CurrentStats()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(stats); err != nil {
		slog.Error("poc-pty: stats encode", "err", err)
	}
}

// handleAttach upgrades the connection to WebSocket and:
//  1. Lazily spawns the subprocess (first attach only).
//  2. Replays the ring buffer to the new client.
//  3. Pumps PTY output to the client and client input to the PTY.
//
// [AC-S1d2278-2-3] ring buffer replay
// [AC-S1d2278-2-4] ?resume=<id> query param for future resume path
func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	// Accept websocket upgrade.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Allow any origin for PoC (not suitable for production).
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Error("poc-pty: ws accept", "err", err)
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()

	// Lazy spawn — priority_rule 4.
	resumeID := r.URL.Query().Get("resume")
	if resumeID != "" {
		slog.Info("poc-pty: attach with resume id", "resume", resumeID)
	}
	if err := s.daemon.EnsureStarted(ctx); err != nil {
		slog.Error("poc-pty: ensure started", "err", err)
		conn.Close(websocket.StatusInternalError, "daemon error")
		return
	}

	// Replay ring buffer immediately after connection.
	ringData := s.daemon.ring.Bytes()
	if len(ringData) > 0 {
		if wErr := conn.Write(ctx, websocket.MessageBinary, ringData); wErr != nil {
			slog.Warn("poc-pty: ring replay write error", "err", wErr)
			return
		}
	}

	// Register subscriber for live PTY output.
	sub := s.daemon.Subscribe()
	defer s.daemon.Unsubscribe(sub)

	// Pump PTY → WS.
	go func() {
		for {
			select {
			case chunk, ok := <-sub.ch:
				if !ok {
					return
				}
				if wErr := conn.Write(ctx, websocket.MessageBinary, chunk); wErr != nil {
					return
				}
			case <-sub.done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	// Pump WS → PTY (blocking; exits when connection closes).
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			// Connection closed normally.
			return
		}
		if wErr := s.daemon.WriteInput(msg); wErr != nil {
			slog.Warn("poc-pty: write input", "err", wErr)
			return
		}
	}
}
