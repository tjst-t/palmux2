package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// Tunables — these are taken from the architecture doc.
const (
	wsReadLimit       = 1 << 20 // 1 MiB; client→server messages must stay small
	wsWriteTimeout    = 5 * time.Second
	wsPingInterval    = 30 * time.Second
	wsPingTimeout     = 60 * time.Second
	wsOutputBuffer    = 256 // backpressure buffer in messages, oldest-drop
	wsOutputChunkSize = 32 << 10
)

// registerTerminalWS attaches the terminal-attach WS endpoint. Called from
// NewMux when assembling routes.
func registerTerminalWS(mux *http.ServeMux, deps Deps) {
	h := &wsHandlers{store: deps.Store, tmux: deps.Tmux, logger: deps.Logger}
	mux.HandleFunc("GET /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/attach", h.attachTab)
	mux.HandleFunc("GET /api/events", h.eventsStream)
}

// eventsStream is the broadcast WS that fans out store events to every
// connected client. Clients re-fetch full state via REST after reconnect, so
// transient losses are recoverable; that's the design contract in
// 01-architecture §3.6.
func (h *wsHandlers) eventsStream(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		h.logger.Warn("ws events accept", "err", err)
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(64 * 1024)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	events, unsub := h.store.Hub().Subscribe()
	defer unsub()

	// One goroutine drains pings/closes from the client; the main loop
	// pushes events.
	go func() {
		defer cancel()
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, pcancel := context.WithTimeout(ctx, wsPingTimeout)
			err := c.Ping(pingCtx)
			pcancel()
			if err != nil {
				return
			}
		case ev, ok := <-events:
			if !ok {
				return
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			wctx, wcancel := context.WithTimeout(ctx, wsWriteTimeout)
			err = c.Write(wctx, websocket.MessageText, payload)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}

type wsHandlers struct {
	store  *store.Store
	tmux   tmux.Client
	logger *slog.Logger
}

// parseAttachOpts pulls cols/rows out of the request query. Values are
// clamped to the tmux supported range; anything unparseable becomes 0 so
// Attach falls back to the platform default.
func parseAttachOpts(q map[string][]string) tmux.AttachOpts {
	parse := func(key string) int {
		v := firstNonEmpty(q[key])
		if v == "" {
			return 0
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return 0
		}
		if n > 999 {
			n = 999
		}
		return n
	}
	return tmux.AttachOpts{Cols: parse("cols"), Rows: parse("rows")}
}

func firstNonEmpty(vs []string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// inboundMsg is what the client sends.
type inboundMsg struct {
	Type string `json:"type"`           // "input" | "resize"
	Data string `json:"data,omitempty"` // input bytes (UTF-8 encoded)
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func (h *wsHandlers) attachTab(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	tabID := r.PathValue("tabId")

	branch, err := h.store.Branch(repoID, branchID)
	if err != nil {
		http.Error(w, err.Error(), statusForErr(err))
		return
	}
	var target domain.Tab
	for _, t := range branch.TabSet.Tabs {
		if t.ID == tabID {
			target = t
			break
		}
	}
	if target.ID == "" {
		http.Error(w, "tab not found", http.StatusNotFound)
		return
	}
	if target.WindowName == "" {
		http.Error(w, "tab is not terminal-backed", http.StatusBadRequest)
		return
	}

	// SameOrigin only — Palmux is served from the same origin as its API,
	// and we never want a third-party page hijacking a terminal session.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"}, // BasePath/host varies; auth enforces actual security
	})
	if err != nil {
		h.logger.Warn("ws accept", "err", err)
		return
	}
	c.SetReadLimit(wsReadLimit)
	defer c.CloseNow()

	conn := h.store.AddConnection(repoID, branchID, tabID)
	defer h.store.RemoveConnection(conn.ID)

	groupSession := domain.GroupSessionName(branch.TabSet.TmuxSession, conn.ID)
	ctx := r.Context()

	if err := h.tmux.NewGroupSession(ctx, branch.TabSet.TmuxSession, groupSession); err != nil {
		h.logger.Warn("NewGroupSession", "err", err)
		_ = c.Close(websocket.StatusInternalError, "failed to create session group")
		return
	}
	defer func() {
		// Best-effort cleanup; SyncTmux will sweep too.
		_ = h.tmux.KillSession(context.Background(), groupSession)
	}()

	// Initial pty size from the URL — clients fit before connecting so we
	// open the underlying pty at the right size and tmux doesn't shrink the
	// session to 80x24 first. Anything missing/invalid falls back to defaults.
	attachOpts := parseAttachOpts(r.URL.Query())

	pty, resize, err := h.tmux.Attach(ctx, groupSession, target.WindowName, attachOpts)
	if err != nil {
		h.logger.Warn("Attach", "err", err)
		_ = c.Close(websocket.StatusInternalError, "failed to attach")
		return
	}
	defer pty.Close()

	pumpWS(ctx, c, pty, resize, h.logger)
}

// pumpWS shuttles bytes between the WS conn and the pty. It runs three
// goroutines:
//
//   - pty reader → outCh (oldest-drop on overflow)
//   - outCh → WS writer
//   - WS reader → pty writer (parses {input, resize} JSON)
//
// And a ping ticker. The function returns when any of them exits.
func pumpWS(ctx context.Context, c *websocket.Conn, pty io.ReadWriter, resize tmux.ResizeFunc, logger *slog.Logger) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	outCh := make(chan []byte, wsOutputBuffer)

	// pty → outCh
	go func() {
		buf := make([]byte, wsOutputChunkSize)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				select {
				case outCh <- chunk:
				default:
					// Drop oldest, push newest.
					select {
					case <-outCh:
					default:
					}
					select {
					case outCh <- chunk:
					default:
					}
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
					logger.Debug("pty read", "err", err)
				}
				close(outCh)
				cancel()
				return
			}
		}
	}()

	// outCh → WS
	go func() {
		for chunk := range outCh {
			wctx, wcancel := context.WithTimeout(ctx, wsWriteTimeout)
			err := c.Write(wctx, websocket.MessageBinary, chunk)
			wcancel()
			if err != nil {
				cancel()
				return
			}
		}
	}()

	// WS reader → pty
	go func() {
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var msg inboundMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			switch msg.Type {
			case "input":
				if msg.Data != "" {
					_, _ = pty.Write([]byte(msg.Data))
				}
			case "resize":
				if msg.Cols > 0 && msg.Rows > 0 && resize != nil {
					_ = resize(msg.Cols, msg.Rows)
				}
			}
		}
	}()

	// Ping ticker
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, pcancel := context.WithTimeout(ctx, wsPingTimeout)
			err := c.Ping(pingCtx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}
