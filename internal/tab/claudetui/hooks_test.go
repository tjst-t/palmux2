package claudetui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// fakePTYRuntime implements runtime.PTYCommander for the in-container spawn
// test. It substitutes the (container) claude bin in argv[0] with the test fake
// bin so the daemon's spawn actually runs, and records the argv/env/cwd.
type fakePTYRuntime struct {
	fakeBin string
	mu      sync.Mutex
	argv    []string
	env     []string
	cwd     string
}

func (f *fakePTYRuntime) PTYCommand(ctx context.Context, argv []string, opts runtime.PTYCommandOpts) *exec.Cmd {
	f.mu.Lock()
	f.argv = append([]string(nil), argv...)
	f.env = append([]string(nil), opts.Env...)
	f.cwd = opts.Cwd
	f.mu.Unlock()
	// Run the fake claude with everything AFTER the container claude bin path.
	cmd := exec.CommandContext(ctx, f.fakeBin, argv[1:]...)
	cmd.Env = append(os.Environ(), opts.Env...)
	return cmd
}

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

// TestDaemonInjectsAddDir verifies that when NotifyURL and HookBinPath are set,
// the daemon spawns claude with both `--settings <hooks JSON>` AND
// `--add-dir /usr/local/share/palmux` (the palmux skill bundle directory).
// This ensures the palmux-browser skill is auto-loaded in every claude-tui
// session without polluting ~/.claude or the project's .claude directory.
// [AC-S62374c-3-1] (--add-dir injection)
func TestDaemonInjectsAddDir(t *testing.T) {
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
		NotifyToken:   "tok",
		HookBinPath:   "/usr/local/bin/palmux",
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

	var rec struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("invocation JSON: %v\n%s", err, raw)
	}

	// S4d8b1c: HOST mode injects --settings (hooks) but NOT a skill flag — the
	// palmux-browser skill plugin only exists inside the container image, so
	// neither --add-dir nor --plugin-dir is injected on the host.
	if !hasArg(rec.Argv, "--settings") {
		t.Errorf("--settings not found in host argv; got: %v", rec.Argv)
	}
	if hasArg(rec.Argv, "--add-dir") {
		t.Errorf("host mode must NOT inject --add-dir (skill plugin is container-only); got: %v", rec.Argv)
	}
	if hasArg(rec.Argv, "--plugin-dir") {
		t.Errorf("host mode must NOT inject --plugin-dir (no container plugin on host); got: %v", rec.Argv)
	}
}

func hasArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func hasArgPair(argv []string, flag, val string) bool {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) && argv[i+1] == val {
			return true
		}
	}
	return false
}

// TestDaemonInContainerInjectsPluginDir verifies that when the workspace runtime
// is a PTYCommander (incus), the daemon injects --plugin-dir (the correct flag
// to load the bundled palmux-browser skill) and routes the spawn through the
// runtime. [AC-S4d8b1c-1-3]
func TestDaemonInContainerInjectsPluginDir(t *testing.T) {
	bin := fakeBin(t)
	dump := filepath.Join(t.TempDir(), "invocation.json")
	fakeRT := &fakePTYRuntime{fakeBin: bin}

	d := NewDaemon(DaemonConfig{
		ClaudeBin:     "/nonexistent/host/claude", // must be overridden by container path
		ClaudeArgs:    []string{"--dump-invocation", dump},
		RingSize:      1 << 16,
		ResumeOnDeath: false,
		RepoID:        "repo1",
		BranchID:      "branch1",
		TabID:         "claude",
		NotifyURL:     "http://127.0.0.1:8080/api/notify",
		NotifyToken:   "tok",
		HookBinPath:   "/host/palmux",
		Worktree:      t.TempDir(),
		RuntimeResolver: func(_, _ string) runtime.PTYCommander {
			return fakeRT
		},
		NotifyURLInContainer: "http://10.0.0.1:8080/api/notify",
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
	var rec struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("invocation JSON: %v\n%s", err, raw)
	}

	// --plugin-dir <palmux plugin> must be injected (registers the skill).
	if !hasArgPair(rec.Argv, "--plugin-dir", palmuxSkillDir) {
		t.Errorf("[AC-S4d8b1c-1-3] --plugin-dir %s not injected; got: %v", palmuxSkillDir, rec.Argv)
	}
	if hasArg(rec.Argv, "--add-dir") {
		t.Errorf("--add-dir must not be used for skills (use --plugin-dir); got: %v", rec.Argv)
	}
	if !hasArg(rec.Argv, "--settings") {
		t.Errorf("--settings (hooks) not injected in-container; got: %v", rec.Argv)
	}

	// The spawn must have routed through the runtime (container claude bin),
	// NOT the host ClaudeBin.
	fakeRT.mu.Lock()
	gotArgv0 := ""
	if len(fakeRT.argv) > 0 {
		gotArgv0 = fakeRT.argv[0]
	}
	gotCwd := fakeRT.cwd
	gotEnv := append([]string(nil), fakeRT.env...)
	fakeRT.mu.Unlock()
	if gotArgv0 != containerClaudeBin {
		t.Errorf("[AC-S4d8b1c-1-1] in-container spawn argv[0]=%q, want container claude bin %q", gotArgv0, containerClaudeBin)
	}
	if gotCwd != d.worktree {
		t.Errorf("in-container cwd=%q, want worktree %q", gotCwd, d.worktree)
	}
	// The bridge notify URL (not 127.0.0.1) must be in the container env. [AC-S4d8b1c-1-5]
	foundBridge := false
	for _, kv := range gotEnv {
		if kv == "PALMUX_NOTIFY_URL=http://10.0.0.1:8080/api/notify" {
			foundBridge = true
		}
	}
	if !foundBridge {
		t.Errorf("[AC-S4d8b1c-1-5] container env missing bridge PALMUX_NOTIFY_URL; got: %v", gotEnv)
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
