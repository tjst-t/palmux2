package server

import (
	"errors"
	"net/http"
	"strings"

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
		// S032: palette.userCommands validation errors surface as 400.
		if isValidationErr(err) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		writeErr(w, err)
		return
	}
	h.store.Hub().Publish(store.Event{Type: store.EventSettings, Payload: updated})
	writeJSON(w, http.StatusOK, updated)
}

// isValidationErr returns true when err is a user-data validation failure
// (e.g. malformed UserCommand). These are 400 Bad Request, not 500.
func isValidationErr(err error) bool {
	if err == nil {
		return false
	}
	// Sentinel errors from store.
	if errors.Is(err, store.ErrInvalidArg) {
		return true
	}
	// config.Patch wraps validation errors with "config: patch: ...".
	// Sa53137-1-3 broadened this from userCommand-only to every field-level
	// validation failure (invalid claude.default_mode / defaultRuntime.kind /
	// branchSortOrder / userCommand) so the GUI shows a 400 + message and the
	// save is rejected rather than surfacing as a 500.
	msg := err.Error()
	return strings.Contains(msg, "config: patch:")
}
