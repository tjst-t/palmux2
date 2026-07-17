package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- buildHookSettings / hookEntries -----------------------------------------

func TestBuildHookSettings(t *testing.T) {
	bin := "/usr/local/bin/palmux"
	out, err := buildHookSettings(bin)
	if err != nil {
		t.Fatalf("buildHookSettings: %v", err)
	}

	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("settings JSON does not parse: %v\n%s", err, out)
	}

	for _, event := range []string{"Notification", "Stop", "UserPromptSubmit"} {
		groups, ok := parsed.Hooks[event]
		if !ok || len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("event %q: unexpected hook shape: %+v", event, groups)
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" {
			t.Errorf("event %q: type = %q, want command", event, h.Type)
		}
		// Command must invoke the (shell-quoted) palmux binary as a hook.
		if !strings.Contains(h.Command, "'"+bin+"' hook") {
			t.Errorf("event %q: command = %q, want it to call %q hook", event, h.Command, bin)
		}
		if h.Timeout != 5 {
			t.Errorf("event %q: timeout = %d, want 5", event, h.Timeout)
		}
	}
}

func TestBuildClaudeSettingsAlwaysDisablesRemoteControl(t *testing.T) {
	out, err := buildClaudeSettings("/usr/local/bin/palmux", false)
	if err != nil {
		t.Fatalf("buildClaudeSettings: %v", err)
	}
	if !strings.Contains(out, `"disableRemoteControl":true`) {
		t.Errorf("settings must always disable remote control: %s", out)
	}
	if strings.Contains(out, "\"hooks\"") {
		t.Errorf("withHooks=false must not include hooks: %s", out)
	}
}

func TestHookEnv(t *testing.T) {
	env := hookEnv("http://127.0.0.1:8080/api/notify", "tok", "r1", "b1", "claude")
	want := map[string]string{
		"PALMUX_NOTIFY_URL": "http://127.0.0.1:8080/api/notify",
		"PALMUX_REPO_ID":    "r1",
		"PALMUX_BRANCH_ID":  "b1",
		"PALMUX_TAB_ID":     "claude",
		"PALMUX_TOKEN":      "tok",
	}
	got := envMap(env)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	// Token omitted when empty.
	noTok := envMap(hookEnv("http://x/api/notify", "", "r", "b", "t"))
	if _, ok := noTok["PALMUX_TOKEN"]; ok {
		t.Errorf("PALMUX_TOKEN should be omitted when token is empty, got %q", noTok["PALMUX_TOKEN"])
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/a b/palmux"); got != "'/a b/palmux'" {
		t.Errorf("shellQuote = %q", got)
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shellQuote(single quote) = %q", got)
	}
}

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

// ---- TranscriptDir / SessionIDFromPath ---------------------------------------

// TestTranscriptDir verifies the slug algorithm: '/' and '.' become '-'.
func TestTranscriptDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	claude := NewClaudeAdapter("claude", nil)

	tests := []struct {
		name     string
		worktree string
		wantSlug string // path segment after ~/.claude/projects/
	}{
		{
			name:     "simple unix path",
			worktree: "/home/ubuntu/ghq/github.com/foo/bar",
			wantSlug: "-home-ubuntu-ghq-github-com-foo-bar",
		},
		{
			name:     "dots become dashes",
			worktree: "/home/ubuntu/go/src/github.com/example.org/proj",
			wantSlug: "-home-ubuntu-go-src-github-com-example-org-proj",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := claude.TranscriptDir(tc.worktree)
			if err != nil {
				t.Fatalf("TranscriptDir(%q): %v", tc.worktree, err)
			}
			want := filepath.Join(home, ".claude", "projects", tc.wantSlug)
			if got != want {
				t.Errorf("TranscriptDir(%q) =\n  %q\nwant\n  %q", tc.worktree, got, want)
			}
		})
	}
}

// TestTranscriptDirEmpty verifies that an empty worktree returns an error.
func TestTranscriptDirEmpty(t *testing.T) {
	_, err := NewClaudeAdapter("claude", nil).TranscriptDir("")
	if err == nil {
		t.Fatal("expected error for empty worktree, got nil")
	}
}

func TestSessionIDFromPath(t *testing.T) {
	claude := NewClaudeAdapter("claude", nil)

	valid := "12345678-abcd-ef01-2345-6789abcdef01"
	if id, ok := claude.SessionIDFromPath("/some/dir/" + valid + ".jsonl"); !ok || id != valid {
		t.Errorf("SessionIDFromPath(valid) = (%q,%v), want (%q,true)", id, ok, valid)
	}
	if _, ok := claude.SessionIDFromPath("/some/dir/not-a-uuid.jsonl"); ok {
		t.Error("SessionIDFromPath should reject non-UUID basenames")
	}
	if _, ok := claude.SessionIDFromPath("/some/dir/" + valid + ".txt"); ok {
		t.Error("SessionIDFromPath should reject non-.jsonl files")
	}
}

// ---- LatestSessionID ----------------------------------------------------------

// TestLatestSessionID creates two .jsonl files with different mtimes and
// verifies that LatestSessionID returns the one with the most recent mtime.
func TestLatestSessionID(t *testing.T) {
	dir := t.TempDir()

	older := "11111111-2222-3333-4444-555555555555"
	newer := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	writeFile(t, filepath.Join(dir, older+".jsonl"), `{"type":"user"}`)
	time.Sleep(20 * time.Millisecond) // ensure different mtime
	writeFile(t, filepath.Join(dir, newer+".jsonl"), `{"type":"assistant"}`)

	got, mtime, err := LatestSessionID(dir)
	if err != nil {
		t.Fatalf("LatestSessionID: %v", err)
	}
	if got != newer {
		t.Errorf("LatestSessionID = %q, want %q", got, newer)
	}
	if mtime.IsZero() {
		t.Error("mtime should not be zero")
	}
}

// TestLatestSessionIDEmpty verifies behaviour on an empty dir.
func TestLatestSessionIDEmpty(t *testing.T) {
	dir := t.TempDir()
	got, mtime, err := LatestSessionID(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
	if !mtime.IsZero() {
		t.Errorf("mtime should be zero, got %v", mtime)
	}
}

// TestLatestSessionIDNonexistentDir verifies that a missing directory returns
// ("", zero, nil) rather than an error.
func TestLatestSessionIDNonexistentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	got, _, err := LatestSessionID(dir)
	if err != nil {
		t.Fatalf("unexpected error for missing dir: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestLatestSessionIDIgnoresNonUUID verifies that non-UUID files are skipped.
func TestLatestSessionIDIgnoresNonUUID(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "not-a-uuid.jsonl"), "{}")
	writeFile(t, filepath.Join(dir, "some-other-file.txt"), "hello")
	got, _, err := LatestSessionID(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty (non-UUID file should be ignored)", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile(%q): %v", path, err)
	}
}

// ---- Capabilities / Kind / DisplayName ----------------------------------------

func TestClaudeAdapterShape(t *testing.T) {
	a := NewClaudeAdapter("", nil)
	if a.Kind() != KindClaude {
		t.Errorf("Kind() = %q, want %q", a.Kind(), KindClaude)
	}
	if a.DisplayName() == "" {
		t.Error("DisplayName() should be non-empty")
	}
	caps := a.Capabilities()
	if !caps.Resume || caps.Notify != NotifyFull || !caps.InContainer || !caps.PermissionMode {
		t.Errorf("Capabilities() = %+v, want full support", caps)
	}
}

func TestClaudeAdapterSetBinArgsHotSwap(t *testing.T) {
	a := NewClaudeAdapter("claude", []string{"--a"})
	spec, err := a.SpawnSpec(SpawnIntent{})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	if spec.Argv[0] != "claude" {
		t.Fatalf("argv[0] = %q, want claude", spec.Argv[0])
	}

	a.SetBin("claude2")
	a.SetArgs([]string{"--b"})
	spec2, err := a.SpawnSpec(SpawnIntent{})
	if err != nil {
		t.Fatalf("SpawnSpec after hot-swap: %v", err)
	}
	if spec2.Argv[0] != "claude2" {
		t.Errorf("argv[0] after SetBin = %q, want claude2", spec2.Argv[0])
	}
	found := false
	for _, a := range spec2.Argv {
		if a == "--b" {
			found = true
		}
	}
	if !found {
		t.Errorf("argv after SetArgs should contain --b: %v", spec2.Argv)
	}

	// Empty SetBin is a no-op (guards against accidental blanking).
	a.SetBin("")
	spec3, _ := a.SpawnSpec(SpawnIntent{})
	if spec3.Argv[0] != "claude2" {
		t.Errorf("SetBin(\"\") should be a no-op; argv[0] = %q", spec3.Argv[0])
	}
}
