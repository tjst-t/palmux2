package server

// handler_branch_settings.go implements the S1f75ec-2 branch-level settings
// endpoints:
//
//	GET  /api/repos/{repoId}/branches/{branchId}/settings
//	PATCH /api/repos/{repoId}/branches/{branchId}/settings
//
// These endpoints persist per-branch settings (currently just claude_mode) in
// repos.json via config.RepoStore.

import (
	"net/http"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/store"
)

// branchSettingsResponse is the JSON shape returned by GET/PATCH .../settings.
type branchSettingsResponse struct {
	ClaudeMode string `json:"claude_mode"`
}

// branchSettingsPatch is the JSON body accepted by PATCH .../settings.
type branchSettingsPatch struct {
	ClaudeMode string `json:"claude_mode"`
}

// getBranchSettings handles GET /api/repos/{repoId}/branches/{branchId}/settings.
// Returns the branch's persisted settings (claude_mode).
// If no entry exists yet for the branch, the migration default "agent" is returned
// (preserves backwards compat for branches opened before Sprint C).
func (h *handlers) getBranchSettings(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")

	// Validate that the branch exists.
	if _, err := h.store.Branch(repoID, branchID); err != nil {
		writeErr(w, err)
		return
	}

	bs := h.store.RepoStore().GetBranchSettings(repoID, branchID)
	writeJSON(w, http.StatusOK, branchSettingsResponse{ClaudeMode: string(bs.ClaudeMode)})
}

// patchBranchSettings handles PATCH /api/repos/{repoId}/branches/{branchId}/settings.
// Accepts {"claude_mode": "agent" | "tui"} and persists to repos.json.
func (h *handlers) patchBranchSettings(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")

	// Validate that the branch exists.
	if _, err := h.store.Branch(repoID, branchID); err != nil {
		writeErr(w, err)
		return
	}

	var body branchSettingsPatch
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, store.ErrInvalidArg)
		return
	}

	mode := config.ClaudeMode(body.ClaudeMode)
	if err := h.store.RepoStore().SetBranchClaudeMode(repoID, branchID, mode); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, branchSettingsResponse{ClaudeMode: string(mode)})
}
