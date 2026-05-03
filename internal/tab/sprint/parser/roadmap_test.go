package parser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestParseRoadmap_Real parses the actual project ROADMAP.json so the
// parser stays in sync with whatever sprint-runner emits today. We
// don't assert on the *content* of the doc (it's a living document)
// — only on shape invariants the dashboard relies on.
func TestParseRoadmap_Real(t *testing.T) {
	repoRoot := findRepoRoot(t)
	src, err := os.ReadFile(filepath.Join(repoRoot, "docs", "ROADMAP.json"))
	if err != nil {
		t.Skipf("docs/ROADMAP.json not found: %v", err)
	}
	rm := ParseRoadmap(src)

	if rm.Title == "" {
		t.Errorf("expected a non-empty project title")
	}
	if rm.Progress.Total == 0 {
		t.Errorf("expected Progress.Total > 0 (got %d)", rm.Progress.Total)
	}
	if len(rm.Sprints) == 0 {
		t.Fatalf("expected at least one sprint entry")
	}
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

	// At least S001 must exist with at least one story.
	var s001 *Sprint
	for i := range rm.Sprints {
		if rm.Sprints[i].ID == "S001" {
			s001 = &rm.Sprints[i]
			break
		}
	}
	if s001 == nil {
		t.Fatalf("expected to find S001 in ROADMAP")
	}
	if len(s001.Stories) == 0 {
		t.Errorf("expected at least one story in S001")
	}
}

// TestParseRoadmap_NullSafety: the FE assumes every list field is an
// array (it does `.map()` directly without a null guard). Empty / missing
// JSON keys must therefore never produce a nil slice.
func TestParseRoadmap_NullSafety(t *testing.T) {
	rm := ParseRoadmap([]byte(`{}`))
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

// TestParseRoadmap_EmptyInput surfaces an empty file as a ParseError so
// the FE can show "no roadmap yet" without special-casing nil.
func TestParseRoadmap_EmptyInput(t *testing.T) {
	rm := ParseRoadmap(nil)
	if len(rm.ParseErrors) == 0 {
		t.Errorf("expected ParseError for empty input")
	}
}

// TestParseRoadmap_SyntaxError fails closed (returns ParseError with
// line/column hints) rather than panicking.
func TestParseRoadmap_SyntaxError(t *testing.T) {
	bad := []byte(`{"project": "broken", "progress": {`) // unterminated
	rm := ParseRoadmap(bad)
	if len(rm.ParseErrors) == 0 {
		t.Fatalf("expected ParseError for syntax error")
	}
	pe := rm.ParseErrors[0]
	if pe.Line == 0 {
		t.Errorf("expected line hint, got %+v", pe)
	}
	if pe.Section != "ROADMAP.json" {
		t.Errorf("expected section ROADMAP.json, got %q", pe.Section)
	}
}

// TestParseRoadmap_HappyPath exercises the basic doc → struct mapping.
func TestParseRoadmap_HappyPath(t *testing.T) {
	doc := map[string]any{
		"project":     "Test",
		"description": "demo",
		"progress": map[string]any{
			"total": 2, "done": 1, "in_progress": 1, "remaining": 0, "percentage": 50,
		},
		"execution_order": []string{"S001", "S002"},
		"sprints": map[string]any{
			"S001": map[string]any{
				"title":  "First",
				"status": "done",
				"stories": map[string]any{
					"S001-1": map[string]any{
						"title":  "story one",
						"status": "done",
						"acceptance_criteria": []map[string]any{
							{"id": "AC-S001-1-1", "description": "do thing", "test": "[AC-S001-1-1] in foo.spec.ts", "status": "pass"},
						},
						"tasks": map[string]any{
							"S001-1-1": map[string]any{"title": "task one", "status": "done"},
						},
					},
				},
			},
			"S002": map[string]any{
				"title":  "Second",
				"status": "in_progress",
				"stories": map[string]any{
					"S002-1": map[string]any{
						"title":  "story two",
						"status": "pending",
						"tasks":  map[string]any{},
					},
				},
			},
		},
		"dependencies": map[string]any{
			"S002": map[string]any{
				"depends_on": []string{"S001"},
				"reason":     "S002 builds on S001",
			},
		},
		"backlog": []map[string]any{
			{"title": "future thing", "description": "later", "added_in": "S001"},
		},
	}
	src, _ := json.Marshal(doc)
	rm := ParseRoadmap(src)

	if rm.Title != "Test" {
		t.Errorf("title: %q", rm.Title)
	}
	if rm.Progress.Total != 2 || rm.Progress.Done != 1 || rm.Progress.Percent != 50 {
		t.Errorf("progress: %+v", rm.Progress)
	}
	if len(rm.Sprints) != 2 {
		t.Fatalf("sprints: %d", len(rm.Sprints))
	}
	if rm.Sprints[0].ID != "S001" {
		t.Errorf("execution order not honored: first sprint = %q", rm.Sprints[0].ID)
	}
	if rm.Sprints[0].StatusKind != "done" {
		t.Errorf("S001 statusKind: %q", rm.Sprints[0].StatusKind)
	}
	if rm.Sprints[1].StatusKind != "in-progress" {
		t.Errorf("S002 statusKind: %q", rm.Sprints[1].StatusKind)
	}
	if len(rm.Sprints[0].Stories) != 1 {
		t.Fatalf("stories in S001: %d", len(rm.Sprints[0].Stories))
	}
	st := rm.Sprints[0].Stories[0]
	if st.ID != "S001-1" {
		t.Errorf("story id: %q", st.ID)
	}
	if len(st.AcceptanceCriteria) != 1 {
		t.Errorf("acs: %d", len(st.AcceptanceCriteria))
	}
	if !st.AcceptanceCriteria[0].Done {
		t.Errorf("ac.Done should be true for status=pass")
	}
	if len(st.Tasks) != 1 {
		t.Errorf("tasks: %d", len(st.Tasks))
	}
	if !st.Tasks[0].Done {
		t.Errorf("task.Done should be true for status=done")
	}
	if len(rm.Dependencies) != 1 {
		t.Fatalf("deps: %d", len(rm.Dependencies))
	}
	dep := rm.Dependencies[0]
	if dep.From != "S002" || len(dep.Refs) != 2 || dep.Refs[0] != "S002" || dep.Refs[1] != "S001" {
		t.Errorf("dep: %+v", dep)
	}
	if len(rm.Backlog) != 1 {
		t.Errorf("backlog: %d", len(rm.Backlog))
	}
}

// TestParseDecisions_HappyPath walks decisions.json into DecisionEntry
// slice and detects NEEDS_HUMAN.
func TestParseDecisions_HappyPath(t *testing.T) {
	src := []byte(`{
		"sprint": "S005",
		"decisions": [
			{"timestamp": "2026-05-01T00:00:00Z", "category": "planning", "title": "Pick framework", "detail": "Chose foo", "reference": "VISION.json"},
			{"timestamp": "2026-05-01T01:00:00Z", "category": "needs_human", "title": "LDAP creds", "detail": "NEEDS_HUMAN: cannot resolve"}
		]
	}`)
	log := ParseDecisions("S005", src)
	if log.SprintID != "S005" {
		t.Errorf("sprintId: %q", log.SprintID)
	}
	if len(log.Entries) != 2 {
		t.Fatalf("entries: %d", len(log.Entries))
	}
	if log.Entries[0].Category != "planning" {
		t.Errorf("category: %q", log.Entries[0].Category)
	}
	if !log.Entries[1].NeedsHuman {
		t.Errorf("entry 1 should be NeedsHuman")
	}
}

// TestParseDecisions_BadJSON returns ParseError, no panic.
func TestParseDecisions_BadJSON(t *testing.T) {
	log := ParseDecisions("S005", []byte("{not valid"))
	if len(log.ParseErrors) == 0 {
		t.Errorf("expected ParseError")
	}
	if log.Entries == nil {
		t.Errorf("Entries should be empty slice, not nil")
	}
}

// TestParseE2EResults walks tests[] into per-bucket summaries by
// inspecting the file path.
func TestParseE2EResults_Buckets(t *testing.T) {
	src := []byte(`{
		"sprint": "S016",
		"summary": {"total": 3, "pass": 2, "fail": 1, "skip": 0},
		"tests": [
			{"name": "mock", "file": "story.mock.spec.ts", "status": "pass"},
			{"name": "e2e", "file": "story.e2e.spec.ts", "status": "fail"},
			{"name": "acc", "file": "tests/acceptance/story.py", "status": "pass"}
		]
	}`)
	r := ParseE2EResults("S016", src)
	if r.Mock.Total != 1 || r.Mock.Passed != 1 {
		t.Errorf("mock: %+v", r.Mock)
	}
	if r.E2E.Total != 1 || r.E2E.Failed != 1 {
		t.Errorf("e2e: %+v", r.E2E)
	}
	if r.Acceptance.Total != 1 || r.Acceptance.Passed != 1 {
		t.Errorf("acceptance: %+v", r.Acceptance)
	}
}

// TestParseE2EResults_FallbackToSummary uses the top-level summary when
// the document has no per-test breakdown.
func TestParseE2EResults_FallbackToSummary(t *testing.T) {
	src := []byte(`{"sprint":"S005","summary":{"total":5,"pass":4,"fail":1,"skip":0},"tests":[]}`)
	r := ParseE2EResults("S005", src)
	if r.E2E.Total != 5 || r.E2E.Passed != 4 || r.E2E.Failed != 1 {
		t.Errorf("fallback summary: %+v", r.E2E)
	}
}

// TestParseAcceptanceMatrix walks matrix{storyID: rows[]} into a flat
// row list with the schema's status enum normalised.
func TestParseAcceptanceMatrix_HappyPath(t *testing.T) {
	src := []byte(`{
		"sprint": "S016",
		"matrix": {
			"S016-1": [
				{"criterion":"AC-S016-1-1","description":"works","test_file":"foo.spec.ts","test_name":"[AC-S016-1-1]","status":"pass"},
				{"criterion":"AC-S016-1-2","description":"broken","test_file":"foo.spec.ts","test_name":"[AC-S016-1-2]","status":"fail","error":"timeout"}
			]
		}
	}`)
	m := ParseAcceptanceMatrix("S016", src)
	if len(m.Rows) != 2 {
		t.Fatalf("rows: %d", len(m.Rows))
	}
	pass, fail := 0, 0
	for _, r := range m.Rows {
		if r.Status == "pass" {
			pass++
		}
		if r.Status == "fail" {
			fail++
		}
	}
	if pass != 1 || fail != 1 {
		t.Errorf("status counts: pass=%d fail=%d", pass, fail)
	}
}

// TestParseRefine maps refinements[] → RefineEntry slice.
func TestParseRefine_HappyPath(t *testing.T) {
	src := []byte(`{
		"sprint": "S001",
		"refinements": [
			{"id":1,"feedback":"thing was broken","change":"Fixed it.","files":["foo.tsx"],"tests_passed":true}
		]
	}`)
	out := ParseRefine("S001", src)
	if len(out) != 1 {
		t.Fatalf("entries: %d", len(out))
	}
	if out[0].Number != 1 || out[0].SprintID != "S001" {
		t.Errorf("entry: %+v", out[0])
	}
	if out[0].Files == nil || out[0].Files[0] != "foo.tsx" {
		t.Errorf("files: %v", out[0].Files)
	}
}

// TestParseFailures basic round-trip.
func TestParseFailures_HappyPath(t *testing.T) {
	src := []byte(`{
		"sprint": "S005",
		"failures": [
			{"story":"S005-1","type":"needs_human","summary":"creds needed","attempts":[{"approach":"mock","result":"schema differs"}]}
		]
	}`)
	out := ParseFailures("S005", src)
	if len(out) != 1 {
		t.Fatalf("failures: %d", len(out))
	}
	if out[0].Story != "S005-1" || out[0].Type != "needs_human" {
		t.Errorf("entry: %+v", out[0])
	}
}

// TestParseGUISpec round-trips state diagram + endpoint contracts.
func TestParseGUISpec_HappyPath(t *testing.T) {
	src := []byte(`{
		"sprint":"S016",
		"story":"S016-1",
		"state_diagram":"stateDiagram-v2",
		"endpoint_contracts":[{"path":"/api/foo","method":"GET","registered":true}],
		"test_files":{"e2e":"tests/e2e/foo.spec.ts","mock":"tests/e2e/foo.mock.spec.ts"}
	}`)
	g := ParseGUISpec("S016", "S016-1", src)
	if g.StateDiagram == "" {
		t.Errorf("state_diagram lost")
	}
	if len(g.EndpointContracts) != 1 || g.EndpointContracts[0].Path != "/api/foo" {
		t.Errorf("endpoint_contracts: %+v", g.EndpointContracts)
	}
	if g.TestFiles["e2e"] == "" {
		t.Errorf("test_files: %v", g.TestFiles)
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
