package git

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/tjst-t/palmux2/internal/store"
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
