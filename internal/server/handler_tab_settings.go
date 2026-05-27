package server

// handler_tab_settings.go implements the Sadf90e tab-scoped settings endpoints:
//
//	GET   /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/settings
//	PATCH /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/settings
//
// Replaces the S1f75ec-2 branch-level claude_mode endpoints. Mode is now a
// property of the individual Claude tab, set at tab-add time from
// settings.claude.default_mode and mutable via PATCH from the FE.

import (
	"net/http"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/store"
)

type tabSettingsResponse struct {
	ClaudeMode string `json:"claude_mode"`
}

type tabSettingsPatch struct {
	ClaudeMode string `json:"claude_mode"`
}

// getTabSettings — GET /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/settings.
//
// Returns 200 {claude_mode: "agent"|"tui"} when the tab has a settings entry,
// or when the tab exists but no entry has been initialised yet (in which case
// we fall back to agent — the same value GetTabClaudeMode synthesises).
// Returns 404 when the underlying tab does not exist on the branch.
func (h *handlers) getTabSettings(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	tabID := r.PathValue("tabId")

	if _, err := h.store.Tab(repoID, branchID, tabID); err != nil {
		writeErr(w, err)
		return
	}

	mode := h.store.RepoStore().GetTabClaudeMode(repoID, branchID, tabID)
	writeJSON(w, http.StatusOK, tabSettingsResponse{ClaudeMode: string(mode)})
}

// patchTabSettings — PATCH /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/settings.
//
// Accepts {"claude_mode": "agent" | "tui"}. Persists the new mode via
// RepoStore.SetTabClaudeMode and returns the updated value. Any other body
// shape (e.g. legacy clients trying to write the old branch-level
// {"claude_mode": ...}) is rejected with 400 via store.ErrInvalidArg —
// callers can still target the same path but the *route key changed* (path
// segment /tabs/{tabId}/ inserted), so legacy clients see a 404 from the
// router before reaching this handler.
func (h *handlers) patchTabSettings(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	tabID := r.PathValue("tabId")

	if _, err := h.store.Tab(repoID, branchID, tabID); err != nil {
		writeErr(w, err)
		return
	}

	var body tabSettingsPatch
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, store.ErrInvalidArg)
		return
	}

	mode := config.ClaudeMode(body.ClaudeMode)
	if err := h.store.RepoStore().SetTabClaudeMode(repoID, branchID, tabID, mode); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, tabSettingsResponse{ClaudeMode: string(mode)})
}
