package sprint

import (
	"path/filepath"
	"testing"

	"github.com/tjst-t/palmux2/internal/worktreewatch"
)

// TestSprintSkipDir_PrunesEverythingButDocsAndClaude pins the registration
// scope. The Sprint tab can only ever act on docs/** and .claude/*.lock, but
// it used to register an inotify watch for every directory in the worktree
// (27,025 on a real repo, ~2 s per tab open / WS attach).
func TestSprintSkipDir_PrunesEverythingButDocsAndClaude(t *testing.T) {
	root := filepath.Clean("/w")
	skip := sprintSkipDir(root)

	keep := []string{
		"/w",
		"/w/docs",
		"/w/docs/sprint-logs",
		"/w/docs/sprint-logs/S1",
		"/w/.claude",
	}
	prune := []string{
		"/w/node_modules",
		"/w/node_modules/react/dist",
		"/w/src",
		"/w/src/components",
		"/w/.git",
		"/w/.git/objects",
		"/w/vendor",
	}
	for _, d := range keep {
		if skip(d) {
			t.Errorf("sprintSkipDir pruned %s, but sprintFilter can accept events under it", d)
		}
	}
	for _, d := range prune {
		if !skip(d) {
			t.Errorf("sprintSkipDir kept %s; sprintFilter rejects everything there, so watching it is pure cost", d)
		}
	}
}

// TestSprintSkipDir_ConsistentWithFilter is the invariant that makes pruning
// safe: every path the FILTER accepts must live under a directory the SKIP
// predicate keeps. Pruning a directory whose events the filter would accept
// silently loses those notifications.
func TestSprintSkipDir_ConsistentWithFilter(t *testing.T) {
	root := filepath.Clean("/w")
	skip := sprintSkipDir(root)
	filter := sprintFilter(root)

	accepted := []string{
		"/w/docs",
		"/w/docs/ROADMAP.json",
		"/w/docs/sprint-logs/S1/decisions.json",
		"/w/.claude/autopilot-main.lock",
	}
	for _, p := range accepted {
		if !filter(worktreewatch.Event{Path: p}) {
			t.Fatalf("test bug: %s is not actually accepted by sprintFilter", p)
		}
		// Every ancestor between root and the file must survive pruning.
		for dir := filepath.Dir(p); len(dir) >= len(root); dir = filepath.Dir(dir) {
			if skip(dir) {
				t.Errorf("filter accepts %s but skip prunes its ancestor %s — the event would never arrive", p, dir)
			}
			if dir == root {
				break
			}
		}
	}
}
