package claudetui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTranscriptBubbles_AbsentFile(t *testing.T) {
	bubbles, err := ReadTranscriptBubbles("/nonexistent/path/x.jsonl")
	if err != nil {
		t.Fatalf("expected nil err for missing file; got %v", err)
	}
	if len(bubbles) != 0 {
		t.Fatalf("expected 0 bubbles for missing file; got %d", len(bubbles))
	}
}

func TestReadTranscriptBubbles_BasicConversation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	lines := []string{
		`{"type":"user","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi there"}]}}`,
		`{"type":"user","message":{"role":"user","content":"do a thing"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"OK"},{"type":"tool_use","name":"Bash","input":{"cmd":"ls"}}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bubbles, err := ReadTranscriptBubbles(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(bubbles) != 4 {
		t.Fatalf("expected 4 bubbles, got %d: %+v", len(bubbles), bubbles)
	}
	if bubbles[0].Speaker != "user" || bubbles[0].Text != "hello" {
		t.Errorf("bubble 0 = %+v", bubbles[0])
	}
	if bubbles[1].Speaker != "assistant" || bubbles[1].Text != "hi there" {
		t.Errorf("bubble 1 = %+v", bubbles[1])
	}
	if bubbles[3].Speaker != "assistant" || !strings.Contains(bubbles[3].Text, "OK") || !strings.Contains(bubbles[3].Text, "[Bash]") {
		t.Errorf("bubble 3 should contain text + [Bash], got %+v", bubbles[3])
	}
}

func TestReadTranscriptBubbles_SkipsMetaAndSidechain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	lines := []string{
		`{"type":"user","message":{"role":"user","content":"real prompt"}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"meta hidden"}}`,
		`{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"sidechain hidden"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"real reply"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bubbles, err := ReadTranscriptBubbles(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(bubbles) != 2 {
		t.Fatalf("expected 2 bubbles after meta/sidechain filter, got %d: %+v", len(bubbles), bubbles)
	}
	if bubbles[0].Text != "real prompt" || bubbles[1].Text != "real reply" {
		t.Errorf("unexpected bubbles: %+v", bubbles)
	}
}

func TestReadTranscriptBubbles_StripCommandWrapper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	// A /clear-style command entry wraps the meta in <command-name>/<command-message>.
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name><command-message>clear</command-message>"}}`,
		`{"type":"user","message":{"role":"user","content":"<local-command-caveat>some caveat</local-command-caveat>"}}`,
		`{"type":"user","message":{"role":"user","content":"keep me"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bubbles, err := ReadTranscriptBubbles(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(bubbles) != 1 || bubbles[0].Text != "keep me" {
		t.Errorf("expected single 'keep me' bubble, got %+v", bubbles)
	}
}

func TestReadTranscriptBubbles_MalformedLineSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	lines := []string{
		`not json at all`,
		`{"type":"user","message":{"role":"user","content":"good"}}`,
		`{broken`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bubbles, err := ReadTranscriptBubbles(path)
	if err != nil {
		t.Fatalf("read should not error on malformed lines, got %v", err)
	}
	if len(bubbles) != 1 || bubbles[0].Text != "good" {
		t.Errorf("expected 1 bubble, got %+v", bubbles)
	}
}
