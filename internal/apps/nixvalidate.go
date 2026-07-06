package apps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

// nixBin returns the nix binary path, honouring PALMUX_NIX_BIN so a dev box
// (no real nix) can inject a fake for E2E (priority_rule 7). Default "nix".
func nixBin() string {
	if v := strings.TrimSpace(os.Getenv("PALMUX_NIX_BIN")); v != "" {
		return v
	}
	return "nix"
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
	bin := nixBin()
	if _, err := exec.LookPath(bin); err != nil {
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
