package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOpencodeAdapterKindAndDisplayName(t *testing.T) {
	a := NewOpencodeAdapter("", nil)
	if a.Kind() != KindOpencode {
		t.Errorf("Kind() = %q, want %q", a.Kind(), KindOpencode)
	}
	if a.DisplayName() != "opencode" {
		t.Errorf("DisplayName() = %q, want opencode", a.DisplayName())
	}
}

func TestOpencodeAdapterCapabilities(t *testing.T) {
	a := NewOpencodeAdapter("opencode", nil)
	a.binResolver = func(string) (string, bool) { return "", false } // host-independent: binary not found
	caps := a.Capabilities()
	if !caps.Resume {
		t.Error("Resume = false, want true")
	}
	if caps.Notify != NotifyFull {
		t.Errorf("Notify = %q, want %q (turn-end AND permission-wait)", caps.Notify, NotifyFull)
	}
	if caps.InContainer {
		t.Error("InContainer = true, want false (binResolver reports not found)")
	}
	if caps.PermissionMode {
		t.Error("PermissionMode = true, want false")
	}
}

// TestOpencodeAdapterCapabilitiesInContainerWhenResolved (S339021) is the
// host-independent mirror of the above: when binResolver DOES find the
// binary, Capabilities().InContainer must flip to true.
func TestOpencodeAdapterCapabilitiesInContainerWhenResolved(t *testing.T) {
	a := NewOpencodeAdapter("opencode", nil)
	a.binResolver = func(string) (string, bool) { return "/usr/lib/node_modules/opencode-ai/bin/opencode.exe", true }
	if !a.Capabilities().InContainer {
		t.Error("InContainer = false, want true (binResolver reports found)")
	}
}

func TestOpencodeAdapterSpawnSpecFresh(t *testing.T) {
	a := NewOpencodeAdapter("opencode", []string{"--model", "x"})
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: "/repo"})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"opencode", "--model", "x"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
	if len(spec.Env) != 0 {
		t.Errorf("Env = %v, want empty (no hook wiring)", spec.Env)
	}
	if len(spec.PreFiles) != 0 {
		t.Errorf("PreFiles = %v, want empty (no hook wiring)", spec.PreFiles)
	}
	if spec.KillPattern != "opencode" {
		t.Errorf("KillPattern = %q, want %q", spec.KillPattern, "opencode")
	}
}

func TestOpencodeAdapterSpawnSpecResumeByID(t *testing.T) {
	a := NewOpencodeAdapter("opencode", nil)
	spec, err := a.SpawnSpec(SpawnIntent{
		Worktree:        "/repo",
		ResumeSessionID: "ses_abc123",
	})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"opencode", "--session", "ses_abc123"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
}

// TestOpencodeAdapterSpawnSpecInContainerRefused (S339021): an opencode
// binary that does not resolve on this host must still be refused
// explicitly (defense in depth behind the agenttui daemon's D12 guard),
// never silently spawned with the plain host bin.
func TestOpencodeAdapterSpawnSpecInContainerRefused(t *testing.T) {
	a := NewOpencodeAdapter("opencode", nil)
	a.binResolver = func(string) (string, bool) { return "", false }
	_, err := a.SpawnSpec(SpawnIntent{Worktree: "/repo", InContainer: true})
	if err == nil {
		t.Fatal("SpawnSpec: want explicit error for in-container spawn, got nil")
	}
	if !strings.Contains(err.Error(), "in-container") {
		t.Errorf("error = %q, want it to mention in-container", err.Error())
	}
}

// TestOpencodeAdapterSpawnSpecInContainerUsesResolvedBinary (S339021): when
// the binary DOES resolve, Argv[0] and KillPattern must be the resolved
// (container-visible, absolute) path, not the plain configured bin.
func TestOpencodeAdapterSpawnSpecInContainerUsesResolvedBinary(t *testing.T) {
	a := NewOpencodeAdapter("opencode", []string{"--model", "x"})
	const resolved = "/usr/lib/node_modules/opencode-ai/bin/opencode.exe"
	a.binResolver = func(string) (string, bool) { return resolved, true }
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: "/repo", InContainer: true})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{resolved, "--model", "x"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
	if spec.KillPattern != resolved {
		t.Errorf("KillPattern = %q, want %q", spec.KillPattern, resolved)
	}
}

func TestOpencodeAdapterSpawnSpecFreshIgnoresIsRespawn(t *testing.T) {
	// A fresh spawn (ResumeSessionID empty, IsRespawn false) must never
	// resume, even though a subprocess-based cwd match would otherwise be
	// attempted for a respawn.
	a := NewOpencodeAdapter("opencode", nil)
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: "/repo"})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"opencode"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v (fresh spawn must not resume)", spec.Argv, want)
	}
}

func TestOpencodeAdapterSpawnSpecNotifyInjection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := NewOpencodeAdapter("opencode", nil)
	spec, err := a.SpawnSpec(SpawnIntent{
		Worktree: "/repo",
		Hook: HookEnv{
			NotifyURL:   "http://127.0.0.1:1234/api/notify",
			Token:       "tok",
			RepoID:      "r1",
			BranchID:    "b1",
			TabID:       "opencode",
			TabName:     "opencode",
			HookBinPath: "/usr/local/bin/palmux",
		},
	})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}

	wantPluginPath := filepath.Join(home, ".local", "share", "palmux", "opencode-plugins", "palmux-notify.js")
	if len(spec.PreFiles) != 1 {
		t.Fatalf("PreFiles = %v, want exactly one FileDrop", spec.PreFiles)
	}
	fd := spec.PreFiles[0]
	if fd.Path != wantPluginPath {
		t.Errorf("PreFiles[0].Path = %q, want %q", fd.Path, wantPluginPath)
	}
	content := string(fd.Content)
	for _, want := range []string{"PalmuxNotify", "session.idle", "permission.asked", "PALMUX_HOOK_BIN", "hook", "--agent=opencode"} {
		if !strings.Contains(content, want) {
			t.Errorf("plugin content missing %q", want)
		}
	}
	// The dedicated "permission.ask" Hooks key must NOT be used (empirically
	// dead in opencode 1.17.18 — see the type doc comment). Note: this is
	// distinct from the "permission.asked" EVENT string the plugin does use
	// (a substring match on `"permission.ask"` alone would false-positive on
	// `"permission.asked"`), so match the Hooks-registration shape
	// specifically (`"permission.ask":`).
	if strings.Contains(content, `"permission.ask":`) {
		t.Error("plugin content should not register the dead \"permission.ask\" hook")
	}

	gotEnv := map[string]string{}
	for _, kv := range spec.Env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			gotEnv[parts[0]] = parts[1]
		}
	}
	wantConfigContent := `{"$schema":"https://opencode.ai/config.json","plugin":["` + wantPluginPath + `"]}`
	if gotEnv["OPENCODE_CONFIG_CONTENT"] != wantConfigContent {
		t.Errorf("OPENCODE_CONFIG_CONTENT = %q, want %q", gotEnv["OPENCODE_CONFIG_CONTENT"], wantConfigContent)
	}
	wantRest := map[string]string{
		"PALMUX_NOTIFY_URL": "http://127.0.0.1:1234/api/notify",
		"PALMUX_REPO_ID":    "r1",
		"PALMUX_BRANCH_ID":  "b1",
		"PALMUX_TAB_ID":     "opencode",
		"PALMUX_TOKEN":      "tok",
		"PALMUX_TAB_NAME":   "opencode",
		"PALMUX_HOOK_BIN":   "/usr/local/bin/palmux",
	}
	for k, want := range wantRest {
		if gotEnv[k] != want {
			t.Errorf("Env[%s] = %q, want %q", k, gotEnv[k], want)
		}
	}
}

func TestOpencodeAdapterSpawnSpecNoNotifyWithoutHookBinOrURL(t *testing.T) {
	a := NewOpencodeAdapter("opencode", nil)
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: "/repo", Hook: HookEnv{HookBinPath: "/usr/local/bin/palmux"}})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	if len(spec.Env) != 0 {
		t.Errorf("Env = %v, want empty when NotifyURL is empty", spec.Env)
	}
	if len(spec.PreFiles) != 0 {
		t.Errorf("PreFiles = %v, want empty when NotifyURL is empty", spec.PreFiles)
	}
}

func TestOpencodeAdapterSetBinSetArgs(t *testing.T) {
	a := NewOpencodeAdapter("opencode", nil)
	a.SetBin("/custom/opencode")
	a.SetArgs([]string{"--model", "z"})
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: "/repo"})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"/custom/opencode", "--model", "z"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
	// Empty SetBin is a no-op.
	a.SetBin("")
	spec, _ = a.SpawnSpec(SpawnIntent{Worktree: "/repo"})
	if spec.Argv[0] != "/custom/opencode" {
		t.Errorf("Argv[0] = %q, want unchanged /custom/opencode after empty SetBin", spec.Argv[0])
	}
}

func TestOpencodeAdapterInterfaces(t *testing.T) {
	var a Adapter = NewOpencodeAdapter("opencode", nil)
	if _, ok := a.(Configurable); !ok {
		t.Error("OpencodeAdapter should implement Configurable")
	}
	if _, ok := a.(SessionDiscoverer); ok {
		t.Error("OpencodeAdapter must NOT implement SessionDiscoverer (resume is handled in SpawnSpec via a `session list` cwd match)")
	}
	if _, ok := a.(InContainerProvider); !ok {
		t.Error("OpencodeAdapter should implement InContainerProvider (S339021)")
	}
}

// TestOpencodeAdapterContainerBinary (S339021) exercises ContainerBinary
// directly with an injected resolver — host-independent.
func TestOpencodeAdapterContainerBinary(t *testing.T) {
	a := NewOpencodeAdapter("opencode", nil)
	a.binResolver = func(bin string) (string, bool) {
		if bin != "opencode" {
			t.Errorf("binResolver called with %q, want %q", bin, "opencode")
		}
		return "/resolved/opencode", true
	}
	got, ok := a.ContainerBinary()
	if !ok || got != "/resolved/opencode" {
		t.Errorf("ContainerBinary() = (%q, %v), want (/resolved/opencode, true)", got, ok)
	}

	a.binResolver = func(string) (string, bool) { return "", false }
	if _, ok := a.ContainerBinary(); ok {
		t.Error("ContainerBinary() ok = true, want false when resolver reports not found")
	}
}

// TestOpencodeAdapterSharedContainerPaths (S339021) verifies the binary
// share (npm-root vs. standalone-file fallback), the auth/config dirs, and
// the always-created notify-plugin drop dir, using a fake HOME so it is
// filesystem-independent of the real host.
func TestOpencodeAdapterSharedContainerPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pluginDir := filepath.Join(home, ".local", "share", "palmux", "opencode-plugins")

	a := NewOpencodeAdapter("opencode", nil)
	a.binResolver = func(string) (string, bool) { return "", false }
	got := a.SharedContainerPaths()
	// No binary, no auth dirs — only the always-MkdirAll'd notify-plugin dir.
	want := []string{pluginDir}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SharedContainerPaths() = %v, want %v", got, want)
	}
	if fi, err := os.Stat(pluginDir); err != nil || !fi.IsDir() {
		t.Errorf("notify-plugin dir %q was not created: %v", pluginDir, err)
	}

	// Auth dirs present + npm-global install: share the whole node_modules
	// root, not the leaf file.
	shareDir := filepath.Join(home, ".local", "share", "opencode")
	configDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	npmRoot := filepath.Join(home, "npm-global", "lib", "node_modules")
	npmBin := filepath.Join(npmRoot, "opencode-ai", "bin", "opencode.exe")
	fakeNode := filepath.Join(home, "fake-node")
	a.binResolver = func(name string) (string, bool) {
		if name == "node" {
			return fakeNode, true
		}
		return npmBin, true
	}
	got = a.SharedContainerPaths()
	wantNpm := []string{npmRoot, fakeNode, shareDir, configDir, pluginDir}
	if !reflect.DeepEqual(got, wantNpm) {
		t.Errorf("SharedContainerPaths() = %v, want %v", got, wantNpm)
	}
}

// writeFakeOpencodeBin writes an executable shell script at a temp path that,
// when invoked as `<bin> session list --format json`, prints script's stdout
// verbatim (the caller embeds $PWD substitution etc. as needed) and exits 0.
func writeFakeOpencodeBin(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode")
	full := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(full), 0o755); err != nil {
		t.Fatalf("WriteFile fake opencode bin: %v", err)
	}
	return path
}

func TestOpencodeAdapterSpawnSpecRespawnCwdMatchPicksNewest(t *testing.T) {
	worktree := t.TempDir()
	// The fake bin echoes its own $PWD as the "directory" of the matching
	// session (mirroring how the real opencode CLI resolves "directory" from
	// its invocation cwd), alongside an unrelated, newer-looking session for
	// a different directory that must be filtered out.
	bin := writeFakeOpencodeBin(t, `cat <<EOF
[
  {"id":"ses_old","directory":"$PWD","updated":100},
  {"id":"ses_new","directory":"$PWD","updated":200},
  {"id":"ses_other","directory":"/some/other/repo","updated":999}
]
EOF`)

	a := NewOpencodeAdapter(bin, nil)
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: worktree, IsRespawn: true})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{bin, "--session", "ses_new"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v (newest cwd-matching session, ignoring the higher-`updated` other-dir entry)", spec.Argv, want)
	}
}

func TestOpencodeAdapterSpawnSpecRespawnNoMatchFallsBackToContinue(t *testing.T) {
	worktree := t.TempDir()
	bin := writeFakeOpencodeBin(t, `echo '[{"id":"ses_other","directory":"/some/other/repo","updated":100}]'`)

	a := NewOpencodeAdapter(bin, nil)
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: worktree, IsRespawn: true})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{bin, "--continue"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
}

func TestOpencodeAdapterSpawnSpecRespawnBinErrorFallsBackToContinue(t *testing.T) {
	worktree := t.TempDir()
	bin := writeFakeOpencodeBin(t, `echo "boom" 1>&2; exit 1`)

	a := NewOpencodeAdapter(bin, nil)
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: worktree, IsRespawn: true})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{bin, "--continue"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v (bin failure is a best-effort miss, not an error)", spec.Argv, want)
	}
}

func TestOpencodeAdapterSpawnSpecRespawnBadJSONFallsBackToContinue(t *testing.T) {
	worktree := t.TempDir()
	bin := writeFakeOpencodeBin(t, `echo "not json"`)

	a := NewOpencodeAdapter(bin, nil)
	spec, err := a.SpawnSpec(SpawnIntent{Worktree: worktree, IsRespawn: true})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{bin, "--continue"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
}

func TestLatestOpencodeSessionForCwd(t *testing.T) {
	worktree := t.TempDir()
	bin := writeFakeOpencodeBin(t, `cat <<EOF
[
  {"id":"ses_a","directory":"$PWD","updated":10},
  {"id":"ses_b","directory":"$PWD","updated":30},
  {"id":"ses_c","directory":"$PWD","updated":20}
]
EOF`)

	id, ok := latestOpencodeSessionForCwd(bin, worktree)
	if !ok {
		t.Fatal("latestOpencodeSessionForCwd: want ok=true")
	}
	if id != "ses_b" {
		t.Errorf("id = %q, want the highest-`updated` cwd-matching session", id)
	}

	if _, ok := latestOpencodeSessionForCwd("", worktree); ok {
		t.Error("latestOpencodeSessionForCwd: want ok=false for empty bin")
	}
	if _, ok := latestOpencodeSessionForCwd(bin, ""); ok {
		t.Error("latestOpencodeSessionForCwd: want ok=false for empty worktree")
	}
	if _, ok := latestOpencodeSessionForCwd(filepath.Join(worktree, "does-not-exist"), worktree); ok {
		t.Error("latestOpencodeSessionForCwd: want ok=false for a nonexistent bin")
	}
}

func TestOpencodeConfigContentValue(t *testing.T) {
	got, err := opencodeConfigContentValue("/home/ubuntu/.local/share/palmux/opencode-plugins/palmux-notify.js")
	if err != nil {
		t.Fatalf("opencodeConfigContentValue: %v", err)
	}
	want := `{"$schema":"https://opencode.ai/config.json","plugin":["/home/ubuntu/.local/share/palmux/opencode-plugins/palmux-notify.js"]}`
	if got != want {
		t.Errorf("opencodeConfigContentValue = %q, want %q", got, want)
	}
}

func TestOpencodeNotifyPluginPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := opencodeNotifyPluginPath()
	if err != nil {
		t.Fatalf("opencodeNotifyPluginPath: %v", err)
	}
	want := filepath.Join(home, ".local", "share", "palmux", "opencode-plugins", "palmux-notify.js")
	if got != want {
		t.Errorf("opencodeNotifyPluginPath = %q, want %q", got, want)
	}
}
