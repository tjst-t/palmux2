package server

import (
	"errors"
	"net/http"

	"github.com/tjst-t/palmux2/internal/selfupdate"
)

// S6ab0ed: self-update HTTP surface.
//
//	GET  /api/selfupdate       — cached detection snapshot (components current→latest)
//	POST /api/selfupdate/run   — trigger the one-click "Update all" (detached)
//
// Detection is privilege-free and always available when the service is wired.
// Execution delegates to the install.sh-generated ~/update-palmux2.sh, which is
// only present on Nix-managed installs (decisions PD-4); manual-override
// installs get a typed 409 telling the GUI to show "manual update" guidance.

func (h *handlers) getSelfUpdate(w http.ResponseWriter, _ *http.Request) {
	if h.selfupdate == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "self-update unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, h.selfupdate.Current())
}

func (h *handlers) postSelfUpdateRun(w http.ResponseWriter, r *http.Request) {
	if h.selfupdate == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "self-update unavailable"})
		return
	}
	if !h.selfupdate.NixManaged() {
		// AC-S6ab0ed-2-4: manual-override install — do not attempt; tell the GUI.
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":         false,
			"nixManaged": false,
			"error":      selfupdate.ErrNotNixManaged.Error(),
		})
		return
	}
	if err := h.selfupdate.RunUpdate(r.Context()); err != nil {
		if errors.Is(err, selfupdate.ErrNotNixManaged) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":         false,
				"nixManaged": false,
				"error":      err.Error(),
			})
			return
		}
		if errors.Is(err, selfupdate.ErrUpdateInFlight) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	// The update helper now runs detached: it will perform the home-manager
	// switch and restart palmux itself. The GUI observes completion via the
	// WS-drop → /api/health version reconnect handshake (AC-S6ab0ed-2-2).
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"nixManaged": true,
		"message":    "更新を開始しました。本体更新後 palmux は自動で再起動し、この画面は数秒で再接続します。",
	})
}
