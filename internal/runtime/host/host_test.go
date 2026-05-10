package host_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/runtime/host"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// TestKindAndConfig is a smoke test that the host runtime declares Kind=host.
//
// [AC-Sdd4ce1-2-1]
func TestKindAndConfig(t *testing.T) {
	t.Parallel()
	cfg := runtime.Config{Kind: runtime.KindHost}
	r := host.New(cfg, t.TempDir(), tmux.NewMockClient())
	if r.Kind() != runtime.KindHost {
		t.Errorf("Kind() = %q, want %q", r.Kind(), runtime.KindHost)
	}
	if r.Config() != cfg {
		t.Errorf("Config() = %+v, want %+v", r.Config(), cfg)
	}
}

// TestStartStopStatus exercises the lifecycle bookkeeping. host has no
// asynchronous bring-up so Start should leave State=ready.
func TestStartStopStatus(t *testing.T) {
	t.Parallel()
	r := host.New(runtime.Config{Kind: runtime.KindHost}, t.TempDir(), tmux.NewMockClient())
	if got := r.Status().State; got != runtime.StateStopped {
		t.Errorf("initial State = %q, want %q", got, runtime.StateStopped)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := r.Status()
	if st.State != runtime.StateReady {
		t.Errorf("after Start: State = %q, want %q", st.State, runtime.StateReady)
	}
	if st.Address != "localhost" {
		t.Errorf("after Start: Address = %q, want %q", st.Address, "localhost")
	}
	if st.StartedAt.IsZero() {
		t.Errorf("after Start: StartedAt is zero")
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := r.Status().State; got != runtime.StateStopped {
		t.Errorf("after Stop: State = %q, want %q", got, runtime.StateStopped)
	}
}

// TestNewTmuxSessionDelegatesToClient verifies AC-Sdd4ce1-2-1: NewTmuxSession
// goes through internal/tmux.Client (the MockClient) — no direct exec.Command.
//
// [AC-Sdd4ce1-2-1]
func TestNewTmuxSessionDelegatesToClient(t *testing.T) {
	t.Parallel()
	mc := tmux.NewMockClient()
	r := host.New(runtime.Config{Kind: runtime.KindHost}, t.TempDir(), mc)
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.NewTmuxSession(context.Background(), "_palmux_test_session"); err != nil {
		t.Fatalf("NewTmuxSession: %v", err)
	}
	calls := mc.Calls()
	found := false
	for _, c := range calls {
		if strings.HasPrefix(c, "NewSession _palmux_test_session") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected MockClient.NewSession call, got: %v", calls)
	}
}

// TestExposePortNoOpHostMatchesContainerPort covers AC-Sdd4ce1-2-2: host
// runtime ExposePort is bookkeeping only — HostPort==ContainerPort and the
// mapping is tracked for later UnexposePort.
//
// [AC-Sdd4ce1-2-2]
func TestExposePortNoOpHostMatchesContainerPort(t *testing.T) {
	t.Parallel()
	r := host.New(runtime.Config{Kind: runtime.KindHost}, t.TempDir(), tmux.NewMockClient())
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	mp, err := r.ExposePort(context.Background(), 5173, 0, "vite", false)
	if err != nil {
		t.Fatalf("ExposePort: %v", err)
	}
	if mp.HostPort != 5173 || mp.ContainerPort != 5173 {
		t.Errorf("HostPort=%d ContainerPort=%d, want both 5173", mp.HostPort, mp.ContainerPort)
	}
	if mp.Name != "vite" {
		t.Errorf("Name = %q, want %q", mp.Name, "vite")
	}
	if got := r.Mappings(); len(got) != 1 || got[0].HostPort != 5173 {
		t.Errorf("Mappings() = %+v, want one mapping for 5173", got)
	}
	if err := r.UnexposePort(context.Background(), mp.ID); err != nil {
		t.Fatalf("UnexposePort: %v", err)
	}
	if got := r.Mappings(); len(got) != 0 {
		t.Errorf("after UnexposePort: Mappings() = %+v, want empty", got)
	}
}

// TestExposePortRejectsHostPortMismatch ensures host runtime refuses to
// pretend a port translation is happening.
func TestExposePortRejectsHostPortMismatch(t *testing.T) {
	t.Parallel()
	r := host.New(runtime.Config{Kind: runtime.KindHost}, t.TempDir(), tmux.NewMockClient())
	_, err := r.ExposePort(context.Background(), 5173, 15173, "vite", false)
	if err == nil {
		t.Errorf("ExposePort with mismatched ports: expected error, got nil")
	}
}

// TestReadWriteStatWalk exercises the Files API on an actual tmpdir.
func TestReadWriteStatWalk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := host.New(runtime.Config{Kind: runtime.KindHost}, dir, tmux.NewMockClient())

	if err := r.WriteFile(context.Background(), "hello.txt", []byte("hi")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := r.WriteFile(context.Background(), "sub/nested.txt", []byte("ok")); err != nil {
		t.Fatalf("WriteFile sub: %v", err)
	}
	data, err := r.ReadFile(context.Background(), "hello.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hi" {
		t.Errorf("ReadFile = %q, want %q", data, "hi")
	}
	info, err := r.Stat(context.Background(), "hello.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Name != "hello.txt" || info.Size != 2 || info.IsDir {
		t.Errorf("Stat = %+v, want hello.txt size=2", info)
	}

	var seen []string
	err = r.Walk(context.Background(), ".", func(e runtime.WalkEntry) error {
		seen = append(seen, e.RelPath)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	wantSubstr := []string{"hello.txt", "sub", filepath.Join("sub", "nested.txt")}
	for _, w := range wantSubstr {
		found := false
		for _, s := range seen {
			if s == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Walk missed %q (got %v)", w, seen)
		}
	}
}

// TestFilesRejectTraversal ensures that ".." paths are refused.
func TestFilesRejectTraversal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Place a file outside the worktree so a successful traversal would
	// reveal it.
	parent := filepath.Dir(dir)
	if err := os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatalf("seed parent file: %v", err)
	}
	r := host.New(runtime.Config{Kind: runtime.KindHost}, dir, tmux.NewMockClient())
	if _, err := r.ReadFile(context.Background(), "../secret.txt"); err == nil {
		t.Errorf("ReadFile ../secret.txt: expected error, got nil")
	}
}

// TestExec runs a no-op command to ensure Exec wires up stdout/stderr/exit.
func TestExec(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := host.New(runtime.Config{Kind: runtime.KindHost}, dir, tmux.NewMockClient())
	res, err := r.Exec(context.Background(), []string{"sh", "-c", "echo hello && echo err 1>&2"}, runtime.ExecOpts{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(string(res.Stdout), "hello") {
		t.Errorf("stdout = %q, want contains 'hello'", res.Stdout)
	}
	if !strings.Contains(string(res.Stderr), "err") {
		t.Errorf("stderr = %q, want contains 'err'", res.Stderr)
	}
}
