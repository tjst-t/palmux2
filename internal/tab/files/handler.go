package files

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
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
		// S011: surface ETag on stat too so the dispatcher / Edit
		// button has the freshness fingerprint without a follow-up
		// raw fetch. Failures here are non-fatal — the client falls
		// back to fetching the body which always carries the ETag.
		if etag, err := EtagFor(root, path); err == nil {
			w.Header().Set("ETag", etag)
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
	// S011: ETag header on the raw body so the client can capture the
	// "version I last read" without a separate metadata round-trip.
	// EtagFor re-stats the file, which is fine: it's microseconds and
	// keeps the ETag derivation in one place.
	if etag, err := EtagFor(root, path); err == nil {
		w.Header().Set("ETag", etag)
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

// writeFile handles `PUT /api/repos/.../files/raw?path=...` (S011-1-1).
// Optimistic-locking flow:
//
//   - Client must send `If-Match: <etag>` (the ETag it received on the
//     last GET). Missing the header → 428 Precondition Required so the
//     client can't accidentally clobber the file.
//   - The handler stats the file, computes the current ETag, and if
//     it doesn't match the supplied If-Match value, replies 412
//     Precondition Failed with the current ETag in the response so
//     the client can drive its conflict-resolution dialog.
//   - On success: write atomically, emit the new ETag, return the new
//     FileInfo (size / MIME / etc.).
//
// Body: JSON `{"content": "...string..."}`. We deliberately keep the
// shape JSON (rather than raw text/octet-stream) so the API surface is
// uniform with the rest of /api — and so we can extend the body later
// (e.g. `{"content": "...", "encoding": "base64"}` for binaries) without
// a breaking change.
func (h *handler) writeFile(w http.ResponseWriter, r *http.Request) {
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

	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPreconditionRequired)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "If-Match header required for PUT (optimistic locking)",
		})
		return
	}

	current, err := EtagFor(root, path)
	if err != nil {
		writeErr(w, err)
		return
	}
	if current != ifMatch {
		w.Header().Set("ETag", current)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPreconditionFailed)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":       "precondition failed: file was modified on disk",
			"currentEtag": current,
			"yourEtag":    ifMatch,
		})
		return
	}

	// Cap upload size to a sane ceiling (~32 MiB) — Files-tab edits are
	// human-typed, so this is a soft anti-abuse bound rather than a real
	// product cap. Above the limit we 413, which the client surfaces.
	const maxUpload = int64(32 << 20)
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	defer r.Body.Close()

	var payload struct {
		Content string `json:"content"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		// Distinguish "too big" from "malformed" for a kinder error.
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
				"error": "request body exceeds 32 MiB upload limit",
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Reject a sneaky trailing body (e.g. concatenated objects).
	if dec.More() {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "trailing content after JSON object",
		})
		return
	}

	info, etag, err := WriteFile(root, path, []byte(payload.Content))
	if err != nil {
		// Translate the underlying file-system errors so the client
		// can show "file vanished" / "permission denied" without
		// guessing.
		switch {
		case errors.Is(err, os.ErrNotExist):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, os.ErrPermission):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		case errors.Is(err, io.ErrUnexpectedEOF):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			writeErr(w, err)
		}
		return
	}
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, map[string]any{
		"path":     info.Path,
		"size":     info.Size,
		"mime":     info.MIME,
		"isBinary": info.IsBinary,
		"etag":     etag,
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
