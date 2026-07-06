package sprint

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tab/sprint/parser"
	"github.com/tjst-t/palmux2/internal/tmux"
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

// gitInit creates a minimal real git repo at dir with one commit so
// `git worktree list --porcelain` reports a primary worktree with a
// (non-detached) branch — which is what store.OpenRepo needs to register it.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("seed README: %v", err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
}

// TestOverviewTimeline_MilestoneAndDependsOn drives the real overview()
// handler end-to-end: it writes a ROADMAP.json fixture (≥2 sprints, one
// milestone, a dependency) into a temp worktree, registers it in a real
// store, issues a GET to the overview route via httptest, decodes the
// OverviewResponse, and asserts the additive TimelineEntry fields
// (Milestone / DependsOn) are populated and that a no-dependency sprint
// serialises dependsOn as [] (not null).
func TestOverviewTimeline_MilestoneAndDependsOn(t *testing.T) {
	// Minimal ROADMAP.json fixture: S_A depends on S_B; S_A is a milestone.
	roadmapJSON := `{
		"project": "Test",
		"description": "test",
		"progress": {"total": 2, "done": 1, "in_progress": 0, "remaining": 1, "percentage": 50},
		"execution_order": ["S_B", "S_A"],
		"sprints": {
			"S_A": {
				"title": "Alpha",
				"status": "pending",
				"description": "# Hello\nlist",
				"milestone": true,
				"stories": {}
			},
			"S_B": {
				"title": "Beta",
				"status": "done",
				"description": "",
				"milestone": false,
				"stories": {}
			}
		},
		"dependencies": {
			"S_A": {"depends_on": ["S_B"], "reason": "A needs B"}
		},
		"backlog": []
	}`

	// 1. Build a real git worktree fixture under a fake ghq root and write
	//    the ROADMAP.json into it.
	ghqRoot := t.TempDir()
	ghqPath := "github.com/tjst-t/fixture"
	worktreeDir := filepath.Join(ghqRoot, ghqPath)
	if err := os.MkdirAll(filepath.Join(worktreeDir, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	gitInit(t, worktreeDir)
	if err := os.WriteFile(filepath.Join(worktreeDir, "docs", "ROADMAP.json"), []byte(roadmapJSON), 0o644); err != nil {
		t.Fatalf("write ROADMAP.json: %v", err)
	}

	// 2. Build a real store backed by a mock tmux + temp config dir and
	//    register the repository (which discovers the primary worktree
	//    branch via `git worktree list`).
	cfgDir := t.TempDir()
	repoStore, err := config.NewRepoStore(cfgDir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	settings, err := config.NewSettingsStore(cfgDir)
	if err != nil {
		t.Fatalf("NewSettingsStore: %v", err)
	}
	registry := tab.NewRegistry()
	st, err := store.New(store.Deps{
		Tmux:      tmux.NewMockClient(),
		RepoStore: repoStore,
		Settings:  settings,
		Registry:  registry,
		GHQRoot:   ghqRoot,
	})
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	repo, err := st.OpenRepo(context.Background(), ghqPath)
	if err != nil {
		t.Fatalf("OpenRepo: %v", err)
	}
	if len(repo.OpenBranches) == 0 {
		t.Fatalf("expected at least one open branch after OpenRepo")
	}
	branchID := repo.OpenBranches[0].ID

	// 3. Issue a GET to the real overview() handler via a recorder. We set
	//    the path values the handler reads with r.PathValue(...).
	h := newHandler(st)
	req := httptest.NewRequest("GET", "/overview", nil)
	req.SetPathValue("repoId", repo.ID)
	req.SetPathValue("branchId", branchID)
	rec := httptest.NewRecorder()
	h.overview(rec, req)

	if rec.Code != 200 {
		t.Fatalf("overview status = %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp OverviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode OverviewResponse: %v\nbody: %s", err, rec.Body.String())
	}
	if len(resp.Timeline) != 2 {
		t.Fatalf("expected 2 timeline entries, got %d (%+v)", len(resp.Timeline), resp.Timeline)
	}

	byID := map[string]TimelineEntry{}
	for _, e := range resp.Timeline {
		byID[e.ID] = e
	}

	entryA, okA := byID["S_A"]
	entryB, okB := byID["S_B"]
	if !okA || !okB {
		t.Fatalf("expected S_A and S_B in timeline, got %+v", resp.Timeline)
	}

	// S_A: milestone=true, dependsOn=["S_B"].
	if !entryA.Milestone {
		t.Errorf("S_A should have Milestone=true")
	}
	if len(entryA.DependsOn) != 1 || entryA.DependsOn[0] != "S_B" {
		t.Errorf("S_A.DependsOn should be [S_B], got %v", entryA.DependsOn)
	}

	// S_B: not a milestone, no dependencies.
	if entryB.Milestone {
		t.Errorf("S_B should not be milestone")
	}
	if len(entryB.DependsOn) != 0 {
		t.Errorf("S_B should have no dependsOn, got %v", entryB.DependsOn)
	}

	// A no-dependency sprint must serialise dependsOn as [] (not null) on the
	// wire — assert against the actual handler-produced JSON body.
	var raw struct {
		Timeline []json.RawMessage `json:"timeline"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw timeline: %v", err)
	}
	foundEmpty := false
	for _, rm := range raw.Timeline {
		var probe struct {
			ID        string          `json:"id"`
			DependsOn json.RawMessage `json:"dependsOn"`
		}
		if err := json.Unmarshal(rm, &probe); err != nil {
			t.Fatalf("decode probe: %v", err)
		}
		if probe.ID == "S_B" {
			if string(probe.DependsOn) != "[]" {
				t.Errorf("S_B empty dependsOn should serialise as [], got: %s", probe.DependsOn)
			}
			foundEmpty = true
		}
	}
	if !foundEmpty {
		t.Errorf("did not find S_B in serialised timeline")
	}
}

// newSprintFixture builds a real git worktree under a fake ghq root with the
// given ROADMAP.json, registers it in a real store, and returns the sprint
// handler plus the ids the route handlers read via r.PathValue and the
// worktree dir (so a test can plant files on disk).
func newSprintFixture(t *testing.T, roadmapJSON string) (h *handler, repoID, branchID, worktreeDir string) {
	t.Helper()
	ghqRoot := t.TempDir()
	ghqPath := "github.com/tjst-t/fixture"
	worktreeDir = filepath.Join(ghqRoot, ghqPath)
	if err := os.MkdirAll(filepath.Join(worktreeDir, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	gitInit(t, worktreeDir)
	if err := os.WriteFile(filepath.Join(worktreeDir, "docs", "ROADMAP.json"), []byte(roadmapJSON), 0o644); err != nil {
		t.Fatalf("write ROADMAP.json: %v", err)
	}
	cfgDir := t.TempDir()
	repoStore, err := config.NewRepoStore(cfgDir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	settings, err := config.NewSettingsStore(cfgDir)
	if err != nil {
		t.Fatalf("NewSettingsStore: %v", err)
	}
	registry := tab.NewRegistry()
	st, err := store.New(store.Deps{
		Tmux:      tmux.NewMockClient(),
		RepoStore: repoStore,
		Settings:  settings,
		Registry:  registry,
		GHQRoot:   ghqRoot,
	})
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	repo, err := st.OpenRepo(context.Background(), ghqPath)
	if err != nil {
		t.Fatalf("OpenRepo: %v", err)
	}
	if len(repo.OpenBranches) == 0 {
		t.Fatalf("expected at least one open branch after OpenRepo")
	}
	return newHandler(st), repo.ID, repo.OpenBranches[0].ID, worktreeDir
}

// TestSprintLog_RejectsPathTraversal is a SECURITY regression test for the
// SEVERE path-traversal in sprintLog (Se173ef fixup BUG 1). Go 1.22 ServeMux
// binds a percent-encoded wildcard to a SINGLE path segment without
// redirecting, so a caller could pass sprintId="../../.." and, combined with
// a base .log name, read any host .log file outside docs/sprint-logs —
// unauthenticated in open-access mode. The handler must reject such a
// sprintId (400/404) and never return the escaped file's contents.
func TestSprintLog_RejectsPathTraversal(t *testing.T) {
	const secret = "TOP-SECRET-HOST-LOG-CONTENTS"
	roadmapJSON := `{
		"project": "Test",
		"description": "test",
		"progress": {"total": 1, "done": 0, "in_progress": 0, "remaining": 1, "percentage": 0},
		"execution_order": ["S_A"],
		"sprints": {"S_A": {"title": "Alpha", "status": "pending", "description": "", "stories": {}}},
		"dependencies": {},
		"backlog": []
	}`
	h, repoID, branchID, worktreeDir := newSprintFixture(t, roadmapJSON)

	// Plant a secret .log at the worktree ROOT — one level above
	// docs/sprint-logs. filepath.Join(root,"docs","sprint-logs","../..","SECRET.log")
	// cleans to root/SECRET.log, so the traversal would reach it absent the guard.
	if err := os.WriteFile(filepath.Join(worktreeDir, "SECRET.log"), []byte(secret), 0o644); err != nil {
		t.Fatalf("write SECRET.log: %v", err)
	}

	// Traversal variants that must all be refused. The first escapes to the
	// worktree root; the others escape further / use different separators.
	for _, sprintID := range []string{"../..", "../../../../var/log", `..\..`, "..", "S_A/../.."} {
		req := httptest.NewRequest("GET", "/sprint-log?file=SECRET.log", nil)
		req.SetPathValue("repoId", repoID)
		req.SetPathValue("branchId", branchID)
		req.SetPathValue("sprintId", sprintID)
		rec := httptest.NewRecorder()
		h.sprintLog(rec, req)

		if rec.Code == 200 {
			t.Errorf("sprintId=%q: expected rejection, got 200 with body: %q", sprintID, rec.Body.String())
		}
		if rec.Code != 400 && rec.Code != 404 {
			t.Errorf("sprintId=%q: expected 400/404, got %d", sprintID, rec.Code)
		}
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("SECURITY: sprintId=%q leaked out-of-tree file contents: %q", sprintID, rec.Body.String())
		}
	}

	// A well-formed sprintId that is not a real ROADMAP sprint is a 404
	// (mirrors sprintDetail's found-or-404 gate) — proves the ROADMAP gate.
	req := httptest.NewRequest("GET", "/sprint-log?file=x.log", nil)
	req.SetPathValue("repoId", repoID)
	req.SetPathValue("branchId", branchID)
	req.SetPathValue("sprintId", "S_NOPE")
	rec := httptest.NewRecorder()
	h.sprintLog(rec, req)
	if rec.Code != 404 {
		t.Errorf("unknown-but-clean sprintId: expected 404, got %d", rec.Code)
	}

	// Happy path: a real sprint + a real .log under its dir is served 200.
	logDir := filepath.Join(worktreeDir, "docs", "sprint-logs", "S_A")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir logDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "verify-run-1.log"), []byte("ok-log-body"), 0o644); err != nil {
		t.Fatalf("write verify log: %v", err)
	}
	req = httptest.NewRequest("GET", "/sprint-log?file=verify-run-1.log", nil)
	req.SetPathValue("repoId", repoID)
	req.SetPathValue("branchId", branchID)
	req.SetPathValue("sprintId", "S_A")
	rec = httptest.NewRecorder()
	h.sprintLog(rec, req)
	if rec.Code != 200 {
		t.Fatalf("happy path: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok-log-body") {
		t.Errorf("happy path: expected log body, got %q", rec.Body.String())
	}
}
