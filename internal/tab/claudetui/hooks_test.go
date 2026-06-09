package claudetui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// TestDaemonInjectsHookSettings is an integration test: when NotifyURL and
// HookBinPath are set, the daemon spawns claude with `--settings <hooks JSON>`
// and the PALMUX_* identity env. fake_claude records its invocation so we can
// assert both reached the subprocess.
func TestDaemonInjectsHookSettings(t *testing.T) {
	bin := fakeBin(t)
	dump := filepath.Join(t.TempDir(), "invocation.json")

	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		ClaudeArgs:    []string{"--dump-invocation", dump},
		RingSize:      1 << 16,
		ResumeOnDeath: false,
		RepoID:        "repo1",
		BranchID:      "branch1",
		TabID:         "claude",
		NotifyURL:     "http://127.0.0.1:8080/api/notify",
		NotifyToken:   "secret-tok",
		HookBinPath:   "/usr/local/bin/palmux",
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	// Wait for fake_claude to write the invocation dump.
	var raw []byte
	deadline := time.After(5 * time.Second)
	for {
		if b, err := os.ReadFile(dump); err == nil && len(b) > 0 {
			raw = b
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for invocation dump")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	var rec struct {
		Argv []string          `json:"argv"`
		Env  map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("invocation JSON: %v\n%s", err, raw)
	}

	// --settings must be present with a value carrying all three hook events.
	settingsVal := ""
	for i, a := range rec.Argv {
		if a == "--settings" && i+1 < len(rec.Argv) {
			settingsVal = rec.Argv[i+1]
		}
	}
	if settingsVal == "" {
		t.Fatalf("--settings not injected; argv=%v", rec.Argv)
	}
	for _, event := range []string{"Notification", "Stop", "UserPromptSubmit"} {
		if !strings.Contains(settingsVal, event) {
			t.Errorf("--settings missing %q hook: %s", event, settingsVal)
		}
	}
	if !strings.Contains(settingsVal, "'/usr/local/bin/palmux' hook") {
		t.Errorf("--settings command does not invoke palmux hook: %s", settingsVal)
	}

	// Identity + callback env must reach the subprocess.
	wantEnv := map[string]string{
		"PALMUX_NOTIFY_URL": "http://127.0.0.1:8080/api/notify",
		"PALMUX_TOKEN":      "secret-tok",
		"PALMUX_REPO_ID":    "repo1",
		"PALMUX_BRANCH_ID":  "branch1",
		"PALMUX_TAB_ID":     "claude",
	}
	for k, v := range wantEnv {
		if rec.Env[k] != v {
			t.Errorf("env %s = %q, want %q", k, rec.Env[k], v)
		}
	}
}

// TestDaemonNoHookInjectionWithoutConfig verifies that omitting NotifyURL /
// HookBinPath leaves the spawn untouched (no --settings) — the path tests and
// fake_claude rely on.
func TestDaemonNoHookInjectionWithoutConfig(t *testing.T) {
	bin := fakeBin(t)
	dump := filepath.Join(t.TempDir(), "invocation.json")

	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		ClaudeArgs:    []string{"--dump-invocation", dump},
		RingSize:      1 << 16,
		ResumeOnDeath: false,
		RepoID:        "repo1",
		BranchID:      "branch1",
		TabID:         "claude",
		// NotifyURL / HookBinPath intentionally empty.
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	var raw []byte
	deadline := time.After(5 * time.Second)
	for {
		if b, err := os.ReadFile(dump); err == nil && len(b) > 0 {
			raw = b
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for invocation dump")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	if strings.Contains(string(raw), "--settings") {
		t.Errorf("--settings should not be injected without hook config: %s", raw)
	}
}
