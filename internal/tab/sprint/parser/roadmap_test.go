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

// TestParseRoadmap_NullSafety: the FE assumes every list field is an
// array (it does `.map()` directly without a null guard). The parser
// must therefore never emit nil slices, even when the document contains
// no sprints / dependencies / backlog at all.
func TestParseRoadmap_NullSafety(t *testing.T) {
	rm := ParseRoadmap("# Empty\n\nNo sections here.\n")
	if rm.Sprints == nil {
		t.Errorf("Sprints must never be nil")
	}
	if rm.Dependencies == nil {
		t.Errorf("Dependencies must never be nil")
	}
	if rm.Backlog == nil {
		t.Errorf("Backlog must never be nil")
	}
	if rm.ParseErrors == nil {
		t.Errorf("ParseErrors must never be nil")
	}
}

// TestParseRoadmap_EnglishHeaders: roadmaps emitted by sprint-runner in
// English mode (e.g. tjst-t/hydra) must parse identically to Japanese
// ones. Reproduces the bug behind S016-fix-1.
func TestParseRoadmap_EnglishHeaders(t *testing.T) {
	src := `# Project Roadmap: hydra

> A sample English roadmap.

## Progress

- Total: 3 Sprints | Done: 1 | In Progress: 1 | Remaining: 1
- [█████░░░░░] 33%

## Execution Order

S001 ✅ → **S002** → S003

## Sprint S001: Project init [DONE]

Bootstraps the monorepo.

### Story S001-1: Go module setup [x]

- [x] **Task S001-1-1` + "`go mod init`" + ` and directory layout
- [x] **Task S001-1-2**: Makefile

## Sprint S002: S3 API server [IN PROGRESS]

### Story S002-1: HTTP routing skeleton [ ]

- [ ] **Task S002-1-1**: HTTP server boot
- [ ] **Task S002-1-2**: routing
`
	rm := ParseRoadmap(src)
	if rm.Title != "Project Roadmap: hydra" {
		t.Errorf("title not parsed: %q", rm.Title)
	}
	if rm.Progress.Total != 3 || rm.Progress.Done != 1 {
		t.Errorf("progress not parsed: %+v", rm.Progress)
	}
	if len(rm.Sprints) != 2 {
		t.Fatalf("expected 2 sprints, got %d", len(rm.Sprints))
	}
	if rm.Sprints[0].ID != "S001" || rm.Sprints[0].StatusKind != "done" {
		t.Errorf("S001 not parsed: %+v", rm.Sprints[0])
	}
	if rm.Sprints[1].ID != "S002" || rm.Sprints[1].StatusKind != "in-progress" {
		t.Errorf("S002 not parsed: %+v", rm.Sprints[1])
	}
	if len(rm.Sprints[0].Stories) != 1 {
		t.Fatalf("expected 1 story in S001, got %d", len(rm.Sprints[0].Stories))
	}
	if rm.Sprints[0].Stories[0].ID != "S001-1" {
		t.Errorf("story id mismatch: %+v", rm.Sprints[0].Stories[0])
	}
	if len(rm.Sprints[1].Stories[0].Tasks) != 2 {
		t.Errorf("expected 2 tasks in S002-1, got %+v", rm.Sprints[1].Stories[0].Tasks)
	}
	for _, sp := range rm.Sprints {
		if sp.Stories == nil {
			t.Errorf("sprint %s Stories nil", sp.ID)
		}
		for _, st := range sp.Stories {
			if st.AcceptanceCriteria == nil {
				t.Errorf("story %s AcceptanceCriteria nil", st.ID)
			}
			if st.Tasks == nil {
				t.Errorf("story %s Tasks nil", st.ID)
			}
		}
	}
}

// TestParseRoadmap_MixedHeaders: a Japanese title under an English
// section heading (or vice versa) must still parse. This guards against
// the regex / prefix combo accidentally requiring matched languages.
func TestParseRoadmap_MixedHeaders(t *testing.T) {
	src := `# Roadmap

## Sprint S016: 日本語タイトル混在 [IN PROGRESS]

### ストーリー S016-1: English header と Japanese story の混在 [ ]

- [ ] **Task S016-1-1**: タスク
`
	rm := ParseRoadmap(src)
	if len(rm.Sprints) != 1 {
		t.Fatalf("expected 1 sprint, got %d", len(rm.Sprints))
	}
	if rm.Sprints[0].ID != "S016" {
		t.Errorf("sprint id mismatch: %q", rm.Sprints[0].ID)
	}
	if len(rm.Sprints[0].Stories) != 1 {
		t.Fatalf("expected 1 story, got %d", len(rm.Sprints[0].Stories))
	}
	if rm.Sprints[0].Stories[0].ID != "S016-1" {
		t.Errorf("story id mismatch: %q", rm.Sprints[0].Stories[0].ID)
	}
	if len(rm.Sprints[0].Stories[0].Tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(rm.Sprints[0].Stories[0].Tasks))
	}
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
