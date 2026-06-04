package files

import (
	"strings"
	"testing"
)

func TestIsMarkdownPath(t *testing.T) {
	cases := map[string]bool{
		"README.md":      true,
		"docs/guide.MD":  true,
		"notes.markdown": true,
		"a/b/c.Markdown": true,
		"index.html":     false,
		"style.css":      false,
		"main.go":        false,
		"plain.txt":      false,
		"no-extension":   false,
		"weird.md.bak":   false,
	}
	for in, want := range cases {
		if got := isMarkdownPath(in); got != want {
			t.Errorf("isMarkdownPath(%q) = %v; want %v", in, got, want)
		}
	}
}

// TestRenderMarkdownDoc guards the full-screen "Open in new tab" preview:
// Markdown source must come back as a complete HTML document with the
// content rendered (headings, GFM tables, task lists) — not raw text.
func TestRenderMarkdownDoc(t *testing.T) {
	src := []byte("# Title\n\n" +
		"Some **bold** text and a [link](https://example.com).\n\n" +
		"| A | B |\n|---|---|\n| 1 | 2 |\n\n" +
		"- [x] done\n- [ ] todo\n")
	out, err := renderMarkdownDoc(src, "guide.md")
	if err != nil {
		t.Fatalf("renderMarkdownDoc: %v", err)
	}
	doc := string(out)

	for _, want := range []string{
		"<!DOCTYPE html>",
		"<title>guide.md</title>",
		`class="markdown-body"`,
		"<h1",         // heading rendered, not literal "# Title"
		">Title</h1>", // heading text
		"<strong>bold</strong>",
		`href="https://example.com"`,
		"<table>",         // GFM table extension active
		`type="checkbox"`, // GFM task list extension active
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("rendered doc missing %q\n---\n%s", want, doc)
		}
	}

	// Raw "# Title" must not survive as literal text (it became a heading).
	if strings.Contains(doc, "# Title") {
		t.Errorf("rendered doc still contains raw markdown heading marker:\n%s", doc)
	}
}

// TestRenderMarkdownDocEscapesTitle ensures the tab title can't break out
// of the <title> element (the file base name is attacker-influenceable via
// crafted filenames in a worktree).
func TestRenderMarkdownDocEscapesTitle(t *testing.T) {
	out, err := renderMarkdownDoc([]byte("hi"), "</title><script>x</script>.md")
	if err != nil {
		t.Fatalf("renderMarkdownDoc: %v", err)
	}
	if strings.Contains(string(out), "<script>x</script>") {
		t.Errorf("title not HTML-escaped:\n%s", string(out))
	}
}

// TestRenderMarkdownDocEscapesRawHTML ensures raw HTML in the Markdown
// body is escaped (goldmark default), since the new-tab preview is served
// same-origin with the session cookie and no iframe sandbox.
func TestRenderMarkdownDocEscapesRawHTML(t *testing.T) {
	out, err := renderMarkdownDoc([]byte("normal\n\n<script>steal()</script>\n"), "x.md")
	if err != nil {
		t.Fatalf("renderMarkdownDoc: %v", err)
	}
	if strings.Contains(string(out), "<script>steal()</script>") {
		t.Errorf("raw HTML <script> was not escaped:\n%s", string(out))
	}
}
