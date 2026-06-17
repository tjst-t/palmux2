// Package selfupdate implements the S6ab0ed GUI self-update feature: a
// declarative manifest of palmux-managed components, a long-interval GitHub
// release poller that computes per-component update-available state, and the
// one-click update execution path shared by the GUI "Update all" button and
// the `palmux update` CLI subcommand.
//
// Design (decisions.json):
//   - The manifest (manifest.json, go:embed'd) declares CORE components (the
//     palmux binary + the palmux-ws image) plus any explicitly-declared tools.
//     palmux never auto-discovers host tooling (priority_rule 7: 明示的>暗黙的).
//   - The poller reuses the GitHub Releases plumbing shape from
//     cmd/palmux/runtime.go (GITHUB_TOKEN-aware) and the runPortScan ticker
//     pattern (priority_rule 6: 既存資産活用).
//   - The privileged update is delegated to the install.sh-generated
//     ~/update-palmux2.sh (flake re-pin → home-manager switch → restart, with
//     Nix-generation rollback) rather than re-implementing a second privilege
//     mechanism. Nix-managed installs are exactly those where that script
//     exists and is executable.
package selfupdate

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed manifest.json
var manifestBytes []byte

// ComponentKind classifies how a managed component's installed version is
// probed and how its latest version is resolved.
type ComponentKind string

const (
	// KindCoreBinary is the palmux binary itself (installed = resolveVersion()).
	KindCoreBinary ComponentKind = "core-binary"
	// KindCoreImage is the palmux-ws incus image (installed = installedImageVersion()).
	KindCoreImage ComponentKind = "core-image"
	// KindTool is an explicitly-declared peripheral tool (installed = `<bin> <versionArgs...>`).
	KindTool ComponentKind = "tool"
)

// Component is one managed component declared in manifest.json.
type Component struct {
	Name        string        `json:"name"`
	Kind        ComponentKind `json:"kind"`
	Display     string        `json:"display"`
	GithubRepo  string        `json:"githubRepo"`
	Source      string        `json:"source"`
	Bin         string        `json:"bin,omitempty"`
	VersionArgs []string      `json:"versionArgs,omitempty"`
}

// Manifest is the parsed manifest.json.
type Manifest struct {
	Schema     int         `json:"schema"`
	Components []Component `json:"components"`
}

// LoadManifest parses the embedded manifest.json.
func LoadManifest() (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse self-update manifest: %w", err)
	}
	if len(m.Components) == 0 {
		return Manifest{}, fmt.Errorf("self-update manifest has no components")
	}
	return m, nil
}
