package server

import (
	"net/http"

	"github.com/tjst-t/palmux2/internal/portman"
)

func (h *handlers) listRepoPortman(w http.ResponseWriter, r *http.Request) {
	if h.portman == nil || !h.portman.Available() {
		writeJSON(w, http.StatusOK, []portman.Lease{})
		return
	}
	branch, err := h.store.Branch(r.PathValue("repoId"), r.PathValue("branchId"))
	if err != nil {
		writeErr(w, err)
		return
	}
	repo, err := h.store.Repo(r.PathValue("repoId"))
	if err != nil {
		writeErr(w, err)
		return
	}
	all, err := h.portman.List(r.Context())
	if err != nil {
		// Don't fail the UI if portman is having a bad day — just return an
		// empty list. The handler still surfaces errors as 200 + [] so the
		// header doesn't break.
		writeJSON(w, http.StatusOK, []portman.Lease{})
		return
	}
	writeJSON(w, http.StatusOK, portman.ForRepo(all, repo.GHQPath, branch.Name))
}
