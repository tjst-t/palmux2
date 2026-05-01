package git

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/tjst-t/palmux2/internal/store"
)

// Event-type aliases. Centralised in store/events.go; mirrored here so
// callers inside this package don't have to dual-import store just to
// publish a status-changed event.
const (
	EventGitStatusChanged    = store.EventGitStatusChanged
	EventGitCredentialPrompt = store.EventGitCredentialPrompt
)

type handler struct {
	store *store.Store
}

func (h *handler) repoDir(r *http.Request) (string, error) {
	branch, err := h.store.Branch(r.PathValue("repoId"), r.PathValue("branchId"))
	if err != nil {
		return "", err
	}
	return branch.WorktreePath, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrRepoNotFound), errors.Is(err, store.ErrBranchNotFound):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrInvalidArg):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// === Read endpoints ========================================================

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	rep, err := Status(r.Context(), dir)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *handler) log(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := Log(r.Context(), dir, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *handler) diff(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	mode := DiffMode(r.URL.Query().Get("mode"))
	if mode == "" {
		mode = DiffWorking
	}
	path := r.URL.Query().Get("path")
	raw, err := RawDiff(r.Context(), dir, mode, path)
	if err != nil {
		writeErr(w, err)
		return
	}
	files := ParseUnifiedDiff(raw)
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":  mode,
		"raw":   raw,
		"files": files,
	})
}

func (h *handler) branches(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	entries, err := Branches(r.Context(), dir)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// show returns the body of <ref>:<path> as JSON `{content}`. Used by the
// Monaco diff viewer (S012-1-10) to fetch the HEAD blob.
func (h *handler) show(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	ref := r.URL.Query().Get("ref")
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path required"})
		return
	}
	body, err := Show(r.Context(), dir, ref, path)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": body})
}

// headCommitMessage returns the previous commit's full message so the FE
// can prefill the Commit form when the user toggles "amend".
func (h *handler) headCommitMessage(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	msg, err := HeadCommitMessage(r.Context(), dir)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// === Write endpoints =======================================================

type pathReq struct {
	Path string `json:"path"`
}

type hunkReq struct {
	File DiffFile `json:"file"`
	Hunk DiffHunk `json:"hunk"`
}

func (h *handler) stage(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req pathReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := Stage(r.Context(), dir, req.Path); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) unstage(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req pathReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := Unstage(r.Context(), dir, req.Path); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) discard(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req pathReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := Discard(r.Context(), dir, req.Path); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) stageHunk(w http.ResponseWriter, r *http.Request) {
	h.applyHunk(w, r, true /*cached*/, false /*reverse*/)
}

func (h *handler) unstageHunk(w http.ResponseWriter, r *http.Request) {
	h.applyHunk(w, r, true /*cached*/, true /*reverse*/)
}

func (h *handler) discardHunk(w http.ResponseWriter, r *http.Request) {
	h.applyHunk(w, r, false /*cached*/, true /*reverse*/)
}

func (h *handler) applyHunk(w http.ResponseWriter, r *http.Request, cached, reverse bool) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req hunkReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	patch := BuildHunkPatch(req.File, req.Hunk)
	if err := ApplyHunk(r.Context(), dir, patch, cached, reverse); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// === S012: line-range staging ==============================================

type stageLinesReq struct {
	Path       string      `json:"path"`
	LineRanges []LineRange `json:"lineRanges"`
}

func (h *handler) stageLines(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req stageLinesReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := StageLines(r.Context(), dir, req.Path, req.LineRanges); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// === S012: commit / push / pull / fetch ====================================

func (h *handler) commit(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req CommitOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := Commit(r.Context(), dir, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Republish a status event so other clients refresh; the watcher
	// will also do this but we don't want to wait for the debounce.
	h.publishStatus(r)
	writeJSON(w, http.StatusOK, res)
}

func (h *handler) push(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req PushOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := Push(r.Context(), dir, req)
	if err != nil {
		h.respondRemoteErr(w, r, "push", out, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func (h *handler) pull(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req PullOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := Pull(r.Context(), dir, req)
	if err != nil {
		h.respondRemoteErr(w, r, "pull", out, err)
		return
	}
	h.publishStatus(r)
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func (h *handler) fetch(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req FetchOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := Fetch(r.Context(), dir, req)
	if err != nil {
		h.respondRemoteErr(w, r, "fetch", out, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

// respondRemoteErr maps push/pull/fetch errors to the right HTTP status
// and emits a `git.credentialRequest` WS event when git couldn't reach
// the remote because of a missing credential. The FE listens for this
// event and pops a credential dialog (S012-1-14).
func (h *handler) respondRemoteErr(w http.ResponseWriter, r *http.Request, op, output string, err error) {
	if errors.Is(err, ErrCredentialRequired) {
		h.store.Hub().Publish(store.Event{
			Type:     EventGitCredentialPrompt,
			RepoID:   r.PathValue("repoId"),
			BranchID: r.PathValue("branchId"),
			Payload: map[string]string{
				"op":     op,
				"output": output,
			},
		})
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":  err.Error(),
			"output": output,
			"reason": "credential_required",
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{
		"error":  err.Error(),
		"output": output,
	})
}

// === S012: branch CRUD ====================================================

func (h *handler) createBranch(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req CreateBranchOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := CreateBranch(r.Context(), dir, req); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type switchReq struct {
	Name string `json:"name"`
}

func (h *handler) switchBranch(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req switchReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := SwitchBranch(r.Context(), dir, req.Name); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) deleteBranch(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "branch name required"})
		return
	}
	force := r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true"
	if err := DeleteBranch(r.Context(), dir, DeleteBranchOptions{Name: name, Force: force}); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) setUpstream(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req SetUpstreamOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := SetUpstream(r.Context(), dir, req); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// === S012: AI commit message ==============================================

func (h *handler) aiCommitMessage(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	prompt, err := AICommitPrompt(r.Context(), dir)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"prompt": prompt})
}

// publishStatus re-broadcasts a `git.statusChanged` event after a write op
// so any clients viewing the Git tab refresh immediately rather than wait
// for the filewatch debounce. Best-effort — ignores errors.
func (h *handler) publishStatus(r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	if repoID == "" || branchID == "" {
		return
	}
	h.store.Hub().Publish(store.Event{
		Type:     EventGitStatusChanged,
		RepoID:   repoID,
		BranchID: branchID,
	})
}
