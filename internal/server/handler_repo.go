package server

import (
	"net/http"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/ghq"
)

func (h *handlers) health(w http.ResponseWriter, _ *http.Request) {
	body := map[string]any{"status": "ok", "phase": 1}
	for k, v := range h.healthDetail {
		body[k] = v
	}
	writeJSON(w, http.StatusOK, body)
}

// availableRepoEntry is the public DTO for `GET /api/repos/available`.
type availableRepoEntry struct {
	ID       string `json:"id"`
	GHQPath  string `json:"ghqPath"`
	FullPath string `json:"fullPath"`
	Open     bool   `json:"open"`
	Starred  bool   `json:"starred"`
}

func (h *handlers) listRepos(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.store.Repos())
}

func (h *handlers) availableRepos(w http.ResponseWriter, r *http.Request) {
	all, err := h.store.AvailableRepos(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	openByID := map[string]bool{}
	starredByID := map[string]bool{}
	for _, r := range h.store.Repos() {
		openByID[r.ID] = true
		starredByID[r.ID] = r.Starred
	}
	out := make([]availableRepoEntry, 0, len(all))
	for _, repo := range all {
		id := domain.RepoSlugID(repo.GHQPath)
		out = append(out, availableRepoEntry{
			ID:       id,
			GHQPath:  repo.GHQPath,
			FullPath: repo.FullPath,
			Open:     openByID[id],
			Starred:  starredByID[id],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) openRepo(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	ghqPath, err := h.lookupGHQPath(r, repoID)
	if err != nil {
		writeErr(w, err)
		return
	}
	repo, err := h.store.OpenRepo(r.Context(), ghqPath)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repo)
}

func (h *handlers) closeRepo(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	if err := h.store.CloseRepo(r.Context(), repoID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) star(w http.ResponseWriter, r *http.Request) {
	if err := h.store.SetStarred(r.PathValue("repoId"), true); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) unstar(w http.ResponseWriter, r *http.Request) {
	if err := h.store.SetStarred(r.PathValue("repoId"), false); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// lookupGHQPath finds the ghq-relative path whose RepoSlugID matches repoID.
// Used for endpoints that take {repoId} but need the original path to call
// store.OpenRepo (which derives the ID itself).
func (h *handlers) lookupGHQPath(r *http.Request, repoID string) (string, error) {
	all, err := h.store.AvailableRepos(r.Context())
	if err != nil {
		return "", err
	}
	for _, repo := range all {
		if domain.RepoSlugID(repo.GHQPath) == repoID {
			return repo.GHQPath, nil
		}
	}
	return "", &repoIDError{ID: repoID}
}

// repoIDError surfaces a 404 with a friendly message.
type repoIDError struct{ ID string }

func (e *repoIDError) Error() string {
	return "no repository with id " + e.ID + " in ghq"
}

func (h *handlers) connections(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.store.Connections())
}

// orphanSessions returns non-Palmux tmux sessions in compat mode. Phase 1
// returns an empty list — full implementation lands later.
func (h *handlers) orphanSessions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

// silence unused import warning in some build configurations
var _ ghq.Repository
