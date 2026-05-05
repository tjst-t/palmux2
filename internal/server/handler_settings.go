package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/netns"
	"github.com/tjst-t/palmux2/internal/store"
)

func (h *handlers) getSettings(w http.ResponseWriter, _ *http.Request) {
	s := h.store.Settings().Get()
	// S034: inject runtime caddy availability into the response.
	// The Available field is not persisted — it reflects whether the caddy
	// binary was found at startup. We always surface a networkIsolation section
	// so the frontend can read caddy.available without nil checks.
	// IMPORTANT: deep-copy the NetworkIsolation pointer before mutating so
	// we do not accidentally write the runtime field back into the store.
	if s.NetworkIsolation == nil {
		s.NetworkIsolation = &config.NetworkIsolationSettings{}
	} else {
		copy := *s.NetworkIsolation
		s.NetworkIsolation = &copy
	}
	s.NetworkIsolation.Caddy.Available = h.caddyAvailable
	writeJSON(w, http.StatusOK, s)
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
	// S034: propagate Caddy config changes to the live CaddyIntegration.
	if h.caddy != nil && updated.NetworkIsolation != nil {
		c := updated.NetworkIsolation.Caddy
		h.caddy.UpdateConfig(netns.CaddyConfig{
			Enabled:      c.Enabled,
			FQDNTemplate: c.FQDNTemplate,
			ConfigPath:   c.ConfigPath,
			ReloadCmd:    c.ReloadCmd,
		})
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
	// S032: config.Patch wraps validation errors with "config: patch: userCommand..."
	msg := err.Error()
	return strings.Contains(msg, "config: patch:") && strings.Contains(msg, "userCommand")
}
