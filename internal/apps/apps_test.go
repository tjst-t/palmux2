package apps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── store + drop-in ─────────────────────────────────────────────────────────

func TestAppsFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if af, err := LoadApps(dir); err != nil || len(af.Installed) != 0 {
		t.Fatalf("empty load: %+v err=%v", af, err)
	}
	want := AppsFile{Installed: []InstalledApp{
		{ID: "infisical", Package: "infisical", AuthPath: "~/.infisical"},
		{ID: "ripgrep", Package: "ripgrep", Custom: true},
	}}
	if err := WriteApps(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadApps(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Installed) != 2 {
		t.Fatalf("want 2, got %d", len(got.Installed))
	}
	// Sorted by ID.
	if got.Installed[0].ID != "infisical" || got.Installed[1].ID != "ripgrep" {
		t.Fatalf("order: %+v", got.Installed)
	}
}

func TestGenerateDropin(t *testing.T) {
	af := AppsFile{Installed: []InstalledApp{
		{ID: "gh", Package: "gh"},
		{ID: "1password-cli", Package: "_1password-cli"},
		{ID: "dup", Package: "gh"}, // deduped
	}}
	out := GenerateDropin(af)
	if !strings.Contains(out, "environment.systemPackages = [ pkgs._1password-cli pkgs.gh ];") {
		t.Fatalf("drop-in packages wrong:\n%s", out)
	}
	if strings.Count(out, "pkgs.gh") != 1 {
		t.Fatalf("gh not deduped:\n%s", out)
	}
	// Empty set → additive-nothing module (clean converge).
	if !strings.Contains(GenerateDropin(AppsFile{}), "environment.systemPackages = [ ];") {
		t.Fatalf("empty drop-in wrong:\n%s", GenerateDropin(AppsFile{}))
	}
}

func TestWriteDropin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "local")
	af := AppsFile{Installed: []InstalledApp{{ID: "infisical", Package: "infisical"}}}
	if err := WriteDropin(dir, af); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, DropinFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "pkgs.infisical") {
		t.Fatalf("drop-in file: %s", b)
	}
}

func TestValidPackageAttr(t *testing.T) {
	ok := []string{"infisical", "_1password-cli", "awscli2", "nodePackages.foo", "gh"}
	bad := []string{"", "foo; rm -rf /", "foo bar", "$(x)", "../x", "foo/bar", "1foo"}
	for _, s := range ok {
		if !ValidPackageAttr(s) {
			t.Errorf("want valid: %q", s)
		}
	}
	for _, s := range bad {
		if ValidPackageAttr(s) {
			t.Errorf("want invalid: %q", s)
		}
	}
	// A dropped-invalid package never reaches the generated drop-in.
	out := GenerateDropin(AppsFile{Installed: []InstalledApp{{ID: "x", Package: "foo; rm -rf /"}}})
	if strings.Contains(out, "rm -rf") {
		t.Fatalf("injection leaked into drop-in:\n%s", out)
	}
}

// ── controller with fakes ───────────────────────────────────────────────────

type fakeShared struct {
	dirs    []string
	applied [][]string
	count   int
}

func (f *fakeShared) CurrentSharedDirs() []string { return append([]string(nil), f.dirs...) }
func (f *fakeShared) ApplySharedDirs(_ context.Context, dirs []string) (int, error) {
	f.dirs = append([]string(nil), dirs...)
	f.applied = append(f.applied, f.dirs)
	return f.count, nil
}

type fakeRebuild struct {
	nixOS     bool
	triggered int
	running   bool
	failed    bool
}

func (f *fakeRebuild) NixOSHost() bool                      { return f.nixOS }
func (f *fakeRebuild) TriggerRebuild(context.Context) error { f.triggered++; return nil }
func (f *fakeRebuild) RebuildStatus(context.Context) (bool, bool, error) {
	return f.running, f.failed, nil
}

func newCtl(t *testing.T, shared *fakeShared, rb *fakeRebuild) (*Controller, string) {
	t.Helper()
	cfg := t.TempDir()
	dropin := filepath.Join(t.TempDir(), "local")
	return New(cfg, dropin, shared, rb, nil), dropin
}

// [AC-S41bdf2-1-1] install persists intent + generates the drop-in + kicks rebuild.
func TestInstallCatalogAppNixOS(t *testing.T) {
	sh := &fakeShared{}
	rb := &fakeRebuild{nixOS: true}
	c, dropin := newCtl(t, sh, rb)
	res, err := c.Install(context.Background(), "infisical", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.RebuildKicked || rb.triggered != 1 {
		t.Fatalf("rebuild not kicked: %+v triggered=%d", res, rb.triggered)
	}
	b, err := os.ReadFile(filepath.Join(dropin, DropinFileName))
	if err != nil || !strings.Contains(string(b), "pkgs.infisical") {
		t.Fatalf("drop-in missing infisical: %s err=%v", b, err)
	}
	// While the kicked rebuild has not yet been observed running, the card holds
	// "installing" (async-start race guard) — it must NOT prematurely settle to
	// installed by reading a stale rebuild result.
	if lv, _ := c.List(context.Background()); findApp(lv, "infisical").State != "installing" {
		t.Fatalf("expected installing while rebuild in flight, got %+v", findApp(lv, "infisical"))
	}
	// Drive the real rebuild lifecycle: observe running, then finish (success).
	rb.running = true
	_, _ = c.List(context.Background()) // observes running → pendingSeen
	rb.running = false
	lv, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a := findApp(lv, "infisical"); a == nil || !a.Installed || a.State != "installed" {
		t.Fatalf("infisical not installed after rebuild finished: %+v", a)
	}
}

// [AC-S41bdf2-1-3] install on a non-NixOS host persists intent but does NOT rebuild.
func TestInstallNonNixOS(t *testing.T) {
	sh := &fakeShared{}
	rb := &fakeRebuild{nixOS: false}
	c, _ := newCtl(t, sh, rb)
	res, err := c.Install(context.Background(), "gh", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.RebuildKicked || rb.triggered != 0 {
		t.Fatalf("should not rebuild on non-nixos: %+v", res)
	}
	if !res.NeedsRebuild {
		t.Fatalf("should still report needsRebuild: %+v", res)
	}
	lv, _ := c.List(context.Background())
	if a := findApp(lv, "gh"); a == nil || !a.Installed {
		t.Fatalf("gh not persisted: %+v", a)
	}
}

// [AC-S41bdf2-2-1] share toggle binds to the shared_dirs single source.
// [AC-S41bdf2-2-2] sharing a non-installed app is refused (従属).
func TestShareDependsOnInstall(t *testing.T) {
	sh := &fakeShared{count: 2}
	rb := &fakeRebuild{nixOS: true}
	c, _ := newCtl(t, sh, rb)

	// Not installed → share refused.
	if _, err := c.Share(context.Background(), "infisical", true); err == nil {
		t.Fatal("expected share-before-install to be refused")
	}

	if _, err := c.Install(context.Background(), "infisical", "", ""); err != nil {
		t.Fatal(err)
	}
	// Settle the install rebuild (observe running → finish) so the card leaves the
	// "installing" state before we assert on the share state.
	rb.running = true
	_, _ = c.List(context.Background())
	rb.running = false
	_, _ = c.List(context.Background())
	n, err := c.Share(context.Background(), "infisical", true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 containers, got %d", n)
	}
	// The auth path landed in the SAME shared_dirs source.
	home, _ := os.UserHomeDir()
	wantAbs := filepath.Join(home, ".infisical")
	if len(sh.dirs) != 1 || sh.dirs[0] != wantAbs {
		t.Fatalf("share did not write shared_dirs single source: %+v", sh.dirs)
	}
	// GET reflects shared state.
	lv, _ := c.List(context.Background())
	if a := findApp(lv, "infisical"); a == nil || !a.Shared || a.State != "shared" {
		t.Fatalf("not shared: %+v", a)
	}
	// Toggle OFF removes it.
	if _, err := c.Share(context.Background(), "infisical", false); err != nil {
		t.Fatal(err)
	}
	if len(sh.dirs) != 0 {
		t.Fatalf("share off did not remove: %+v", sh.dirs)
	}
}

// [AC-S41bdf2-3-4] installing state is attributed to the pending app while the
// rebuild runs; a failed run surfaces as error state.
func TestInstallingAndErrorState(t *testing.T) {
	sh := &fakeShared{}
	rb := &fakeRebuild{nixOS: true, running: true}
	c, _ := newCtl(t, sh, rb)
	if _, err := c.Install(context.Background(), "awscli2", "", ""); err != nil {
		t.Fatal(err)
	}
	lv, _ := c.List(context.Background())
	if a := findApp(lv, "awscli2"); a == nil || a.State != "installing" {
		t.Fatalf("want installing, got %+v", a)
	}
	// Rebuild finishes failed → error state.
	rb.running = false
	rb.failed = true
	lv, _ = c.List(context.Background())
	if a := findApp(lv, "awscli2"); a == nil || a.State != "error" || a.Error == "" {
		t.Fatalf("want error, got %+v", a)
	}
}

// [AC-S41bdf2-1-2] the list always exposes the curated catalog with reach/boundary meta.
func TestListCatalogMeta(t *testing.T) {
	c, _ := newCtl(t, &fakeShared{}, &fakeRebuild{nixOS: true})
	lv, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(lv.Apps) < len(catalog) {
		t.Fatalf("catalog not fully listed: %d", len(lv.Apps))
	}
	a := findApp(lv, "infisical")
	if a == nil || a.InstallReach != "host+containers" || a.ShareBoundary != "hot" || a.InstallBoundary != "rebuild" {
		t.Fatalf("reach/boundary meta wrong: %+v", a)
	}
	if a.State != "available" {
		t.Fatalf("fresh app should be available: %+v", a)
	}
}

// Regression (review finding #1): a PRIOR run's terminal state must not settle a
// freshly-kicked install before the new rebuild is observed running (async-start
// race). rb.failed=true models a prior failed run; the just-kicked install must
// show "installing" (held by grace), NOT a spurious "error".
func TestInstallDoesNotSettleOnPriorResult(t *testing.T) {
	sh := &fakeShared{}
	rb := &fakeRebuild{nixOS: true, running: false, failed: true} // prior run failed, not running yet
	c, _ := newCtl(t, sh, rb)
	if _, err := c.Install(context.Background(), "infisical", "", ""); err != nil {
		t.Fatal(err)
	}
	lv, _ := c.List(context.Background())
	if a := findApp(lv, "infisical"); a == nil || a.State != "installing" || a.Error != "" {
		t.Fatalf("prior failed result must not settle the new install; want installing/no-error, got %+v", a)
	}
	// Once the new rebuild is observed running and then finishes failed, it settles to error.
	rb.running = true
	_, _ = c.List(context.Background())
	rb.running = false
	lv, _ = c.List(context.Background())
	if a := findApp(lv, "infisical"); a == nil || a.State != "error" {
		t.Fatalf("want error after observed-running rebuild failed, got %+v", a)
	}
}

// Regression (review finding #2): a custom app whose validated nixpkgs attr starts
// with "_" must be installable (ValidAppID must accept a leading underscore).
func TestCustomUnderscorePackageInstallable(t *testing.T) {
	if !ValidAppID("_1password-cli") {
		t.Fatal("ValidAppID must accept a leading underscore")
	}
	c, _ := newCtl(t, &fakeShared{}, &fakeRebuild{nixOS: true})
	if _, err := c.Install(context.Background(), "_mypkg", "_mypkg", ""); err != nil {
		t.Fatalf("underscore-prefixed custom app should install, got %v", err)
	}
	lv, _ := c.List(context.Background())
	if a := findApp(lv, "_mypkg"); a == nil || !a.Installed {
		t.Fatalf("_mypkg not installed: %+v", a)
	}
}

func findApp(lv ListView, id string) *AppView {
	for i := range lv.Apps {
		if lv.Apps[i].ID == id {
			return &lv.Apps[i]
		}
	}
	return nil
}

// [AC-S41bdf2-1-5] package validation rejects a bad charset without shelling out.
func TestValidatePackageCharset(t *testing.T) {
	c, _ := newCtl(t, &fakeShared{}, &fakeRebuild{nixOS: true})
	r := c.Validate(context.Background(), "foo; rm -rf /")
	if r.Valid {
		t.Fatalf("charset-bad package must not be valid: %+v", r)
	}
}

// [AC-S41bdf2-4-1] on the restricted systemd-service PATH `nix` is not found via
// LookPath; resolveNixBin must fall back to a well-known absolute candidate. With no
// candidate present it resolves to "" → Unavailable (never a silent valid).
func TestResolveNixFallback(t *testing.T) {
	// Ensure the default candidate ("nix") is used and is NOT on PATH.
	t.Setenv("PALMUX_NIX_BIN", "")
	t.Setenv("PATH", "")

	// A stubbed absolute candidate exists → resolveNixBin returns it.
	dir := t.TempDir()
	fake := filepath.Join(dir, "nix")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := nixFallbackPaths
	t.Cleanup(func() { nixFallbackPaths = orig })

	nixFallbackPaths = func() []string { return []string{"/nonexistent/a/nix", fake} }
	if got := resolveNixBin(); got != fake {
		t.Fatalf("want fallback to stubbed absolute candidate %q, got %q", fake, got)
	}

	// No candidate present → "" (Unavailable), never a silent pass.
	nixFallbackPaths = func() []string { return []string{"/nonexistent/a/nix", "/nonexistent/b/nix"} }
	if got := resolveNixBin(); got != "" {
		t.Fatalf("want empty (Unavailable) when no candidate exists, got %q", got)
	}
	// ValidatePackage surfaces that as Unavailable (not Valid).
	r := ValidatePackage(context.Background(), "ripgrep")
	if r.Valid || !r.Unavailable {
		t.Fatalf("with no nix resolvable, want Unavailable/not-valid, got %+v", r)
	}

	// An explicit override that does not resolve is honoured exactly (no fallback):
	// even though a real fallback candidate exists, the override wins → "".
	t.Setenv("PALMUX_NIX_BIN", "/definitely/not/here/mynix")
	nixFallbackPaths = func() []string { return []string{fake} }
	if got := resolveNixBin(); got != "" {
		t.Fatalf("explicit unresolved override must not fall back, got %q", got)
	}
}

// [AC-S41bdf2-4-2] editing an installed app's auth path persists an override, rejects
// out-of-$HOME, and moves the share to the new path when the old one was shared.
func TestSetAuthPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	sh := &fakeShared{count: 3}
	rb := &fakeRebuild{nixOS: true}
	c, _ := newCtl(t, sh, rb)

	// Editing before install is refused (dependent on install).
	if _, _, err := c.SetAuthPath(context.Background(), "infisical", "~/.config/new"); err == nil {
		t.Fatal("expected auth-path edit before install to be refused")
	}

	// Install + share the catalog app (writes ~/.infisical to shared_dirs).
	if _, err := c.Install(context.Background(), "infisical", "", ""); err != nil {
		t.Fatal(err)
	}
	rb.running = true
	_, _ = c.List(context.Background())
	rb.running = false
	_, _ = c.List(context.Background())
	if _, err := c.Share(context.Background(), "infisical", true); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".infisical"); len(sh.dirs) != 1 || sh.dirs[0] != want {
		t.Fatalf("precondition: share not written: %+v", sh.dirs)
	}

	// Out-of-$HOME path → rejected, no state change.
	if _, _, err := c.SetAuthPath(context.Background(), "infisical", "/etc/secret"); err == nil {
		t.Fatal("out-of-$HOME auth path must be rejected")
	}
	af, _ := LoadApps(c.configDir)
	if af.Installed[0].AuthPath != "" && af.Installed[0].AuthPath != "~/.infisical" {
		t.Fatalf("rejected edit must not persist: %+v", af.Installed)
	}

	// Valid in-$HOME edit → override persisted + share MOVED to the new path.
	saved, n, err := c.SetAuthPath(context.Background(), "infisical", "~/.config/infisical-new")
	if err != nil {
		t.Fatal(err)
	}
	if saved != "~/.config/infisical-new" {
		t.Fatalf("returned saved path wrong: %q", saved)
	}
	if n != 3 {
		t.Fatalf("want 3 containers refreshed, got %d", n)
	}
	newAbs := filepath.Join(home, ".config", "infisical-new")
	oldAbs := filepath.Join(home, ".infisical")
	if len(sh.dirs) != 1 || sh.dirs[0] != newAbs {
		t.Fatalf("share did not follow the edit to new path: %+v", sh.dirs)
	}
	for _, d := range sh.dirs {
		if d == oldAbs {
			t.Fatalf("old shared path must be dropped: %+v", sh.dirs)
		}
	}

	// GET reflects the effective (override) auth path + still shared.
	lv, _ := c.List(context.Background())
	a := findApp(lv, "infisical")
	if a == nil || a.AuthPath != "~/.config/infisical-new" || !a.Shared || a.State != "shared" {
		t.Fatalf("list did not reflect override/shared: %+v", a)
	}

	// apps.json persisted the override.
	af, _ = LoadApps(c.configDir)
	if af.Installed[0].AuthPath != "~/.config/infisical-new" {
		t.Fatalf("override not persisted in apps.json: %+v", af.Installed)
	}
}

// [AC-S41bdf2-4-2] editing an installed-but-not-shared app persists the override
// without touching shared_dirs (no spurious share).
func TestSetAuthPathNotShared(t *testing.T) {
	sh := &fakeShared{}
	rb := &fakeRebuild{nixOS: true}
	c, _ := newCtl(t, sh, rb)
	if _, err := c.Install(context.Background(), "gh", "", ""); err != nil {
		t.Fatal(err)
	}
	saved, n, err := c.SetAuthPath(context.Background(), "gh", "~/.config/gh-corrected")
	if err != nil {
		t.Fatal(err)
	}
	if saved != "~/.config/gh-corrected" || n != 0 {
		t.Fatalf("unshared edit should touch no containers: saved=%q n=%d", saved, n)
	}
	if len(sh.applied) != 0 {
		t.Fatalf("unshared edit must not call ApplySharedDirs: %+v", sh.applied)
	}
	lv, _ := c.List(context.Background())
	if a := findApp(lv, "gh"); a == nil || a.AuthPath != "~/.config/gh-corrected" {
		t.Fatalf("override not reflected: %+v", a)
	}
}
