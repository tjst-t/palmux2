// Package server: handler_runtime.go (Sdd4ce1)
//
// HTTP endpoints supporting the WorkspaceRuntime UI:
//
//   GET  /api/runtime/lxd/available
//        Returns { available: bool, reason?: string } so the FE can grey
//        out lxd-* runtime options in the Open Repository / Workspace
//        creation modals (AC-Sdd4ce1-5-1).
//
//   PATCH /api/repos/{repoId}/default-runtime
//        Body: runtime.Config — sets the per-repo default runtime
//        (AC-Sdd4ce1-6-3). Pass {kind:""} to clear.
//
//   PATCH /api/repos/{repoId}/branches/{branchId}/runtime
//        Body: runtime.Config — sets the per-Workspace runtime override
//        (AC-Sdd4ce1-6-1). Pass {kind:""} to clear and inherit.
//
//   GET  /api/repos/{repoId}/branches/{branchId}/runtime
//        Returns the resolved runtime config + current state, useful for
//        the Header chip and the runtime-switch confirmation dialog.

package server

import (
	"net/http"
	"os/exec"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/runtime"
)

// runtimeAvailableResponse is returned by GET /api/runtime/lxd/available.
type runtimeAvailableResponse struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// runtimeAvailableLXD performs a cheap detection: `lxc` binary on PATH AND
// `lxc info` succeeds. The latter ensures the LXD daemon is reachable
// (`lxc info` exits non-zero if the daemon socket is unreachable / user
// missing from the lxd group). The result is fresh each call — small cost
// (~10ms) and no caching means a host that just installed LXD via snap is
// detected on the next modal open without waiting for a palmux restart.
func (h *handlers) runtimeAvailableLXD(w http.ResponseWriter, r *http.Request) {
	// Step 1: binary on PATH.
	if _, err := exec.LookPath("lxc"); err != nil {
		writeJSON(w, http.StatusOK, runtimeAvailableResponse{
			Available: false,
			Reason:    "lxc binary not found on PATH",
		})
		return
	}
	// Step 2: daemon reachable. We don't need the body — just the exit code.
	cmd := exec.CommandContext(r.Context(), "lxc", "info")
	if err := cmd.Run(); err != nil {
		writeJSON(w, http.StatusOK, runtimeAvailableResponse{
			Available: false,
			Reason:    "lxc info failed: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, runtimeAvailableResponse{Available: true})
}

// patchRepoDefaultRuntime sets the per-repo default runtime config.
//
// AC-Sdd4ce1-6-3.
func (h *handlers) patchRepoDefaultRuntime(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	// Repo must exist (Open or Closed).
	if _, _, err := h.resolveRepoPaths(r, repoID); err != nil {
		writeErr(w, err)
		return
	}
	var body runtime.Config
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	rs := h.store.RepoStore()
	if body.Kind == "" {
		// Pass nil to clear the per-repo default.
		if _, err := rs.SetDefaultRuntime(repoID, nil); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !body.Kind.IsValid() {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid runtime kind: " + string(body.Kind)})
		return
	}
	cp := body
	if _, err := rs.SetDefaultRuntime(repoID, &cp); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// patchBranchRuntime sets the per-Workspace runtime override.
//
// AC-Sdd4ce1-6-1.
func (h *handlers) patchBranchRuntime(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	branch, err := h.store.Branch(repoID, branchID)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body runtime.Config
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	rs := h.store.RepoStore()
	if body.Kind == "" {
		if _, err := rs.SetBranchRuntime(repoID, branch.Name, nil); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !body.Kind.IsValid() {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid runtime kind: " + string(body.Kind)})
		return
	}
	cp := body
	if _, err := rs.SetBranchRuntime(repoID, branch.Name, &cp); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// branchRuntimeResponse is the shape returned by GET .../runtime.
type branchRuntimeResponse struct {
	// Resolved is the priority-chain output (per-Workspace → per-repo →
	// global → host fallback).
	Resolved runtime.Config `json:"resolved"`
	// View carries the lifecycle snapshot (state/address/error). Mirrors
	// Branch.Runtime in repos snapshots.
	View *domain.BranchRuntimeView `json:"view"`
	// PerWorkspace and PerRepo are the unresolved inputs — useful for the
	// Settings page so the user can tell which level set what. Either may
	// be nil.
	PerWorkspace *runtime.Config `json:"per_workspace,omitempty"`
	PerRepo      *runtime.Config `json:"per_repo,omitempty"`
	Global       runtime.Config  `json:"global"`
}

// getBranchRuntime returns the resolved runtime + raw priority inputs.
func (h *handlers) getBranchRuntime(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	branch, err := h.store.Branch(repoID, branchID)
	if err != nil {
		writeErr(w, err)
		return
	}
	rs := h.store.RepoStore()
	settings := h.store.Settings()
	global := runtime.Config{}
	if settings != nil {
		global = settings.DefaultRuntime()
	}
	resolved := rs.ResolveBranchRuntime(repoID, branch.Name, global)
	resp := branchRuntimeResponse{
		Resolved:     resolved,
		View:         branch.Runtime,
		PerWorkspace: rs.BranchRuntime(repoID, branch.Name),
		PerRepo:      rs.DefaultRuntime(repoID),
		Global:       global,
	}
	writeJSON(w, http.StatusOK, resp)
}
