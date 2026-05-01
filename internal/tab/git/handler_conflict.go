// Sprint S014 — Conflict / rebase / merge / submodule / reflog / bisect
// HTTP handlers. Hooks into the existing handler.go: same `*handler`
// receiver, same JSON helpers (writeJSON, decodeJSON, repoDir, writeErr).
// Routes are registered in provider.go's RegisterRoutes (S014 block).

package git

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// === Conflict listing & per-file ==========================================

func (h *handler) conflicts(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	files, err := Conflicts(r.Context(), dir)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"files": files,
	})
}

func (h *handler) conflictFile(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	path, err := pathFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	body, err := GetConflictFile(r.Context(), dir, path)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

type conflictPutReq struct {
	Content string `json:"content"`
}

func (h *handler) conflictFilePut(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	path, err := pathFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var req conflictPutReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := PutConflictFile(r.Context(), dir, path, req.Content); err != nil {
		writeErr(w, err)
		return
	}
	h.publishStatus(r)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) conflictMarkResolved(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	path, err := pathFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := MarkConflictResolved(r.Context(), dir, path); err != nil {
		writeErr(w, err)
		return
	}
	h.publishStatus(r)
	w.WriteHeader(http.StatusNoContent)
}

// pathFromRequest extracts the {path...} path-value (Go 1.22 wildcard
// segment) from a request and validates it. Strips a leading '/' that
// stdlib leaves behind when a {path...} segment matches.
func pathFromRequest(r *http.Request) (string, error) {
	raw := r.PathValue("path")
	if raw == "" {
		// Some routes use a query parameter so the FE doesn't need to
		// percent-encode every directory separator.
		raw = r.URL.Query().Get("path")
	}
	dec, err := url.PathUnescape(raw)
	if err != nil {
		return "", err
	}
	dec = strings.TrimPrefix(dec, "/")
	if dec == "" {
		return "", errPathRequired
	}
	if strings.Contains(dec, "..") {
		return "", errInvalidPath
	}
	return dec, nil
}

var (
	errPathRequired = &errString{"path required"}
	errInvalidPath  = &errString{"invalid path"}
)

type errString struct{ s string }

func (e *errString) Error() string { return e.s }

// === Rebase TODO ==========================================================

func (h *handler) rebaseTodoGet(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := GetRebaseStatus(r.Context(), dir)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type rebaseTodoPutReq struct {
	Entries  []RebaseTodoEntry `json:"entries"`
	Continue bool              `json:"continue,omitempty"`
}

func (h *handler) rebaseTodoPut(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req rebaseTodoPutReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := PutRebaseTodo(r.Context(), dir, req.Entries, req.Continue)
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

// === Rebase ops ===========================================================

func (h *handler) rebaseStart(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req RebaseStartOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := RebaseStart(r.Context(), dir, req)
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

func (h *handler) rebaseAbort(w http.ResponseWriter, r *http.Request) {
	h.rebaseRun(w, r, "abort")
}
func (h *handler) rebaseContinue(w http.ResponseWriter, r *http.Request) {
	h.rebaseRun(w, r, "continue")
}
func (h *handler) rebaseSkip(w http.ResponseWriter, r *http.Request) {
	h.rebaseRun(w, r, "skip")
}

func (h *handler) rebaseRun(w http.ResponseWriter, r *http.Request, action string) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var (
		out  string
		opErr error
	)
	switch action {
	case "abort":
		out, opErr = RebaseAbort(r.Context(), dir)
	case "continue":
		out, opErr = RebaseContinue(r.Context(), dir)
	case "skip":
		out, opErr = RebaseSkip(r.Context(), dir)
	}
	if opErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  opErr.Error(),
			"output": out,
		})
		return
	}
	h.publishStatus(r)
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

// === Merge ops ============================================================

func (h *handler) mergeStart(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req MergeOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := Merge(r.Context(), dir, req)
	if err != nil {
		// Conflict surfaces in stdout/stderr; we want to return 200 OK
		// with the output if the merge produced conflicts (so the FE
		// can transition to the conflict UI), but other errors should
		// be 500.
		if strings.Contains(strings.ToLower(out), "conflict") {
			h.publishStatus(r)
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

func (h *handler) mergeAbort(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := MergeAbort(r.Context(), dir)
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

// === Submodules ===========================================================

func (h *handler) submodulesList(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	subs, err := Submodules(r.Context(), dir)
	if err != nil {
		writeErr(w, err)
		return
	}
	if subs == nil {
		subs = []Submodule{}
	}
	writeJSON(w, http.StatusOK, subs)
}

func (h *handler) submoduleInit(w http.ResponseWriter, r *http.Request) {
	h.submoduleAction(w, r, true)
}

func (h *handler) submoduleUpdate(w http.ResponseWriter, r *http.Request) {
	h.submoduleAction(w, r, false)
}

func (h *handler) submoduleAction(w http.ResponseWriter, r *http.Request, init bool) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	path, err := pathFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var (
		out   string
		opErr error
	)
	if init {
		out, opErr = SubmoduleInit(r.Context(), dir, path)
	} else {
		out, opErr = SubmoduleUpdate(r.Context(), dir, path)
	}
	if opErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  opErr.Error(),
			"output": out,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

// === Reflog ===============================================================

func (h *handler) reflog(w http.ResponseWriter, r *http.Request) {
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
	entries, err := Reflog(r.Context(), dir, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	if entries == nil {
		entries = []ReflogEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// === Bisect ===============================================================

func (h *handler) bisectStatus(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := GetBisectStatus(r.Context(), dir)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *handler) bisectStart(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req BisectStartOptions
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := BisectStart(r.Context(), dir, req)
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

func (h *handler) bisectGood(w http.ResponseWriter, r *http.Request) { h.bisectMarkRun(w, r, "good") }
func (h *handler) bisectBad(w http.ResponseWriter, r *http.Request)  { h.bisectMarkRun(w, r, "bad") }
func (h *handler) bisectSkip(w http.ResponseWriter, r *http.Request) { h.bisectMarkRun(w, r, "skip") }

func (h *handler) bisectMarkRun(w http.ResponseWriter, r *http.Request, term string) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := BisectMark(r.Context(), dir, term)
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

func (h *handler) bisectReset(w http.ResponseWriter, r *http.Request) {
	dir, err := h.repoDir(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := BisectReset(r.Context(), dir)
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
