package selfupdate

import (
	"testing"
)

func coreSnap() *Snapshot {
	return &Snapshot{Components: []ComponentStatus{
		{Name: "palmux", Kind: string(KindCoreBinary), Installed: "v1.2.3"},
		{Name: "palmux-ws", Kind: string(KindCoreImage), Installed: "v1.2.3"},
	}}
}

// TestForceDisabledIsInert verifies the whole mechanism no-ops without the env
// gate — the critical production-safety property.
func TestForceDisabledIsInert(t *testing.T) {
	dir := t.TempDir()
	// env unset by default in tests; assert explicitly.
	t.Setenv("PALMUX_SELFUPDATE_FORCE", "")

	if ForceEnabled() {
		t.Fatal("ForceEnabled should be false when env unset")
	}
	if got := ForceVersionSuffix(dir); got != "" {
		t.Fatalf("suffix should be empty when disabled, got %q", got)
	}
	if ForceArmed(dir) {
		t.Fatal("ForceArmed should be false when disabled")
	}
	if _, err := ArmForce(dir, "v1.2.3"); err == nil {
		t.Fatal("ArmForce should error when disabled")
	}
	if applyForce(dir) {
		t.Fatal("applyForce should be false when disabled")
	}
	snap := coreSnap()
	overlayForce(dir, "v1.2.3", snap)
	if snap.Available || snap.Forced || snap.Components[0].Available {
		t.Fatal("overlayForce should be a no-op when disabled")
	}
}

// TestForceArmApplyCycle walks the full arm → overlay → apply → suffix cycle and
// asserts the version delta + badge-clearing behavior the handshake relies on.
func TestForceArmApplyCycle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PALMUX_SELFUPDATE_FORCE", "1")
	const base = "v1.2.3"

	// Initially: not armed, no suffix, overlay inert.
	if ForceArmed(dir) {
		t.Fatal("fresh state should not be armed")
	}
	snap := coreSnap()
	overlayForce(dir, base, snap)
	if snap.Available {
		t.Fatal("overlay before arm must be inert")
	}

	// Arm → target is base+force.1 (Current 0 → +1).
	target, err := ArmForce(dir, base)
	if err != nil {
		t.Fatalf("ArmForce: %v", err)
	}
	if target != "v1.2.3+force.1" {
		t.Fatalf("target = %q, want v1.2.3+force.1", target)
	}
	if !ForceArmed(dir) {
		t.Fatal("should be armed after ArmForce")
	}

	// Overlay now marks the core-binary available; suffix still empty (Current 0).
	snap = coreSnap()
	overlayForce(dir, base, snap)
	if !snap.Available || !snap.Forced {
		t.Fatal("overlay should mark snapshot available+forced after arm")
	}
	c := snap.Components[0]
	if !c.Available || c.Installed != "v1.2.3" || c.Latest != "v1.2.3+force.1" {
		t.Fatalf("core-binary overlay wrong: installed=%q latest=%q avail=%v", c.Installed, c.Latest, c.Available)
	}
	if snap.Components[1].Available {
		t.Fatal("non-core-binary component must not be overlaid")
	}
	if got := ForceVersionSuffix(dir); got != "" {
		t.Fatalf("suffix should be empty before apply, got %q", got)
	}

	// Apply (the forced run's pre-restart step): Current++ and disarm.
	if !applyForce(dir) {
		t.Fatal("applyForce should report true when armed")
	}
	if ForceArmed(dir) {
		t.Fatal("should be disarmed after apply")
	}
	if got := ForceVersionSuffix(dir); got != "+force.1" {
		t.Fatalf("suffix after apply = %q, want +force.1", got)
	}

	// Post-apply detection: not armed → overlay inert → badge clears.
	snap = coreSnap()
	overlayForce(dir, base, snap)
	if snap.Available || snap.Forced {
		t.Fatal("overlay must be inert after apply (badge clears)")
	}

	// Second cycle is monotonic: arm → target force.2, apply → suffix force.2.
	target, err = ArmForce(dir, base)
	if err != nil {
		t.Fatalf("ArmForce 2: %v", err)
	}
	if target != "v1.2.3+force.2" {
		t.Fatalf("second target = %q, want v1.2.3+force.2", target)
	}
	if !applyForce(dir) {
		t.Fatal("applyForce 2 should be true")
	}
	if got := ForceVersionSuffix(dir); got != "+force.2" {
		t.Fatalf("suffix after 2nd apply = %q, want +force.2", got)
	}
}

// TestForceDisarm cancels an armed update without advancing the counter.
func TestForceDisarm(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PALMUX_SELFUPDATE_FORCE", "1")

	if _, err := ArmForce(dir, "v1.0.0"); err != nil {
		t.Fatalf("ArmForce: %v", err)
	}
	if err := DisarmForce(dir); err != nil {
		t.Fatalf("DisarmForce: %v", err)
	}
	if ForceArmed(dir) {
		t.Fatal("should not be armed after disarm")
	}
	if applyForce(dir) {
		t.Fatal("applyForce should be false after disarm (counter must not advance)")
	}
	if got := ForceVersionSuffix(dir); got != "" {
		t.Fatalf("suffix should stay empty after disarm, got %q", got)
	}
}

// TestForceEnabledTruthiness checks the env-value parsing.
func TestForceEnabledTruthiness(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"", false}, {"0", false}, {"false", false}, {"no", false}, {"off", false},
		{"1", true}, {"true", true}, {"yes", true}, {"on", true},
	} {
		t.Setenv("PALMUX_SELFUPDATE_FORCE", tc.val)
		if got := ForceEnabled(); got != tc.want {
			t.Errorf("ForceEnabled(%q) = %v, want %v", tc.val, got, tc.want)
		}
	}
}
