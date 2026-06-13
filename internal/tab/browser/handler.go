// Package browser — HTTP handlers for the Browser tab (S62374c-1).
package browser

import (
	"encoding/json"
	"net/http"
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
