package server

import (
	"net/http"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/worktree"
)

func (h *handlers) listBranches(w http.ResponseWriter, r *http.Request) {
	repo, err := h.store.Repo(r.PathValue("repoId"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repo.OpenBranches)
}

// branchPickerEntry is one row in `GET /api/repos/{repoId}/branch-picker`.
type branchPickerEntry struct {
	Name     string `json:"name"`
	State    string `json:"state"` // "open" | "local" | "remote"
	BranchID string `json:"branchId,omitempty"`
}

func (h *handlers) branchPicker(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	repo, err := h.store.Repo(repoID)
	if err != nil {
		writeErr(w, err)
		return
	}
	openByName := map[string]string{}
	for _, b := range repo.OpenBranches {
		openByName[b.Name] = b.ID
	}
	all, err := worktree.ListAllBranches(r.Context(), repo.FullPath)
	if err != nil {
		writeErr(w, err)
		return
	}
	seen := map[string]bool{}
	var entries []branchPickerEntry
	for _, b := range all {
		if seen[b.Name] {
			continue
		}
		seen[b.Name] = true
		state := "local"
		if b.IsRemote {
			state = "remote"
		}
		if id, isOpen := openByName[b.Name]; isOpen {
			entries = append(entries, branchPickerEntry{Name: b.Name, State: "open", BranchID: id})
			continue
		}
		entries = append(entries, branchPickerEntry{Name: b.Name, State: state})
	}
	writeJSON(w, http.StatusOK, entries)
}

type openBranchRequest struct {
	BranchName string `json:"branchName"`
}

func (h *handlers) openBranch(w http.ResponseWriter, r *http.Request) {
	var req openBranchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	branch, err := h.store.OpenBranch(r.Context(), r.PathValue("repoId"), req.BranchName)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, branch)
}

func (h *handlers) closeBranch(w http.ResponseWriter, r *http.Request) {
	if err := h.store.CloseBranch(r.Context(), r.PathValue("repoId"), r.PathValue("branchId")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// promoteBranch (S015) records the branch in `repos.json#userOpenedBranches`
// so the Drawer moves it from `unmanaged` (or `subagent`) to `my`. The
// branch must already be Open. Idempotent — promoting an already-promoted
// branch returns 200 with the current snapshot.
func (h *handlers) promoteBranch(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	branch, err := h.store.Branch(repoID, branchID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.store.PromoteBranch(r.Context(), repoID, branch.Name); err != nil {
		writeErr(w, err)
		return
	}
	updated, err := h.store.Branch(repoID, branchID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// demoteBranch (S015) removes the branch from `userOpenedBranches`. The
// resulting Drawer category falls back to `subagent` (path matches an
// auto pattern) or `unmanaged` (otherwise). Idempotent.
func (h *handlers) demoteBranch(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	branch, err := h.store.Branch(repoID, branchID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.store.DemoteBranch(r.Context(), repoID, branch.Name); err != nil {
		writeErr(w, err)
		return
	}
	updated, err := h.store.Branch(repoID, branchID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// silence unused import in build configurations
var _ = domain.RepoSlugID
