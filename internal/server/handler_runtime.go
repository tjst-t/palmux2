package server

// handler_runtime.go — S8478ca-5: runtime capability + per-workspace runtime
// PATCH endpoints.
//
// Endpoints:
//   GET  /api/runtimes
//     → {kinds:[{kind,available,reason?}]}
//     host is always available.
//     incus-container is available iff `incus` is on PATH.
//
//   GET  /api/repos/{repoId}
//     → full Repository JSON with branch.runtime views populated.
//     Used by the badge test that needs a single-repo snapshot.
//
//   PATCH /api/repos/{repoId}/branches/{branchId}/runtime
//     body: {kind}
//     Persists the per-Workspace runtime override via RepoStore.SetWorkspaceRuntime.
//     Returns 200 on success, 400 on invalid kind, 500 on persistence error.
//     Emits branch.runtimeChanged WS event.

import (
	"net/http"
	"os/exec"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/store"
)

// ─── GET /api/runtimes ───────────────────────────────────────────────────────

// runtimeKindEntry is one element in the runtimes capability response.
type runtimeKindEntry struct {
	Kind      string `json:"kind"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// runtimesResponse is the body returned by GET /api/runtimes.
type runtimesResponse struct {
	Kinds []runtimeKindEntry `json:"kinds"`
}

// incusAvailable reports whether `incus` is on PATH. It uses exec.LookPath
// which is the same mechanism the incus package uses internally.
func incusAvailable() (bool, string) {
	_, err := exec.LookPath("incus")
	if err == nil {
		return true, ""
	}
	return false, "Incus is not installed on this host (incus binary not found on PATH)"
}

func (h *handlers) getRuntimes(w http.ResponseWriter, _ *http.Request) {
	incusOK, reason := incusAvailable()
	writeJSON(w, http.StatusOK, runtimesResponse{
		Kinds: []runtimeKindEntry{
			{Kind: "host", Available: true},
			{Kind: "incus-container", Available: incusOK, Reason: reason},
		},
	})
}

// ─── GET /api/repos/{repoId} ─────────────────────────────────────────────────

// getRepo handles GET /api/repos/{repoId}.  It returns the full Repository
// object (including openBranches with runtime views) for the given repoId.
// This endpoint did not previously exist; the FE mock test routes to it, and
// the E2E test calls it directly for AC-5-2.
func (h *handlers) getRepo(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	repo, err := h.store.Repo(repoID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repo)
}

// ─── PATCH /api/repos/{repoId}/branches/{branchId}/runtime ──────────────────

// patchRuntimeRequest is the body for PATCH .../runtime.
type patchRuntimeRequest struct {
	Kind string `json:"kind"`
}

func (h *handlers) patchWorkspaceRuntime(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")

	var req patchRuntimeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	k := runtime.Kind(req.Kind)
	if !k.IsValid() {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "invalid runtime kind " + req.Kind + " (must be host or incus-container)",
		})
		return
	}

	// Capture the runtime the workspace is CURRENTLY running on BEFORE we
	// persist the new kind — the registry re-resolves the persisted config on
	// every Get, so capturing after the persist would yield the NEW runtime and
	// make the in-place restart a silent no-op (host session never killed /
	// switch-back a no-op).
	oldRT := h.store.CurrentRuntime(repoID, branchID)

	cfg := &runtime.Config{Kind: k}
	if err := h.store.RepoStore().SetWorkspaceRuntime(repoID, branchID, cfg); err != nil {
		if err.Error() == store.ErrRepoNotFound.Error() || err.Error() == store.ErrBranchNotFound.Error() {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist runtime: " + err.Error()})
		return
	}

	// S8478ca-refine: if the workspace is currently open AND the kind actually
	// changed, perform an in-place restart: kill the old session, stop/evict
	// the old runtime, and bring up a fresh session in the new runtime.
	// RestartBranchRuntime returns (false,nil) as a no-op when:
	//   - the branch is not currently open
	//   - the old and new kinds are the same
	//   - no RuntimeRegistry is wired
	restarted, restartErr := h.store.RestartBranchRuntime(r.Context(), repoID, branchID, oldRT)

	// See8bd4-3: the runtime kind change flips the Ports tab's visibility
	// (incus-only). Recompute the workspace's tabs so tab.added / tab.removed
	// propagate to all clients. Safe regardless of restart outcome — it reads
	// the (possibly rolled-back) persisted kind via the runtime registry.
	if rcErr := h.store.RecomputeBranchTabs(repoID, branchID); rcErr != nil {
		// non-fatal: the tab will reconcile on the next branch reload
		_ = rcErr
	}

	if restartErr != nil {
		// Restart failure is surfaced as a non-fatal warning: the config was
		// persisted successfully; the user can close+reopen to apply it.
		// We return 200 with restarted=false so the FE can show a warning.
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":           true,
			"restarted":    false,
			"restartError": restartErr.Error(),
			"runtime":      h.store.RuntimeViewFor(repoID, branchID),
		})
		return
	}

	rtView := h.store.RuntimeViewFor(repoID, branchID)
	// Ensure the FE sees the new kind immediately even when the runtime is
	// still starting (state may be "starting" rather than "ready" yet).
	if rtView != nil && rtView.Kind != string(k) {
		rtView.Kind = string(k)
	}

	// For no-op cases (branch not open, same kind, no registry) the
	// runtimeChanged event was NOT emitted. Emit it so the FE badge refreshes.
	if !restarted {
		h.store.Hub().Publish(store.Event{
			Type:     store.EventBranchRuntimeChanged,
			RepoID:   repoID,
			BranchID: branchID,
			Payload:  rtView,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"restarted": restarted,
		"runtime":   rtView,
	})
}

// runtimeViewFromBranch is a helper used in tests.  It extracts the runtime
// view from a branch or returns the host-ready default.
func runtimeViewFromBranch(b *domain.Branch) *domain.RuntimeView {
	if b == nil || b.Runtime == nil {
		return &domain.RuntimeView{Kind: "host", State: "ready", Address: "localhost"}
	}
	return b.Runtime
}
