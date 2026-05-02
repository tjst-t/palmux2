package server

import (
	"net/http"
)

type addTabRequest struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type renameTabRequest struct {
	Name string `json:"name"`
}

type reorderTabsRequest struct {
	Order []string `json:"order"`
}

func (h *handlers) listTabs(w http.ResponseWriter, r *http.Request) {
	branch, err := h.store.Branch(r.PathValue("repoId"), r.PathValue("branchId"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, branch.TabSet)
}

func (h *handlers) addTab(w http.ResponseWriter, r *http.Request) {
	var req addTabRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	tab, err := h.store.AddTab(r.Context(), r.PathValue("repoId"), r.PathValue("branchId"), req.Type, req.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, tab)
}

func (h *handlers) removeTab(w http.ResponseWriter, r *http.Request) {
	if err := h.store.RemoveTab(r.Context(), r.PathValue("repoId"), r.PathValue("branchId"), r.PathValue("tabId")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) renameTab(w http.ResponseWriter, r *http.Request) {
	var req renameTabRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.store.RenameTab(r.Context(), r.PathValue("repoId"), r.PathValue("branchId"), r.PathValue("tabId"), req.Name); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// S020: PUT /api/repos/{repoId}/branches/{branchId}/tabs/order
// Body: {"order": ["bash:dev", "bash:test", ...]}.
// All IDs must share one Multiple()=true type (cross-group reorder
// is forbidden — Files/Git/Claude/Bash each have their own group).
func (h *handlers) reorderTabs(w http.ResponseWriter, r *http.Request) {
	var req reorderTabsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.store.ReorderTabs(r.Context(), r.PathValue("repoId"), r.PathValue("branchId"), req.Order); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
