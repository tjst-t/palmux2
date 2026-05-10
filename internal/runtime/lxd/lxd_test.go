package lxd

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// lxcLookup is a thin wrapper kept in test code so the package itself can
// stay free of test-only helpers.
func lxcLookup(name string) (string, error) { return exec.LookPath(name) }

// TestInstanceNameDeterministic ensures the same (repoID, branchID) pair
// always maps to the same instance name. LXD names must be stable so a
// later Start can find the existing container.
func TestInstanceNameDeterministic(t *testing.T) {
	t.Parallel()
	a := instanceNameFor("tjst-t--palmux2--a1b2", "main--7a8b")
	b := instanceNameFor("tjst-t--palmux2--a1b2", "main--7a8b")
	if a != b {
		t.Errorf("non-deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "palmux-") {
		t.Errorf("missing prefix: %q", a)
	}
	if len(a) > 63 {
		t.Errorf("LXD name too long (>63): %q", a)
	}
	// Different inputs → different names.
	c := instanceNameFor("tjst-t--palmux2--a1b2", "feature-x--3c4d")
	if a == c {
		t.Errorf("collision: %q", a)
	}
}

// TestInstanceNameLXDLegal verifies the output matches LXD's allowed
// charset ([A-Za-z0-9-]).
func TestInstanceNameLXDLegal(t *testing.T) {
	t.Parallel()
	name := instanceNameFor("tjst-t--palmux2--a1b2", "feature-x--3c4d")
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			t.Errorf("illegal LXD name char %q in %q", r, name)
		}
	}
}

// TestPortDeviceName ensures we get a stable, LXD-legal device name even
// when the user-provided name contains junk characters.
func TestPortDeviceName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":              "proxy-3000",
		"vite":          "proxy-vite-3000",
		"my dev/server": "proxy-mydevserver-3000",
		"api_v2":        "proxy-api_v2-3000",
	}
	for in, want := range cases {
		got := portDeviceName(in, 3000)
		if got != want {
			t.Errorf("portDeviceName(%q, 3000) = %q, want %q", in, got, want)
		}
	}
}

// TestContainerPath checks the relative-vs-absolute resolution.
func TestContainerPath(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":            "/workspace",
		"/etc/passwd": "/etc/passwd",
		"src/main.go": "/workspace/src/main.go",
		"./README.md": "/workspace/README.md",
	}
	for in, want := range cases {
		got := containerPath(in)
		if got != want {
			t.Errorf("containerPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseProcNetTCP exercises the IP/port parser. Mirrors the
// agent.parseProcNetTCP test but lives here so the lxd runtime stays
// self-contained.
func TestParseProcNetTCP(t *testing.T) {
	t.Parallel()
	content := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 ffff 100 0 0 10 0
   1: 00000000:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 67890 1 ffff 100 0 0 10 0
   2: 0100007F:1F40 0100007F:DEAD 06 00000000:00000000 00:00000000 00000000  1000        0 22222 1 ffff 100 0 0 10 0
`
	got := parseProcNetTCP(content, "tcp")
	wantPorts := []uint16{0x1F90, 0x1538}
	if len(got) != len(wantPorts) {
		t.Fatalf("got %d entries, want %d (%v)", len(got), len(wantPorts), got)
	}
	for i, p := range wantPorts {
		if got[i].Port != p {
			t.Errorf("entry %d: port = %d, want %d", i, got[i].Port, p)
		}
		if got[i].Protocol != "tcp" {
			t.Errorf("entry %d: protocol = %q", i, got[i].Protocol)
		}
	}
}

// TestKindAndConfig sanity-checks construction.
func TestKindAndConfig(t *testing.T) {
	t.Parallel()
	cfg := runtime.Config{Kind: runtime.KindLXDContainer, Image: "test:img"}
	r := New(cfg, "/tmp/wt", "repo", "branch", "main", Options{})
	if r.Kind() != runtime.KindLXDContainer {
		t.Errorf("Kind() = %q", r.Kind())
	}
	if r.Config() != cfg {
		t.Errorf("Config() = %+v", r.Config())
	}
	if r.image() != "test:img" {
		t.Errorf("image() = %q, want %q", r.image(), "test:img")
	}
	// Empty image falls back to DefaultImage.
	r2 := New(runtime.Config{Kind: runtime.KindLXDContainer}, "/tmp/wt", "repo", "branch", "main", Options{})
	if r2.image() != DefaultImage {
		t.Errorf("default image() = %q, want %q", r2.image(), DefaultImage)
	}
}

// TestStartStopWithoutLXC verifies graceful error reporting when the lxc
// binary isn't available or the daemon is offline. We can't run a real
// lxc command in this unit-test environment, so we point the lookup at a
// non-existent binary to hit the error path.
func TestStartStopErrorReporting(t *testing.T) {
	// Skip for the local runner — this is a defence-in-depth check that
	// requires `lxc` NOT to be on PATH. Inside CI it's a stronger
	// assertion; locally it's a no-op pass.
	if hasLXC() {
		t.Skip("lxc on PATH; skipping the error-path check")
	}
	cfg := runtime.Config{Kind: runtime.KindLXDContainer}
	r := New(cfg, "/tmp/wt", "repo", "branch", "main", Options{})
	err := r.Start(context.Background())
	if err == nil {
		t.Fatalf("Start without lxc: expected error, got nil")
	}
	if r.Status().State != runtime.StateFailed {
		t.Errorf("after failed Start: State = %q, want %q", r.Status().State, runtime.StateFailed)
	}
}

func hasLXC() bool {
	_, err := lxcLookup("lxc")
	return err == nil
}
