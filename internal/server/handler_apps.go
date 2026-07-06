package server

import (
	"net/http"
)

// S41bdf2: app card model HTTP surface (1アプリ=1カード).
//
//	GET  /api/apps            — catalog ∪ installed cards with live state + share
//	POST /api/apps/install    — mark installed → generate drop-in → kick rebuild
//	POST /api/apps/uninstall  — remove → regenerate drop-in → kick rebuild
//	POST /api/apps/share      — toggle the auth folder in shared_dirs (hot)
//	POST /api/apps/validate   — `nix eval` a user-defined nixpkgs name (no rebuild)
//
// Install state lives in a dedicated store (apps.json); share state is DERIVED
// from [workspace].shared_dirs so the card and the generic 共有フォルダ list are a
// single source (AC-S41bdf2-2-1). A nil controller returns 503.

func (h *handlers) getApps(w http.ResponseWriter, r *http.Request) {
	if h.apps == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "apps plane unavailable"})
		return
	}
	v, err := h.apps.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// appInstallRequest installs a catalog app (id only) or a custom app (id +
// package + optional authPath). For catalog apps package/authPath are ignored
// (the catalog is authoritative).
type appInstallRequest struct {
	ID       string `json:"id"`
	Package  string `json:"package"`
	AuthPath string `json:"authPath"`
}

func (h *handlers) postAppInstall(w http.ResponseWriter, r *http.Request) {
	if h.apps == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "apps plane unavailable"})
		return
	}
	var req appInstallRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.apps.Install(r.Context(), req.ID, req.Package, req.AuthPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type appIDRequest struct {
	ID string `json:"id"`
}

func (h *handlers) postAppUninstall(w http.ResponseWriter, r *http.Request) {
	if h.apps == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "apps plane unavailable"})
		return
	}
	var req appIDRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.apps.Uninstall(r.Context(), req.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type appShareRequest struct {
	ID string `json:"id"`
	On bool   `json:"on"`
}

func (h *handlers) postAppShare(w http.ResponseWriter, r *http.Request) {
	if h.apps == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "apps plane unavailable"})
		return
	}
	var req appShareRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	n, err := h.apps.Share(r.Context(), req.ID, req.On)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"shared":     req.On,
		"containers": n,
	})
}

type appValidateRequest struct {
	Package string `json:"package"`
}

func (h *handlers) postAppValidate(w http.ResponseWriter, r *http.Request) {
	if h.apps == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "apps plane unavailable"})
		return
	}
	var req appValidateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.apps.Validate(r.Context(), req.Package))
}
