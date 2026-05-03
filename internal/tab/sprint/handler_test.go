package sprint

import (
	"strings"
	"testing"

	"github.com/tjst-t/palmux2/internal/tab/sprint/parser"
)

// Ensure the Mermaid graph emitter handles titles containing parens
// without producing a parse error. Regression for the
// `[S003: Sub-agent (Task) 入れ子ツリー]` case where the unquoted
// `[...(Task)...]` was parsed as a node-shape sequence.
func TestBuildMermaid_ParensInTitle(t *testing.T) {
	sprints := []TimelineEntry{
		{ID: "S003", Title: "Sub-agent (Task) 入れ子ツリー", StatusKind: "done"},
		{ID: "S004", Title: `Quote " test`, StatusKind: "pending"},
	}
	deps := []parser.Dependency{}

	got := buildMermaid(sprints, deps)

	// Every node label must be wrapped in double quotes so that
	// `(`, `)`, `[`, `]` inside the title do not interact with the
	// surrounding `[ ... ]` Mermaid syntax.
	if !strings.Contains(got, `S003["S003: Sub-agent (Task) 入れ子ツリー"]`) {
		t.Errorf("expected quoted label with parens preserved, got:\n%s", got)
	}
	// Internal `"` must be entity-escaped so the closing `"]` is not
	// triggered prematurely.
	if !strings.Contains(got, `S004["S004: Quote #quot; test"]`) {
		t.Errorf("expected entity-escaped quote in label, got:\n%s", got)
	}
	// Sanity: no bare `(` immediately followed by an unescaped close
	// — this is the exact pattern that produced the parse error.
	if strings.Contains(got, "Sub-agent (Task)]") {
		t.Errorf("found unsafe unquoted parens before closing bracket: %s", got)
	}
}

func TestEscapeMermaid_TruncatesLongTitles(t *testing.T) {
	long := strings.Repeat("a", 60)
	got := escapeMermaid(long)
	if len([]rune(got)) != 40 {
		t.Errorf("expected truncated to 40 runes, got %d (%q)", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
}

// Multi-byte (UTF-8) titles must not be sliced mid-rune by truncation.
func TestEscapeMermaid_RuneAwareTruncation(t *testing.T) {
	// 50 Japanese runes — well past the 40-rune cap.
	long := strings.Repeat("入", 50)
	got := escapeMermaid(long)
	for _, r := range got {
		if r == '�' {
			t.Errorf("byte-level slice produced invalid UTF-8 in: %q", got)
		}
	}
}
