package agenttui

import (
	"context"
	"os"
	"os/exec"
	"sync"

	wsruntime "github.com/tjst-t/palmux2/internal/runtime"
)

// This file holds test doubles/helpers shared across MULTIPLE _test.go files
// in this package (fakePTYRuntime is used by both the in-container spawn
// tests and shutdown_reap_test.go's reap-wiring tests). It used to live in
// claudetui/hooks_test.go alongside the inline-claude-arg-builder tests that
// S0e8afb-2's P2 graft superseded (that logic, and its own unit tests, moved
// verbatim to internal/agent/claude.go + claude_test.go + claude_golden_test.go
// — see docs/sprint-logs/S0e8afb/verification-S0e8afb-2.md). These shared
// helpers are still needed here because in-container PTYCommander WIRING
// (spawnWithArgs handing spec.Argv to pc.PTYCommand) is still this package's
// responsibility, not the Adapter's.

// fakePTYRuntime implements runtime.PTYCommander for the in-container spawn
// tests. It substitutes the (container) claude bin in argv[0] with the test
// fake bin so the daemon's spawn actually runs, and records the argv/env/cwd.
type fakePTYRuntime struct {
	fakeBin string
	mu      sync.Mutex
	argv    []string
	env     []string
	cwd     string
}

func (f *fakePTYRuntime) PTYCommand(ctx context.Context, argv []string, opts wsruntime.PTYCommandOpts) *exec.Cmd {
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

// hasArg reports whether want appears anywhere in argv.
func hasArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

// hasArgPair reports whether flag is immediately followed by val in argv.
func hasArgPair(argv []string, flag, val string) bool {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) && argv[i+1] == val {
			return true
		}
	}
	return false
}
