package server

import (
	"errors"
	"net/http"

	"github.com/tjst-t/palmux2/internal/deploy"
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

// postSelfUpdateRebuild kicks the appliance HOST update on a NixOS appliance:
// `nix flake update palmux` (bump the pin) + `nixos-rebuild switch`, run by the
// verb-limited palmux-rebuild-update.service (S673a42-2). The GUI observes
// completion via the same S6ab0ed WS-drop → /health reconnect handshake (the
// switch restarts palmux2 onto the new pin → new version) and polls
// GET /api/selfupdate/rebuild to catch a pre-restart failure. NixOS-only; 409
// elsewhere (the Ubuntu path is the "Update all" button above).
func (h *handlers) postSelfUpdateRebuild(w http.ResponseWriter, r *http.Request) {
	if h.selfupdate == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "self-update unavailable"})
		return
	}
	if !selfupdate.IsNixOSHost() {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":    false,
			"error": "GUI からの nixos-rebuild 更新は NixOS アプライアンス専用です。この形態では『すべてまとめて更新』を使ってください。",
		})
		return
	}
	if err := deploy.TriggerRebuildUpdate(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":      true,
		"status":  "triggered",
		"message": "本体更新 (nix flake update palmux + nixos-rebuild switch) を開始しました。切替後 palmux2 は再起動し、この画面は自動で再接続します。",
	})
}

// getSelfUpdateRebuild reports the version-update oneshot's state so the GUI can
// detect a failure that does NOT restart palmux2 (e.g. a `nix flake update` / eval
// error aborts before the switch). NixOS-only.
func (h *handlers) getSelfUpdateRebuild(w http.ResponseWriter, r *http.Request) {
	if h.selfupdate == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "self-update unavailable"})
		return
	}
	if !selfupdate.IsNixOSHost() {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "nixos-rebuild update is for the NixOS appliance only"})
		return
	}
	st, err := deploy.QueryRebuildUpdate(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// postSelfUpdateImageInstall kicks a background palmux-ws image fetch+import
// (`palmux runtime install`) so the appliance can update the incus image from the
// GUI (S673a42-3). Returns 202; the GUI polls GET /api/selfupdate/image-install.
func (h *handlers) postSelfUpdateImageInstall(w http.ResponseWriter, _ *http.Request) {
	if h.selfupdate == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "self-update unavailable"})
		return
	}
	if err := h.selfupdate.RunImageInstall(); err != nil {
		if errors.Is(err, selfupdate.ErrImageInstallInFlight) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":      true,
		"status":  "started",
		"message": "palmux-ws image の取得を開始しました。完了後、コンテナは各 Workspace の『Update container』で再生成できます。",
	})
}

// getSelfUpdateImageInstall reports the image-fetch job state (running/done/error).
func (h *handlers) getSelfUpdateImageInstall(w http.ResponseWriter, _ *http.Request) {
	if h.selfupdate == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "self-update unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, h.selfupdate.ImageInstallStatus())
}
