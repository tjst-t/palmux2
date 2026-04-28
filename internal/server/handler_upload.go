package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const uploadMaxBytes = 16 << 20 // 16 MiB

// uploadResult is what /api/upload returns. The frontend forwards `path` as
// terminal input so Claude / shells can read the file directly.
type uploadResult struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// uploadImage accepts multipart/form-data with a single file field "file" and
// writes it to settings.imageUploadDir. Returns the saved path so the caller
// can inject it into the terminal as a literal.
func (h *handlers) uploadImage(w http.ResponseWriter, r *http.Request) {
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

	settings := h.store.Settings().Get()
	dir := settings.ImageUploadDir
	if dir == "" {
		dir = "/tmp/palmux-uploads"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, fmt.Errorf("mkdir %s: %w", dir, err))
		return
	}

	ext := pickExtension(header.Filename, header.Header.Get("Content-Type"))
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		writeErr(w, fmt.Errorf("generate filename: %w", err))
		return
	}
	name := fmt.Sprintf("palmux-%s-%s%s", time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(suffix), ext)
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
	writeJSON(w, http.StatusCreated, uploadResult{Path: full, Size: info.Size()})
}

// fetchUpload serves a previously-uploaded file by basename. The path
// MUST stay inside the configured upload dir — we reject anything that
// resolves outside via filepath.Clean / EvalSymlinks comparison so a
// crafted basename can't punch into the rest of the filesystem.
//
//	GET /api/upload/{name}
func (h *handlers) fetchUpload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}
	settings := h.store.Settings().Get()
	dir := settings.ImageUploadDir
	if dir == "" {
		dir = "/tmp/palmux-uploads"
	}
	full := filepath.Join(dir, name)
	// Defence in depth: ensure the resolved path is still under dir.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		http.Error(w, "bad upload dir", http.StatusInternalServerError)
		return
	}
	absFile, err := filepath.Abs(full)
	if err != nil || !strings.HasPrefix(absFile, absDir+string(os.PathSeparator)) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	info, err := os.Stat(absFile)
	if err != nil || info.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Reasonable cache lifetime — these blobs are content-addressed by
	// random suffix so they never change.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if ct := mime.TypeByExtension(filepath.Ext(absFile)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeFile(w, r, absFile)
}

func pickExtension(name, contentType string) string {
	if name != "" {
		ext := filepath.Ext(name)
		if ext != "" && len(ext) <= 8 && !strings.ContainsAny(ext, "/\\") {
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
