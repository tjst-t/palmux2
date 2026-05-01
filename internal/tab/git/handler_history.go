// Sprint S013 — Git History & Common Ops handlers.
//
// Hooks into the existing handler.go: same `*handler` receiver, same
// JSON helpers (writeJSON, decodeJSON, repoDir, writeErr). Routes are
// registered in provider.go's RegisterRoutes.

package git

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// === Read endpoints ========================================================

// logFiltered handles GET /git/log with extended filter parameters.
//
// Backwards compatible with the S012 /log endpoint: when no filters are
// passed and only `limit` is present we still produce a usable response.
// The FE migrates progressively to the rich filter, so both shapes need
// to keep working.
func (h *handler) logFiltered(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	filter := LogFilter{
		Author: q.Get("author"),
		Grep:   q.Get("grep"),
		Since:  q.Get("since"),
		Until:  q.Get("until"),
		Path:   q.Get("path"),
		Branch: q.Get("branch"),
	}
	if v := q.Get("skip"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			filter.Skip = n
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	if v := q.Get("all"); v == "1" || v == "true" {
		filter.All = true
	}
	entries, err := LogFiltered(r.Context(), dir, filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"skip":    filter.Skip,
		"limit":   firstNonZero(filter.Limit, 50),
	})
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// branchGraph handles GET /git/branch-graph (S013-1-2).
func (h *handler) branchGraph(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	all := false
	if v := r.URL.Query().Get("all"); v == "1" || v == "true" {
		all = true
	}
	entries, err := BranchGraph(r.Context(), dir, limit, all)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
	})
}

// === Stash =================================================================

func (h *handler) stashList(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	entries, err := StashList(r.Context(), dir)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *handler) stashPush(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req StashPushOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := StashPush(r.Context(), dir, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  err.Error(),
			"output": out,
		})
		return
	}
	h.publishStatus(r)
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func (h *handler) stashApply(w http.ResponseWriter, r *http.Request) {
	h.stashAction(w, r, false /*pop*/)
}

func (h *handler) stashPop(w http.ResponseWriter, r *http.Request) {
	h.stashAction(w, r, true /*pop*/)
}

func (h *handler) stashAction(w http.ResponseWriter, r *http.Request, pop bool) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	name, err := url.PathUnescape(r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad stash name"})
		return
	}
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "stash name required"})
		return
	}
	var (
		out string
	)
	if pop {
		out, err = StashPop(r.Context(), dir, name)
	} else {
		out, err = StashApply(r.Context(), dir, name)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  err.Error(),
			"output": out,
		})
		return
	}
	h.publishStatus(r)
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func (h *handler) stashDrop(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	name, err := url.PathUnescape(r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad stash name"})
		return
	}
	if err := StashDrop(r.Context(), dir, name); err != nil {
		writeErr(w, err)
		return
	}
	h.publishStatus(r)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) stashDiff(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	name, err := url.PathUnescape(r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad stash name"})
		return
	}
	raw, err := StashDiff(r.Context(), dir, name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":  name,
		"raw":   raw,
		"files": ParseUnifiedDiff(raw),
	})
}

// === Cherry-pick / Revert / Reset =========================================

func (h *handler) cherryPick(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req CherryPickOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := CherryPick(r.Context(), dir, req)
	if err != nil {
		if errors.Is(err, ErrCherryPickConflict) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  err.Error(),
				"output": out,
				"reason": "conflict",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  err.Error(),
			"output": out,
		})
		return
	}
	h.publishStatus(r)
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func (h *handler) revert(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req RevertOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := Revert(r.Context(), dir, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  err.Error(),
			"output": out,
		})
		return
	}
	h.publishStatus(r)
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func (h *handler) reset(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req ResetOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := Reset(r.Context(), dir, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  err.Error(),
			"output": out,
		})
		return
	}
	h.publishStatus(r)
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

// === Tag CRUD =============================================================

func (h *handler) tagList(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	entries, err := TagList(r.Context(), dir)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *handler) tagCreate(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req CreateTagOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := CreateTag(r.Context(), dir, req); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) tagDelete(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	name, err := url.PathUnescape(r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad tag name"})
		return
	}
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tag name required"})
		return
	}
	remote := r.URL.Query().Get("remote")
	out, err := DeleteTag(r.Context(), dir, DeleteTagOptions{Name: name, Remote: remote})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  err.Error(),
			"output": out,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func (h *handler) tagPush(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req PushTagOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := PushTag(r.Context(), dir, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  err.Error(),
			"output": out,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

// === File history & blame =================================================

func (h *handler) fileHistory(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path required"})
		return
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := FileHistory(r.Context(), dir, path, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    path,
		"entries": entries,
	})
}

func (h *handler) blame(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path required"})
		return
	}
	rev := r.URL.Query().Get("revision")
	lines, err := Blame(r.Context(), dir, rev, path)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":     path,
		"revision": rev,
		"lines":    lines,
	})
}
