package git

import (
	"strings"
	"testing"
)

func TestParseConflictBody_TwoWay(t *testing.T) {
	body := strings.Join([]string{
		"line 1",
		"<<<<<<< HEAD",
		"ours line a",
		"ours line b",
		"=======",
		"theirs line a",
		">>>>>>> feature",
		"line 5",
	}, "\n")
	cf := parseConflictBody("foo.txt", body)
	if cf.Path != "foo.txt" {
		t.Fatalf("path = %q", cf.Path)
	}
	if cf.HasBase {
		t.Fatal("expected 2-way conflict (HasBase=false)")
	}
	if len(cf.Hunks) != 1 {
		t.Fatalf("hunks = %d", len(cf.Hunks))
	}
	h := cf.Hunks[0]
	if got := strings.Join(h.Ours, "\n"); got != "ours line a\nours line b" {
		t.Fatalf("ours = %q", got)
	}
	if got := strings.Join(h.Theirs, "\n"); got != "theirs line a" {
		t.Fatalf("theirs = %q", got)
	}
	if h.OursLabel != "HEAD" {
		t.Fatalf("oursLabel = %q", h.OursLabel)
	}
	if h.TheirsLabel != "feature" {
		t.Fatalf("theirsLabel = %q", h.TheirsLabel)
	}
}

func TestParseConflictBody_DiffThree(t *testing.T) {
	body := strings.Join([]string{
		"<<<<<<< HEAD",
		"ours",
		"||||||| base",
		"original",
		"=======",
		"theirs",
		">>>>>>> branch",
	}, "\n")
	cf := parseConflictBody("a", body)
	if !cf.HasBase {
		t.Fatal("expected diff3-style HasBase=true")
	}
	if len(cf.Hunks) != 1 {
		t.Fatal("expected one hunk")
	}
	if cf.Hunks[0].Base[0] != "original" {
		t.Fatalf("base = %v", cf.Hunks[0].Base)
	}
}

func TestParseRebaseTodo(t *testing.T) {
	src := `pick aaaaaaa first
pick bbbbbbb second
# comment
squash ccccccc third
`
	entries := parseRebaseTodo(src)
	if len(entries) != 3 {
		t.Fatalf("got %d entries", len(entries))
	}
	if entries[0].Action != "pick" || entries[0].SHA != "aaaaaaa" {
		t.Fatalf("entry[0] = %+v", entries[0])
	}
	if entries[2].Action != "squash" {
		t.Fatalf("entry[2] = %+v", entries[2])
	}
}

func TestSerializeRebaseTodo_Roundtrip(t *testing.T) {
	src := []RebaseTodoEntry{
		{Action: "pick", SHA: "abc1234", Subject: "first"},
		{Action: "squash", SHA: "def5678", Subject: "second"},
	}
	out := serializeRebaseTodo(src)
	want := "pick abc1234 first\nsquash def5678 second\n"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}
