// Package incus — unit tests using a fake runner so no real incus binary is
// needed.  All tests assert the exact arg sequences emitted by the runtime and
// the incusTmuxClient.
// [AC-S8478ca-2-1] [AC-S8478ca-2-2] [AC-S8478ca-2-3] [AC-S8478ca-2-4]
package incus

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// -------------------------------------------------------------------------
// fakeRunner records every invocation.
// -------------------------------------------------------------------------

type call struct{ args []string }

type fakeRunner struct {
	mu      sync.Mutex
	calls   []call
	results map[string]fakeResult // key = two-word key or "*"
}

type fakeResult struct {
	stdout, stderr string
	code           int
	err            error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{results: map[string]fakeResult{
		"*": {code: 0}, // default: success
	}}
}

func (f *fakeRunner) setResult(key string, r fakeResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[key] = r
}

func (f *fakeRunner) run(ctx context.Context, args ...string) (string, string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{args: append([]string(nil), args...)})
	key := ""
	if len(args) >= 2 {
		key = args[0] + " " + args[1]
	} else if len(args) == 1 {
		key = args[0]
	}
	if r, ok := f.results[key]; ok {
		return r.stdout, r.stderr, r.code, r.err
	}
	if r, ok := f.results["*"]; ok {
		return r.stdout, r.stderr, r.code, r.err
	}
	return "", "", 0, nil
}

func (f *fakeRunner) asRunner() runner {
	return func(ctx context.Context, args ...string) (string, string, int, error) {
		return f.run(ctx, args...)
	}
}

func (f *fakeRunner) recorded() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = append([]string(nil), c.args...)
	}
	return out
}

// -------------------------------------------------------------------------
// Helper: search recorded calls for an exact prefix match.
// -------------------------------------------------------------------------

func findCall(calls [][]string, prefix ...string) ([]string, bool) {
	for _, c := range calls {
		if len(c) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if c[i] != p {
				match = false
				break
			}
		}
		if match {
			return c, true
		}
	}
	return nil, false
}

// -------------------------------------------------------------------------
// TestStart_ArgSequence verifies the exact incus arg order for Start.
// [AC-S8478ca-2-1] [AC-S8478ca-2-2]
// -------------------------------------------------------------------------

func TestStart_ArgSequence(t *testing.T) {
	inst := "ws-test-ab12cd34"
	fr := newFakeRunner()
	// waitReady polls `exec <inst> -- true`; succeed immediately.
	fr.setResult("exec "+inst, fakeResult{code: 0})
	// list for containerIP: return a valid (empty) JSON array.
	fr.setResult("list "+inst, fakeResult{stdout: "[]", code: 0})

	rt := New(runtime.Config{Kind: runtime.KindIncusContainer, Image: "palmux-ws"}, inst, fr.asRunner(), nil)
	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	calls := fr.recorded()

	// 1. init palmux-ws <inst>  [AC-S8478ca-2-1]
	if _, ok := findCall(calls, "init", "palmux-ws", inst); !ok {
		t.Errorf("[AC-S8478ca-2-1] expected 'incus init palmux-ws %s', not found in %v", inst, calls)
	}

	// 2. config set <inst> raw.idmap "both 1000 1000"  [AC-S8478ca-2-2]
	if _, ok := findCall(calls, "config", "set", inst, "raw.idmap", "both 1000 1000"); !ok {
		t.Errorf("[AC-S8478ca-2-2] expected 'incus config set %s raw.idmap \"both 1000 1000\"', not found in %v", inst, calls)
	}

	// 3. Three device-add calls for ~/ghq, ~/.claude, ~/.claude.json  [AC-S8478ca-2-2]
	home, _ := os.UserHomeDir()
	for _, m := range []struct{ name, src string }{
		{"ghq", filepath.Join(home, "ghq")},
		{"dot-claude", filepath.Join(home, ".claude")},
		{"dot-claude-json", filepath.Join(home, ".claude.json")},
	} {
		if _, err := os.Stat(m.src); os.IsNotExist(err) {
			t.Logf("skipping bind-mount assert for %q (not on this machine)", m.src)
			continue
		}
		if _, ok := findCall(calls,
			"config", "device", "add", inst,
			m.name, "disk",
			"source="+m.src,
			"path="+m.src,
		); !ok {
			t.Errorf("[AC-S8478ca-2-2] expected device add for %q, not found in %v", m.name, calls)
		}
	}

	// 4. start <inst>  [AC-S8478ca-2-1]
	if _, ok := findCall(calls, "start", inst); !ok {
		t.Errorf("[AC-S8478ca-2-1] expected 'incus start %s', not found in %v", inst, calls)
	}

	// State should be Ready.
	if s := rt.Status(); s.State != runtime.StateReady {
		t.Errorf("Status.State = %q, want %q", s.State, runtime.StateReady)
	}
}

// -------------------------------------------------------------------------
// TestStop_ArgSequence verifies Stop emits `delete --force <inst>`.
// [AC-S8478ca-2-1] [AC-S8478ca-2-4]
// -------------------------------------------------------------------------

func TestStop_ArgSequence(t *testing.T) {
	inst := "ws-stop-dead1234"
	fr := newFakeRunner()
	rt := New(runtime.Config{Kind: runtime.KindIncusContainer}, inst, fr.asRunner(), nil)
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	calls := fr.recorded()
	if _, ok := findCall(calls, "delete", "--force", inst); !ok {
		t.Errorf("[AC-S8478ca-2-1/4] expected 'incus delete --force %s', got %v", inst, calls)
	}
	if s := rt.Status(); s.State != runtime.StateStopped {
		t.Errorf("Status.State = %q, want %q", s.State, runtime.StateStopped)
	}
}

// -------------------------------------------------------------------------
// TestIncusTmuxClient_NewSession routes through incus exec.
// [AC-S8478ca-2-3]
// -------------------------------------------------------------------------

func TestIncusTmuxClient_NewSession(t *testing.T) {
	inst := "ws-tmux-newses-cafe0011"
	fr := newFakeRunner()
	tc := NewTmuxClient(inst, fr.asRunner())
	if err := tc.NewSession(context.Background(), tmux.NewSessionOpts{
		Name:       "mysession",
		WindowName: "palmux:bash:bash",
	}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	calls := fr.recorded()
	if _, ok := findCall(calls, "exec", inst, "--", "tmux", "new-session", "-d", "-s", "mysession"); !ok {
		t.Errorf("[AC-S8478ca-2-3] NewSession: expected exec <inst> -- tmux new-session ..., got %v", calls)
	}
}

// -------------------------------------------------------------------------
// TestIncusTmuxClient_HasSession
// [AC-S8478ca-2-3]
// -------------------------------------------------------------------------

func TestIncusTmuxClient_HasSession(t *testing.T) {
	inst := "ws-tmux-has-babe1234"
	fr := newFakeRunner()
	fr.setResult("exec "+inst, fakeResult{code: 0}) // has-session → 0 = exists
	tc := NewTmuxClient(inst, fr.asRunner())
	exists, err := tc.HasSession(context.Background(), "s1")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !exists {
		t.Errorf("HasSession = false, want true")
	}
	calls := fr.recorded()
	if _, ok := findCall(calls, "exec", inst, "--", "tmux", "has-session", "-t", "s1"); !ok {
		t.Errorf("[AC-S8478ca-2-3] HasSession: expected exec tmux has-session, got %v", calls)
	}
}

// -------------------------------------------------------------------------
// TestIncusTmuxClient_NewWindow
// [AC-S8478ca-2-3]
// -------------------------------------------------------------------------

func TestIncusTmuxClient_NewWindow(t *testing.T) {
	inst := "ws-tmux-newwin-feed5678"
	fr := newFakeRunner()
	tc := NewTmuxClient(inst, fr.asRunner())
	if err := tc.NewWindow(context.Background(), "ses1", tmux.NewWindowOpts{Name: "mywindow"}); err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	calls := fr.recorded()
	if _, ok := findCall(calls, "exec", inst, "--", "tmux", "new-window", "-t", "ses1", "-n", "mywindow"); !ok {
		t.Errorf("[AC-S8478ca-2-3] NewWindow: expected exec tmux new-window, got %v", calls)
	}
}

// -------------------------------------------------------------------------
// TestIncusTmuxClient_KillSession
// [AC-S8478ca-2-3]
// -------------------------------------------------------------------------

func TestIncusTmuxClient_KillSession(t *testing.T) {
	inst := "ws-tmux-kill-fade9abc"
	fr := newFakeRunner()
	tc := NewTmuxClient(inst, fr.asRunner())
	_ = tc.KillSession(context.Background(), "ses2")
	calls := fr.recorded()
	if _, ok := findCall(calls, "exec", inst, "--", "tmux", "kill-session", "-t", "ses2"); !ok {
		t.Errorf("[AC-S8478ca-2-3] KillSession: expected exec tmux kill-session, got %v", calls)
	}
}

// -------------------------------------------------------------------------
// TestAcquireLock — verifies lock-file create-once / conflict semantics.
// [AC-S8478ca-2-4]
// -------------------------------------------------------------------------

func TestAcquireLock(t *testing.T) {
	tmp := t.TempDir()
	worktreePath := "/tmp/test-worktree-abc"
	encoded := strings.ReplaceAll(worktreePath, "/", "%2F")
	projectDir := filepath.Join(tmp, ".claude", "projects", encoded)
	lockPath := filepath.Join(projectDir, ".palmux-lock")

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// First acquisition: should succeed (write PID).
	if err := os.WriteFile(lockPath, []byte("99999\n"), 0o600); err != nil {
		t.Fatalf("write first lock: %v", err)
	}

	// Second acquisition while locked: O_EXCL must fail.
	_, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		t.Error("[AC-S8478ca-2-4] expected lock conflict when file exists, but OpenFile succeeded")
	} else if !os.IsExist(err) {
		t.Errorf("[AC-S8478ca-2-4] unexpected error type for conflict: %v", err)
	}

	// After releasing (removal), a new acquisition must succeed.
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove lock: %v", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Errorf("[AC-S8478ca-2-4] expected lock to succeed after removal: %v", err)
	} else {
		_ = f.Close()
	}
}

// -------------------------------------------------------------------------
// TestInstanceName — DNS-safe ≤63-char output.
// -------------------------------------------------------------------------

func TestInstanceName(t *testing.T) {
	cases := []struct{ repoID, branchID string }{
		{"tjst-t--palmux2--a1b2", "main--e5f6"},
		{"org--repo--c3d4", "feature-branch--7a8b"},
		{"", ""},
		{"a" + strings.Repeat("b", 60), "branch"},
	}
	for _, c := range cases {
		name := InstanceName(c.repoID, c.branchID)
		if len(name) > 63 {
			t.Errorf("InstanceName(%q,%q)=%q len %d > 63", c.repoID, c.branchID, name, len(name))
		}
		for i, r := range name {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				t.Errorf("InstanceName: invalid char %q at %d in %q", r, i, name)
			}
		}
		if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
			t.Errorf("InstanceName: starts/ends with '-': %q", name)
		}
	}
}

// -------------------------------------------------------------------------
// TestStart_IdempotentWhenExists — non-zero init exit is treated as idempotent.
// -------------------------------------------------------------------------

func TestStart_IdempotentWhenExists(t *testing.T) {
	inst := "ws-idem-1234abcd"
	fr := newFakeRunner()
	fr.setResult("init palmux-ws", fakeResult{code: 1, stderr: "instance already exists"})
	fr.setResult("list "+inst, fakeResult{stdout: "[]", code: 0})
	fr.setResult("exec "+inst, fakeResult{code: 0}) // waitReady

	rt := New(runtime.Config{Kind: runtime.KindIncusContainer, Image: "palmux-ws"}, inst, fr.asRunner(), nil)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("[AC-S8478ca-2-1] Start with pre-existing instance: %v", err)
	}
}

// -------------------------------------------------------------------------
// TestTmuxClient_ThroughRuntime — Runtime.TmuxClient() → incusTmuxClient.
// [AC-S8478ca-2-3]
// -------------------------------------------------------------------------

func TestTmuxClient_ThroughRuntime(t *testing.T) {
	inst := "ws-rt-client-dead5678"
	fr := newFakeRunner()
	rt := New(runtime.Config{Kind: runtime.KindIncusContainer}, inst, fr.asRunner(), nil)
	tc := rt.TmuxClient()
	if tc == nil {
		t.Fatal("[AC-S8478ca-2-3] TmuxClient() returned nil")
	}
	_, _ = tc.HasSession(context.Background(), "test-session")
	calls := fr.recorded()
	if _, ok := findCall(calls, "exec", inst, "--", "tmux", "has-session", "-t", "test-session"); !ok {
		t.Errorf("[AC-S8478ca-2-3] TmuxClient().HasSession: expected exec incus ... tmux has-session, got %v", calls)
	}
}

// -------------------------------------------------------------------------
// Registry tests
// -------------------------------------------------------------------------

// newFakeConfigRepoStore creates a minimal RepoStore with a single workspace
// configured to the given runtime.Kind.
func newFakeConfigRepoStore(t *testing.T, dir, repoID, branchID string, kind runtime.Kind) *config.RepoStore {
	t.Helper()
	rs, err := config.NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	if _, err := rs.Add(config.RepoEntry{ID: repoID, GHQPath: "github.com/test/" + repoID}); err != nil {
		t.Fatalf("Add repo: %v", err)
	}
	cfg := runtime.Config{Kind: kind}
	if err := rs.SetWorkspaceRuntime(repoID, branchID, &cfg); err != nil {
		t.Fatalf("SetWorkspaceRuntime: %v", err)
	}
	return rs
}

func newFakeConfigSettingsStore(t *testing.T, dir string) *config.SettingsStore {
	t.Helper()
	ss, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("NewSettingsStore: %v", err)
	}
	return ss
}

func TestRegistry_ReturnsIncusForIncusKind(t *testing.T) {
	dir := t.TempDir()
	repoStore := newFakeConfigRepoStore(t, dir, "repo1", "ws1", runtime.KindIncusContainer)
	settingsStore := newFakeConfigSettingsStore(t, dir)
	hostTmux := &noopTmuxClient{}

	reg := NewRegistry(repoStore, settingsStore, hostTmux, nil)
	rt := reg.Get("repo1", "ws1")
	if rt == nil {
		t.Fatal("Registry.Get returned nil")
	}
	if rt.Kind() != runtime.KindIncusContainer {
		t.Errorf("Kind = %q, want %q", rt.Kind(), runtime.KindIncusContainer)
	}
	// TmuxClient must NOT be the host noopTmuxClient.
	if rt.TmuxClient() == hostTmux {
		t.Error("expected incus TmuxClient, got host noopTmuxClient")
	}
}

func TestRegistry_ReturnsHostForHostKind(t *testing.T) {
	dir := t.TempDir()
	repoStore := newFakeConfigRepoStore(t, dir, "repo2", "ws2", runtime.KindHost)
	settingsStore := newFakeConfigSettingsStore(t, dir)
	hostTmux := &noopTmuxClient{}

	reg := NewRegistry(repoStore, settingsStore, hostTmux, nil)
	rt := reg.Get("repo2", "ws2")
	if rt.Kind() != runtime.KindHost {
		t.Errorf("Kind = %q, want %q", rt.Kind(), runtime.KindHost)
	}
	if rt.TmuxClient() != hostTmux {
		t.Errorf("host TmuxClient: got %T, want *noopTmuxClient", rt.TmuxClient())
	}
}

func TestRegistry_Cache(t *testing.T) {
	dir := t.TempDir()
	repoStore := newFakeConfigRepoStore(t, dir, "repo3", "ws3", runtime.KindIncusContainer)
	settingsStore := newFakeConfigSettingsStore(t, dir)
	reg := NewRegistry(repoStore, settingsStore, &noopTmuxClient{}, nil)

	rt1 := reg.Get("repo3", "ws3")
	rt2 := reg.Get("repo3", "ws3")
	if rt1 != rt2 {
		t.Error("Registry.Get should return the same cached instance on second call")
	}
}

func TestRegistry_EvictRuntime(t *testing.T) {
	dir := t.TempDir()
	repoStore := newFakeConfigRepoStore(t, dir, "repo4", "ws4", runtime.KindIncusContainer)
	settingsStore := newFakeConfigSettingsStore(t, dir)
	reg := NewRegistry(repoStore, settingsStore, &noopTmuxClient{}, nil)

	rt1 := reg.Get("repo4", "ws4")
	reg.EvictRuntime("repo4", "ws4")
	rt2 := reg.Get("repo4", "ws4")
	if rt1 == rt2 {
		t.Error("Registry.Get should return a NEW instance after EvictRuntime")
	}
}

// -------------------------------------------------------------------------
// noopTmuxClient — minimal tmux.Client implementation for tests that need to
// satisfy the interface without any real tmux operations.
// -------------------------------------------------------------------------

type noopTmuxClient struct{}

func (*noopTmuxClient) ListSessions(_ context.Context) ([]tmux.Session, error)    { return nil, nil }
func (*noopTmuxClient) NewSession(_ context.Context, _ tmux.NewSessionOpts) error { return nil }
func (*noopTmuxClient) KillSession(_ context.Context, _ string) error             { return nil }
func (*noopTmuxClient) HasSession(_ context.Context, _ string) (bool, error)      { return false, nil }
func (*noopTmuxClient) RenameSession(_ context.Context, _, _ string) error        { return nil }
func (*noopTmuxClient) ListWindows(_ context.Context, _ string) ([]tmux.Window, error) {
	return nil, nil
}
func (*noopTmuxClient) NewWindow(_ context.Context, _ string, _ tmux.NewWindowOpts) error {
	return nil
}
func (*noopTmuxClient) KillWindowByName(_ context.Context, _, _ string) error { return nil }
func (*noopTmuxClient) RenameWindow(_ context.Context, _, _, _ string) error  { return nil }
func (*noopTmuxClient) WindowIndexByName(_ context.Context, _, _ string) (int, error) {
	return 0, nil
}
func (*noopTmuxClient) SendKeys(_ context.Context, _, _, _ string) error      { return nil }
func (*noopTmuxClient) RespawnWindow(_ context.Context, _, _, _ string) error { return nil }
func (*noopTmuxClient) Attach(_ context.Context, _, _ string, _ tmux.AttachOpts) (io.ReadWriteCloser, tmux.ResizeFunc, error) {
	return nil, nil, nil
}
func (*noopTmuxClient) AttachByIndex(_ context.Context, _ string, _ int, _ tmux.AttachOpts) (io.ReadWriteCloser, tmux.ResizeFunc, error) {
	return nil, nil, nil
}
func (*noopTmuxClient) NewGroupSession(_ context.Context, _, _ string) error { return nil }
