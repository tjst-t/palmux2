// Package ports — HTTP handlers for the Ports tab (See8bd4-3).
package ports

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/tjst-t/palmux2/internal/store"
)

type handler struct {
	st *store.Store
}

func newHandler(s *store.Store) *handler { return &handler{st: s} }

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

// list handles GET .../ports → {runtimeKind, ports}.
func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	writeJSON(w, http.StatusOK, h.st.WorkspacePortsView(repoID, branchID))
}

type exposeRequest struct {
	Public bool `json:"public"`
}

type exposeResponse struct {
	Port      int    `json:"port"`
	Public    bool   `json:"public"`
	PublicURL string `json:"publicUrl"`
}

// expose handles POST .../ports/{port}/expose.
func (h *handler) expose(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid port")
		return
	}

	var req exposeRequest
	if r.Body != nil {
		// An empty body is allowed (defaults public=false).
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	url, err := h.st.ExposeWorkspacePort(r.Context(), repoID, branchID, port, req.Public)
	if err != nil {
		if err == store.ErrPortsUnsupported {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, exposeResponse{Port: port, Public: req.Public, PublicURL: url})
}

// unexpose handles DELETE .../ports/{port}/expose.
func (h *handler) unexpose(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid port")
		return
	}
	if err := h.st.UnexposeWorkspacePort(r.Context(), repoID, branchID, port); err != nil {
		if err == store.ErrPortsUnsupported {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
