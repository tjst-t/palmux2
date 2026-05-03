package files

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tjst-t/palmux2/internal/store"
)

// mimeForPath maps a worktree-relative path to the MIME type the browser
// should see when loading the file via `/files/raw` (S026).
//
// Pre-S026 the raw endpoint returned `application/json` for all
// non-binary content (a JSON envelope around the file body) and only
// switched to a binary content-type for image/*-class files. That worked
// for the dispatcher's stat / Monaco / image viewers but broke the new
// HTML preview iframe — the browser treated `style.css` and `app.js` as
// JSON, refused to apply / execute them, and the rendered preview showed
// no styles or behavior.
//
// S026 introduces a separate MIME table that's consulted before falling
// back to the JSON envelope. When `mimeForPath` returns a non-empty
// string we serve the body directly with that Content-Type so the
// browser renders / executes the resource as the author intended. The
// extension list is intentionally narrow: only formats the iframe
// preview actually needs (HTML / CSS / JS / common images / a couple of
// lighter text formats).
//
// CDN-hosted assets are unaffected — they're loaded by the iframe with
// their own origin's headers; we only control resources served from our
// own origin.
func mimeForPath(name string) string {
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".xml":
		return "application/xml; charset=utf-8"
	}
	return ""
}

// rawCSP is the Content-Security-Policy header attached to every raw
// response (S026). It applies *inside* the sandboxed iframe that
// renders HTML previews, providing defense-in-depth alongside the
// `<iframe sandbox="allow-scripts">` restriction (which already
// prevents the iframe from claiming our origin and reaching the
// session cookie).
//
//   - `default-src 'self'`: same-origin assets only by default. The
//     iframe is treated by the browser as a unique opaque origin
//     because we deliberately omit `allow-same-origin`, so 'self'
//     here means "the iframe's own origin" — i.e. nothing is loadable
//     unless explicitly allowed below.
//   - `script-src 'self' 'unsafe-inline' 'unsafe-eval'`: developer
//     HTML almost always uses inline `<script>` blocks during local
//     iteration; eval is needed by some popular libs (Vue templates,
//     etc.). These are safe under sandbox-without-same-origin because
//     anything the script does still cannot touch palmux2's session.
//   - `style-src 'self' 'unsafe-inline'`: same reasoning for inline
//     `<style>` tags.
//   - `img-src 'self' data: blob:`: data: and blob: URLs are common in
//     hand-rolled HTML; the iframe's same-origin resolves to its
//     opaque origin so 'self' covers worktree-relative `<img src>`
//     references.
//   - `font-src` / `connect-src`: 'self' only — no covert exfiltration.
//
// We intentionally do NOT whitelist any external CDN here. CDN hosting
// is supported (the developer's own `<script src="https://cdn...">`
// works because the browser fetches them directly) but our header
// declares the strictest viable policy — the browser merges the
// document's own CSP (if any) with this one using the most-restrictive
// directive.
const rawCSP = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline' 'unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'"

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
	// S026: every raw response (JSON envelope OR direct body) carries
	// the same CSP. The header is harmless on the JSON envelope (the
	// browser doesn't render JSON as a document) and load-bearing on
	// the iframe-targeted direct-body responses.
	w.Header().Set("Content-Security-Policy", rawCSP)
	// S026: when the request is *not* asking for the JSON envelope
	// (the Files-tab dispatcher always sends `Accept: application/json`)
	// AND we have a direct MIME mapping for the extension, serve the
	// body straight back to the caller with the correct Content-Type.
	// This is the path the HTML preview iframe takes — it loads the
	// raw URL like a normal browser navigation, sending the default
	// `Accept: text/html,…` header, and needs to receive `text/html`
	// (not `application/json`) so the browser renders the document.
	//
	// The same path returns CSS / JS / images for assets the rendered
	// HTML references via relative URLs, so a `<link href="style.css">`
	// inside the iframe resolves to a sibling raw URL and gets the
	// right Content-Type for application.
	if !wantsJSON(r) {
		if mt := mimeForPath(info.Path); mt != "" {
			w.Header().Set("Content-Type", mt)
			w.Header().Set("X-Palmux-Path", info.Path)
			w.Header().Set("X-Palmux-Size", strconv.FormatInt(info.Size, 10))
			// X-Content-Type-Options: keeps the browser from
			// MIME-sniffing the body and overriding our type.
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(body)
			return
		}
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

// wantsJSON returns true when the caller's Accept header explicitly
// asks for `application/json` (S026). The Files-tab dispatcher always
// sets `Accept: application/json` so it stays on the JSON envelope
// path; the HTML preview iframe uses a default browser Accept header
// (`text/html,...`) and gets the raw direct-body path instead.
//
// We do a substring check rather than a strict media-type parse — the
// browser's default Accept header can be "text/html,application/xhtml+xml,…"
// or similar, and we just need the binary "did the dispatcher ask for
// JSON or not?" answer.
func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// previewFile handles `GET /files/preview/{path...}` (S026).
//
// Why a separate endpoint? The S010 / S011 raw endpoint encodes the
// worktree path in the query string (`?path=preview/index.html`),
// which is fine for the dispatcher's API calls but breaks relative
// URL resolution inside an iframe document. When the rendered HTML
// contains `<link href="style.css">`, the browser resolves that
// against the iframe's URL — and a query-string base means the
// relative href clobbers the `?path=` parameter, producing
// `?path=style.css` (not `?path=preview/style.css`). The result is
// a 404 / wrong file.
//
// Putting the worktree path in the URL path itself fixes that —
// relative resolution then works the way the browser expects:
//
//	iframe.src = ".../files/preview/preview/index.html"
//	<link href="style.css"> →
//	  ".../files/preview/preview/style.css" → correct.
//
// We serve every file (not just HTML) through this endpoint so the
// iframe can pull CSS / JS / images via relative paths. MIME mapping
// + CSP behavior is identical to the raw endpoint's S026 path.
//
// Auth still flows through the standard middleware (cookie / bearer);
// the iframe inherits the parent's cookie at *load time* but, because
// of `sandbox` without `allow-same-origin`, scripts inside the iframe
// see a unique opaque origin and CANNOT read the cookie themselves.
func (h *handler) previewFile(w http.ResponseWriter, r *http.Request) {
	root, err := h.branchPath(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Worktree-relative path lives in `{path...}` (Go 1.22+ wildcard
	// pattern). Empty path → 400.
	path := r.PathValue("path")
	if path == "" {
		writeErr(w, errors.New("path required"))
		return
	}
	// Read the body up to the soft cap so a single huge file can't
	// exhaust the server. We deliberately use the same default cap
	// the raw endpoint uses; the dispatcher's `previewMaxBytes`
	// gate prevents the iframe from loading too-large HTML in the
	// first place.
	body, info, err := ReadFile(root, path, defaultReadLimit)
	if err != nil {
		writeErr(w, err)
		return
	}
	if etag, err := EtagFor(root, path); err == nil {
		w.Header().Set("ETag", etag)
	}
	w.Header().Set("Content-Security-Policy", rawCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if mt := mimeForPath(info.Path); mt != "" {
		w.Header().Set("Content-Type", mt)
	} else {
		// Fall back to the sniffed MIME — important for arbitrary
		// binary assets the rendered HTML may reference.
		w.Header().Set("Content-Type", info.MIME)
	}
	w.Header().Set("X-Palmux-Path", info.Path)
	w.Header().Set("X-Palmux-Size", strconv.FormatInt(info.Size, 10))
	_, _ = w.Write(body)
}

func maxResultsParam(q map[string][]string, fallback int) int {
	if v := q["max"]; len(v) > 0 {
		if n, err := strconv.Atoi(v[0]); err == nil && n > 0 && n < 5000 {
			return n
		}
	}
	return fallback
}
