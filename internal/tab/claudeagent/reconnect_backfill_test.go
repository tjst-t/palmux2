package claudeagent

// S862203-3 review / AC-3-2(a): the in-flight-turn transcript must be
// reconstructed WITH the pre-permission content (user bubble + earlier
// tool_use) after a restart, not just the replayed pending permission.
//
// These tests cover the two halves of the fix at the unit level (the
// real-claude E2E in tests/e2e/s862203_agent_restart_survival.py covers the
// integrated behaviour):
//
//   1. Root cause: SetLastInit must NOT reset the WorkspaceMigrationV1
//      bookkeeping flag, otherwise the next restart re-runs the legacy-id
//      migration which drops the tab's Active resume pointer — the pointer
//      the reconnect transcript backfill (EnsureAgent's LoadTranscriptTurns)
//      depends on.
//   2. Merge: backfilling the transcript from the CLI's .jsonl (source of
//      truth) and then feeding the post-frontier replay lines through the
//      UNCHANGED processStreamMessage path yields the FULL turn — pre-frontier
//      content (from .jsonl) + post-frontier content (from replay) — with no
//      double-count at the seam.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetLastInit_PreservesWorkspaceMigrationFlag is the root-cause
// regression: caching a fresh CLI init payload must carry the existing
// WorkspaceMigrationV1 forward, so the migration guard stays armed across
// turns and a subsequent restart does NOT re-run the migration (which would
// drop the Active resume pointer and break reconnect transcript restore).
func TestSetLastInit_PreservesWorkspaceMigrationFlag(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Simulate the migration having run once (sets the guard flag).
	resolver := func(_, _ string) (string, bool) { return "", true }
	if _, _, err := s.MigrateLegacyBranchIDs(resolver, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if s.LastInit().WorkspaceMigrationV1 == 0 {
		t.Fatal("precondition: migration flag should be set after MigrateLegacyBranchIDs")
	}
	flagAfterMigrate := s.LastInit().WorkspaceMigrationV1

	// A completed turn caches a fresh CLI init payload (no migration flag in it).
	if err := s.SetLastInit(InitInfo{Models: []ModelDescriptor{{Value: "opus"}}}); err != nil {
		t.Fatalf("SetLastInit: %v", err)
	}
	if got := s.LastInit().WorkspaceMigrationV1; got != flagAfterMigrate {
		t.Fatalf("SetLastInit reset WorkspaceMigrationV1 to %d, want preserved %d (AC-3-2(a) root cause: a reset flag re-runs the migration on the next restart, dropping the Active resume pointer)", got, flagAfterMigrate)
	}
	// The fresh payload's own fields must still be applied.
	if len(s.LastInit().Models) != 1 || s.LastInit().Models[0].Value != "opus" {
		t.Fatalf("SetLastInit did not apply the fresh init payload: %+v", s.LastInit())
	}

	// And the guard must actually short-circuit a second migration (so a real
	// restart won't drop Active).
	dropped := 0
	warnf := func(string, ...any) { dropped++ }
	rw, dr, err := s.MigrateLegacyBranchIDs(func(_, _ string) (string, bool) { return "", false }, warnf)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if rw != 0 || dr != 0 || dropped != 0 {
		t.Fatalf("migration re-ran after SetLastInit (rewritten=%d dropped=%d warns=%d) — guard defeated", rw, dr, dropped)
	}
}

// TestReconnectBackfill_MergesJSONLThenReplayNoDup is the merge test the
// review asked for: a fake .jsonl (the CLI's durable source of truth) holding
// an in-flight turn's user message + assistant tool_use is loaded via the
// SAME LoadTranscriptTurns path EnsureAgent uses on reconnect; then the
// post-frontier replay lines (the pending permission's tool_result +
// follow-up assistant text — what streams live after the user answers) are
// fed through the UNCHANGED processStreamMessage. The resulting snapshot must
// contain BOTH the pre-frontier (.jsonl) content and the post-frontier
// (replay) content, with no double-count at the seam.
func TestReconnectBackfill_MergesJSONLThenReplayNoDup(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "sess.jsonl")
	// A realistic in-flight-turn .jsonl at permission-pending time: user
	// message + an assistant message carrying a text preamble and a Bash
	// tool_use. (Shape mirrors what the CLI actually writes — see the
	// s862_probe empirical dump.)
	jsonl := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"PRECONTENT_USER_BUBBLE run the command"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"PRECONTENT_ASSISTANT_TEXT sure"},{"type":"tool_use","id":"toolu_backfill1","name":"Bash","input":{"command":"echo hi"}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(jsonlPath, []byte(jsonl), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	// Backfill from .jsonl (the reconnect step).
	turns, err := LoadTranscriptTurns(jsonlPath)
	if err != nil {
		t.Fatalf("LoadTranscriptTurns: %v", err)
	}
	if len(turns) == 0 {
		t.Fatal("backfill produced no turns from the .jsonl")
	}
	sess := NewSession("repo", "branch", "sess", "opus", "manual")
	sess.SetTurns(turns)

	// Now feed the POST-frontier replay lines through the unchanged parse
	// path — the tool_result that arrives once the gated tool runs, then a
	// short follow-up assistant message. These are DISJOINT from the .jsonl
	// content (they happen after the pending permission's frontier), so the
	// seam must not duplicate anything.
	for _, line := range []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_backfill1","content":"POSTCONTENT_TOOL_RESULT ok"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"POSTCONTENT_ASSISTANT done"}]}}`,
	} {
		processStreamMessage(sess, parse(t, line))
	}

	// Flatten the snapshot to text and assert both halves are present, once each.
	var sb strings.Builder
	for _, tn := range sess.Snapshot().Turns {
		for _, b := range tn.Blocks {
			sb.WriteString(b.Text)
			sb.WriteString(" ")
			sb.WriteString(b.Output)
			sb.WriteString(" ")
			sb.WriteString(b.Name)
			sb.WriteString("\n")
		}
	}
	flat := sb.String()

	for _, want := range []string{
		"PRECONTENT_USER_BUBBLE",    // pre-frontier user bubble (from .jsonl)
		"PRECONTENT_ASSISTANT_TEXT", // pre-frontier assistant text (from .jsonl)
		"POSTCONTENT_TOOL_RESULT",   // post-frontier tool result (from replay)
		"POSTCONTENT_ASSISTANT",     // post-frontier assistant text (from replay)
	} {
		if n := strings.Count(flat, want); n != 1 {
			t.Fatalf("expected %q exactly once in merged transcript, got %d\n---\n%s", want, n, flat)
		}
	}
	// The Bash tool_use (pre-frontier) must survive the merge exactly once too.
	if n := strings.Count(flat, "Bash"); n != 1 {
		t.Fatalf("expected the Bash tool_use exactly once in merged transcript, got %d\n---\n%s", n, flat)
	}
}
