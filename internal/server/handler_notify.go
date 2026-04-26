package server

import (
	"net/http"

	"github.com/tjst-t/palmux2/internal/notify"
)

func (h *handlers) ingestNotification(w http.ResponseWriter, r *http.Request) {
	if h.notify == nil {
		http.Error(w, "notifications disabled", http.StatusServiceUnavailable)
		return
	}
	var req notify.IngestRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	repoID, branchID, err := h.notify.Ingest(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"repoId":   repoID,
		"branchId": branchID,
	})
}

type clearNotifyRequest struct {
	RepoID   string `json:"repoId"`
	BranchID string `json:"branchId"`
}

func (h *handlers) clearNotifications(w http.ResponseWriter, r *http.Request) {
	if h.notify == nil {
		http.Error(w, "notifications disabled", http.StatusServiceUnavailable)
		return
	}
	var req clearNotifyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.RepoID == "" || req.BranchID == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "repoId and branchId are required"})
		return
	}
	state := h.notify.Clear(req.RepoID, req.BranchID)
	writeJSON(w, http.StatusOK, state)
}

func (h *handlers) listNotifications(w http.ResponseWriter, _ *http.Request) {
	if h.notify == nil {
		writeJSON(w, http.StatusOK, map[string]notify.BranchState{})
		return
	}
	writeJSON(w, http.StatusOK, h.notify.All())
}
