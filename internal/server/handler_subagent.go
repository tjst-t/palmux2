package server

// S021: subagent worktree lifecycle handlers — cleanup + promote.

import (
	"net/http"
)

// cleanupSubagentRequest is the body of POST /worktrees/cleanup-subagent.
//
// `dryRun=true` returns the candidate list without removing anything;
// `branchNames` (optional) narrows the candidate set when the user has
// unchecked rows in the dialog. `thresholdDays` (optional) overrides the
// global setting for one-off invocations (the FE rarely sets this).
type cleanupSubagentRequest struct {
	DryRun        bool     `json:"dryRun"`
	BranchNames   []string `json:"branchNames,omitempty"`
	ThresholdDays int      `json:"thresholdDays,omitempty"`
}

func (h *handlers) cleanupSubagentWorktrees(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	var req cleanupSubagentRequest
	// Body is optional — POST with empty body means "run with defaults".
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, err)
			return
		}
	}

	if req.DryRun {
		candidates, err := h.store.ListStaleSubagentWorktrees(r.Context(), repoID, req.ThresholdDays)
		if err != nil {
			writeErr(w, err)
			return
		}
		threshold := req.ThresholdDays
		if threshold <= 0 {
			threshold = h.store.Settings().SubagentStaleAfterDays()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"thresholdDays": threshold,
			"candidates":    candidates,
		})
		return
	}

	result, err := h.store.CleanupSubagentWorktrees(r.Context(), repoID, req.BranchNames, req.ThresholdDays)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Always 200 — partial failures are reported in `failed[]`.
	writeJSON(w, http.StatusOK, result)
}

// promoteSubagentBranch handles POST /branches/{branchId}/promote-subagent.
// Moves the worktree to gwq's standard path AND records it as user-opened
// (so the Drawer reclassifies it from `subagent` to `my`).
func (h *handlers) promoteSubagentBranch(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")

	dest, err := h.store.PromoteSubagentBranch(r.Context(), repoID, branchID)
	if err != nil {
		writeErr(w, err)
		return
	}
	updated, err := h.store.Branch(repoID, branchID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"branch":      updated,
		"destination": dest,
	})
}
