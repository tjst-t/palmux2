package main

import (
	"context"
	"testing"

	"github.com/tjst-t/palmux2/internal/tab/claudeagent"
)

// recordingCloser records CloseDaemon calls so the test can assert the
// teardown wiring without spawning a real PTY daemon / ptyhost.
type recordingCloser struct {
	calls []string
	err   error
}

func (r *recordingCloser) CloseDaemon(_ context.Context, repoID, branchID, tabID string) error {
	r.calls = append(r.calls, repoID+"/"+branchID+"/"+tabID)
	return r.err
}

// TestClaudeMultiTabHook_DeleteTabClosesTuiDaemon is the regression test for a
// user-reported defect: deleting a Claude tab left its PTY daemon (and the
// ptyhost child) running. Because pickNextClaudeTabID reuses the lowest free
// id, re-adding a Claude tab produced the SAME id and the first WS attach
// re-adopted the surviving process — the deleted session reappeared with
// whatever the user had typed in it.
//
// The root cause was an asymmetry: agenttab (the generic, non-claude agent
// kinds) called agenttui.Manager.CloseDaemon on delete, but the claude path
// never did, even though CloseDaemon's own doc comment says it is "called from
// the tab-removal handler so a deleted Claude(tui) tab does not leave a zombie
// process / watcher behind".
//
// The test drives the hook the way store.RemoveTab does and asserts the daemon
// registry is empty afterwards.
func TestClaudeMultiTabHook_DeleteTabClosesTuiDaemon(t *testing.T) {
	dir := t.TempDir()
	store, err := claudeagent.NewStore(dir)
	if err != nil {
		t.Fatalf("claudeagent.NewStore: %v", err)
	}
	mgr := claudeagent.NewManager(claudeagent.Config{}, store, nil, nil, nil, nil)

	rec := &recordingCloser{}
	hook := claudeMultiTabHook{mgr: mgr, tuiMgr: rec}

	const repoID, branchID, tabID = "r--0001", "b--0001", "claude:claude-2"
	if err := hook.DeleteTab(context.Background(), repoID, branchID, tabID); err != nil {
		t.Fatalf("DeleteTab: %v", err)
	}

	want := repoID + "/" + branchID + "/" + tabID
	if len(rec.calls) != 1 || rec.calls[0] != want {
		t.Errorf("deleting a Claude tab must close its PTY daemon exactly once for %s; got %v\n"+
			"Without this the ptyhost survives, and because pickNextClaudeTabID reuses the lowest "+
			"free id, re-adding a Claude tab re-adopts the deleted session.", want, rec.calls)
	}
}

// TestClaudeMultiTabHook_DeleteTabWithoutTuiManagerIsSafe guards the nil path:
// a hook built without a tui manager must still delete the tab, not panic.
func TestClaudeMultiTabHook_DeleteTabWithoutTuiManagerIsSafe(t *testing.T) {
	dir := t.TempDir()
	store, err := claudeagent.NewStore(dir)
	if err != nil {
		t.Fatalf("claudeagent.NewStore: %v", err)
	}
	hook := claudeMultiTabHook{mgr: claudeagent.NewManager(claudeagent.Config{}, store, nil, nil, nil, nil)}
	if err := hook.DeleteTab(context.Background(), "r--0001", "b--0001", "claude:claude-2"); err != nil {
		t.Fatalf("DeleteTab with nil tuiMgr: %v", err)
	}
}
