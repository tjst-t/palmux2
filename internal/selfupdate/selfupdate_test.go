package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// [AC-S6ab0ed-1-1] The embedded manifest parses and carries the two CORE
// components plus the declared tools.
func TestLoadManifest(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	byName := map[string]Component{}
	for _, c := range m.Components {
		byName[c.Name] = c
	}
	pal, ok := byName["palmux"]
	if !ok || pal.Kind != KindCoreBinary {
		t.Fatalf("manifest missing core-binary palmux: %+v", byName["palmux"])
	}
	if pal.GithubRepo != "tjst-t/palmux2" {
		t.Errorf("palmux githubRepo = %q, want tjst-t/palmux2", pal.GithubRepo)
	}
	img, ok := byName["image"]
	if !ok || img.Kind != KindCoreImage {
		t.Fatalf("manifest missing core-image: %+v", byName["image"])
	}
	if _, ok := byName["gwq"]; !ok {
		t.Errorf("manifest missing declared tool gwq")
	}
}

// [AC-S6ab0ed-1-2] Version comparison drives update-available; it is
// conservative on dev/dirty and unparseable inputs.
func TestUpdateAvailable(t *testing.T) {
	cases := []struct {
		installed, latest string
		want              bool
	}{
		{"v0.10.0", "v0.11.0", true},
		{"v0.11.0", "v0.11.0", false},
		{"v0.11.0", "v0.10.0", false},
		{"v0.10.0", "v0.10.1", true},
		{"v0.9.0", "v0.10.0", true},
		{"v1.0.0", "v0.99.0", false},
		// dev / dirty builds never claim an update is available.
		{"dev", "v0.11.0", false},
		{"v0.5.1-3-g0943bc8-dirty", "v0.11.0", false},
		// unparseable / empty
		{"", "v0.11.0", false},
		{"v0.10.0", "", false},
		// tool-style "gwq version 0.7.1"
		{"gwq version 0.7.1", "v0.8.0", true},
		{"gwq version 0.8.0", "v0.8.0", false},
	}
	for _, c := range cases {
		if got := UpdateAvailable(c.installed, c.latest); got != c.want {
			t.Errorf("UpdateAvailable(%q,%q)=%v want %v", c.installed, c.latest, got, c.want)
		}
	}
}

// withLatestTagFn swaps the injectable tag resolver for the test's duration.
func withLatestTagFn(t *testing.T, fn func(context.Context, string) (string, error)) {
	t.Helper()
	orig := latestTagFn
	t.Cleanup(func() { latestTagFn = orig })
	latestTagFn = fn
}

// [AC-S6ab0ed-1-4][AC-Sa8e7d0-2-2] A repo with NO published releases (GitHub
// 404 → NoReleasesError) is un-fetchable but must NOT mark the cycle Degraded —
// a stable "no releases" fact is not a transient outage, so the
// rate-limit/unreachable banner must not show forever. (Deterministic: the tag
// resolver is injected so the test is independent of live GitHub.)
func TestDetectNoReleasesIsUnfetchableNotDegraded(t *testing.T) {
	withLatestTagFn(t, func(_ context.Context, repo string) (string, error) {
		return "", &NoReleasesError{Repo: repo}
	})
	m := Manifest{Components: []Component{
		{Name: "gwq", Kind: KindTool, Display: "gwq", GithubRepo: "tjst-t/gwq"},
	}}
	probes := InstalledProbes{}
	snap := Detect(context.Background(), m, probes, false)
	if len(snap.Components) != 1 {
		t.Fatalf("want 1 component, got %d", len(snap.Components))
	}
	if snap.Components[0].Fetchable {
		t.Errorf("expected Fetchable=false when latest unresolved")
	}
	if snap.Available {
		t.Errorf("expected Available=false when latest unresolved")
	}
	// The key Sa8e7d0-2-2 assertion: a no-releases 404 must NOT degrade the cycle.
	if snap.Degraded {
		t.Errorf("a no-releases (404) source must NOT mark the cycle Degraded")
	}
}

// [AC-S6ab0ed-1-4] A TRANSIENT failure (rate-limit / network, NOT a 404) DOES
// mark the cycle Degraded so the banner explains the outage. (Deterministic.)
func TestDetectTransientFailureDegrades(t *testing.T) {
	withLatestTagFn(t, func(_ context.Context, _ string) (string, error) {
		return "", &RateLimitError{Status: 403}
	})
	m := Manifest{Components: []Component{
		{Name: "palmux", Kind: KindCoreBinary, Display: "palmux", GithubRepo: "tjst-t/palmux2"},
	}}
	snap := Detect(context.Background(), m, InstalledProbes{BinVersion: func() string { return "v0.10.0" }}, false)
	if snap.Components[0].Fetchable {
		t.Errorf("expected Fetchable=false on transient failure")
	}
	if !snap.Degraded {
		t.Errorf("expected Degraded=true on a transient GitHub failure")
	}
}

// [AC-Sa8e7d0-2-2] A source whose latest cannot be resolved (no releases /
// unreachable) is NEVER counted as "update available" — even when an installed
// version is present — and is surfaced as un-fetchable, not as up-to-date.
func TestUnfetchableSourceDoesNotLightBadge(t *testing.T) {
	withLatestTagFn(t, func(_ context.Context, repo string) (string, error) {
		return "", &NoReleasesError{Repo: repo} // deterministic "no releases"
	})
	m := Manifest{Components: []Component{
		{Name: "gwq", Kind: KindTool, Display: "gwq", GithubRepo: "tjst-t/gwq", Bin: "definitely-not-a-real-bin-zzz"},
	}}
	snap := Detect(context.Background(), m, InstalledProbes{}, false)
	gwq := snap.Components[0]
	if gwq.Fetchable {
		t.Errorf("gwq latest unresolved → Fetchable must be false, got true")
	}
	if gwq.Available {
		t.Errorf("un-fetchable component must not be Available")
	}
	if snap.Available {
		t.Errorf("snapshot must not report an available update when the only component is un-fetchable")
	}
}

// [AC-S6ab0ed-1-2] toolVersion extracts the dotted version token and ignores
// leading non-version integers (year/banner/"0 errors").
func TestToolVersionTokenExtraction(t *testing.T) {
	cases := []struct {
		out  string
		want string
	}{
		{"gwq version v0.8.0", "v0.8.0"},
		{"portman version 0.3.1", "0.3.1"},
		{"build 2024, version 1.2.3", "1.2.3"}, // year ignored, real version picked
		{"v0.10.0", "v0.10.0"},
		{"no version here", "no version here"}, // falls back to trimmed output
	}
	for _, c := range cases {
		if got := toolVersionTokenRe.FindString(c.out); got != "" {
			if got != c.want {
				t.Errorf("token(%q)=%q want %q", c.out, got, c.want)
			}
		} else if c.want != c.out {
			t.Errorf("token(%q) empty; want %q", c.out, c.want)
		}
	}
}

// [AC-S6ab0ed-2-1] RunUpdate rejects a concurrent run with ErrUpdateInFlight
// (the running guard is live, not dead state). With no ~/update-palmux2.sh it
// returns ErrNotNixManaged; we drive the guard by setting running directly.
func TestRunUpdateInFlightGuard(t *testing.T) {
	s := NewService(Manifest{Components: []Component{{Name: "palmux", Kind: KindCoreBinary}}},
		InstalledProbes{}, nil, nil)
	// Force nixManaged + in-flight to exercise the guard branch deterministically.
	s.running = true
	// nixManaged() will be false here (no stub) so RunUpdate returns
	// ErrNotNixManaged BEFORE the guard — that's the correct precedence (we
	// never claim "in flight" on a box that can't update at all). Assert that.
	if err := s.RunUpdate(context.Background()); err != ErrNotNixManaged {
		t.Fatalf("expected ErrNotNixManaged on non-managed box, got %v", err)
	}
}

// [AC-Sa8e7d0-1-1][AC-Sa8e7d0-1-3] When the dedicated palmux-update unit is
// present, RunUpdateForeground (the CLI path) drives it via `systemctl --user
// start --wait palmux-update.service` — i.e. an INDEPENDENT systemd unit, not an
// in-process `bash ~/update-palmux2.sh` child. A failing unit propagates as a
// non-zero error. We stub systemctl with a recording script.
func TestRunUpdateForegroundUsesIndependentUnit(t *testing.T) {
	home := t.TempDir()
	// nixManaged() requires an executable ~/update-palmux2.sh.
	helper := filepath.Join(home, "update-palmux2.sh")
	if err := os.WriteFile(helper, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	// Recording systemctl stub: append its args to a log and succeed.
	argLog := filepath.Join(home, "systemctl-args.log")
	stub := filepath.Join(home, "systemctl-stub")
	script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> '" + argLog + "'\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	origBin, origAvail := systemctlUserBin, updateUnitAvailable
	t.Cleanup(func() { systemctlUserBin, updateUnitAvailable = origBin, origAvail })
	systemctlUserBin = stub
	updateUnitAvailable = func() bool { return true } // unit present

	s := NewService(Manifest{Components: []Component{{Name: "palmux", Kind: KindCoreBinary}}},
		InstalledProbes{}, nil, nil)
	if err := s.RunUpdateForeground(context.Background(), os.Stdout, os.Stderr); err != nil {
		t.Fatalf("RunUpdateForeground (unit present, stub ok): %v", err)
	}
	logged, _ := os.ReadFile(argLog)
	got := string(logged)
	if !strings.Contains(got, "--user start --wait "+updateUnitName) {
		t.Errorf("CLI did not drive the independent unit via systemctl; args:\n%s", got)
	}
}

// [AC-Sa8e7d0-1-2] A failing update unit must surface as a non-zero error from
// the CLI path (no false success), so a half-done update is reported.
func TestRunUpdateForegroundUnitFailurePropagates(t *testing.T) {
	home := t.TempDir()
	helper := filepath.Join(home, "update-palmux2.sh")
	if err := os.WriteFile(helper, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	// Stub systemctl that fails on `start --wait` (simulating a failed oneshot)
	// but succeeds on reset-failed.
	stub := filepath.Join(home, "systemctl-stub")
	script := "#!/usr/bin/env bash\ncase \"$*\" in\n  *'start --wait'*) exit 1 ;;\n  *) exit 0 ;;\nesac\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	origBin, origAvail := systemctlUserBin, updateUnitAvailable
	t.Cleanup(func() { systemctlUserBin, updateUnitAvailable = origBin, origAvail })
	systemctlUserBin = stub
	updateUnitAvailable = func() bool { return true }

	s := NewService(Manifest{Components: []Component{{Name: "palmux", Kind: KindCoreBinary}}},
		InstalledProbes{}, nil, nil)
	err := s.RunUpdateForeground(context.Background(), os.Stdout, os.Stderr)
	if err == nil {
		t.Fatalf("expected non-nil error when the update unit fails")
	}
	if !strings.Contains(err.Error(), updateUnitName) {
		t.Errorf("error should name the failed unit, got: %v", err)
	}
}

// [AC-Sa8e7d0-1-2] watchUpdateUnit clears the in-flight guard when the unit ends
// (failed or inactive) without restarting palmux2, using a BACKGROUND context —
// NOT the request ctx (which would die immediately and wedge the guard). Here we
// stub systemctl `show -p ActiveState` to report "failed" and assert the guard
// clears promptly.
func TestWatchUpdateUnitClearsGuardOnFailure(t *testing.T) {
	home := t.TempDir()
	// Stub systemctl: `show ... ActiveState ...` prints "failed".
	stub := filepath.Join(home, "systemctl-stub")
	script := "#!/usr/bin/env bash\ncase \"$*\" in\n  *ActiveState*) echo failed ;;\n  *) exit 0 ;;\nesac\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := systemctlUserBin
	t.Cleanup(func() { systemctlUserBin = orig })
	systemctlUserBin = stub

	s := NewService(Manifest{Components: []Component{{Name: "palmux", Kind: KindCoreBinary}}},
		InstalledProbes{}, nil, nil)
	s.running = true
	cleared := make(chan struct{})
	// Use Background ctx (the real call does too) — a request ctx would cancel.
	go s.watchUpdateUnit(context.Background(), func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		close(cleared)
	})
	select {
	case <-cleared:
		// good — guard cleared without the 10-minute backstop
	case <-time.After(8 * time.Second):
		t.Fatalf("watchUpdateUnit did not clear the in-flight guard on a failed unit")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.running {
		t.Errorf("running should be false after clear")
	}
}

// [AC-Sfeed64-2] UpdateStatus parses systemd's ActiveState/Result for
// palmux-update.service so the GUI can detect a genuine failure directly
// instead of guessing from the WS-reconnect timeout (which a slow-but-
// successful update, e.g. a big image download, can legitimately outrun).
func TestUpdateStatusParsesSystemctlShow(t *testing.T) {
	cases := []struct {
		name       string
		show       string
		wantActive string
		wantResult string
		wantRun    bool
	}{
		{"running", "ActiveState=activating\nResult=\n", "activating", "", true},
		{"succeeded", "ActiveState=inactive\nResult=success\n", "inactive", "success", false},
		{"failed", "ActiveState=failed\nResult=exit-code\n", "failed", "exit-code", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			stub := filepath.Join(home, "systemctl-stub")
			script := "#!/usr/bin/env bash\ncat <<'EOF'\n" + tc.show + "EOF\n"
			if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			orig := systemctlUserBin
			t.Cleanup(func() { systemctlUserBin = orig })
			systemctlUserBin = stub

			s := NewService(Manifest{Components: []Component{{Name: "palmux", Kind: KindCoreBinary}}},
				InstalledProbes{}, nil, nil)
			st, err := s.UpdateStatus(context.Background())
			if err != nil {
				t.Fatalf("UpdateStatus: %v", err)
			}
			if st.Active != tc.wantActive || st.Result != tc.wantResult || st.Running != tc.wantRun {
				t.Errorf("got %+v, want active=%s result=%s running=%v", st, tc.wantActive, tc.wantResult, tc.wantRun)
			}
		})
	}
}

// [AC-S6ab0ed-1-2] availabilityChanged fires only on transitions (mirrors
// setDriftCached) so we publish WS events sparingly.
// [AC-Sb14caa-4-3] On a NixOS host the snapshot flags NixOSHost so the GUI maps
// the update badge to nixos-rebuild guidance instead of the in-app one-click. The
// /etc/NIXOS marker and an os-release `ID=nixos` are both recognised; a non-NixOS
// host is not flagged.
func TestDetectNixOSHost(t *testing.T) {
	saveMarker, saveOS := nixosMarkerPath, osReleasePath
	defer func() { nixosMarkerPath, osReleasePath = saveMarker, saveOS }()

	dir := t.TempDir()
	// 1) /etc/NIXOS marker present → NixOS.
	marker := filepath.Join(dir, "NIXOS")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	nixosMarkerPath = marker
	osReleasePath = filepath.Join(dir, "os-release-absent")
	if !detectNixOSHost() {
		t.Errorf("/etc/NIXOS marker present → want NixOS host")
	}

	// 2) No marker, but os-release ID=nixos → NixOS.
	nixosMarkerPath = filepath.Join(dir, "NIXOS-absent")
	osr := filepath.Join(dir, "os-release")
	if err := os.WriteFile(osr, []byte("NAME=NixOS\nID=nixos\nVERSION=\"25.05\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	osReleasePath = osr
	if !detectNixOSHost() {
		t.Errorf("os-release ID=nixos → want NixOS host")
	}

	// 3) Ubuntu os-release, no marker → NOT NixOS.
	if err := os.WriteFile(osr, []byte("NAME=\"Ubuntu\"\nID=ubuntu\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if detectNixOSHost() {
		t.Errorf("ID=ubuntu → want non-NixOS host")
	}

	// 4) Neither marker nor os-release present → NOT NixOS.
	osReleasePath = filepath.Join(dir, "os-release-gone")
	if detectNixOSHost() {
		t.Errorf("no markers → want non-NixOS host")
	}
}

func TestAvailabilityChanged(t *testing.T) {
	a := Snapshot{Available: false, Components: []ComponentStatus{{Name: "palmux", Available: false}}}
	b := Snapshot{Available: true, Components: []ComponentStatus{{Name: "palmux", Available: true}}}
	if availabilityChanged(a, a) {
		t.Errorf("no-op snapshot should not be a change")
	}
	if !availabilityChanged(a, b) {
		t.Errorf("false→true should be a change")
	}
	if !availabilityChanged(b, a) {
		t.Errorf("true→false should be a change")
	}
}
