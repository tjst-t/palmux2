package apps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// AppsFileName is the dedicated install-state store basename under the config dir.
// It is separate from config.toml so the deploy plane's whole-file writer
// (config.WriteMaster) never clobbers app state and vice-versa.
const AppsFileName = "apps.json"

// InstalledApp is one persisted install record. Custom apps carry their own
// package/authPath (from validated user input); known apps mirror the catalog so
// the record is self-contained even if the catalog later changes.
type InstalledApp struct {
	ID       string `json:"id"`
	Package  string `json:"package"`
	AuthPath string `json:"authPath,omitempty"`
	Display  string `json:"display,omitempty"`
	Icon     string `json:"icon,omitempty"`
	Custom   bool   `json:"custom,omitempty"`
}

// AppsFile is the on-disk shape of apps.json.
type AppsFile struct {
	Installed []InstalledApp `json:"installed"`
}

// packageAttrRe restricts a nixpkgs attr path to a safe charset so a generated
// drop-in / a `nix eval` argument can never carry shell/nix injection
// (priority_rule 7). Allows dotted attr paths (e.g. nodePackages.foo).
var packageAttrRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9._-]*$`)

// ValidPackageAttr reports whether s is a syntactically valid nixpkgs attr path.
func ValidPackageAttr(s string) bool { return packageAttrRe.MatchString(s) }

// appIDRe restricts a card id to a slug charset (also used as a data-testid
// suffix). Derived from the package for custom apps, so it must accept every
// char a valid nixpkgs attr can start with — including a leading underscore
// (e.g. `_1password-cli`, which exists because attrs cannot begin with a digit).
var appIDRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// ValidAppID reports whether s is a valid card id.
func ValidAppID(s string) bool { return appIDRe.MatchString(s) }

// LoadApps reads apps.json under dir. A missing file yields an empty AppsFile
// (no error). A malformed file is a hard error so a corrupt store surfaces
// instead of silently resetting install state.
func LoadApps(dir string) (AppsFile, error) {
	var af AppsFile
	b, err := os.ReadFile(filepath.Join(dir, AppsFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return af, nil
		}
		return af, fmt.Errorf("apps: read %s: %w", AppsFileName, err)
	}
	if err := json.Unmarshal(b, &af); err != nil {
		return af, fmt.Errorf("apps: parse %s: %w", AppsFileName, err)
	}
	return af, nil
}

// WriteApps persists apps.json atomically (0600 — user-owned config bundle).
func WriteApps(dir string, af AppsFile) error {
	// Stable order so the file (and the generated drop-in) is deterministic.
	sort.Slice(af.Installed, func(i, j int) bool { return af.Installed[i].ID < af.Installed[j].ID })
	b, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return fmt.Errorf("apps: encode: %w", err)
	}
	dst := filepath.Join(dir, AppsFileName)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("apps: write %s: %w", tmp, err)
	}
	return os.Rename(tmp, dst)
}

// GenerateDropin renders the one-way §8.2 drop-in from the installed set:
//
//	{ pkgs, ... }: {
//	  environment.systemPackages = [ pkgs.infisical pkgs.gh ];
//	}
//
// The appliance uses environment.systemPackages (no home-manager on the OS), which
// is the AC's "home.packages 相当の宣言". Packages land in /run/current-system/sw/bin
// on the host and reach every container via the shared /nix/store + sysbin mount
// (S41bdf2-1-4). An empty installed set yields a module that adds nothing (so
// removing the last app converges cleanly rather than leaving a stale package).
func GenerateDropin(af AppsFile) string {
	pkgs := make([]string, 0, len(af.Installed))
	seen := map[string]bool{}
	for _, a := range af.Installed {
		if !ValidPackageAttr(a.Package) || seen[a.Package] {
			continue
		}
		seen[a.Package] = true
		pkgs = append(pkgs, "pkgs."+a.Package)
	}
	sort.Strings(pkgs)
	var b strings.Builder
	b.WriteString("{ pkgs, ... }: {\n")
	b.WriteString("  # Generated from apps.json by palmux2 (S41bdf2). One-way: edit apps in the\n")
	b.WriteString("  # GUI アプリ section, not this file. Reaches the host + every container via\n")
	b.WriteString("  # the shared /nix/store.\n")
	if len(pkgs) == 0 {
		b.WriteString("  environment.systemPackages = [ ];\n")
	} else {
		b.WriteString("  environment.systemPackages = [ " + strings.Join(pkgs, " ") + " ];\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// DropinFileName is the fixed basename written into the on-appliance flake's
// local/ drop-in dir. The "20-" prefix orders it after 10-public.nix (domain).
const DropinFileName = "20-apps.nix"

// WriteDropin writes the generated drop-in into dropinDir atomically. dropinDir is
// the on-appliance flake's local/ dir (imported via listFilesRecursive ./local).
// A best-effort mkdir handles a fresh flake dir; the real ownership/mode is set by
// the appliance state-init (which chowns local/ to the palmux user, S41bdf2).
func WriteDropin(dropinDir string, af AppsFile) error {
	if err := os.MkdirAll(dropinDir, 0o755); err != nil {
		return fmt.Errorf("apps: mkdir %s: %w", dropinDir, err)
	}
	dst := filepath.Join(dropinDir, DropinFileName)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, []byte(GenerateDropin(af)), 0o644); err != nil {
		return fmt.Errorf("apps: write %s: %w", tmp, err)
	}
	return os.Rename(tmp, dst)
}
