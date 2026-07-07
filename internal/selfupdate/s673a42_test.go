package selfupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// [AC-S673a42-1-1] On a NixOS host, Detect surfaces the exact appliance flake
// target so the GUI/README render a backend-sourced (never-drifting) command.
// Off-appliance it is empty.
func TestDetect_ApplianceFlakeTarget(t *testing.T) {
	saveMarker, saveOS, saveTag := nixosMarkerPath, osReleasePath, latestTagFn
	saveReady := rebuildUpdaterReadyFn
	defer func() {
		nixosMarkerPath, osReleasePath, latestTagFn = saveMarker, saveOS, saveTag
		rebuildUpdaterReadyFn = saveReady
	}()
	// Hermetic: don't shell out to systemctl for the rebuild-unit probe.
	rebuildUpdaterReadyFn = func(context.Context) bool { return true }
	// Hermetic: no GitHub. Report "no releases" so nothing is Available/Degraded.
	latestTagFn = func(context.Context, string) (string, error) {
		return "", &NoReleasesError{Repo: "x"}
	}
	m := Manifest{Components: []Component{{Name: "palmux", Kind: KindCoreBinary, GithubRepo: "tjst-t/palmux2"}}}
	probes := InstalledProbes{BinVersion: func() string { return "v0.12.0" }}

	dir := t.TempDir()

	// NixOS host → target populated to the fixed appliance flake target.
	marker := filepath.Join(dir, "NIXOS")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	nixosMarkerPath = marker
	osReleasePath = filepath.Join(dir, "os-release-absent")
	snap := Detect(context.Background(), m, probes, false)
	if snap.ApplianceFlakeTarget != ApplianceFlakeTarget {
		t.Errorf("NixOS host: ApplianceFlakeTarget = %q; want %q", snap.ApplianceFlakeTarget, ApplianceFlakeTarget)
	}
	if ApplianceFlakeTarget != "/persist/palmux/nixos#appliance" {
		t.Errorf("ApplianceFlakeTarget const = %q; MUST be the real on-appliance flake target (not /etc/palmux)", ApplianceFlakeTarget)
	}

	// Non-NixOS host → empty (meaningless off-appliance).
	nixosMarkerPath = filepath.Join(dir, "NIXOS-absent")
	osReleasePath = filepath.Join(dir, "os-release-gone")
	snap = Detect(context.Background(), m, probes, false)
	if snap.ApplianceFlakeTarget != "" {
		t.Errorf("non-NixOS host: ApplianceFlakeTarget = %q; want empty", snap.ApplianceFlakeTarget)
	}
}

// On a NixOS host the snapshot reports whether the GUI version-update unit is
// actually defined in the running generation (RebuildUpdaterReady). On the
// bootstrap gap (newer palmux binary on an older generation, unit absent) it is
// false so the GUI shows manual guidance instead of a button that would fail with
// a polkit "Access denied". Off-appliance the probe is never consulted (false).
func TestDetect_RebuildUpdaterReady(t *testing.T) {
	saveMarker, saveOS, saveTag := nixosMarkerPath, osReleasePath, latestTagFn
	saveReady := rebuildUpdaterReadyFn
	defer func() {
		nixosMarkerPath, osReleasePath, latestTagFn = saveMarker, saveOS, saveTag
		rebuildUpdaterReadyFn = saveReady
	}()
	latestTagFn = func(context.Context, string) (string, error) { return "", &NoReleasesError{Repo: "x"} }
	m := Manifest{Components: []Component{{Name: "palmux", Kind: KindCoreBinary, GithubRepo: "tjst-t/palmux2"}}}
	probes := InstalledProbes{BinVersion: func() string { return "v0.12.0" }}
	dir := t.TempDir()
	marker := filepath.Join(dir, "NIXOS")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// NixOS host, unit present → ready true.
	nixosMarkerPath = marker
	osReleasePath = filepath.Join(dir, "os-release-absent")
	rebuildUpdaterReadyFn = func(context.Context) bool { return true }
	if snap := Detect(context.Background(), m, probes, false); !snap.RebuildUpdaterReady {
		t.Errorf("unit present → RebuildUpdaterReady = false; want true")
	}

	// NixOS host, unit absent (bootstrap gap) → ready false.
	rebuildUpdaterReadyFn = func(context.Context) bool { return false }
	if snap := Detect(context.Background(), m, probes, false); snap.RebuildUpdaterReady {
		t.Errorf("unit absent → RebuildUpdaterReady = true; want false")
	}

	// Non-NixOS host → probe not consulted, stays false even if it would return true.
	nixosMarkerPath = filepath.Join(dir, "NIXOS-absent")
	osReleasePath = filepath.Join(dir, "os-release-gone")
	rebuildUpdaterReadyFn = func(context.Context) bool { return true }
	if snap := Detect(context.Background(), m, probes, false); snap.RebuildUpdaterReady {
		t.Errorf("non-NixOS host → RebuildUpdaterReady = true; want false (probe must not be consulted)")
	}
}

// [AC-S673a42-3-1] RunImageInstall runs the image-fetch command in the background
// and reports done on success; the badge-clearing Refresh runs without error.
// [AC-S673a42-3-2] A second call while one is in flight is refused.
func TestRunImageInstall_SuccessAndInFlightGuard(t *testing.T) {
	saveCmd, saveTag := imageInstallCmd, latestTagFn
	defer func() { imageInstallCmd, latestTagFn = saveCmd, saveTag }()
	latestTagFn = func(context.Context, string) (string, error) {
		return "", &NoReleasesError{Repo: "x"}
	}

	// Block the fake install until we release it, so we can assert the in-flight
	// guard while it is genuinely running.
	release := make(chan struct{})
	started := make(chan struct{})
	imageInstallCmd = func(ctx context.Context) (*exec.Cmd, error) {
		close(started)
		<-release
		return exec.CommandContext(ctx, "true"), nil
	}

	m := Manifest{Components: []Component{{Name: "image", Kind: KindCoreImage, GithubRepo: "tjst-t/palmux2"}}}
	probes := InstalledProbes{ImageVersion: func() string { return "v0.13.0" }}
	svc := NewService(m, probes, nil, nil)

	if err := svc.RunImageInstall(); err != nil {
		t.Fatalf("RunImageInstall: %v", err)
	}
	<-started
	if st := svc.ImageInstallStatus(); !st.Running {
		t.Errorf("status should be Running while the fetch is in flight, got %+v", st)
	}
	// In-flight guard.
	if err := svc.RunImageInstall(); err != ErrImageInstallInFlight {
		t.Errorf("second RunImageInstall = %v; want ErrImageInstallInFlight", err)
	}
	close(release)

	waitUntil(t, func() bool { return !svc.ImageInstallStatus().Running }, 5*time.Second)
	st := svc.ImageInstallStatus()
	if !st.Done || st.Error != "" {
		t.Errorf("after success: got %+v; want Done, no Error", st)
	}
	if st.Installed != "v0.13.0" {
		t.Errorf("Installed = %q; want v0.13.0", st.Installed)
	}
}

// [AC-S673a42-3-2] A failing install records the error (old image kept — no crash).
func TestRunImageInstall_Failure(t *testing.T) {
	saveCmd := imageInstallCmd
	defer func() { imageInstallCmd = saveCmd }()
	imageInstallCmd = func(ctx context.Context) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "false"), nil // nonzero exit
	}
	svc := NewService(Manifest{}, InstalledProbes{}, nil, nil)
	if err := svc.RunImageInstall(); err != nil {
		t.Fatalf("RunImageInstall: %v", err)
	}
	waitUntil(t, func() bool { return !svc.ImageInstallStatus().Running }, 5*time.Second)
	st := svc.ImageInstallStatus()
	if st.Done || st.Error == "" {
		t.Errorf("after failure: got %+v; want not-Done and a non-empty Error", st)
	}
}

func waitUntil(t *testing.T, cond func() bool, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}
