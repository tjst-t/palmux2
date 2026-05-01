package files

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/tjst-t/palmux2/internal/store"
)

const (
	defaultReadLimit  = int64(2 << 20) // 2 MiB
	defaultMaxResults = 500
)

type handler struct {
	store *store.Store
}

func (h *handler) branchPath(r *http.Request) (string, error) {
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
	case errors.Is(err, ErrInvalidPath), errors.Is(err, store.ErrInvalidArg):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (h *handler) listDir(w http.ResponseWriter, r *http.Request) {
	root, err := h.branchPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	path := r.URL.Query().Get("path")
	entries, err := ListDir(root, path)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "entries": entries})
}

func (h *handler) readFile(w http.ResponseWriter, r *http.Request) {
	root, err := h.branchPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeErr(w, errors.New("path required"))
		return
	}
	// S010: `stat=1` returns metadata only (path, size, mime) without
	// reading or shipping the body. The Files-tab viewer dispatcher
	// uses this to decide whether to skip the preview entirely (file
	// over the `previewMaxBytes` threshold) before incurring any
	// bandwidth cost. We deliberately only sniff the first 512 bytes
	// for MIME — the dispatcher mostly cares about extension anyway,
	// and a tiny stat call should stay cheap on huge files.
	if r.URL.Query().Get("stat") == "1" {
		info, err := StatFile(root, path)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"path":     info.Path,
			"size":     info.Size,
			"mime":     info.MIME,
			"isBinary": info.IsBinary,
		})
		return
	}
	limit := defaultReadLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			limit = n
		}
	}
	body, info, err := ReadFile(root, path, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	if info.IsBinary {
		w.Header().Set("Content-Type", info.MIME)
		w.Header().Set("X-Palmux-Path", info.Path)
		w.Header().Set("X-Palmux-Size", strconv.FormatInt(info.Size, 10))
		_, _ = w.Write(body)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      info.Path,
		"size":      info.Size,
		"mime":      info.MIME,
		"isBinary":  info.IsBinary,
		"content":   string(body),
		"truncated": int64(len(body)) < info.Size,
	})
}

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	root, err := h.branchPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	query := q.Get("query")
	if query == "" {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	results, err := SearchEntries(root, q.Get("path"), query, q.Get("case") == "1", maxResultsParam(q, defaultMaxResults))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (h *handler) grep(w http.ResponseWriter, r *http.Request) {
	root, err := h.branchPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	pattern := q.Get("pattern")
	if pattern == "" {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	hits, err := Grep(root, q.Get("path"), pattern, q.Get("case") == "1", maxResultsParam(q, defaultMaxResults))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

func maxResultsParam(q map[string][]string, fallback int) int {
	if v := q["max"]; len(v) > 0 {
		if n, err := strconv.Atoi(v[0]); err == nil && n > 0 && n < 5000 {
			return n
		}
	}
	return fallback
}
