package agenttui

import (
	"bytes"
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// fakeReadyCommander is a runtime.PTYCommander whose readiness can be flipped at
// runtime, used to exercise the gateRespawn container-readiness gate.
type fakeReadyCommander struct {
	mu    sync.Mutex
	state runtime.State
}

func (f *fakeReadyCommander) setState(s runtime.State) {
	f.mu.Lock()
	f.state = s
	f.mu.Unlock()
}

func (f *fakeReadyCommander) Status() runtime.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return runtime.Status{State: f.state}
}

func (f *fakeReadyCommander) PTYCommand(ctx context.Context, argv []string, _ runtime.PTYCommandOpts) *exec.Cmd {
	return exec.CommandContext(ctx, argv[0], argv[1:]...)
}

// TestGateRespawnWaitsForContainerReady verifies that during an incus container
// regenerate (runtime not StateReady) gateRespawn blocks — so the respawn loop
// does NOT re-exec `incus exec` and spam "Instance is not running" — and resumes
// promptly once the container is ready again. It also asserts the one-time
// "container is restarting" status line is written to the ring instead.
func TestGateRespawnWaitsForContainerReady(t *testing.T) {
	fc := &fakeReadyCommander{state: runtime.StateStopped}
	d := NewDaemon(DaemonConfig{
		ClaudeBin:       "true",
		RingSize:        1 << 16,
		RuntimeResolver: func(_, _ string) runtime.PTYCommander { return fc },
	})
	t.Cleanup(func() { d.Shutdown() })

	done := make(chan bool, 1)
	go func() { done <- d.gateRespawn() }()

	// Must NOT return while the container is not ready.
	select {
	case <-done:
		t.Fatal("gateRespawn returned while runtime was not ready (would re-exec into a dead container)")
	case <-time.After(1500 * time.Millisecond):
	}

	// The user should see a single status line, not error spam.
	if !bytes.Contains(d.ring.Bytes(), []byte("container is restarting")) {
		t.Errorf("expected 'container is restarting' notice in ring; got %q", d.ring.Bytes())
	}

	// Container comes back → gateRespawn returns true promptly.
	fc.setState(runtime.StateReady)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("gateRespawn returned false (shutdown) but only readiness changed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("gateRespawn did not resume after the container became ready")
	}
}

// TestGateRespawnHostIsNoop verifies host runtime (nil resolver) makes
// gateRespawn an immediate no-op — respawn timing is unchanged off-incus.
func TestGateRespawnHostIsNoop(t *testing.T) {
	d := NewDaemon(DaemonConfig{ClaudeBin: "true", RingSize: 1 << 16}) // nil RuntimeResolver
	t.Cleanup(func() { d.Shutdown() })

	start := time.Now()
	if !d.gateRespawn() {
		t.Fatal("host gateRespawn should return true")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("host gateRespawn should be immediate, took %v", elapsed)
	}
}

// TestGateRespawnShutdownDuringWait verifies a shutdown signal unblocks
// gateRespawn (returns false) even while the container is stuck not-ready.
func TestGateRespawnShutdownDuringWait(t *testing.T) {
	fc := &fakeReadyCommander{state: runtime.StateStopped}
	d := NewDaemon(DaemonConfig{
		ClaudeBin:       "true",
		RingSize:        1 << 16,
		RuntimeResolver: func(_, _ string) runtime.PTYCommander { return fc },
	})

	done := make(chan bool, 1)
	go func() { done <- d.gateRespawn() }()

	time.Sleep(200 * time.Millisecond)
	d.Shutdown() // closes shutdownCh

	select {
	case ok := <-done:
		if ok {
			t.Fatal("gateRespawn should return false on shutdown while waiting")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("gateRespawn did not return on shutdown")
	}
}
