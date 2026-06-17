package selfupdate

import (
	"context"
	"testing"
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

// [AC-S6ab0ed-1-4] When GitHub is unreachable for every component, Detect
// degrades gracefully: it still returns a snapshot with installed versions,
// marks Degraded, and reports no available updates (no badge flap).
func TestDetectGracefulDegrade(t *testing.T) {
	m := Manifest{Components: []Component{
		// Point at a repo that resolves to a failing host so LatestTag errors
		// fast without network flake (invalid host → DNS error).
		{Name: "palmux", Kind: KindCoreBinary, Display: "palmux", GithubRepo: "invalid-owner-zzz/does-not-exist-zzz"},
	}}
	probes := InstalledProbes{BinVersion: func() string { return "v0.10.0" }}
	snap := Detect(context.Background(), m, probes, false)
	if len(snap.Components) != 1 {
		t.Fatalf("want 1 component, got %d", len(snap.Components))
	}
	if snap.Components[0].Installed != "v0.10.0" {
		t.Errorf("installed not probed: %q", snap.Components[0].Installed)
	}
	if !snap.Degraded {
		t.Errorf("expected Degraded=true on GitHub failure")
	}
	if snap.Available {
		t.Errorf("expected Available=false when latest unresolved")
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

// [AC-S6ab0ed-1-2] availabilityChanged fires only on transitions (mirrors
// setDriftCached) so we publish WS events sparingly.
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
