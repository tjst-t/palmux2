package files

import (
	"bytes"
	"html"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// isMarkdownPath reports whether the worktree-relative path names a
// Markdown document. Mirrors the extension set the browser MIME sniffer
// maps to `text/markdown` (see browser.go).
func isMarkdownPath(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// mdRenderer is the shared goldmark instance used to render the
// full-screen "Open in new tab" Markdown preview.
//
// Extensions: GFM (tables / strikethrough / autolinks / task lists) so
// the rendering matches what GitHub — and the inline ReactMarkdown +
// remark-gfm viewer — produce. Auto heading IDs mirror the inline
// viewer's rehype-slug so in-document `#anchor` links resolve.
//
// Raw HTML is intentionally NOT unsafe-rendered (goldmark escapes HTML
// blocks by default). The new-tab preview is served same-origin and
// carries the session cookie (no iframe sandbox), so passing through
// arbitrary `<script>` from a worktree file would be an XSS vector —
// escaping matches the inline viewer (react-markdown without rehype-raw)
// and keeps the cookie out of reach.
var mdRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(gmhtml.WithHardWraps()),
)

// mdPreviewCSP locks down the Markdown preview document. Unlike the
// generic rawCSP (which intentionally allows CDN scripts so user HTML
// mocks render), the Markdown doc is fully server-generated: it needs
// only its own inline `<style>` and whatever images the Markdown
// references (relative — served by this same endpoint — or https/data).
// No script is ever emitted, so `script-src` is omitted (falls back to
// `default-src 'none'`).
const mdPreviewCSP = "default-src 'none'; " +
	"style-src 'unsafe-inline'; " +
	"img-src 'self' https: data: blob:; " +
	"font-src 'self' data:; " +
	"base-uri 'none'"

// renderMarkdownDoc renders Markdown source into a complete, self-
// contained HTML document (embedded CSS, theme-aware via
// prefers-color-scheme). title is shown in the browser tab and as the
// document's accessible name; it is HTML-escaped.
func renderMarkdownDoc(source []byte, title string) ([]byte, error) {
	var rendered bytes.Buffer
	if err := mdRenderer.Convert(source, &rendered); err != nil {
		return nil, err
	}

	var doc bytes.Buffer
	doc.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n")
	doc.WriteString("<meta charset=\"utf-8\">\n")
	doc.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	doc.WriteString("<title>")
	doc.WriteString(html.EscapeString(title))
	doc.WriteString("</title>\n<style>\n")
	doc.WriteString(mdPreviewCSS)
	doc.WriteString("\n</style>\n</head>\n<body>\n<article class=\"markdown-body\">\n")
	doc.Write(rendered.Bytes())
	doc.WriteString("\n</article>\n</body>\n</html>\n")
	return doc.Bytes(), nil
}

// mdPreviewCSS is a compact GitHub-flavored Markdown stylesheet, theme-
// aware via prefers-color-scheme and loosely aligned with palmux2's Fog
// palette (accent #7c8aff). Kept inline so the document is fully self-
// contained and works offline (single-binary philosophy).
const mdPreviewCSS = `:root {
  --md-fg: #1f2328; --md-bg: #ffffff; --md-muted: #59636e;
  --md-border: #d1d9e0; --md-code-bg: #f6f8fa; --md-accent: #5c6ae0;
  --md-quote: #59636e; --md-quote-border: #d1d9e0;
}
@media (prefers-color-scheme: dark) {
  :root {
    --md-fg: #d4d4d8; --md-bg: #0f1117; --md-muted: #8b8fa0;
    --md-border: #1e2028; --md-code-bg: #13151c; --md-accent: #9ba6ff;
    --md-quote: #8b8fa0; --md-quote-border: #1e2028;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0; background: var(--md-bg); color: var(--md-fg);
  font-family: "Geist", "Noto Sans JP", -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  font-size: 16px; line-height: 1.6;
}
.markdown-body {
  max-width: 860px; margin: 0 auto; padding: 2.5rem 1.5rem 5rem;
  word-wrap: break-word;
}
.markdown-body h1, .markdown-body h2, .markdown-body h3,
.markdown-body h4, .markdown-body h5, .markdown-body h6 {
  margin: 1.6em 0 0.6em; font-weight: 600; line-height: 1.25;
}
.markdown-body h1 { font-size: 2em; padding-bottom: .3em; border-bottom: 1px solid var(--md-border); }
.markdown-body h2 { font-size: 1.5em; padding-bottom: .3em; border-bottom: 1px solid var(--md-border); }
.markdown-body h3 { font-size: 1.25em; }
.markdown-body h4 { font-size: 1em; }
.markdown-body p, .markdown-body ul, .markdown-body ol, .markdown-body blockquote, .markdown-body table { margin: 0 0 1em; }
.markdown-body a { color: var(--md-accent); text-decoration: none; }
.markdown-body a:hover { text-decoration: underline; }
.markdown-body code {
  font-family: "Geist Mono", "Cascadia Code", "Fira Code", ui-monospace, monospace;
  font-size: 85%; background: var(--md-code-bg);
  padding: .2em .4em; border-radius: 6px;
}
.markdown-body pre {
  background: var(--md-code-bg); padding: 1rem; border-radius: 8px;
  overflow: auto; margin: 0 0 1em;
}
.markdown-body pre code { background: none; padding: 0; font-size: 90%; }
.markdown-body blockquote {
  color: var(--md-quote); border-left: .25em solid var(--md-quote-border);
  padding: 0 1em; margin-left: 0;
}
.markdown-body table { border-collapse: collapse; display: block; overflow: auto; width: max-content; max-width: 100%; }
.markdown-body th, .markdown-body td { border: 1px solid var(--md-border); padding: 6px 13px; }
.markdown-body th { font-weight: 600; background: var(--md-code-bg); }
.markdown-body img { max-width: 100%; }
.markdown-body hr { height: 1px; border: 0; background: var(--md-border); margin: 1.5em 0; }
.markdown-body ul.contains-task-list { list-style: none; padding-left: 1em; }
.markdown-body li { margin: .25em 0; }`
