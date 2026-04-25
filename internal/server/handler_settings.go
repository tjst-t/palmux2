package server

import (
	"net/http"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/store"
)

func (h *handlers) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.store.Settings().Get())
}

func (h *handlers) patchSettings(w http.ResponseWriter, r *http.Request) {
	var req config.Settings
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	updated, err := h.store.Settings().Patch(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.store.Hub().Publish(store.Event{Type: store.EventSettings, Payload: updated})
	writeJSON(w, http.StatusOK, updated)
}
