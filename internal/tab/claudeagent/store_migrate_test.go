package claudeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// S1e8d02 [AC-S1e8d02-2-1, AC-S1e8d02-2-2, AC-S1e8d02-2-3]:
// MigrateLegacyBranchIDs rewrites pre-S1e8d02 sessions.json keys, drops
// orphaned entries the resolver cannot map, and is idempotent on repeat.
func TestMigrateLegacyBranchIDs_RewritesAndDropsAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	// Seed sessions.json with legacy keys.
	legacy := PersistedShape{
		Sessions: map[string]SessionMeta{
			"sess-A": {ID: "sess-A", RepoID: "repo1", BranchID: "main--1234"},
			"sess-B": {ID: "sess-B", RepoID: "repo1", BranchID: "feat--abcd"},
			"sess-C": {ID: "sess-C", RepoID: "repo2", BranchID: "gone--ffff"},
		},
		Active: map[string]string{
			"repo1/main--1234/claude:claude": "sess-A",
			"repo1/feat--abcd/claude:claude": "sess-B",
			"repo2/gone--ffff/claude:claude": "sess-C", // resolver returns false for this
		},
		BranchTabs: map[string][]string{
			"repo1/main--1234": {"claude:claude"},
			"repo1/feat--abcd": {"claude:claude", "claude:claude-2"},
			"repo2/gone--ffff": {"claude:claude"}, // dropped
		},
		BranchPrefs: map[string]BranchPrefs{
			"repo1/main--1234/claude:claude": {Model: "opus"},
		},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions.json"), raw, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Resolver: legacy IDs from repo1 map to new path-based IDs; repo2's
	// entries are not resolvable (worktree gone).
	resolver := func(repoID, oldID string) (string, bool) {
		if repoID == "repo1" && oldID == "main--1234" {
			return "palmux2--ab12", true
		}
		if repoID == "repo1" && oldID == "feat--abcd" {
			return "feat--cd34", true
		}
		return "", false
	}

	var warnings []string
	rew, drp, err := st.MigrateLegacyBranchIDs(resolver, func(format string, args ...any) {
		warnings = append(warnings, format)
	})
	if err != nil {
		t.Fatalf("Migrate1: %v", err)
	}
	if rew == 0 {
		t.Errorf("expected rewrites > 0, got %d", rew)
	}
	if drp == 0 {
		t.Errorf("expected drops > 0 (repo2 entries), got %d", drp)
	}
	if len(warnings) == 0 {
		t.Errorf("expected at least one warn for repo2 orphan entries")
	}

	// Active map keys are now path-based. The repo2 orphan is gone.
	if _, ok := st.data.Active["repo1/palmux2--ab12/claude:claude"]; !ok {
		t.Errorf("expected new Active key after rewrite, got: %v", st.data.Active)
	}
	for k := range st.data.Active {
		if strings.Contains(k, "main--1234") || strings.Contains(k, "gone--ffff") {
			t.Errorf("legacy Active key not rewritten/dropped: %q", k)
		}
	}
	// BranchTabs analogous.
	if _, ok := st.data.BranchTabs["repo1/palmux2--ab12"]; !ok {
		t.Errorf("expected new BranchTabs key, got: %v", st.data.BranchTabs)
	}
	if _, ok := st.data.BranchTabs["repo1/feat--cd34"]; !ok {
		t.Errorf("expected feat new key, got: %v", st.data.BranchTabs)
	}
	for k := range st.data.BranchTabs {
		if strings.Contains(k, "main--1234") || strings.Contains(k, "gone--ffff") {
			t.Errorf("legacy BranchTabs key not rewritten/dropped: %q", k)
		}
	}
	// BranchPrefs analogous.
	if _, ok := st.data.BranchPrefs["repo1/palmux2--ab12/claude:claude"]; !ok {
		t.Errorf("expected BranchPrefs rewritten, got: %v", st.data.BranchPrefs)
	}
	// Sessions: BranchID rewritten in place (orphan repo2 left as-is — Claude
	// CLI may still resume by UUID).
	if st.data.Sessions["sess-A"].BranchID != "palmux2--ab12" {
		t.Errorf("session-A BranchID not rewritten: %q", st.data.Sessions["sess-A"].BranchID)
	}
	if st.data.Sessions["sess-B"].BranchID != "feat--cd34" {
		t.Errorf("session-B BranchID not rewritten: %q", st.data.Sessions["sess-B"].BranchID)
	}
	// Marker is set.
	if st.data.LastInit == nil || st.data.LastInit.WorkspaceMigrationV1 == 0 {
		t.Errorf("WorkspaceMigrationV1 marker not set")
	}

	// Second run is no-op (idempotent).
	rew2, drp2, err := st.MigrateLegacyBranchIDs(resolver, nil)
	if err != nil {
		t.Fatalf("Migrate2: %v", err)
	}
	if rew2 != 0 || drp2 != 0 {
		t.Errorf("second migrate not idempotent: rewrote=%d dropped=%d", rew2, drp2)
	}
}

// S1e8d02: when no resolver entries match (e.g. fresh install with no
// sessions.json), migration is a no-op and writes the marker once.
func TestMigrateLegacyBranchIDs_EmptyStoreNoOp(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	rew, drp, err := st.MigrateLegacyBranchIDs(
		func(string, string) (string, bool) { return "", false },
		nil,
	)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if rew != 0 || drp != 0 {
		t.Errorf("empty store should be no-op, got rewrote=%d dropped=%d", rew, drp)
	}
	if st.data.LastInit == nil || st.data.LastInit.WorkspaceMigrationV1 == 0 {
		t.Errorf("marker should still be set after empty migrate")
	}
}
