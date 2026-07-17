package agenttui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/agent"
	"github.com/tjst-t/palmux2/internal/runtime"
)

// This file is the S0e8afb-2 (P2 — graft seam) golden-argv equivalence test
// required by the design doc's P2 verification section
// ("golden-argv 等価テスト: claude の spec.Argv → ptyhost.Config.Argv が
// host/in-container両方で既存と一致"). internal/agent/claude_golden_test.go
// already pins that agent.ClaudeAdapter.SpawnSpec itself reproduces the
// pre-extraction argv/env formula byte-for-byte (Sdec0a7-1's own golden
// test, brought in verbatim). THIS file closes the other half of the loop:
// that spawnWithArgs's WIRING — spec.Argv handed to launchAndAttach on the
// host path (through resolveClaudeBin), or wrapped via pc.PTYCommand on the
// in-container path — actually delivers that exact argv to the ptyhost that
// ends up executing it. Without this second test, a bug introduced ONLY in
// the wiring (e.g. dropping spec.Env, or handing PTYCommand a mangled argv)
// would pass claude_golden_test.go yet still change real spawn behavior.

// TestGoldenArgv_HostSpawnMatchesAdapterSpec is the host half: a real Daemon
// spawn (via the in-process ptyhost fallback — DaemonConfig.PalmuxBin unset)
// whose fake_claude child dumps the argv/env it actually received, compared
// against agent.NewClaudeAdapter(...).SpawnSpec(...)'s independently-computed
// Argv/Env for the SAME intent (fresh spawn, hooks enabled, permission mode
// set).
func TestGoldenArgv_HostSpawnMatchesAdapterSpec(t *testing.T) {
	bin := fakeBin(t) // already absolute (t.TempDir()-rooted) — resolveClaudeBin is a no-op passthrough
	dump := filepath.Join(t.TempDir(), "invocation.json")
	extraArgs := []string{"--dump-invocation", dump, "--foo"}

	const (
		repoID, branchID, tabID = "golden-repo", "golden-branch", "claude"
		notifyURL               = "http://127.0.0.1:8080/api/notify"
		token                   = "golden-tok"
		hookBin                 = "/usr/local/bin/palmux"
		permissionMode          = "bypassPermissions"
	)

	d := NewDaemon(DaemonConfig{
		ClaudeBin:        bin,
		ClaudeArgs:       extraArgs,
		RingSize:         1 << 16,
		ResumeOnDeath:    false,
		RepoID:           repoID,
		BranchID:         branchID,
		TabID:            tabID,
		Worktree:         t.TempDir(),
		NotifyURL:        notifyURL,
		NotifyToken:      token,
		HookBinPath:      hookBin,
		PermissionModeFn: func() string { return permissionMode },
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
		Argv []string          `json:"argv"`
		Env  map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("invocation JSON: %v\n%s", err, raw)
	}

	// Independently compute what the Adapter builds for the SAME intent
	// spawnWithArgs constructs for a fresh (non-respawn, no resume) host
	// spawn — see spawnWithArgs's own SpawnIntent construction.
	adapter := agent.NewClaudeAdapter(bin, extraArgs)
	wantSpec, err := adapter.SpawnSpec(agent.SpawnIntent{
		Worktree:        d.Worktree(),
		ResumeSessionID: "",
		InContainer:     false,
		Hook: agent.HookEnv{
			NotifyURL:   notifyURL,
			Token:       token,
			RepoID:      repoID,
			BranchID:    branchID,
			TabID:       tabID,
			HookBinPath: hookBin,
		},
		PermissionMode: permissionMode,
		IsRespawn:      false,
	})
	if err != nil {
		t.Fatalf("adapter.SpawnSpec: %v", err)
	}

	// Host-side argv[0] additionally goes through resolveClaudeBin — a no-op
	// here since bin is already an absolute path (contains a path
	// separator), so wantArgv == wantSpec.Argv unchanged. This mirrors
	// exactly what spawnWithArgs's host branch does:
	//   argv = append([]string{resolveClaudeBin(spec.Argv[0])}, spec.Argv[1:]...)
	wantArgv := append([]string{resolveClaudeBin(wantSpec.Argv[0])}, wantSpec.Argv[1:]...)

	if !reflect.DeepEqual(rec.Argv[1:], wantArgv[1:]) {
		t.Errorf("[AC-S0e8afb-2-3] host argv (flags) mismatch:\n  ptyhost-executed: %#v\n  adapter-predicted: %#v", rec.Argv[1:], wantArgv[1:])
	}
	// argv[0] is the fake_claude binary's own os.Args[0] as exec'd — must
	// equal the resolved bin path (proves ptyhost.Config.Argv[0] was handed
	// the adapter's (post-resolveClaudeBin) binary path unmodified).
	if len(rec.Argv) == 0 || rec.Argv[0] != wantArgv[0] {
		t.Errorf("[AC-S0e8afb-2-3] host argv[0] mismatch: got %v, want %q", rec.Argv, wantArgv[0])
	}

	// Env: every KEY=VALUE the adapter declared (hookEnv output) must be
	// observed in the actually-spawned process's environment.
	for _, kv := range wantSpec.Env {
		i := indexByte(kv, '=')
		if i < 0 {
			continue
		}
		key, val := kv[:i], kv[i+1:]
		if got := rec.Env[key]; got != val {
			t.Errorf("[AC-S0e8afb-2-3] host env %s = %q, want %q (adapter-declared)", key, got, val)
		}
	}
}

// TestGoldenArgv_InContainerSpawnMatchesAdapterSpec is the in-container
// half: spawnWithArgs must hand pc.PTYCommand EXACTLY spec.Argv (the design
// doc's literal "container: pc.PTYCommand(daemonCtx, spec.Argv, opts)"),
// verified here by a full (not spot-check) equality against the
// independently-computed agent.ClaudeAdapter.SpawnSpec output for an
// InContainer intent.
func TestGoldenArgv_InContainerSpawnMatchesAdapterSpec(t *testing.T) {
	bin := fakeBin(t)
	dump := filepath.Join(t.TempDir(), "invocation.json")
	fakeRT := &fakePTYRuntime{fakeBin: bin}
	extraArgs := []string{"--dump-invocation", dump}

	const (
		repoID, branchID, tabID = "golden-repo-c", "golden-branch-c", "claude"
		notifyURLInContainer    = "http://10.0.0.1:8080/api/notify"
		token                   = "golden-tok-c"
		permissionMode          = "acceptEdits"
	)
	// hookBinPath (host) is deliberately unreachable — spawnWithArgs must use
	// containerHookBinPath for the in-container branch instead, exactly as
	// it did before the graft.
	d := NewDaemon(DaemonConfig{
		ClaudeBin:            "/nonexistent/host/claude",
		ClaudeArgs:           extraArgs,
		RingSize:             1 << 16,
		ResumeOnDeath:        false,
		RepoID:               repoID,
		BranchID:             branchID,
		TabID:                tabID,
		Worktree:             t.TempDir(),
		NotifyURL:            "http://127.0.0.1:8080/api/notify", // host URL — must NOT be used in-container
		NotifyURLInContainer: notifyURLInContainer,
		NotifyToken:          token,
		HookBinPath:          "/host/only/palmux",
		PermissionModeFn:     func() string { return permissionMode },
		RuntimeResolver: func(_, _ string) runtime.PTYCommander {
			return fakeRT
		},
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	fakeRT.mu.Lock()
	gotArgv := append([]string(nil), fakeRT.argv...)
	fakeRT.mu.Unlock()

	adapter := agent.NewClaudeAdapter("/nonexistent/host/claude", extraArgs)
	wantSpec, err := adapter.SpawnSpec(agent.SpawnIntent{
		Worktree:        d.Worktree(),
		ResumeSessionID: "",
		InContainer:     true,
		Hook: agent.HookEnv{
			NotifyURL:   notifyURLInContainer,
			Token:       token,
			RepoID:      repoID,
			BranchID:    branchID,
			TabID:       tabID,
			HookBinPath: containerHookBinPath,
		},
		PermissionMode: permissionMode,
		IsRespawn:      false,
	})
	if err != nil {
		t.Fatalf("adapter.SpawnSpec: %v", err)
	}

	// pc.PTYCommand must have been handed spec.Argv OPAQUELY and VERBATIM —
	// full equality, not a spot-check (fakePTYRuntime.PTYCommand records
	// exactly the argv it was called with).
	if !reflect.DeepEqual(gotArgv, wantSpec.Argv) {
		t.Errorf("[AC-S0e8afb-2-3] in-container argv mismatch:\n  handed to PTYCommand: %#v\n  adapter-predicted spec.Argv: %#v", gotArgv, wantSpec.Argv)
	}
	if gotArgv[0] != containerClaudeBin {
		t.Errorf("[AC-S0e8afb-2-3] in-container argv[0] = %q, want %q", gotArgv[0], containerClaudeBin)
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
