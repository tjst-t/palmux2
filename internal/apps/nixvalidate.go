package apps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ValidateResult is the outcome of validating a user-defined nixpkgs package name
// BEFORE any rebuild (S41bdf2-1-5). Exactly one of Valid / Invalid / Unavailable
// is meaningful; the GUI renders each distinctly (✓ / ⚠ / "検証不可").
type ValidateResult struct {
	Package     string `json:"package"`
	Valid       bool   `json:"valid"`       // attr resolved in nixpkgs
	Unavailable bool   `json:"unavailable"` // nix binary not present → cannot validate
	Message     string `json:"message"`
}

// nixBin returns the configured nix binary candidate, honouring PALMUX_NIX_BIN so a
// dev box (no real nix) can inject a fake for E2E (priority_rule 7). Default "nix".
func nixBin() string {
	if v := strings.TrimSpace(os.Getenv("PALMUX_NIX_BIN")); v != "" {
		return v
	}
	return "nix"
}

// nixFallbackPaths are well-known absolute nix locations tried when the default
// "nix" is NOT on PATH (AC-S41bdf2-4-1). On a real NixOS appliance palmux2 runs as a
// systemd service whose PATH is restricted (tmux/git/ghq/gwq/incus — palmux.nix), so
// a plain LookPath("nix") fails even though nix exists at the system profile; without
// this fallback every nixpkgs validation would wrongly report "Unavailable". It is a
// package var so a unit test can inject a stubbed candidate (priority_rule 7).
var nixFallbackPaths = defaultNixFallbackPaths

func defaultNixFallbackPaths() []string {
	paths := []string{
		"/run/current-system/sw/bin/nix",        // NixOS system profile
		"/nix/var/nix/profiles/default/bin/nix", // multi-user default profile
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".nix-profile", "bin", "nix"))
	}
	return paths
}

// resolveNixBin returns an absolute, runnable nix binary path, or "" if none is
// found. The configured candidate (default "nix", or PALMUX_NIX_BIN) is resolved via
// PATH first; when it is the default "nix" and not on PATH, the well-known absolute
// locations are tried in order (the restricted systemd-service PATH case). A
// non-default override is honoured exactly — no fallback — so an E2E fake is never
// shadowed by a real system nix (priority_rule 7).
func resolveNixBin() string {
	bin := nixBin()
	if p, err := exec.LookPath(bin); err == nil {
		return p
	}
	if bin != "nix" {
		return "" // explicit override that did not resolve — do not second-guess it
	}
	for _, cand := range nixFallbackPaths() {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	return ""
}

// ValidatePackage checks that `pkgs.<name>` resolves in nixpkgs by evaluating its
// `.name` attribute (cheap — evaluates, does not build). A non-zero exit means the
// attr does not exist → Invalid (no rebuild is allowed). If the nix binary is not
// on PATH the result is Unavailable (never a silent "valid"). The name is charset-
// guarded first so nothing untrusted reaches the nix CLI.
func ValidatePackage(ctx context.Context, name string) ValidateResult {
	name = strings.TrimSpace(name)
	res := ValidateResult{Package: name}
	if name == "" {
		res.Message = "パッケージ名が空です"
		return res
	}
	if !ValidPackageAttr(name) {
		res.Message = "パッケージ名に使えない文字が含まれています（[A-Za-z0-9._-] のみ）"
		return res
	}
	bin := resolveNixBin()
	if bin == "" {
		res.Unavailable = true
		res.Message = "nix が見つからないため検証できません（NixOS アプライアンス上でのみ検証されます）"
		return res
	}
	// `nix eval --raw nixpkgs#<name>.name` evaluates the derivation name attr
	// without realising it. flakes/nix-command are enabled on the appliance; pass
	// the flags defensively so a minimally-configured nix still evaluates.
	cmd := exec.CommandContext(ctx, bin, "eval", "--raw",
		"--extra-experimental-features", "nix-command flakes",
		"nixpkgs#"+name+".name")
	out, err := cmd.CombinedOutput()
	if err != nil {
		res.Message = fmt.Sprintf("nixpkgs に %q が見つかりません", name)
		return res
	}
	res.Valid = true
	res.Message = fmt.Sprintf("nixpkgs#%s 解決 OK (%s)", name, strings.TrimSpace(string(out)))
	return res
}
