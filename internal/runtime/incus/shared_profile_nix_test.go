package incus

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// [AC-S41bdf2-1-4] On a NixOS host (/nix/store present) declaredDevices adds a
// read-only /nix/store share + the resolved system bin dir, and neither is
// symlink-skipped despite living outside $HOME.
func TestNixDevicesDeclaredWhenStorePresent(t *testing.T) {
	// Point the store/sysbin at fixtures that exist so the existence gate fires.
	store := t.TempDir()
	sysReal := t.TempDir()
	// A symlink chain so we exercise the resolve-to-real-path behaviour.
	sysLink := filepath.Join(t.TempDir(), "sw-bin")
	if err := os.Symlink(sysReal, sysLink); err != nil {
		t.Fatal(err)
	}
	oldStore, oldSys := nixStorePath, nixSysbinSource
	nixStorePath, nixSysbinSource = store, sysLink
	t.Cleanup(func() { nixStorePath, nixSysbinSource = oldStore, oldSys })

	m := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	devs := m.declaredDevices()

	var nixStore, nixSysbin *deviceSpec
	for i := range devs {
		switch devs[i].name {
		case nixStoreDevice:
			nixStore = &devs[i]
		case nixSysbinDevice:
			nixSysbin = &devs[i]
		}
	}
	if nixStore == nil || !nixStore.readonly || nixStore.source != store || nixStore.path != store {
		t.Fatalf("nix-store device wrong: %+v", nixStore)
	}
	if nixSysbin == nil || !nixSysbin.readonly {
		t.Fatalf("nix-sysbin device missing/not-ro: %+v", nixSysbin)
	}
	// Source must be the RESOLVED real path (generation-diff self-heal depends on it).
	if nixSysbin.source != sysReal {
		t.Fatalf("nix-sysbin source not resolved: got %q want %q", nixSysbin.source, sysReal)
	}
	if nixSysbin.path != nixBinContainerPath {
		t.Fatalf("nix-sysbin container path wrong: %q", nixSysbin.path)
	}
}

// When /nix/store is absent (Ubuntu host) the nix devices are NOT declared.
func TestNixDevicesAbsentWithoutStore(t *testing.T) {
	oldStore := nixStorePath
	nixStorePath = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { nixStorePath = oldStore })

	m := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	for _, d := range m.declaredDevices() {
		if d.name == nixStoreDevice || d.name == nixSysbinDevice {
			t.Fatalf("nix device declared on non-nix host: %+v", d)
		}
	}
}

// [AC-S41bdf2-1-4] Ensure sets the profile environment.PATH to prepend the Nix
// bin dir when /nix/store is present.
func TestEnsureSetsEnvPathOnNix(t *testing.T) {
	store := t.TempDir()
	oldStore := nixStorePath
	nixStorePath = store
	t.Cleanup(func() { nixStorePath = oldStore })

	fr := newFakeRunner()
	fr.setResult("profile show", fakeResult{stderr: "Error: not found", code: 1})
	m := NewSharedProfileManager(fr.asRunner(), nil, nil)
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range fr.recorded() {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "profile set palmux-shared environment.PATH="+nixBinContainerPath) {
			found = true
		}
	}
	if !found {
		t.Fatalf("environment.PATH not set with nix bin dir; calls: %v", fr.recorded())
	}
}
