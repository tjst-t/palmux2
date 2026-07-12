package incus

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// [AC-Sd44947-1-1] Ensure creates the profile when it does not exist and adds
// the declared shared devices for sources that exist on the host.
func TestSharedProfile_EnsureCreatesAndPopulates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Create a couple of real sources so declaredDevices includes them.
	_ = os.MkdirAll(filepath.Join(home, "ghq"), 0o755)
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)

	fr := newFakeRunner()
	// profile show → not found the first time so Ensure creates it.
	fr.setResult("profile show", fakeResult{stderr: "Error: not found", code: 1})

	m := NewSharedProfileManager(fr.asRunner(), nil, nil)
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	calls := fr.recorded()

	if _, ok := findCall(calls, "profile", "create", SharedProfileName); !ok {
		t.Errorf("[AC-Sd44947-1-1] expected 'profile create %s' when absent, got %v", SharedProfileName, calls)
	}
	for _, name := range []string{"ghq", "dot-claude"} {
		if _, ok := findCall(calls, "profile", "device", "add", SharedProfileName, name, "disk"); !ok {
			t.Errorf("[AC-Sd44947-1-1] expected profile device add for %q, got %v", name, calls)
		}
	}
}

// [AC-Sd44947-1-2] Reconcile restores a hand-stripped device on the next tick:
// when `profile show` reports the profile is MISSING ghq (drift), Ensure re-adds
// it (converges to the declaration).
func TestSharedProfile_ReconcileRestoresStrippedDevice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, "ghq"), 0o755)
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)

	ghqSrc := filepath.Join(home, "ghq")
	claudeSrc := filepath.Join(home, ".claude")

	fr := newFakeRunner()
	// Profile exists but ghq was removed by hand — only dot-claude remains.
	yaml := "name: " + SharedProfileName + "\n" +
		"devices:\n" +
		"  dot-claude:\n" +
		"    type: disk\n" +
		"    source: " + claudeSrc + "\n" +
		"    path: " + claudeSrc + "\n" +
		"used_by: []\n"
	fr.setResult("profile show", fakeResult{stdout: yaml, code: 0})

	m := NewSharedProfileManager(fr.asRunner(), nil, nil)
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	calls := fr.recorded()

	// ghq must be re-added (converge to declaration).
	if _, ok := findCall(calls, "profile", "device", "add", SharedProfileName, "ghq", "disk", "source="+ghqSrc, "path="+ghqSrc); !ok {
		t.Errorf("[AC-Sd44947-1-2] expected ghq re-added on reconcile, got %v", calls)
	}
	// dot-claude already present + matching → must NOT be re-added.
	if _, ok := findCall(calls, "profile", "device", "add", SharedProfileName, "dot-claude"); ok {
		t.Errorf("[AC-Sd44947-1-2] dot-claude already converged, should not re-add: %v", calls)
	}
}

// [AC-Sd44947-1-2] Reconcile removes a stale device present in the profile but
// not in the declaration (e.g. a shared_dir the user removed).
func TestSharedProfile_ReconcileRemovesStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, "ghq"), 0o755)

	fr := newFakeRunner()
	yaml := "name: " + SharedProfileName + "\n" +
		"devices:\n" +
		"  sf-deadbeef1234:\n" +
		"    type: disk\n" +
		"    source: /home/ubuntu/.gone\n" +
		"    path: /home/ubuntu/.gone\n" +
		"used_by: []\n"
	fr.setResult("profile show", fakeResult{stdout: yaml, code: 0})

	m := NewSharedProfileManager(fr.asRunner(), nil, nil)
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, ok := findCall(fr.recorded(), "profile", "device", "remove", SharedProfileName, "sf-deadbeef1234"); !ok {
		t.Errorf("[AC-Sd44947-1-2] expected stale device removed, got %v", fr.recorded())
	}
}

// [AC-Sd44947-2-1] SetSharedDirs makes declaredDevices include the user folder
// (as an sf-<hash> device) when the source exists.
func TestSharedProfile_SharedDirsIncluded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	infPath := filepath.Join(home, ".infisical")
	_ = os.MkdirAll(infPath, 0o755)

	m := NewSharedProfileManager(newFakeRunner().asRunner(), nil, []string{infPath})
	devs := m.declaredDevices()
	want := sharedDirDeviceName(infPath)
	found := false
	for _, d := range devs {
		if d.name == want {
			found = true
			if d.source != infPath || d.path != infPath {
				t.Errorf("shared dir device source/path = %s/%s, want %s", d.source, d.path, infPath)
			}
		}
	}
	if !found {
		t.Errorf("[AC-Sd44947-2-1] expected shared dir device %q in %v", want, devs)
	}

	// A non-existent shared dir is skipped (not an error).
	m2 := NewSharedProfileManager(newFakeRunner().asRunner(), nil, []string{filepath.Join(home, ".nope")})
	for _, d := range m2.declaredDevices() {
		if strings.HasPrefix(d.name, "sf-") {
			t.Errorf("[AC-Sd44947-2-3] absent shared dir should be skipped, got %v", d)
		}
	}
}

// SetWorktreeBasedirFunc makes declaredDevices include the gwq worktree base dir
// as a same-path bind-mount, so a Claude/Bash tab opened on a linked (gwq)
// worktree finds its cwd inside the container. The base dir may live OUTSIDE
// ~/ghq (default ~/worktrees), so this also guards the symlink-skip exemption.
func TestSharedProfile_GwqWorktreesIncluded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	basedir := filepath.Join(home, "worktrees")
	_ = os.MkdirAll(basedir, 0o755)

	m := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	m.SetWorktreeBasedirFunc(func(context.Context) (string, error) { return basedir, nil })

	found := false
	for _, d := range m.declaredDevices() {
		if d.name == gwqWorktreesDevice {
			found = true
			if d.source != basedir || d.path != basedir {
				t.Errorf("gwq worktrees device source/path = %s/%s, want %s", d.source, d.path, basedir)
			}
		}
	}
	if !found {
		t.Errorf("expected gwq worktrees device %q in declaredDevices", gwqWorktreesDevice)
	}

	// Caching: the resolver must be consulted once, then the cached value reused.
	calls := 0
	m.resolvedBasedir = "" // reset cache primed by the first declaredDevices above
	m.SetWorktreeBasedirFunc(func(context.Context) (string, error) { calls++; return basedir, nil })
	_ = m.declaredDevices()
	_ = m.declaredDevices()
	if calls != 1 {
		t.Errorf("resolver called %d times, want 1 (cached)", calls)
	}

	// No resolver → device absent (feature off, existing hosts unaffected).
	m2 := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	for _, d := range m2.declaredDevices() {
		if d.name == gwqWorktreesDevice {
			t.Errorf("no resolver should omit the gwq worktrees device, got %v", d)
		}
	}

	// basedir == ~/ghq must NOT add a duplicate-path device (incus error).
	ghq := filepath.Join(home, "ghq")
	_ = os.MkdirAll(ghq, 0o755)
	m3 := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	m3.SetWorktreeBasedirFunc(func(context.Context) (string, error) { return ghq, nil })
	for _, d := range m3.declaredDevices() {
		if d.name == gwqWorktreesDevice {
			t.Errorf("basedir coinciding with ~/ghq must be skipped (duplicate path), got %v", d)
		}
	}
}

// SetAttachmentDir makes declaredDevices include the attachment upload root as a
// same-path bind-mount, so Ctrl+V-pasted images (saved on the host) are readable
// by in-container Claude at the exact absolute path the composer injects. The root
// lives OUTSIDE $HOME (default /tmp/palmux-uploads), so this also guards that it
// is exempt from the outside-$HOME symlink-skip (regression: without the exemption
// declaredDevices would drop it).
func TestSharedProfile_AttachmentDirIncluded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A dir outside HOME, mirroring the /tmp/palmux-uploads default.
	attach := filepath.Join(t.TempDir(), "palmux-uploads")
	if err := os.MkdirAll(attach, 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	m.SetAttachmentDir(attach + "/") // trailing slash must be trimmed
	found := false
	for _, d := range m.declaredDevices() {
		if d.name == attachmentDevice {
			found = true
			if d.source != attach || d.path != attach {
				t.Errorf("attachment device source/path = %s/%s, want %s (same path, no trailing slash)", d.source, d.path, attach)
			}
		}
	}
	if !found {
		t.Errorf("expected attachment device %q (outside-$HOME dir must survive the symlink-skip)", attachmentDevice)
	}

	// Absent root is skipped (not an error); empty disables the mount entirely.
	m2 := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	m2.SetAttachmentDir(filepath.Join(t.TempDir(), "does-not-exist"))
	for _, d := range m2.declaredDevices() {
		if d.name == attachmentDevice {
			t.Errorf("absent attachment root should be skipped, got %v", d)
		}
	}
	m3 := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	for _, d := range m3.declaredDevices() {
		if d.name == attachmentDevice {
			t.Errorf("empty attachment root should add no device, got %v", d)
		}
	}
}

// parseProfileDevices extracts name/source/path triples and ignores non-disk
// keys and the used_by block.
func TestParseProfileDevices(t *testing.T) {
	yaml := `name: palmux-shared
description: managed
config: {}
devices:
  ghq:
    type: disk
    source: /home/ubuntu/ghq
    path: /home/ubuntu/ghq
  dot-ssh:
    type: disk
    source: /home/ubuntu/.ssh
    path: /home/ubuntu/.ssh
used_by:
- /1.0/instances/ws-a
- /1.0/instances/ws-b
`
	got := parseProfileDevices(yaml)
	if len(got) != 2 {
		t.Fatalf("expected 2 devices, got %d: %v", len(got), got)
	}
	if got["ghq"].source != "/home/ubuntu/ghq" || got["ghq"].path != "/home/ubuntu/ghq" {
		t.Errorf("ghq device parsed wrong: %+v", got["ghq"])
	}
	if got["dot-ssh"].source != "/home/ubuntu/.ssh" {
		t.Errorf("dot-ssh source parsed wrong: %+v", got["dot-ssh"])
	}
}

// UsedByCount counts the used_by list entries.
func TestSharedProfile_UsedByCount(t *testing.T) {
	fr := newFakeRunner()
	fr.setResult("profile show", fakeResult{stdout: `name: palmux-shared
devices: {}
used_by:
- /1.0/instances/ws-a
- /1.0/instances/ws-b
- /1.0/instances/ws-c
`, code: 0})
	m := NewSharedProfileManager(fr.asRunner(), nil, nil)
	if n := m.UsedByCount(context.Background()); n != 3 {
		t.Errorf("UsedByCount = %d, want 3", n)
	}
}

// [AC-Sd44947-1-3] Migration: Start applies the shared profile to the instance
// AND strips the legacy per-container shared devices so the profile's copies win
// (no-downtime migration of a pre-Sd44947 container).
func TestStart_MigratesLegacyDevices(t *testing.T) {
	inst := "ws-migrate-12ab34cd"
	fr := newFakeRunner()
	fr.setResult("exec "+inst, fakeResult{code: 0})
	fr.setResult("list "+inst, fakeResult{stdout: "[]", code: 0})

	rt := newIncusRTWithShared(runtime.Config{Kind: runtime.KindIncusContainer, Image: "palmux-ws"}, inst, fr)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	calls := fr.recorded()

	// The instance references the shared profile.
	if _, ok := findCall(calls, "profile", "add", inst, SharedProfileName); !ok {
		t.Errorf("[AC-Sd44947-1-3] expected 'profile add %s %s', got %v", inst, SharedProfileName, calls)
	}
	// Legacy instance-local shared devices are removed (migration).
	for _, name := range []string{"ghq", "dot-claude", "palmux-hook-bin"} {
		if _, ok := findCall(calls, "config", "device", "remove", inst, name); !ok {
			t.Errorf("[AC-Sd44947-1-3] expected legacy 'config device remove %s %s', got %v", inst, name, calls)
		}
	}
}

// [AC-Sd44947-1-1] InstanceProfiles is default + palmux-shared.
func TestSharedProfile_InstanceProfiles(t *testing.T) {
	m := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	got := m.InstanceProfiles()
	if len(got) != 2 || got[0] != "default" || got[1] != SharedProfileName {
		t.Errorf("InstanceProfiles = %v, want [default %s]", got, SharedProfileName)
	}
}
