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
// We deliberately don't spin up the full handler here — the handler's
// path through the store / branch lookup is exercised by E2E tests.
// These unit tests guard the small pure helpers so a typo in the MIME
// table or the CSP doesn't slip past the type checker.

package files

import (
	"net/http/httptest"
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
