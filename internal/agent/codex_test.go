package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCodexAdapterKindAndDisplayName(t *testing.T) {
	a := NewCodexAdapter("", nil)
	if a.Kind() != KindCodex {
		t.Errorf("Kind() = %q, want %q", a.Kind(), KindCodex)
	}
	if a.DisplayName() != "Codex" {
		t.Errorf("DisplayName() = %q, want Codex", a.DisplayName())
	}
}

func TestCodexAdapterCapabilities(t *testing.T) {
	a := NewCodexAdapter("codex", nil)
	a.binResolver = func(string) (string, bool) { return "", false } // host-independent: binary not found
	caps := a.Capabilities()
	if !caps.Resume {
		t.Error("Resume = false, want true")
	}
	if caps.Notify != NotifyTurnEnd {
		t.Errorf("Notify = %q, want %q", caps.Notify, NotifyTurnEnd)
	}
	if caps.InContainer {
		t.Error("InContainer = true, want false (binResolver reports not found)")
	}
	if caps.PermissionMode {
		t.Error("PermissionMode = true, want false")
	}
}

// TestCodexAdapterCapabilitiesInContainerWhenResolved (S339021) is the
// host-independent mirror of the above: when binResolver DOES find the
// binary, Capabilities().InContainer must flip to true.
func TestCodexAdapterCapabilitiesInContainerWhenResolved(t *testing.T) {
	a := NewCodexAdapter("codex", nil)
	a.binResolver = func(string) (string, bool) { return "/usr/lib/node_modules/@openai/codex/bin/codex.js", true }
	if !a.Capabilities().InContainer {
		t.Error("InContainer = false, want true (binResolver reports found)")
	}
}

func TestCodexAdapterSpawnSpecFresh(t *testing.T) {
	a := NewCodexAdapter("codex", []string{"--extra"})
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: "/repo"})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"codex", "--extra"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
	if len(spec.Env) != 0 {
		t.Errorf("Env = %v, want empty (no hook wiring)", spec.Env)
	}
	if spec.KillPattern != "codex" {
		t.Errorf("KillPattern = %q, want %q", spec.KillPattern, "codex")
	}
}

func TestCodexAdapterSpawnSpecResumeByID(t *testing.T) {
	a := NewCodexAdapter("codex", nil)
	spec, err := a.SpawnSpec(SpawnIntent{
		Worktree:        "/repo",
		ResumeSessionID: "11111111-1111-1111-1111-111111111111",
	})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"codex", "resume", "11111111-1111-1111-1111-111111111111"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
}

// TestCodexAdapterSpawnSpecInContainerRefused (S339021): a codex binary that
// does not resolve on this host must still be refused explicitly (defense
// in depth behind the agenttui daemon's D12 guard), never silently spawned
// with the plain host bin.
func TestCodexAdapterSpawnSpecInContainerRefused(t *testing.T) {
	a := NewCodexAdapter("codex", nil)
	a.binResolver = func(string) (string, bool) { return "", false }
	_, err := a.SpawnSpec(SpawnIntent{Worktree: "/repo", InContainer: true})
	if err == nil {
		t.Fatal("SpawnSpec: want explicit error for in-container spawn, got nil")
	}
	if !strings.Contains(err.Error(), "in-container") {
		t.Errorf("error = %q, want it to mention in-container", err.Error())
	}
}

// TestCodexAdapterSpawnSpecInContainerUsesResolvedBinary (S339021): when the
// binary DOES resolve, Argv[0] and KillPattern must be the resolved
// (container-visible, absolute) path, not the plain configured bin.
func TestCodexAdapterSpawnSpecInContainerUsesResolvedBinary(t *testing.T) {
	a := NewCodexAdapter("codex", []string{"--extra"})
	const resolved = "/usr/lib/node_modules/@openai/codex/bin/codex.js"
	a.binResolver = func(string) (string, bool) { return resolved, true }
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: "/repo", InContainer: true})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{resolved, "--extra"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
	if spec.KillPattern != resolved {
		t.Errorf("KillPattern = %q, want %q", spec.KillPattern, resolved)
	}
}

func TestCodexAdapterSpawnSpecNotifyInjection(t *testing.T) {
	a := NewCodexAdapter("codex", nil)
	spec, err := a.SpawnSpec(SpawnIntent{
		Worktree: "/repo",
		Hook: HookEnv{
			NotifyURL:   "http://127.0.0.1:1234/api/notify",
			Token:       "tok",
			RepoID:      "r1",
			BranchID:    "b1",
			TabID:       "codex",
			TabName:     "Codex",
			HookBinPath: "/usr/local/bin/palmux",
		},
	})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"codex", "-c", `notify=["/usr/local/bin/palmux","hook","--agent=codex"]`}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}

	wantEnv := map[string]string{
		"PALMUX_NOTIFY_URL": "http://127.0.0.1:1234/api/notify",
		"PALMUX_REPO_ID":    "r1",
		"PALMUX_BRANCH_ID":  "b1",
		"PALMUX_TAB_ID":     "codex",
		"PALMUX_TOKEN":      "tok",
		"PALMUX_TAB_NAME":   "Codex",
	}
	gotEnv := map[string]string{}
	for _, kv := range spec.Env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			gotEnv[parts[0]] = parts[1]
		}
	}
	if !reflect.DeepEqual(gotEnv, wantEnv) {
		t.Errorf("Env = %v, want %v", gotEnv, wantEnv)
	}
}

func TestCodexAdapterSpawnSpecNoNotifyWithoutHookBinOrURL(t *testing.T) {
	a := NewCodexAdapter("codex", nil)
	// HookBinPath set but NotifyURL empty — must not inject -c notify.
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: "/repo", Hook: HookEnv{HookBinPath: "/usr/local/bin/palmux"}})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	for _, a := range spec.Argv {
		if a == "-c" {
			t.Errorf("Argv = %v, want no -c flag when NotifyURL is empty", spec.Argv)
		}
	}
}

// writeCodexRollout creates a fake rollout-*.jsonl transcript under
// sessionsRoot/YYYY/MM/DD/ whose first line is a session_meta record for
// (sessionID, cwd), and sets its mtime to mtime.
func writeCodexRollout(t *testing.T, sessionsRoot, sessionID, cwd string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(sessionsRoot, "2026", "07", "11")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "rollout-"+sessionID+".jsonl")
	line := `{"type":"session_meta","payload":{"session_id":"` + sessionID + `","cwd":"` + cwd + `"}}` + "\n" +
		`{"type":"turn_context","payload":{}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	return path
}

func TestCodexAdapterSpawnSpecRespawnCwdMatchPicksNewest(t *testing.T) {
	sessionsRoot := t.TempDir()
	worktree := t.TempDir() // used as the "cwd" to match — must be absolute

	now := time.Now()
	writeCodexRollout(t, sessionsRoot, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "/some/other/repo", now.Add(-1*time.Minute))
	writeCodexRollout(t, sessionsRoot, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", worktree, now.Add(-2*time.Minute))
	newest := writeCodexRollout(t, sessionsRoot, "cccccccc-cccc-cccc-cccc-cccccccccccc", worktree, now)
	_ = newest

	a := NewCodexAdapter("codex", nil)
	a.sessionsRoot = sessionsRoot

	spec, err := a.SpawnSpec(SpawnIntent{Worktree: worktree, IsRespawn: true})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"codex", "resume", "cccccccc-cccc-cccc-cccc-cccccccccccc"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v (newest cwd-matching session)", spec.Argv, want)
	}
}

func TestCodexAdapterSpawnSpecRespawnNoMatchFallsBackToLast(t *testing.T) {
	sessionsRoot := t.TempDir()
	worktree := t.TempDir()

	// Only a session for a DIFFERENT cwd exists.
	writeCodexRollout(t, sessionsRoot, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "/some/other/repo", time.Now())

	a := NewCodexAdapter("codex", nil)
	a.sessionsRoot = sessionsRoot

	spec, err := a.SpawnSpec(SpawnIntent{Worktree: worktree, IsRespawn: true})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"codex", "resume", "--last"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
}

func TestCodexAdapterSpawnSpecFreshIgnoresIsRespawn(t *testing.T) {
	// A fresh spawn (ResumeSessionID empty, IsRespawn false) must never
	// resume, even if matching sessions exist on disk.
	sessionsRoot := t.TempDir()
	worktree := t.TempDir()
	writeCodexRollout(t, sessionsRoot, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", worktree, time.Now())

	a := NewCodexAdapter("codex", nil)
	a.sessionsRoot = sessionsRoot

	spec, err := a.SpawnSpec(SpawnIntent{Worktree: worktree})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"codex"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v (fresh spawn must not resume)", spec.Argv, want)
	}
}

func TestLatestCodexSessionForCwd(t *testing.T) {
	sessionsRoot := t.TempDir()
	cwd := t.TempDir()
	now := time.Now()

	writeCodexRollout(t, sessionsRoot, "11111111-1111-1111-1111-111111111111", cwd, now.Add(-time.Hour))
	writeCodexRollout(t, sessionsRoot, "22222222-2222-2222-2222-222222222222", cwd, now)
	writeCodexRollout(t, sessionsRoot, "33333333-3333-3333-3333-333333333333", "/unrelated", now.Add(time.Hour))

	id, ok := latestCodexSessionForCwd(sessionsRoot, cwd)
	if !ok {
		t.Fatal("latestCodexSessionForCwd: want ok=true")
	}
	if id != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("id = %q, want the newest cwd-matching session", id)
	}

	if _, ok := latestCodexSessionForCwd(sessionsRoot, "/no/such/cwd"); ok {
		t.Error("latestCodexSessionForCwd: want ok=false for a cwd with no matching session")
	}

	if _, ok := latestCodexSessionForCwd("", cwd); ok {
		t.Error("latestCodexSessionForCwd: want ok=false for empty sessionsRoot")
	}
	if _, ok := latestCodexSessionForCwd(filepath.Join(sessionsRoot, "does-not-exist"), cwd); ok {
		t.Error("latestCodexSessionForCwd: want ok=false for a nonexistent sessionsRoot")
	}
}

func TestCodexAdapterSetBinSetArgs(t *testing.T) {
	a := NewCodexAdapter("codex", nil)
	a.SetBin("/custom/codex")
	a.SetArgs([]string{"--model", "o3"})
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: "/repo"})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"/custom/codex", "--model", "o3"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
	// Empty SetBin is a no-op.
	a.SetBin("")
	spec, _ = a.SpawnSpec(SpawnIntent{Worktree: "/repo"})
	if spec.Argv[0] != "/custom/codex" {
		t.Errorf("Argv[0] = %q, want unchanged /custom/codex after empty SetBin", spec.Argv[0])
	}
}

func TestCodexAdapterInterfaces(t *testing.T) {
	var a Adapter = NewCodexAdapter("codex", nil)
	if _, ok := a.(Configurable); !ok {
		t.Error("CodexAdapter should implement Configurable")
	}
	if _, ok := a.(SessionDiscoverer); ok {
		t.Error("CodexAdapter must NOT implement SessionDiscoverer (resume is handled in SpawnSpec via cwd match)")
	}
	if _, ok := a.(InContainerProvider); !ok {
		t.Error("CodexAdapter should implement InContainerProvider (S339021)")
	}
}

// TestCodexAdapterContainerBinary (S339021) exercises ContainerBinary
// directly with an injected resolver — host-independent.
func TestCodexAdapterContainerBinary(t *testing.T) {
	a := NewCodexAdapter("codex", nil)
	a.binResolver = func(bin string) (string, bool) {
		if bin != "codex" {
			t.Errorf("binResolver called with %q, want %q", bin, "codex")
		}
		return "/resolved/codex", true
	}
	got, ok := a.ContainerBinary()
	if !ok || got != "/resolved/codex" {
		t.Errorf("ContainerBinary() = (%q, %v), want (/resolved/codex, true)", got, ok)
	}

	a.binResolver = func(string) (string, bool) { return "", false }
	if _, ok := a.ContainerBinary(); ok {
		t.Error("ContainerBinary() ok = true, want false when resolver reports not found")
	}
}

// TestCodexAdapterSharedContainerPaths (S339021) verifies the binary share
// (npm-root vs. standalone-file fallback) and the ~/.codex auth dir, using
// a fake HOME so it is filesystem-independent of the real host.
func TestCodexAdapterSharedContainerPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := NewCodexAdapter("codex", nil)
	a.binResolver = func(string) (string, bool) { return "", false }
	if got := a.SharedContainerPaths(); len(got) != 0 {
		t.Errorf("SharedContainerPaths() = %v, want empty when binary does not resolve and ~/.codex absent", got)
	}

	// Standalone binary (no npm node_modules ancestor) + ~/.codex present.
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	standalone := filepath.Join(home, ".local", "bin", "codex")
	a.binResolver = func(string) (string, bool) { return standalone, true }
	got := a.SharedContainerPaths()
	wantStandalone := []string{standalone, codexHome}
	if !reflect.DeepEqual(got, wantStandalone) {
		t.Errorf("SharedContainerPaths() = %v, want %v", got, wantStandalone)
	}

	// npm-global install: share the whole node_modules root (not the leaf
	// file) AND the node runtime the wrapper's shebang needs.
	npmRoot := filepath.Join(home, "npm-global", "lib", "node_modules")
	npmBin := filepath.Join(npmRoot, "@openai", "codex", "bin", "codex.js")
	fakeNode := filepath.Join(home, "fake-node")
	a.binResolver = func(name string) (string, bool) {
		if name == "node" {
			return fakeNode, true
		}
		return npmBin, true
	}
	got = a.SharedContainerPaths()
	wantNpm := []string{npmRoot, fakeNode, codexHome}
	if !reflect.DeepEqual(got, wantNpm) {
		t.Errorf("SharedContainerPaths() = %v, want %v", got, wantNpm)
	}
}
