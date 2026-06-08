// Sprint S026 — handler-level tests for MIME mapping + CSP wiring.
//
// These cover the new "raw-body for iframe preview" path on the
// `/files/raw` endpoint:
//
//   - `mimeForPath` returns the right Content-Type for the formats the
//     HTML preview iframe needs (HTML / CSS / JS / common images).
//   - `wantsJSON` distinguishes the dispatcher (`Accept: application/json`)
//     from the iframe (default browser Accept).
//   - The CSP constant is shape-correct (no `allow-same-origin` style
//     leakage, baseline directives present).
//
// Sc7818e — download handler unit tests (downloadFromRoot):
//
//   - single file: body matches file bytes, Content-Disposition is attachment.
//   - RFC 5987: non-ASCII filename (Japanese) is percent-encoded in filename*.
//   - traversal path: 400, no body written.
//   - directory zip: archive contains expected entries including empty subdirs.
//   - multi-path zip: archive contains exactly the requested files.
//   - bad path mixed into multi-request: 400, no zip body.
//
// The store / branch-lookup path is exercised by E2E tests; these unit
// tests call downloadFromRoot directly to stay self-contained.

package files

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMimeForPath(t *testing.T) {
	cases := map[string]string{
		"index.html":           "text/html; charset=utf-8",
		"page.htm":             "text/html; charset=utf-8",
		"style.css":            "text/css; charset=utf-8",
		"app.js":               "application/javascript; charset=utf-8",
		"module.mjs":           "application/javascript; charset=utf-8",
		"data.json":            "application/json; charset=utf-8",
		"icon.svg":             "image/svg+xml",
		"photo.png":            "image/png",
		"photo.jpg":            "image/jpeg",
		"photo.JPEG":           "image/jpeg",
		"animation.gif":        "image/gif",
		"hero.webp":            "image/webp",
		"feed.xml":             "application/xml; charset=utf-8",
		"sub/dir/main.js":      "application/javascript; charset=utf-8",
		"NESTED/Deep/PAGE.HTM": "text/html; charset=utf-8",
		// Negative cases: extensions we don't preview must fall through
		// to the existing JSON-envelope path (mimeForPath returns "").
		"go.mod":     "",
		"main.go":    "",
		"README.md":  "",
		"no-ext":     "",
		"binary.bin": "",
	}
	for path, want := range cases {
		got := mimeForPath(path)
		if got != want {
			t.Errorf("mimeForPath(%q) = %q; want %q", path, got, want)
		}
	}
}

// TestEnsureTextCharset guards the full-screen "Open" preview mojibake
// fix: the sniffed MIME for Markdown / source files has no charset, and
// with `nosniff` the browser decodes UTF-8 (CJK) with the platform
// default unless we anchor charset=utf-8 on text/* responses.
func TestEnsureTextCharset(t *testing.T) {
	cases := map[string]string{
		// text/* without charset → charset appended.
		"text/markdown":   "text/markdown; charset=utf-8",
		"text/plain":      "text/plain; charset=utf-8",
		"text/x-go":       "text/x-go; charset=utf-8",
		"text/x-python":   "text/x-python; charset=utf-8",
		"text/typescript": "text/typescript; charset=utf-8",
		// Already has charset → unchanged.
		"text/html; charset=utf-8": "text/html; charset=utf-8",
		// Non-text MIME → unchanged.
		"image/png":                "image/png",
		"application/octet-stream": "application/octet-stream",
		"application/json":         "application/json",
		"":                         "",
	}
	for in, want := range cases {
		if got := ensureTextCharset(in); got != want {
			t.Errorf("ensureTextCharset(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestWantsJSON(t *testing.T) {
	cases := []struct {
		accept string
		want   bool
	}{
		{"application/json", true},
		{"application/json, */*", true},
		{"text/html,application/xhtml+xml,application/xml;q=0.9", false},
		{"text/html", false},
		{"*/*", false},
		{"", false},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/foo", nil)
		if c.accept != "" {
			req.Header.Set("Accept", c.accept)
		}
		got := wantsJSON(req)
		if got != c.want {
			t.Errorf("wantsJSON(Accept=%q) = %v; want %v", c.accept, got, c.want)
		}
	}
}

func TestRawCSP_Shape(t *testing.T) {
	// Defense-in-depth invariants. The CSP is one of two rails (the
	// other being `iframe sandbox="allow-scripts"` on the frontend).
	// If any of these directives drift the security review will fail.
	mustContain := []string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src 'self'",
		"connect-src 'self'",
	}
	for _, d := range mustContain {
		if !strings.Contains(rawCSP, d) {
			t.Errorf("rawCSP missing directive %q; got %q", d, rawCSP)
		}
	}
	// Must NOT contain wildcard origin in script-src / connect-src —
	// that would let exfiltration succeed if the iframe ever escapes
	// the sandbox.
	if strings.Contains(rawCSP, "script-src *") {
		t.Errorf("rawCSP allows wildcard script-src: %q", rawCSP)
	}
	if strings.Contains(rawCSP, "connect-src *") {
		t.Errorf("rawCSP allows wildcard connect-src: %q", rawCSP)
	}
}

// ── Sc7818e: download handler unit tests ──────────────────────────────────

// makeDownloadRequest is a helper that builds a GET request for downloadFromRoot
// with one or more ?path= params.
func makeDownloadRequest(t *testing.T, paths ...string) *http.Request {
	t.Helper()
	u := "/download"
	for i, p := range paths {
		if i == 0 {
			u += "?path=" + p
		} else {
			u += "&path=" + p
		}
	}
	return httptest.NewRequest("GET", u, nil)
}

// TestDownloadSingleFile verifies that a single regular file is streamed as
// an attachment with the correct body and Content-Disposition header.
func TestDownloadSingleFile(t *testing.T) {
	root := t.TempDir()
	content := []byte("hello download world")
	if err := os.WriteFile(filepath.Join(root, "readme.txt"), content, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := makeDownloadRequest(t, "readme.txt")
	rr := httptest.NewRecorder()
	downloadFromRoot(rr, req, root)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	if got := rr.Body.Bytes(); !bytes.Equal(got, content) {
		t.Errorf("body: got %q, want %q", got, content)
	}
	cd := rr.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") {
		t.Errorf("Content-Disposition must start with 'attachment': %q", cd)
	}
	if !strings.Contains(cd, "readme.txt") {
		t.Errorf("Content-Disposition must contain filename: %q", cd)
	}
}

// TestDownloadSingleFileRFC5987 verifies that a file with a non-ASCII (Japanese)
// name yields a Content-Disposition header containing the RFC 5987 encoded form.
func TestDownloadSingleFileRFC5987(t *testing.T) {
	root := t.TempDir()
	filename := "設計メモ.txt"
	if err := os.WriteFile(filepath.Join(root, filename), []byte("内容"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := makeDownloadRequest(t, filename)
	rr := httptest.NewRecorder()
	downloadFromRoot(rr, req, root)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (got body: %s)", rr.Code, rr.Body.String())
	}
	cd := rr.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "filename*=UTF-8''") {
		t.Errorf("Content-Disposition missing RFC5987 encoding: %q", cd)
	}
	// The percent-encoded form of the Japanese chars must be present.
	if !strings.Contains(cd, "%E8%A8%AD") {
		t.Errorf("Content-Disposition missing percent-encoded Japanese: %q", cd)
	}
}

// TestDownloadTraversalRejected verifies that a path traversal attempt returns
// 400 and no zip body.
func TestDownloadTraversalRejected(t *testing.T) {
	root := t.TempDir()

	req := makeDownloadRequest(t, "../../etc/passwd")
	rr := httptest.NewRecorder()
	downloadFromRoot(rr, req, root)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
}

// TestDownloadDirectoryZip verifies that downloading a directory returns a
// valid zip containing expected file entries and an empty-subdirectory entry.
func TestDownloadDirectoryZip(t *testing.T) {
	root := t.TempDir()
	// Create: src/main.go, src/util/helper.go, src/empty/ (empty dir).
	if err := os.MkdirAll(filepath.Join(root, "src", "util"), 0o755); err != nil {
		t.Fatalf("mkdir util: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src", "empty"), 0o755); err != nil {
		t.Fatalf("mkdir empty: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("seed main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "util", "helper.go"), []byte("package util"), 0o644); err != nil {
		t.Fatalf("seed helper.go: %v", err)
	}

	req := makeDownloadRequest(t, "src")
	rr := httptest.NewRecorder()
	downloadFromRoot(rr, req, root)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	cd := rr.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "src.zip") {
		t.Errorf("Content-Disposition should reference src.zip: %q", cd)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "application/zip" {
		t.Errorf("Content-Type: got %q, want application/zip", ct)
	}

	// Parse the zip.
	body := rr.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}

	wantFiles := []string{"src/main.go", "src/util/helper.go"}
	for _, want := range wantFiles {
		if !names[want] {
			t.Errorf("zip missing expected entry %q; got %v", want, names)
		}
	}

	// Empty dir should be present as a directory entry (name ending in /).
	hasEmptyDir := false
	for n := range names {
		if strings.HasSuffix(n, "empty/") {
			hasEmptyDir = true
			break
		}
	}
	if !hasEmptyDir {
		t.Errorf("zip missing empty-dir entry (must end with /); got %v", names)
	}

	// Verify file content round-trips.
	for _, f := range zr.File {
		if f.Name == "src/main.go" {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			if string(data) != "package main" {
				t.Errorf("src/main.go content: got %q", data)
			}
		}
	}
}

// TestDownloadMultiPathZip verifies that ?path=a&path=b returns a zip with
// exactly those two entries and excludes other files in the worktree.
func TestDownloadMultiPathZip(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatalf("seed a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("bbb"), 0o644); err != nil {
		t.Fatalf("seed b.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "c.txt"), []byte("ccc"), 0o644); err != nil {
		t.Fatalf("seed c.txt: %v", err)
	}

	req := makeDownloadRequest(t, "a.txt", "b.txt")
	rr := httptest.NewRecorder()
	downloadFromRoot(rr, req, root)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}

	body := rr.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}

	if !names["a.txt"] {
		t.Errorf("zip missing a.txt; got %v", names)
	}
	if !names["b.txt"] {
		t.Errorf("zip missing b.txt; got %v", names)
	}
	if names["c.txt"] {
		t.Errorf("zip must NOT contain c.txt (not requested); got %v", names)
	}
}

// TestDownloadBadPathInMultiRequest verifies that a bad path mixed into a
// multi-path request returns 400 and no zip body (no "PK" prefix).
func TestDownloadBadPathInMultiRequest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "good.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := makeDownloadRequest(t, "good.txt", "../../etc/passwd")
	rr := httptest.NewRecorder()
	downloadFromRoot(rr, req, root)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
	body := rr.Body.Bytes()
	if bytes.HasPrefix(body, []byte("PK")) {
		t.Errorf("response must not be a zip (got PK prefix)")
	}
}

// TestContentDispositionAttachment verifies the helper's ASCII fallback and
// RFC 5987 encoding for plain ASCII names.
func TestContentDispositionAttachment(t *testing.T) {
	cases := []struct {
		name        string
		wantPrefix  string
		wantContain string
	}{
		{
			name:        "report.pdf",
			wantPrefix:  `attachment; filename="report.pdf"`,
			wantContain: `filename*=UTF-8''report.pdf`,
		},
		{
			name:        "data file.csv",
			wantPrefix:  `attachment; filename="data file.csv"`,
			wantContain: `filename*=UTF-8''`,
		},
	}
	for _, c := range cases {
		got := contentDispositionAttachment(c.name)
		if !strings.HasPrefix(got, c.wantPrefix) {
			t.Errorf("contentDispositionAttachment(%q) = %q; want prefix %q", c.name, got, c.wantPrefix)
		}
		if !strings.Contains(got, c.wantContain) {
			t.Errorf("contentDispositionAttachment(%q) = %q; want to contain %q", c.name, got, c.wantContain)
		}
	}
}

// TestDownloadNoPaths verifies that a request with no ?path params returns 400.
func TestDownloadNoPaths(t *testing.T) {
	root := t.TempDir()
	req := httptest.NewRequest("GET", "/download", nil)
	rr := httptest.NewRecorder()
	downloadFromRoot(rr, req, root)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
}

// TestDownloadDirSkipsSymlink verifies that symlinks inside a downloaded
// directory are not included in the resulting zip (symlink-escape guard).
func TestDownloadDirSkipsSymlink(t *testing.T) {
	root := t.TempDir()
	// Create: d/real.txt (a real file) and d/link -> /etc/hostname (external
	// symlink) and d/inlink -> d/real.txt (intra-worktree symlink).
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatalf("mkdir d: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "d", "real.txt"), []byte("real content"), 0o644); err != nil {
		t.Fatalf("seed real.txt: %v", err)
	}
	// Create symlinks; skip the test if the filesystem doesn't support them.
	if err := os.Symlink("/etc/hostname", filepath.Join(root, "d", "link")); err != nil {
		t.Skip("symlink creation not supported:", err)
	}
	if err := os.Symlink(filepath.Join(root, "d", "real.txt"), filepath.Join(root, "d", "inlink")); err != nil {
		t.Skip("symlink creation not supported:", err)
	}

	req := makeDownloadRequest(t, "d")
	rr := httptest.NewRecorder()
	downloadFromRoot(rr, req, root)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	body := rr.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}

	// real.txt must be present.
	if !names["d/real.txt"] {
		t.Errorf("zip missing d/real.txt; got %v", names)
	}
	// Symlinks must NOT appear.
	if names["d/link"] {
		t.Errorf("zip must NOT contain symlink entry d/link; got %v", names)
	}
	if names["d/inlink"] {
		t.Errorf("zip must NOT contain intra-worktree symlink entry d/inlink; got %v", names)
	}
}

// TestRFC5987Encode verifies that rfc5987Encode percent-encodes characters
// that url.PathEscape would leave unencoded (=, @) and that attr-chars are
// left as-is.
func TestRFC5987Encode(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		// Pure attr-chars → unchanged.
		{"report.pdf", "report.pdf"},
		// = and @ must be percent-encoded (RFC 5987 attr-char set excludes them).
		{"a=b@c.txt", "a%3Db%40c.txt"},
		// Space must be percent-encoded.
		{"my file.txt", "my%20file.txt"},
		// Japanese characters → multi-byte UTF-8, each byte encoded.
		// 設 = 0xE8 0xA8 0xAD
		{"設", "%E8%A8%AD"},
	}
	for _, c := range cases {
		if got := rfc5987Encode(c.input); got != c.want {
			t.Errorf("rfc5987Encode(%q) = %q; want %q", c.input, got, c.want)
		}
	}
}

// TestContentDispositionControlBytes verifies that control characters (< 0x20)
// in a filename are replaced with '_' in the ASCII fallback portion.
func TestContentDispositionControlBytes(t *testing.T) {
	// Filename with a tab (0x09) and a newline (0x0A) embedded.
	filename := "file\tname\nhere.txt"
	cd := contentDispositionAttachment(filename)
	// The ASCII fallback (filename= part) must not contain raw control chars.
	// Extract the quoted filename= value.
	if strings.Contains(cd, "\t") || strings.Contains(cd, "\n") {
		t.Errorf("Content-Disposition ASCII fallback contains raw control char: %q", cd)
	}
	// Should contain underscores replacing the control chars.
	if !strings.Contains(cd, "file_name_here.txt") {
		t.Errorf("Content-Disposition ASCII fallback should replace control chars with _: %q", cd)
	}
}
