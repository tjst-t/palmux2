// Package browser — HTTP handlers for the Browser tab (S62374c-1/2).
//
// S62374c-2 adds:
//   GET  WS .../tabs/browser/attach   → CDP screencast + input proxy
//   POST    .../tabs/browser/navigate → {url} → {ok:true}
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

// attach handles WS .../tabs/browser/attach — CDP screencast + input proxy.
// [AC-S62374c-2-1] [AC-S62374c-2-2] [AC-S62374c-2-3] [AC-S62374c-2-5]
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
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		h.p.log.Warn("browser: ws accept", "err", err)
		return
	}
	conn.SetReadLimit(256 * 1024) // client input frames are small
	defer conn.CloseNow()

	ctx := r.Context()
	mgr.AttachScreencast(ctx, conn)
}

// navigate handles POST .../tabs/browser/navigate.
// Body: {"url":"..."}, Response: {"ok":true}
// [AC-S62374c-2-3]
func (h *handler) navigate(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")

	if !h.p.isIncus(repoID, branchID) {
		writeErr(w, http.StatusBadRequest,
			"Browser tab is only available for incus-container workspaces")
		return
	}

	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
		writeErr(w, http.StatusBadRequest, "navigate: body must be {\"url\":\"...\"}")
		return
	}

	mgr := h.p.managerFor(repoID, branchID)
	if mgr == nil {
		writeErr(w, http.StatusConflict, "browser not running")
		return
	}

	if err := mgr.NavigatePage(r.Context(), body.URL); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
