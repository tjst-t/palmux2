// Package browser — CDP (Chrome DevTools Protocol) WebSocket proxy.
//
// Story 2 (S62374c-2): screencast + input + navigate via a palmux WS that
// bridges between the browser client and the in-container CDP endpoint.
//
// Wire protocol (palmux WS, JSON frames):
//
//   Server → Client:
//     {"type":"frame","data":"<base64 jpeg>","meta":{"deviceWidth":W,"deviceHeight":H}}
//     {"type":"url","url":"<current page URL>"}
//     {"type":"error","msg":"..."}
//
//   Client → Server:
//     {"type":"input","kind":"mouse","eventType":"mousePressed","x":10,"y":20,
//      "button":"left","clickCount":1}
//     {"type":"input","kind":"mouse","eventType":"mouseWheel","x":0,"y":0,
//      "deltaX":0,"deltaY":100}
//     {"type":"input","kind":"key","eventType":"keyDown","key":"Enter","text":"\r"}
//     {"type":"input","kind":"touch","x":0,"y":0,"touchType":"touchStart"}
//     {"type":"navigate","url":"http://localhost:3000"}
//     {"type":"reload"}
//     {"type":"back"}
//     {"type":"forward"}
//
// [AC-S62374c-2-1] [AC-S62374c-2-2] [AC-S62374c-2-3] [AC-S62374c-2-5]
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// ─── CDP JSON message shapes ──────────────────────────────────────────────────

type cdpRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type cdpResponse struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// screencastFrameParams is the shape of Page.screencastFrame event params.
type screencastFrameParams struct {
	Data      string `json:"data"`
	SessionID int    `json:"sessionId"`
	Metadata  struct {
		DeviceWidth  int `json:"deviceWidth"`
		DeviceHeight int `json:"deviceHeight"`
		OffsetTop    int `json:"offsetTop"`
	} `json:"metadata"`
}

// navigationHistoryResult is the result of Page.getNavigationHistory.
type navigationHistoryResult struct {
	CurrentIndex int `json:"currentIndex"`
	Entries      []struct {
		ID  int    `json:"id"`
		URL string `json:"url"`
	} `json:"entries"`
}

// ─── palmux client → server frames ───────────────────────────────────────────

type clientFrame struct {
	Type      string  `json:"type"`
	Kind      string  `json:"kind,omitempty"`
	EventType string  `json:"eventType,omitempty"`
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
	Button    string  `json:"button,omitempty"`
	ClickCount int    `json:"clickCount,omitempty"`
	DeltaX    float64 `json:"deltaX,omitempty"`
	DeltaY    float64 `json:"deltaY,omitempty"`
	Key       string  `json:"key,omitempty"`
	Text      string  `json:"text,omitempty"`
	TouchType string  `json:"touchType,omitempty"`
	URL       string  `json:"url,omitempty"`
}

// ─── palmux server → client frames ───────────────────────────────────────────

type serverFrame struct {
	Type string         `json:"type"`
	Data string         `json:"data,omitempty"`
	Meta *frameMeta     `json:"meta,omitempty"`
	URL  string         `json:"url,omitempty"`
	Msg  string         `json:"msg,omitempty"`
}

type frameMeta struct {
	DeviceWidth  int `json:"deviceWidth"`
	DeviceHeight int `json:"deviceHeight"`
}

// ─── CDPProxy ────────────────────────────────────────────────────────────────

// CDPProxy manages a single CDP WebSocket connection, proxying screencast
// frames to a palmux client and forwarding input/navigate commands back.
// It is created fresh per AttachScreencast invocation.
type CDPProxy struct {
	cdpAddr string
	log     *slog.Logger

	mu      sync.Mutex
	cdpConn *websocket.Conn
	idSeq   atomic.Int64

	// writeMu serialises ALL writes to cdpConn. coder/websocket forbids
	// concurrent Write — both pump goroutines (frame Ack from the reader,
	// input/navigate from the writer) call sendCDP, so every write must hold
	// this. Reads happen on ONE goroutine only (pumpCDPToClient).
	writeMu sync.Mutex

	// navReq tracks in-flight Page.getNavigationHistory requests (id → delta)
	// for back/forward. The single reader (pumpCDPToClient) matches the
	// response by id and issues navigateToHistoryEntry — so navigateHistory
	// never does its own Read (which would race the reader goroutine).
	navMu  sync.Mutex
	navReq map[int]int

	// Cache the current page URL so we can surface it in the "url" server frame.
	currentURL string
}

func newCDPProxy(cdpAddr string, log *slog.Logger) *CDPProxy {
	if log == nil {
		log = slog.Default()
	}
	return &CDPProxy{cdpAddr: cdpAddr, log: log, navReq: map[int]int{}}
}

func (p *CDPProxy) nextID() int {
	return int(p.idSeq.Add(1))
}

// sendCDP sends a CDP message and returns the request id. All writes to the CDP
// connection go through here so writeMu serialises them (concurrent Write is a
// coder/websocket violation).
func (p *CDPProxy) sendCDP(ctx context.Context, method string, params any) (int, error) {
	id := p.nextID()
	msg := cdpRequest{ID: id, Method: method, Params: params}
	b, err := json.Marshal(msg)
	if err != nil {
		return id, fmt.Errorf("cdp marshal %s: %w", method, err)
	}
	p.mu.Lock()
	cc := p.cdpConn
	p.mu.Unlock()
	if cc == nil {
		return id, fmt.Errorf("cdp: no connection")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return id, cc.Write(ctx, websocket.MessageText, b)
}

// ─── AttachScreencast ─────────────────────────────────────────────────────────

// AttachScreencast connects to the CDP endpoint, starts a screencast, and
// bidirectionally pumps frames between the CDP socket and the palmux client.
//
// It blocks until either end disconnects or ctx is cancelled.
// [AC-S62374c-2-1] [AC-S62374c-2-2] [AC-S62374c-2-3]
func (m *Manager) AttachScreencast(ctx context.Context, clientConn *websocket.Conn) {
	m.mu.Lock()
	state := m.state
	addr := m.cdpAddr
	m.mu.Unlock()

	if state != StateRunning || addr == "" {
		sendClientErr(ctx, clientConn, "browser not running")
		return
	}

	proxy := newCDPProxy(addr, m.log)
	if err := proxy.connect(ctx); err != nil {
		m.log.Warn("cdp proxy: connect", "err", err)
		sendClientErr(ctx, clientConn, fmt.Sprintf("cdp connect: %v", err))
		return
	}
	defer func() {
		proxy.mu.Lock()
		cc := proxy.cdpConn
		proxy.mu.Unlock()
		if cc != nil {
			_ = cc.CloseNow()
		}
	}()

	if err := proxy.startScreencast(ctx); err != nil {
		m.log.Warn("cdp proxy: startScreencast", "err", err)
		sendClientErr(ctx, clientConn, fmt.Sprintf("startScreencast: %v", err))
		return
	}

	proxyCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Two goroutines: CDP → client, client → CDP.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer cancel()
		proxy.pumpCDPToClient(proxyCtx, clientConn)
	}()

	go func() {
		defer wg.Done()
		defer cancel()
		proxy.pumpClientToCDP(proxyCtx, clientConn)
	}()

	wg.Wait()
}

// connect opens a CDP WebSocket to the first page target.
func (p *CDPProxy) connect(ctx context.Context) error {
	debugURL := fmt.Sprintf("http://%s:%d/json", p.cdpAddr, CDPPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, debugURL, nil)
	if err != nil {
		return fmt.Errorf("get /json: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("get /json: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var targets []struct {
		Type                string `json:"type"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		URL                  string `json:"url"`
	}
	if err := json.Unmarshal(body, &targets); err != nil {
		return fmt.Errorf("parse /json: %w", err)
	}

	wsURL := ""
	for _, t := range targets {
		if t.Type == "page" && t.WebSocketDebuggerURL != "" {
			wsURL = t.WebSocketDebuggerURL
			p.currentURL = t.URL
			break
		}
	}
	if wsURL == "" {
		return fmt.Errorf("no page target in CDP /json")
	}

	// chromium binds CDP to 127.0.0.1 and embeds that host in webSocketDebuggerUrl.
	// We reach it through the relay on <cdpAddr>:9222, so rewrite the ws host to
	// the bridge IP (dialing 127.0.0.1 from the palmux host would hit the wrong
	// machine). [AC-S62374c-2-1 real-mode fix]
	if u, perr := url.Parse(wsURL); perr == nil {
		u.Host = fmt.Sprintf("%s:%d", p.cdpAddr, CDPPort)
		wsURL = u.String()
	}

	// Must send Origin header — Chrome rejects without it.
	// [AC-S62374c-2-5 spike fact]
	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Origin": []string{fmt.Sprintf("http://%s:%d", p.cdpAddr, CDPPort)},
		},
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()

	cc, _, err := websocket.Dial(dialCtx, wsURL, opts)
	if err != nil {
		return fmt.Errorf("dial cdp ws %s: %w", wsURL, err)
	}
	cc.SetReadLimit(16 * 1024 * 1024) // 16 MB — screencast frames can be large

	p.mu.Lock()
	p.cdpConn = cc
	p.mu.Unlock()

	p.log.Info("cdp proxy: connected", "url", wsURL)
	return nil
}

// startScreencast sends Page.enable + Page.startScreencast.
func (p *CDPProxy) startScreencast(ctx context.Context) error {
	if _, err := p.sendCDP(ctx, "Page.enable", nil); err != nil {
		return fmt.Errorf("Page.enable: %w", err)
	}
	params := map[string]any{
		"format":        "jpeg",
		"quality":       80,
		"maxWidth":      1920,
		"maxHeight":     1200,
		"everyNthFrame": 1,
	}
	if _, err := p.sendCDP(ctx, "Page.startScreencast", params); err != nil {
		return fmt.Errorf("Page.startScreencast: %w", err)
	}
	return nil
}

// pumpCDPToClient reads CDP messages and forwards screencastFrame events to
// the palmux client connection.
func (p *CDPProxy) pumpCDPToClient(ctx context.Context, clientConn *websocket.Conn) {
	p.mu.Lock()
	cc := p.cdpConn
	p.mu.Unlock()

	for {
		_, b, err := cc.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				p.log.Debug("cdp proxy: cdp read closed", "err", err)
			}
			return
		}

		var msg cdpResponse
		if err := json.Unmarshal(b, &msg); err != nil {
			continue
		}

		// Response to an in-flight back/forward getNavigationHistory request?
		// Handle it here (single reader) so navigateHistory never reads itself.
		if msg.ID != 0 {
			p.navMu.Lock()
			delta, ok := p.navReq[msg.ID]
			if ok {
				delete(p.navReq, msg.ID)
			}
			p.navMu.Unlock()
			if ok {
				var hr navigationHistoryResult
				if json.Unmarshal(msg.Result, &hr) == nil {
					target := hr.CurrentIndex + delta
					if target >= 0 && target < len(hr.Entries) {
						_, _ = p.sendCDP(ctx, "Page.navigateToHistoryEntry", map[string]any{
							"entryId": hr.Entries[target].ID,
						})
					}
				}
				continue
			}
		}

		switch msg.Method {
		case "Page.screencastFrame":
			var params screencastFrameParams
			if err := json.Unmarshal(msg.Params, &params); err != nil {
				p.log.Warn("cdp proxy: parse screencastFrame", "err", err)
				continue
			}

			// Forward frame to client.
			frame := serverFrame{
				Type: "frame",
				Data: params.Data,
				Meta: &frameMeta{
					DeviceWidth:  params.Metadata.DeviceWidth,
					DeviceHeight: params.Metadata.DeviceHeight,
				},
			}
			if fb, err := json.Marshal(frame); err == nil {
				if err := clientConn.Write(ctx, websocket.MessageText, fb); err != nil {
					return
				}
			}

			// ACK the frame — required or Chrome stops sending.
			_, _ = p.sendCDP(ctx, "Page.screencastFrameAck", map[string]any{
				"sessionId": params.SessionID,
			})

		case "Page.navigatedWithinDocument", "Page.frameNavigated":
			// Try to surface the new URL to the client.
			var nav struct {
				Frame struct {
					URL string `json:"url"`
				} `json:"frame"`
				URL string `json:"url"`
			}
			if msg.Params != nil {
				_ = json.Unmarshal(msg.Params, &nav)
			}
			u := nav.Frame.URL
			if u == "" {
				u = nav.URL
			}
			if u != "" && u != p.currentURL {
				p.currentURL = u
				if ub, err := json.Marshal(serverFrame{Type: "url", URL: u}); err == nil {
					_ = clientConn.Write(ctx, websocket.MessageText, ub)
				}
			}
		}
	}
}

// pumpClientToCDP reads palmux client messages and dispatches them as CDP commands.
func (p *CDPProxy) pumpClientToCDP(ctx context.Context, clientConn *websocket.Conn) {
	for {
		_, b, err := clientConn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				p.log.Debug("cdp proxy: client read closed", "err", err)
			}
			return
		}

		var cf clientFrame
		if err := json.Unmarshal(b, &cf); err != nil {
			continue
		}

		switch cf.Type {
		case "input":
			p.dispatchInput(ctx, cf)

		case "navigate":
			if cf.URL != "" {
				_, _ = p.sendCDP(ctx, "Page.navigate", map[string]any{"url": cf.URL})
			}

		case "reload":
			_, _ = p.sendCDP(ctx, "Page.reload", nil)

		case "back":
			p.navigateHistory(ctx, -1)

		case "forward":
			p.navigateHistory(ctx, +1)
		}
	}
}

// dispatchInput converts a client input frame to CDP Input.dispatch* calls.
func (p *CDPProxy) dispatchInput(ctx context.Context, cf clientFrame) {
	switch cf.Kind {
	case "mouse":
		params := map[string]any{
			"type": cf.EventType,
			"x":    cf.X,
			"y":    cf.Y,
		}
		if cf.Button != "" {
			params["button"] = cf.Button
		}
		if cf.ClickCount > 0 {
			params["clickCount"] = cf.ClickCount
		}
		if cf.DeltaX != 0 {
			params["deltaX"] = cf.DeltaX
		}
		if cf.DeltaY != 0 {
			params["deltaY"] = cf.DeltaY
		}
		_, _ = p.sendCDP(ctx, "Input.dispatchMouseEvent", params)

	case "key":
		params := map[string]any{
			"type": cf.EventType,
			"key":  cf.Key,
		}
		if cf.Text != "" {
			params["text"] = cf.Text
		}
		_, _ = p.sendCDP(ctx, "Input.dispatchKeyEvent", params)

	case "touch":
		touchType := cf.TouchType
		if touchType == "" {
			touchType = "touchStart"
		}
		touchPoint := map[string]any{"x": cf.X, "y": cf.Y}
		params := map[string]any{
			"type":        touchType,
			"touchPoints": []any{touchPoint},
		}
		_, _ = p.sendCDP(ctx, "Input.dispatchTouchEvent", params)
	}
}

// navigateHistory implements back (-1) / forward (+1). It sends
// Page.getNavigationHistory and registers the request id in navReq; the single
// reader goroutine (pumpCDPToClient) matches the response and issues the
// Page.navigateToHistoryEntry. This avoids a second concurrent Read on cdpConn
// (which coder/websocket forbids and which would race the screencast reader).
func (p *CDPProxy) navigateHistory(ctx context.Context, delta int) {
	id, err := p.sendCDP(ctx, "Page.getNavigationHistory", nil)
	if err != nil {
		return
	}
	p.navMu.Lock()
	p.navReq[id] = delta
	p.navMu.Unlock()
}

// ─── Navigate (REST) ──────────────────────────────────────────────────────────

// NavigatePage sends Page.navigate to the CDP endpoint (no client WS required).
// Used by the POST .../browser/navigate REST handler.
// [AC-S62374c-2-3]
func (m *Manager) NavigatePage(ctx context.Context, url string) error {
	m.mu.Lock()
	state := m.state
	addr := m.cdpAddr
	m.mu.Unlock()

	if state != StateRunning || addr == "" {
		return fmt.Errorf("browser not running")
	}

	proxy := newCDPProxy(addr, m.log)
	if err := proxy.connect(ctx); err != nil {
		return fmt.Errorf("cdp connect: %w", err)
	}
	defer func() {
		proxy.mu.Lock()
		cc := proxy.cdpConn
		proxy.mu.Unlock()
		if cc != nil {
			_ = cc.CloseNow()
		}
	}()

	_, err := proxy.sendCDP(ctx, "Page.navigate", map[string]any{"url": url})
	return err
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func sendClientErr(ctx context.Context, conn *websocket.Conn, msg string) {
	b, _ := json.Marshal(serverFrame{Type: "error", Msg: msg})
	_ = conn.Write(ctx, websocket.MessageText, b)
}
