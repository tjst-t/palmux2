// [AC-S8478ca-1-2]
package host_test

import (
	"context"
	"testing"

	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/runtime/host"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// newTestHost builds a host Runtime around a fresh tmux.MockClient.
func newTestHost() (runtime.Runtime, *tmux.MockClient) {
	mock := tmux.NewMockClient()
	rt := host.NewHost(mock)
	return rt, mock
}

// TestHostStatusReady asserts that a freshly-created host runtime reports
// State=ready and Address=localhost without any Start call.
// [AC-S8478ca-1-2]
func TestHostStatusReady(t *testing.T) {
	rt, _ := newTestHost()
	s := rt.Status()
	if s.State != runtime.StateReady {
		t.Errorf("Status.State = %q, want %q", s.State, runtime.StateReady)
	}
	if s.Address != "localhost" {
		t.Errorf("Status.Address = %q, want \"localhost\"", s.Address)
	}
	if s.Error != "" {
		t.Errorf("Status.Error = %q, want empty", s.Error)
	}
}

// TestHostKind asserts Kind() == KindHost.
// [AC-S8478ca-1-2]
func TestHostKind(t *testing.T) {
	rt, _ := newTestHost()
	if rt.Kind() != runtime.KindHost {
		t.Errorf("Kind() = %q, want %q", rt.Kind(), runtime.KindHost)
	}
}

// TestHostConfig asserts Config().Kind == KindHost and no image.
// [AC-S8478ca-1-2]
func TestHostConfig(t *testing.T) {
	rt, _ := newTestHost()
	cfg := rt.Config()
	if cfg.Kind != runtime.KindHost {
		t.Errorf("Config().Kind = %q, want %q", cfg.Kind, runtime.KindHost)
	}
	if cfg.Image != "" {
		t.Errorf("Config().Image = %q, want empty", cfg.Image)
	}
}

// TestHostStartStopNoOp asserts that Start and Stop are no-ops (no error).
// [AC-S8478ca-1-2]
func TestHostStartStopNoOp(t *testing.T) {
	rt, _ := newTestHost()
	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Errorf("Start: unexpected error: %v", err)
	}
	if err := rt.Stop(ctx); err != nil {
		t.Errorf("Stop: unexpected error: %v", err)
	}
	// Status is still ready after stop (host never actually stops).
	if s := rt.Status(); s.State != runtime.StateReady {
		t.Errorf("Status after Stop: %q, want ready", s.State)
	}
}

// TestHostNewTmuxSessionDelegates asserts that NewTmuxSession delegates to the
// injected tmux.Client by recording a NewSession call.
// [AC-S8478ca-1-2]
func TestHostNewTmuxSessionDelegates(t *testing.T) {
	rt, mock := newTestHost()
	ctx := context.Background()

	const session = "_pmx_dev__host--0000_host"
	if err := rt.NewTmuxSession(ctx, session); err != nil {
		t.Fatalf("NewTmuxSession: %v", err)
	}
	calls := mock.Calls()
	found := false
	for _, c := range calls {
		if len(c) > 10 && c[:10] == "NewSession" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a NewSession call, got calls: %v", calls)
	}
	// The session should actually exist in the mock.
	exists, err := mock.HasSession(ctx, session)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !exists {
		t.Error("session not present in mock after NewTmuxSession")
	}
}

// TestHostExecCaptures asserts that Exec runs a host command and captures output.
// [AC-S8478ca-1-2]
func TestHostExecCaptures(t *testing.T) {
	rt, _ := newTestHost()
	ctx := context.Background()

	res, err := rt.Exec(ctx, []string{"echo", "hello-from-host"}, runtime.ExecOpts{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	want := "hello-from-host\n"
	if res.Stdout != want {
		t.Errorf("Stdout = %q, want %q", res.Stdout, want)
	}
}

// TestHostExecNonZeroExit asserts that a failing command produces ExitCode != 0
// but no Go error (non-zero exit is a result, not a failure at the Go level).
// [AC-S8478ca-1-2]
func TestHostExecNonZeroExit(t *testing.T) {
	rt, _ := newTestHost()
	ctx := context.Background()

	res, err := rt.Exec(ctx, []string{"false"}, runtime.ExecOpts{})
	if err != nil {
		t.Fatalf("Exec with 'false': unexpected Go error: %v", err)
	}
	if res.ExitCode == 0 {
		t.Error("ExitCode = 0, want non-zero for 'false'")
	}
}

// TestHostExposePortStub asserts that ExposePort returns a PortMapping that
// echoes Proto and Public from the spec (stub, not nil).
// [AC-S8478ca-1-2]
func TestHostExposePortStub(t *testing.T) {
	rt, _ := newTestHost()
	ctx := context.Background()

	spec := runtime.PortSpec{
		Internal: 3000,
		Proto:    "tcp",
		Name:     "dev-server",
		Public:   false,
		HostPort: 0,
	}
	m, err := rt.ExposePort(ctx, spec)
	if err != nil {
		t.Fatalf("ExposePort: %v", err)
	}
	if m.Proto != "tcp" {
		t.Errorf("PortMapping.Proto = %q, want \"tcp\"", m.Proto)
	}
	if m.Internal != 3000 {
		t.Errorf("PortMapping.Internal = %d, want 3000", m.Internal)
	}
	if m.ID == "" {
		t.Error("PortMapping.ID must not be empty")
	}
}

// TestHostExposePortUDPPublic mirrors scenario-1: ExposePort accepts
// Proto:"udp", Public:true and returns a matching mapping.
// [AC-S8478ca-1-2]
func TestHostExposePortUDPPublic(t *testing.T) {
	rt, _ := newTestHost()
	ctx := context.Background()

	spec := runtime.PortSpec{
		Internal: 5004,
		Proto:    "udp",
		Name:     "webrtc",
		Public:   true,
		HostPort: 49152,
	}
	m, err := rt.ExposePort(ctx, spec)
	if err != nil {
		t.Fatalf("ExposePort udp/public: %v", err)
	}
	if m.Proto != "udp" {
		t.Errorf("Proto = %q, want \"udp\"", m.Proto)
	}
	if !m.Public {
		t.Error("Public = false, want true")
	}
}

// TestHostUnexposePortStub asserts UnexposePort is a no-op (no error).
// [AC-S8478ca-1-2]
func TestHostUnexposePortStub(t *testing.T) {
	rt, _ := newTestHost()
	ctx := context.Background()
	if err := rt.UnexposePort(ctx, "host-tcp-3000"); err != nil {
		t.Errorf("UnexposePort: unexpected error: %v", err)
	}
}

// TestHostListListeningPortsEmpty asserts that the host stub returns nil/empty.
// [AC-S8478ca-1-2]
func TestHostListListeningPortsEmpty(t *testing.T) {
	rt, _ := newTestHost()
	ports, err := rt.ListListeningPorts(context.Background())
	if err != nil {
		t.Fatalf("ListListeningPorts: %v", err)
	}
	if len(ports) != 0 {
		t.Errorf("expected empty ports list, got %v", ports)
	}
}

// TestDefaultRegistryAlwaysReturnsHost asserts that DefaultRegistry.Get always
// returns a non-nil host Runtime regardless of the workspace IDs.
// [AC-S8478ca-1-2]
func TestDefaultRegistryAlwaysReturnsHost(t *testing.T) {
	mock := tmux.NewMockClient()
	reg := host.NewDefaultRegistry(mock)

	cases := [][2]string{
		{"repo-a--1234", "main--ab12"},
		{"", ""},
		{"host--0000", "host"},
	}
	for _, c := range cases {
		rt := reg.Get(c[0], c[1])
		if rt == nil {
			t.Errorf("Get(%q,%q) returned nil", c[0], c[1])
			continue
		}
		if rt.Kind() != runtime.KindHost {
			t.Errorf("Get(%q,%q).Kind() = %q, want host", c[0], c[1], rt.Kind())
		}
		if s := rt.Status(); s.State != runtime.StateReady {
			t.Errorf("Get(%q,%q).Status().State = %q, want ready", c[0], c[1], s.State)
		}
	}
}
