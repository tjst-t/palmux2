// Package browser — HTTP handlers for the Browser tab.
//
// noVNC rework: the WS /attach route now calls AttachVNC (raw RFB byte-pipe)
// instead of the old CDP screencast proxy. The POST /navigate route is removed
// (chromium's own address bar handles navigation in headful mode).
//
// Routes:
//
//	GET  .../tabs/browser/state   → StateView
//	POST .../tabs/browser/start   → StartResponse
//	POST .../tabs/browser/stop    → StopResponse
//	GET  WS .../browser/attach    → VNC byte-pipe (subprotocol "binary")
package browser

import (
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
)

type handler struct {
	p *Provider
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// state handles GET .../tabs/browser/state.
// Returns StateView with available=false when the runtime is not incus-container.
// [AC-S62374c-1-4] [AC-S62374c-1-6]
func (h *handler) state(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")

	// Fast path: host runtime → not available.
	if !h.p.isIncus(repoID, branchID) {
		writeJSON(w, http.StatusOK, StateView{
			State:        StateStopped,
			CDPReachable: false,
			Available:    false,
		})
		return
	}

	mgr := h.p.managerFor(repoID, branchID)
	if mgr == nil {
		// Tab present but no manager yet (e.g. OnBranchOpen hasn't fired or
		// the provider was registered after the branch was opened). Create lazily.
		mgr = h.p.getOrCreateManager(repoID, branchID)
	}

	writeJSON(w, http.StatusOK, mgr.State(r.Context()))
}

// start handles POST .../tabs/browser/start.
// [AC-S62374c-1-1]
func (h *handler) start(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")

	if !h.p.isIncus(repoID, branchID) {
		writeErr(w, http.StatusBadRequest,
			"Browser tab is only available for incus-container workspaces; use portman for host runtimes")
		return
	}

	mgr := h.p.managerFor(repoID, branchID)
	if mgr == nil {
		mgr = h.p.getOrCreateManager(repoID, branchID)
	}

	resp, err := mgr.Start(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// stop handles POST .../tabs/browser/stop.
// [AC-S62374c-1-1]
func (h *handler) stop(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")

	if !h.p.isIncus(repoID, branchID) {
		writeErr(w, http.StatusBadRequest,
			"Browser tab is only available for incus-container workspaces")
		return
	}

	mgr := h.p.managerFor(repoID, branchID)
	if mgr == nil {
		// No manager → already stopped.
		writeJSON(w, http.StatusOK, StopResponse{State: StateStopped})
		return
	}

	resp, err := mgr.Stop(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// attach handles WS .../tabs/browser/attach — raw VNC byte-pipe for noVNC.
//
// noVNC requests the "binary" WebSocket subprotocol; we must accept it. Raw RFB
// bytes flow in both directions without any additional framing.
func (h *handler) attach(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")

	if !h.p.isIncus(repoID, branchID) {
		http.Error(w, "Browser tab is only available for incus-container workspaces", http.StatusBadRequest)
		return
	}

	mgr := h.p.managerFor(repoID, branchID)
	if mgr == nil {
		mgr = h.p.getOrCreateManager(repoID, branchID)
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// noVNC requests the "binary" subprotocol; palmux must accept it or the
		// browser-side RFB handshake fails.
		Subprotocols:   []string{"binary"},
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		h.p.log.Warn("browser: ws accept", "err", err)
		return
	}
	// noVNC sends large initial framebuffer updates; 16 MB covers worst-case.
	conn.SetReadLimit(16 * 1024 * 1024)
	defer conn.CloseNow()

	ctx := r.Context()
	mgr.AttachVNC(ctx, conn)
}
