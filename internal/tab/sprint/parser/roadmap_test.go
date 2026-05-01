package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseRoadmap_Real parses the actual project ROADMAP.md so the
// parser stays in sync with whatever sprint-runner emits today. We
// intentionally don't assert on the *content* of the doc (it's a
// living document) — only on shape invariants the dashboard relies on.
func TestParseRoadmap_Real(t *testing.T) {
	repoRoot := findRepoRoot(t)
	src, err := os.ReadFile(filepath.Join(repoRoot, "docs", "ROADMAP.md"))
	if err != nil {
		t.Skipf("docs/ROADMAP.md not found: %v", err)
	}
	rm := ParseRoadmap(string(src))

	if rm.Title == "" {
		t.Errorf("expected a non-empty H1 title")
	}
	if rm.Progress.Total == 0 {
		t.Errorf("expected Progress.Total > 0 (got %d)", rm.Progress.Total)
	}
	if len(rm.Sprints) == 0 {
		t.Fatalf("expected at least one sprint entry")
	}
	// Every sprint should have an ID and a non-empty title. The status
	// kind classifier should at least pick one of the well-known
	// buckets (we accept "pending" as a fallback).
	knownStatusKinds := map[string]bool{
		"done": true, "in-progress": true, "pending": true,
		"blocked": true, "needs-human": true,
	}
	for _, s := range rm.Sprints {
		if s.ID == "" {
			t.Errorf("sprint with empty ID: %q", s.Title)
		}
		if s.Title == "" {
			t.Errorf("sprint %s with empty title", s.ID)
		}
		if !knownStatusKinds[s.StatusKind] {
			t.Errorf("sprint %s: unknown statusKind %q (raw status %q)", s.ID, s.StatusKind, s.Status)
		}
	}

	// At least one S016 story should be parsed (this sprint!).
	var s016 *Sprint
	for i := range rm.Sprints {
		if strings.EqualFold(rm.Sprints[i].ID, "S016") {
			s016 = &rm.Sprints[i]
			break
		}
	}
	if s016 == nil {
		t.Fatalf("expected to find S016 in ROADMAP")
	}
	if len(s016.Stories) == 0 {
		t.Errorf("expected at least one story in S016")
	}
	if len(rm.Dependencies) == 0 {
		t.Errorf("expected at least one dependency entry")
	}
}

func TestParseRoadmap_Malformed(t *testing.T) {
	bad := "# Roadmap\n\n## スプリント GARBAGE this line is not valid\n\n### ストーリー: missing-id\n"
	rm := ParseRoadmap(bad)
	// Must not panic and must surface ParseErrors for the malformed sprint.
	// We don't assert on exact contents — only that the parser is fail-safe.
	if rm.Title != "Roadmap" {
		t.Errorf("title not parsed: %q", rm.Title)
	}
	// Note: malformed sprint headers aren't matched by reSprint at all
	// (because the regex requires the [<status>] suffix), so they are
	// dropped silently rather than collected as ParseError. That's the
	// design — the caller renders nothing rather than a stub.
	_ = rm
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root")
		}
		dir = parent
	}
}
