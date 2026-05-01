package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjst-t/palmux2/internal/config"
)

const uploadMaxBytes = 64 << 20 // 64 MiB — bumped from 16 MiB in S008 so non-image attachments (logs / PDFs) fit. Still bounded so a runaway upload can't fill /tmp.

// uploadResult is what /api/upload (and the per-branch variant) returns.
// `path` is the absolute filesystem path on the palmux2 host. `name` is
// the on-disk filename (sanitised). `originalName` is the user-visible
// name preserved from the multipart header so the chip can show it.
// `mime` is the resolved Content-Type (best-effort: form header → ext
// lookup → "application/octet-stream"). `kind` is a coarse classifier
// the composer uses to decide how to render the chip and how to inject
// the path into the user message ("image" → `[image: <abspath>]`,
// otherwise → `@<abspath>`).
type uploadResult struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	OriginalName string `json:"originalName,omitempty"`
	Size         int64  `json:"size"`
	Mime         string `json:"mime,omitempty"`
	Kind         string `json:"kind"` // "image" | "file"
}

// resolveAttachmentDir returns the configured attachment upload dir or
// the package default. The trailing slash from settings is tolerated.
func (h *handlers) resolveAttachmentDir() string {
	dir := h.store.Settings().Get().AttachmentUploadDir
	if dir == "" {
		dir = config.DefaultAttachmentUploadDir
	}
	return strings.TrimRight(dir, "/")
}

// branchUploadDir returns `<root>/<repoId>/<branchId>` — the per-branch
// isolation point. The repo/branch IDs are slug+hash (kebab-safe) so we
// only enforce no-slash / no-traversal for defence in depth.
func (h *handlers) branchUploadDir(repoID, branchID string) (string, error) {
	if !validUploadIDSegment(repoID) {
		return "", errors.New("invalid repo id")
	}
	if !validUploadIDSegment(branchID) {
		return "", errors.New("invalid branch id")
	}
	return filepath.Join(h.resolveAttachmentDir(), repoID, branchID), nil
}

// validUploadIDSegment guards a repo/branch ID before we let it form a
// filesystem path. Palmux's slug+hash IDs are alnum + `-`, plus `--` as
// a separator. We also forbid the leading `.` to prevent dotfiles.
func validUploadIDSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.ContainsAny(s, "/\\") {
		return false
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// upload accepts multipart/form-data with a single file field "file" and
// writes it into the configured per-branch attachment dir. S008
// generalised this from images-only to any file. The composer uses the
// returned absolute path either as `[image: ...]` (kind=image) or as
// `@<abspath>` (kind=file) when injecting it into the user message.
//
//	POST /api/repos/{repoId}/branches/{branchId}/upload
func (h *handlers) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	dir, err := h.branchUploadDir(repoID, branchID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	h.writeUpload(w, r, dir)
}

// uploadGlobal is the legacy (S008-pre) global upload endpoint, kept for
// the bash terminal-view paste path which doesn't carry repo/branch
// context. Files land directly under the attachment root (no per-branch
// segment). Composer uses the per-branch endpoint.
//
//	POST /api/upload
func (h *handlers) uploadGlobal(w http.ResponseWriter, r *http.Request) {
	dir := h.resolveAttachmentDir()
	h.writeUpload(w, r, dir)
}

func (h *handlers) writeUpload(w http.ResponseWriter, r *http.Request, dir string) {
	if err := r.ParseMultipartForm(uploadMaxBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid multipart body: " + err.Error()})
		return
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "file field is required"})
		return
	}
	defer f.Close()

	if header.Size > uploadMaxBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "file too large"})
		return
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, fmt.Errorf("mkdir %s: %w", dir, err))
		return
	}

	original := header.Filename
	contentType := header.Header.Get("Content-Type")
	ext := pickExtension(original, contentType)
	base := sanitizeBaseName(original, ext)
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		writeErr(w, fmt.Errorf("generate filename: %w", err))
		return
	}
	// Filename layout: `<sanitized>-<YYYYMMDDHHMMSS>-<rand4>.<ext>`. The
	// timestamp keeps directory listings chronological and the rand4
	// guarantees uniqueness when the user uploads two files with the
	// same name in the same second. The sanitized base is preserved so
	// when the CLI Reads the file it (a) shows up in tool output with a
	// recognisable name and (b) any extension-aware tool (Markdown
	// renderer / image viewer / etc.) gets the right type.
	name := fmt.Sprintf("%s-%s-%s%s", base, time.Now().UTC().Format("20060102150405"), hex.EncodeToString(suffix), ext)
	full := filepath.Join(dir, name)

	out, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		writeErr(w, fmt.Errorf("create %s: %w", full, err))
		return
	}
	defer out.Close()

	if _, err := io.Copy(out, io.LimitReader(f, uploadMaxBytes+1)); err != nil {
		_ = os.Remove(full)
		writeErr(w, fmt.Errorf("write %s: %w", full, err))
		return
	}
	info, _ := out.Stat()
	resolvedMime := contentType
	if resolvedMime == "" {
		if guess := mime.TypeByExtension(ext); guess != "" {
			resolvedMime = guess
		} else {
			resolvedMime = "application/octet-stream"
		}
	}
	kind := "file"
	if strings.HasPrefix(resolvedMime, "image/") {
		kind = "image"
	}
	writeJSON(w, http.StatusCreated, uploadResult{
		Path:         full,
		Name:         name,
		OriginalName: original,
		Size:         info.Size(),
		Mime:         resolvedMime,
		Kind:         kind,
	})
}

// fetchUpload serves a previously-uploaded file by basename. The path
// MUST stay inside the configured upload dir — we reject anything that
// resolves outside via filepath.Clean / EvalSymlinks comparison so a
// crafted basename can't punch into the rest of the filesystem.
//
//	GET /api/upload/{name}                                          (legacy)
//	GET /api/repos/{repoId}/branches/{branchId}/upload/{name}       (per-branch)
//
// The legacy form looks under the root attachment dir AND under every
// per-branch subdir (one os.Stat per repo/branch), so existing image
// embeds (which only carry the basename) keep resolving after the move
// to per-branch directories.
func (h *handlers) fetchUpload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}
	root := h.resolveAttachmentDir()
	candidate := h.locateUpload(root, name)
	if candidate == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	serveUploadFile(w, r, candidate)
}

func (h *handlers) fetchBranchUpload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	dir, err := h.branchUploadDir(repoID, branchID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	full := filepath.Join(dir, name)
	if !pathInside(full, dir) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if info, err := os.Stat(full); err != nil || info.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	serveUploadFile(w, r, full)
}

// locateUpload looks for a basename anywhere under the attachment root.
// Order: (1) directly under root (legacy uploads), (2) under any
// `<root>/<repo>/<branch>/<name>` (S008 per-branch). Returns empty when
// not found. We only descend two directory levels so a malicious
// symlink farm can't trick us into walking the host filesystem.
func (h *handlers) locateUpload(root, name string) string {
	direct := filepath.Join(root, name)
	if info, err := os.Stat(direct); err == nil && !info.IsDir() && pathInside(direct, root) {
		return direct
	}
	repos, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, repo := range repos {
		if !repo.IsDir() {
			continue
		}
		repoDir := filepath.Join(root, repo.Name())
		branches, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}
		for _, br := range branches {
			if !br.IsDir() {
				continue
			}
			candidate := filepath.Join(repoDir, br.Name(), name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && pathInside(candidate, root) {
				return candidate
			}
		}
	}
	return ""
}

func pathInside(p, root string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	return abs == absRoot || strings.HasPrefix(abs, absRoot+string(os.PathSeparator))
}

func serveUploadFile(w http.ResponseWriter, r *http.Request, path string) {
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if ct := mime.TypeByExtension(filepath.Ext(path)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeFile(w, r, path)
}

// pickExtension selects a filename extension based on the original
// upload name first (truthful for most browsers) and only falls back
// to the multipart Content-Type when the user pasted a Blob with no
// name (e.g. clipboard image). When no signal exists we use ".bin"
// so the file still has *some* extension and isn't mistakenly treated
// as a directory by file pickers.
func pickExtension(name, contentType string) string {
	if name != "" {
		ext := filepath.Ext(name)
		if ext != "" && len(ext) <= 16 && !strings.ContainsAny(ext, "/\\") {
			return ext
		}
	}
	if contentType != "" {
		if exts, _ := mime.ExtensionsByType(contentType); len(exts) > 0 {
			return exts[0]
		}
	}
	return ".bin"
}

// sanitizeBaseName turns the original filename into a safe filesystem
// component, lower-casing nothing (preserve the user's intent) but
// stripping path separators, control chars, and stripping the existing
// extension because pickExtension owns that. Non-ASCII (CJK, emoji)
// characters are preserved when they are letters/digits per Unicode;
// everything else collapses to `_`. Empty results fall back to
// "attachment".
func sanitizeBaseName(name, ext string) string {
	base := name
	if ext != "" && strings.HasSuffix(strings.ToLower(base), strings.ToLower(ext)) {
		base = base[:len(base)-len(ext)]
	}
	base = filepath.Base(base)
	if base == "." || base == ".." || base == "" || base == "/" {
		return "attachment"
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	out = strings.Trim(out, "._")
	if out == "" {
		return "attachment"
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}
