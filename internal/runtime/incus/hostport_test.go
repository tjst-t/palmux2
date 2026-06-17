// Package incus — host-port publishing unit tests (S4c591a).
// Uses a fake runner + an injected host-port probe so no real incus / sockets
// are touched. Asserts: mode selection, proxy-device add/remove arg sequences,
// and collision-driven auto-reassignment.
// [AC-S4c591a-1-1] [AC-S4c591a-1-2] [AC-S4c591a-1-3] [AC-S4c591a-1-4]
package incus

import (
	"context"
	"strings"
	"testing"

	"github.com/tjst-t/palmux2/internal/runtime"
)

func newHostPortRuntime(t *testing.T, fr *fakeRunner) *incusRuntime {
	t.Helper()
	inst := "ws-hostport-ab12cd34"
	// list (containerIP) → return a bridge IP so ExposePort uses it.
	fr.setResult("list "+inst, fakeResult{stdout: `[{"state":{"network":{"eth0":{"addresses":[{"family":"inet","address":"10.20.30.40"}]}}}}]`, code: 0})
	rt := New(runtime.Config{Kind: runtime.KindIncusContainer, Image: "palmux-ws"}, inst, fr.asRunner(), nil)
	ir, ok := rt.(*incusRuntime)
	if !ok {
		t.Fatalf("expected *incusRuntime")
	}
	// pub == nil → HostPortMode() true (no public domain configured).
	return ir
}

// [AC-S4c591a-1-1] [AC-S4c591a-1-4] mode selection: no public domain → host-port mode.
func TestHostPortMode_NoPublicDomain(t *testing.T) {
	ir := newHostPortRuntime(t, newFakeRunner())
	if !ir.HostPortMode() {
		t.Fatalf("expected HostPortMode()=true when pub is nil")
	}
	// With a configured public domain, host-port mode is OFF (subdomain mode).
	ir.pub = &publishConfig{baseDomain: "example.test", caddyAdmin: "http://localhost:2019"}
	if ir.HostPortMode() {
		t.Fatalf("expected HostPortMode()=false when public domain configured")
	}
}

// [AC-S4c591a-1-1] expose adds an incus proxy device listen=tcp:0.0.0.0:<hostPort>.
func TestExposePortHost_AddsProxyDevice(t *testing.T) {
	fr := newFakeRunner()
	ir := newHostPortRuntime(t, fr)
	// Force the container IP resolution path.
	ir.setStatus(runtime.Status{State: runtime.StateReady, Address: "10.20.30.40"})

	// All ports free.
	old := hostPortListenProbe
	hostPortListenProbe = func(int) bool { return true }
	defer func() { hostPortListenProbe = old }()

	url, err := ir.ExposePortHost(context.Background(), 5173)
	if err != nil {
		t.Fatalf("ExposePortHost: %v", err)
	}
	if url == "" || url[:7] != "http://" {
		t.Fatalf("expected http:// host URL, got %q", url)
	}

	calls := fr.recorded()
	dev, ok := findCall(calls, "config", "device", "add", ir.inst)
	if !ok {
		t.Fatalf("expected 'incus config device add %s ...', calls=%v", ir.inst, calls)
	}
	var listen, connect string
	for _, a := range dev {
		if v, ok := strings.CutPrefix(a, "listen="); ok {
			listen = v
		}
		if v, ok := strings.CutPrefix(a, "connect="); ok {
			connect = v
		}
	}
	if listen != "tcp:0.0.0.0:5173" {
		t.Errorf("listen mismatch: got %q want tcp:0.0.0.0:5173", listen)
	}
	// connect must target the CONTAINER bridge IP (NOT 127.0.0.1 — incus
	// forkproxy runs in host netns). PD-2 in decisions.json.
	if connect != "tcp:10.20.30.40:5173" {
		t.Errorf("connect mismatch: got %q want tcp:10.20.30.40:5173", connect)
	}

	// PortsView reflects the host publish.
	pv := ir.PortViewFor(5173)
	if pv == nil || !pv.HostPublished || pv.HostPort != 5173 {
		t.Errorf("PortsView not updated for host publish: %+v", pv)
	}
}

// [AC-S4c591a-1-2] collision: container port busy on host → auto-reassign.
func TestExposePortHost_CollisionReassign(t *testing.T) {
	fr := newFakeRunner()
	ir := newHostPortRuntime(t, fr)
	ir.setStatus(runtime.Status{State: runtime.StateReady, Address: "10.20.30.40"})

	old := hostPortListenProbe
	// 6006 busy, 16006 free.
	hostPortListenProbe = func(p int) bool { return p != 6006 }
	defer func() { hostPortListenProbe = old }()

	url, err := ir.ExposePortHost(context.Background(), 6006)
	if err != nil {
		t.Fatalf("ExposePortHost: %v", err)
	}
	if url[len(url)-6:] != ":16006" {
		t.Errorf("expected reassigned host port 16006 in URL, got %q", url)
	}
	pv := ir.PortViewFor(6006)
	if pv == nil || pv.HostPort != 16006 {
		t.Errorf("expected HostPort=16006 (reassigned), got %+v", pv)
	}
}

// [AC-S4c591a-1-2] unexpose removes the proxy device.
func TestUnexposePortHost_RemovesProxyDevice(t *testing.T) {
	fr := newFakeRunner()
	ir := newHostPortRuntime(t, fr)
	ir.setStatus(runtime.Status{State: runtime.StateReady, Address: "10.20.30.40"})
	old := hostPortListenProbe
	hostPortListenProbe = func(int) bool { return true }
	defer func() { hostPortListenProbe = old }()

	if _, err := ir.ExposePortHost(context.Background(), 8080); err != nil {
		t.Fatalf("ExposePortHost: %v", err)
	}
	if err := ir.UnexposePortHost(context.Background(), 8080); err != nil {
		t.Fatalf("UnexposePortHost: %v", err)
	}
	calls := fr.recorded()
	if _, ok := findCall(calls, "config", "device", "remove", ir.inst); !ok {
		t.Errorf("expected 'incus config device remove %s ...', calls=%v", ir.inst, calls)
	}
	if pv := ir.PortViewFor(8080); pv != nil && pv.HostPublished {
		t.Errorf("expected port 8080 not host-published after unexpose, got %+v", pv)
	}
	// Idempotent: second unexpose is a no-op (no error).
	if err := ir.UnexposePortHost(context.Background(), 8080); err != nil {
		t.Errorf("second UnexposePortHost should be no-op, got %v", err)
	}
}

func TestHostIP_NonEmpty(t *testing.T) {
	if ip := HostIP(); ip == "" {
		t.Fatalf("HostIP returned empty string")
	}
}
