// Package apps implements the S41bdf2 "1アプリ=1カード" app model: a curated
// catalog of known CLI apps plus user-defined nixpkgs packages, each installable
// (→ generated home.packages/systemPackages drop-in → nixos-rebuild, reaching the
// host AND every container via the shared /nix/store) and optionally sharing its
// auth folder into every Workspace container (→ Sd44947 shared_dirs, hot).
//
// Design authority: docs/palmux2-nixos-appliance-design.md §7 / §8.2 / §8.3.
// Install state is persisted to a dedicated store (<configDir>/apps.json) so it
// never collides with the deploy plane's config.toml writer. Share state is NOT
// stored here — it is DERIVED from [workspace].shared_dirs so the app card's share
// toggle and the generic 共有フォルダ list are a single source (AC-S41bdf2-2-1).
package apps

// CatalogEntry is one known app in the curated catalog. The mapping id→{package,
// authPath} is palmux's curation convention (not a self-declaration by Nix or the
// app); custom user apps carry the same fields but originate from user input.
type CatalogEntry struct {
	ID          string `json:"id"`          // stable card id, also the URL/testid slug
	Display     string `json:"display"`     // human name shown on the card
	Description string `json:"description"` // one-line description
	Icon        string `json:"icon"`        // emoji shown in the card header
	Package     string `json:"package"`     // nixpkgs attr path (e.g. "_1password-cli")
	AuthPath    string `json:"authPath"`    // auth folder candidate for the share toggle ("" = no share)
}

// catalog is the built-in known-app list (AC-S41bdf2-1-2). The auth-folder paths
// use ~ (expanded against $HOME by the share path validator). 1Password's nixpkgs
// attr starts with an underscore because Nix attrs cannot begin with a digit.
var catalog = []CatalogEntry{
	{
		ID:          "infisical",
		Display:     "Infisical",
		Description: "シークレット管理 CLI。infisical run で env を注入。",
		Icon:        "🔐",
		Package:     "infisical",
		AuthPath:    "~/.infisical",
	},
	{
		ID:          "1password-cli",
		Display:     "1Password CLI",
		Description: "1Password の op CLI。ボールト参照を env に展開。",
		Icon:        "🔑",
		Package:     "_1password-cli",
		AuthPath:    "~/.config/op",
	},
	{
		ID:          "gh",
		Display:     "GitHub CLI",
		Description: "GitHub の gh CLI。PR・issue 操作。",
		Icon:        "🐙",
		Package:     "gh",
		AuthPath:    "~/.config/gh",
	},
	{
		ID:          "awscli2",
		Display:     "AWS CLI",
		Description: "AWS の aws CLI v2。",
		Icon:        "☁️",
		Package:     "awscli2",
		AuthPath:    "~/.aws",
	},
}

// Catalog returns a copy of the built-in catalog.
func Catalog() []CatalogEntry {
	out := make([]CatalogEntry, len(catalog))
	copy(out, catalog)
	return out
}

// catalogByID looks up a built-in catalog entry.
func catalogByID(id string) (CatalogEntry, bool) {
	for _, e := range catalog {
		if e.ID == id {
			return e, true
		}
	}
	return CatalogEntry{}, false
}
