package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/ghq"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/worktree"
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

// resolveRepoPaths returns the absolute fullPath and ghq-relative path for
// repoID, regardless of whether the repo is currently Open in Palmux. The
// Open list is checked first (cheap, in-memory); if absent we fall back
// to AvailableRepos (calls `ghq list`).
//
// hotfix: deletePreview / deleteRepo previously consulted only the Open
// repos via h.store.Repos(), so the unpushed-work check returned 404
// when the user requested deletion from the Open Repository modal —
// where the listed repos are by definition Closed.
func (h *handlers) resolveRepoPaths(r *http.Request, repoID string) (fullPath, ghqPath string, err error) {
	for _, rp := range h.store.Repos() {
		if rp.ID == repoID {
			return rp.FullPath, rp.GHQPath, nil
		}
	}
	all, listErr := h.store.AvailableRepos(r.Context())
	if listErr != nil {
		return "", "", listErr
	}
	for _, rp := range all {
		if domain.RepoSlugID(rp.GHQPath) == repoID {
			return rp.FullPath, rp.GHQPath, nil
		}
	}
	return "", "", &repoIDError{ID: repoID}
}

// repoIDError surfaces a 404 with a friendly message.
type repoIDError struct{ ID string }

func (e *repoIDError) Error() string {
	return "no repository with id " + e.ID + " in ghq"
}

func (h *handlers) connections(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.store.Connections())
}

// orphanSessions returns non-Palmux tmux sessions in compat mode.
func (h *handlers) orphanSessions(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.OrphanSessions(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ─── S030: clone + delete ─────────────────────────────────────────────────────

// cloneRepoRequest is the body for POST /api/repos/clone.
type cloneRepoRequest struct {
	URL string `json:"url"`
}

// cloneRepoResponse is returned on success.
type cloneRepoResponse struct {
	RepoID   string `json:"repoId"`
	GHQPath  string `json:"ghqPath"`
	FullPath string `json:"fullPath"`
}

// cloneRepo handles POST /api/repos/clone.
// It calls `ghq get <url>`, then opens the repo and its primary branch.
func (h *handlers) cloneRepo(w http.ResponseWriter, r *http.Request) {
	var body cloneRepoRequest
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if err := validateCloneURL(body.URL); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	ghqClient := h.store.GHQClient()
	if ghqClient == nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "ghq client not available"})
		return
	}

	// Run ghq get — this is the slow part (git clone). The ctx carries any
	// client disconnect so we cancel it cleanly.
	dest, err := ghqClient.Get(r.Context(), body.URL)
	if err != nil {
		// Surface the stderr to the user verbatim.
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	// Derive the ghqPath from the URL (host/owner/repo).
	ghqPath := ghqPathFromURL(body.URL)

	// Open the repo in Palmux (records it in repos.json).
	repo, err := h.store.OpenRepo(r.Context(), ghqPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "clone succeeded but open failed: " + err.Error()})
		return
	}

	// Open the primary branch (the first branch whose IsPrimary flag is set).
	// We silently ignore errors here — the FE will show the repo even without
	// an open branch, and the user can open branches from the Drawer.
	for _, b := range repo.OpenBranches {
		if b.IsPrimary {
			_, _ = h.store.OpenBranchInternal(r.Context(), repo.ID, b.Name, true)
			break
		}
	}

	if dest == "" {
		dest = repo.FullPath
	}

	writeJSON(w, http.StatusCreated, cloneRepoResponse{
		RepoID:   repo.ID,
		GHQPath:  repo.GHQPath,
		FullPath: dest,
	})
}

// deletePreviewResponse is returned by GET /api/repos/{repoId}/delete-preview.
type deletePreviewResponse struct {
	HasUnpushed bool                       `json:"hasUnpushed"`
	Worktrees   []worktree.WorktreeStatus  `json:"worktrees"`
}

// deletePreview handles GET /api/repos/{repoId}/delete-preview.
//
// Works for both Open and Closed repos — the Open Repository modal calls
// this on closed repos when the user clicks the per-row 🗑.
func (h *handlers) deletePreview(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")

	repoFullPath, _, err := h.resolveRepoPaths(r, repoID)
	if err != nil {
		writeErr(w, err)
		return
	}

	statuses, err := worktree.UnpushedSummary(r.Context(), repoFullPath)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Ensure JSON encodes as [] not null for an empty slice.
	if statuses == nil {
		statuses = []worktree.WorktreeStatus{}
	}

	hasUnpushed := false
	for _, s := range statuses {
		if s.HasWarnings() {
			hasUnpushed = true
			break
		}
	}
	writeJSON(w, http.StatusOK, deletePreviewResponse{
		HasUnpushed: hasUnpushed,
		Worktrees:   statuses,
	})
}

// deleteRepoRequest is the body for DELETE /api/repos/{repoId}.
type deleteRepoRequest struct {
	ConfirmName string `json:"confirmName,omitempty"`
}

// deleteRepo handles DELETE /api/repos/{repoId}.
//
// Works for both Open and Closed repos — Closed-repo deletion is requested
// from the Open Repository modal's per-row 🗑.
func (h *handlers) deleteRepo(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")

	repoFullPath, ghqPath, err := h.resolveRepoPaths(r, repoID)
	if err != nil {
		writeErr(w, err)
		return
	}

	statuses, err := worktree.UnpushedSummary(r.Context(), repoFullPath)
	if err != nil {
		// Best-effort: if we can't check, require confirmation anyway.
		h.logger.Warn("deleteRepo: UnpushedSummary failed", "repo", repoID, "err", err)
	}
	hasUnpushed := false
	for _, s := range statuses {
		if s.HasWarnings() {
			hasUnpushed = true
			break
		}
	}

	if hasUnpushed {
		var body deleteRepoRequest
		_ = decodeJSON(r, &body)

		// Derive the "owner/repo" slug from the ghqPath for typed confirmation.
		// ghqPath is host/owner/repo; we want the last two segments.
		expectedName := repoShortName(ghqPath)
		if !strings.EqualFold(body.ConfirmName, expectedName) {
			writeJSON(w, http.StatusPreconditionFailed, errorResponse{
				Error: "unpushed work detected — type the repository name (" + expectedName + ") to confirm deletion",
			})
			return
		}
	}

	if err := h.store.DeleteRepo(r.Context(), repoID); err != nil {
		if errors.Is(err, store.ErrRepoNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
			return
		}
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// repoShortName returns the "owner/repo" portion from a ghqPath like
// "github.com/owner/repo". Falls back to the full path for unusual formats.
func repoShortName(ghqPath string) string {
	parts := strings.Split(ghqPath, "/")
	if len(parts) >= 3 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return ghqPath
}

// validateCloneURL performs basic URL validation.
func validateCloneURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("url is required")
	}
	// Accept git@ SSH URLs.
	if strings.HasPrefix(raw, "git@") {
		return nil
	}
	// Accept owner/repo shorthand.
	if parts := strings.Split(raw, "/"); len(parts) == 2 && !strings.Contains(raw, ":") {
		return nil
	}
	// Full URL validation.
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return errors.New("invalid URL: " + err.Error())
	}
	if u.Host == "" {
		return errors.New("invalid URL: missing host")
	}
	return nil
}

// ghqPathFromURL converts a clone URL into the ghq-relative path.
// Duplicated from ghq package for use in the handler layer.
func ghqPathFromURL(rawURL string) string {
	rawURL = strings.TrimSuffix(rawURL, ".git")
	if strings.HasPrefix(rawURL, "git@") {
		rawURL = strings.TrimPrefix(rawURL, "git@")
		return strings.Replace(rawURL, ":", "/", 1)
	}
	for _, pf := range []string{"https://", "http://"} {
		if strings.HasPrefix(rawURL, pf) {
			return strings.TrimPrefix(rawURL, pf)
		}
	}
	if parts := strings.Split(rawURL, "/"); len(parts) == 2 {
		return "github.com/" + rawURL
	}
	return rawURL
}

// silence unused import warning in some build configurations
var _ ghq.Repository
