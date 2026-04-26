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

func TestHubCapsHistory(t *testing.T) {
	h := New(nil, nil)
	for i := 0; i < maxPerBranch+10; i++ {
		_, _, _ = h.Ingest(IngestRequest{RepoID: "r", BranchID: "b", Type: "info"})
	}
	if got := len(h.Snapshot("r", "b").Notifications); got != maxPerBranch {
		t.Fatalf("kept %d, want %d", got, maxPerBranch)
	}
}
