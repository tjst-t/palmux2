package notify

import (
	"testing"
)

type capturedEvent struct {
	typ      string
	repoID   string
	branchID string
}

type fakePub struct {
	events []capturedEvent
}

func (f *fakePub) Publish(typ, repoID, branchID string, _ any) {
	f.events = append(f.events, capturedEvent{typ, repoID, branchID})
}

func TestHubIngestPublishesAndIncrements(t *testing.T) {
	pub := &fakePub{}
	h := New(nil, pub)
	r, b, err := h.Ingest(IngestRequest{
		RepoID:   "r1",
		BranchID: "b1",
		Type:     "stop",
		Message:  "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r != "r1" || b != "b1" {
		t.Fatalf("got %s/%s", r, b)
	}
	state := h.Snapshot("r1", "b1")
	if state.UnreadCount != 1 {
		t.Fatalf("unread = %d, want 1", state.UnreadCount)
	}
	if state.LastMessage != "hi" {
		t.Fatalf("lastMessage = %q", state.LastMessage)
	}
	if len(pub.events) != 1 || pub.events[0].typ != "notification" {
		t.Fatalf("expected one notification event, got %+v", pub.events)
	}
}

func TestHubIngestResolvesSession(t *testing.T) {
	resolver := func(name string) (string, string, bool) {
		if name == "_palmux_R_B" {
			return "R", "B", true
		}
		return "", "", false
	}
	h := New(resolver, nil)
	r, b, err := h.Ingest(IngestRequest{TmuxSession: "_palmux_R_B", Type: "info"})
	if err != nil {
		t.Fatal(err)
	}
	if r != "R" || b != "B" {
		t.Fatalf("got %s/%s", r, b)
	}
}

func TestHubIngestUnknownSession(t *testing.T) {
	resolver := func(string) (string, string, bool) { return "", "", false }
	h := New(resolver, nil)
	if _, _, err := h.Ingest(IngestRequest{TmuxSession: "nope", Type: "info"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestHubClearResetsCount(t *testing.T) {
	pub := &fakePub{}
	h := New(nil, pub)
	for i := 0; i < 3; i++ {
		_, _, _ = h.Ingest(IngestRequest{RepoID: "r", BranchID: "b", Type: "info"})
	}
	if h.Snapshot("r", "b").UnreadCount != 3 {
		t.Fatal("expected unread=3")
	}
	got := h.Clear("r", "b")
	if got.UnreadCount != 0 {
		t.Fatalf("after clear: %d", got.UnreadCount)
	}
	if pub.events[len(pub.events)-1].typ != "notification.cleared" {
		t.Fatalf("expected last event to be cleared, got %+v", pub.events)
	}
}

// TestHubIngestExternalDedup verifies that an external POST carrying a stable
// RequestID refreshes the existing inbox entry in place (Claude Code hook path)
// rather than piling up — the fix for the claude-tui notification flood.
func TestHubIngestExternalDedup(t *testing.T) {
	pub := &fakePub{}
	h := New(nil, pub)

	for i, msg := range []string{"first", "second", "third"} {
		_, _, err := h.Ingest(IngestRequest{
			RepoID:    "r",
			BranchID:  "b",
			Type:      "claudetui.task_complete",
			Message:   msg,
			RequestID: "claudetui-hook-claude",
			TabID:     "claude",
		})
		if err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}

	state := h.Snapshot("r", "b")
	if state.UnreadCount != 1 {
		t.Fatalf("UnreadCount = %d, want 1 (deduped)", state.UnreadCount)
	}
	if len(state.Notifications) != 1 {
		t.Fatalf("notifications = %d, want 1", len(state.Notifications))
	}
	if state.LastMessage != "third" {
		t.Fatalf("LastMessage = %q, want third", state.LastMessage)
	}
	if state.Notifications[0].TabID != "claude" {
		t.Errorf("TabID = %q, want claude", state.Notifications[0].TabID)
	}
}

// TestHubIngestExternalResolve verifies the UserPromptSubmit hook path: a
// resolve request flips the matching notification's Resolved flag.
func TestHubIngestExternalResolve(t *testing.T) {
	h := New(nil, &fakePub{})
	reqID := "claudetui-hook-claude"

	if _, _, err := h.Ingest(IngestRequest{
		RepoID: "r", BranchID: "b", Type: "claudetui.permission_prompt",
		Message: "Allow?", RequestID: reqID, TabID: "claude",
	}); err != nil {
		t.Fatal(err)
	}
	if got := h.Snapshot("r", "b").Notifications[0].Resolved; got {
		t.Fatal("notification should start unresolved")
	}

	if _, _, err := h.Ingest(IngestRequest{
		RepoID: "r", BranchID: "b", RequestID: reqID, Resolve: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := h.Snapshot("r", "b").Notifications[0].Resolved; !got {
		t.Fatal("notification should be resolved after resolve request")
	}
}

func TestHubCapsHistory(t *testing.T) {
	h := New(nil, nil)
	for i := 0; i < maxPerBranch+10; i++ {
		_, _, _ = h.Ingest(IngestRequest{RepoID: "r", BranchID: "b", Type: "info"})
	}
	if got := len(h.Snapshot("r", "b").Notifications); got != maxPerBranch {
		t.Fatalf("kept %d, want %d", got, maxPerBranch)
	}
}
