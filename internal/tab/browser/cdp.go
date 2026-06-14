// Package browser — VNC byte-pipe and CDP helpers.
//
// noVNC rework: the CDP screencast proxy (AttachScreencast, CDPProxy,
// pumpCDPToClient, pumpClientToCDP, navigate/back/forward/reload) is replaced
// by a raw binary byte-pipe between the palmux WebSocket client and the x11vnc
// TCP socket inside the container. noVNC on the browser side speaks raw RFB
// binary over the WebSocket, so palmux just needs to forward bytes in both
// directions without any framing.
//
// CheckCDP and the cdpRelayScript (for bridging bridgeIP:9222→127.0.0.1:9222)
// are kept: Story 3 (palmux-browser CLI) uses CDP for automation.
package browser

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// ─── AttachVNC ────────────────────────────────────────────────────────────────

// AttachVNC bridges the client WebSocket connection to the x11vnc TCP socket
// inside the container. It pumps raw RFB binary in both directions until either
// end closes or ctx is cancelled. noVNC speaks binary RFB over WebSocket, so no
// framing or parsing is needed — bytes go straight through.
//
// The dial address is <cdpAddr>:5900 (bridge IP, same address used for CDP).
// x11vnc is launched with no -listen flag so it binds 0.0.0.0:5900 and is
// reachable from the host on the bridge interface.
func (m *Manager) AttachVNC(ctx context.Context, clientConn *websocket.Conn) {
	m.mu.Lock()
	state := m.state
	addr := m.cdpAddr
	m.mu.Unlock()

	if state != StateRunning || addr == "" {
		// Refuse the connection cleanly: close with 1011 (internal error).
		_ = clientConn.Close(websocket.StatusInternalError, "browser not running")
		return
	}

	vncAddr := net.JoinHostPort(addr, fmt.Sprintf("%d", VNCPort))
	tcpConn, err := net.DialTimeout("tcp", vncAddr, 10*time.Second)
	if err != nil {
		m.log.Warn("browser: VNC dial failed", "inst", m.inst, "addr", vncAddr, "err", err)
		_ = clientConn.Close(websocket.StatusInternalError, fmt.Sprintf("vnc dial: %v", err))
		return
	}
	defer tcpConn.Close()

	m.log.Info("browser: VNC attach", "inst", m.inst, "vnc", vncAddr)

	pipeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// WS → TCP: read binary frames from the WebSocket client and write to x11vnc.
	go func() {
		defer cancel()
		for {
			_, data, err := clientConn.Read(pipeCtx)
			if err != nil {
				return
			}
			if _, err := tcpConn.Write(data); err != nil {
				return
			}
		}
	}()

	// TCP → WS: read from x11vnc and send as binary WebSocket frames.
	buf := make([]byte, 65536)
	for {
		n, err := tcpConn.Read(buf)
		if err != nil {
			if err != io.EOF && pipeCtx.Err() == nil {
				m.log.Debug("browser: VNC TCP read closed", "inst", m.inst, "err", err)
			}
			return
		}
		if err := clientConn.Write(pipeCtx, websocket.MessageBinary, buf[:n]); err != nil {
			return
		}
	}
}

// ─── CheckCDP ─────────────────────────────────────────────────────────────────

// CheckCDP does a quick HTTP GET to /json/version on the CDP endpoint and
// returns true if the response is HTTP 200. Used for state detection and by
// the CDP relay readiness check.
// [AC-S62374c-1-6]
func CheckCDP(ctx context.Context, addr string) bool {
	url := fmt.Sprintf("http://%s:%d/json/version", addr, CDPPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
