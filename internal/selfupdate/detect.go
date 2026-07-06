package selfupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ComponentStatus is the per-component detection result surfaced to the GUI
// panel and the `palmux update --check` CLI.
type ComponentStatus struct {
	Name      string `json:"name"`
	Display   string `json:"display"`
	Source    string `json:"source"`
	Kind      string `json:"kind"`
	Installed string `json:"installed"` // "" when not installed / unknown
	Latest    string `json:"latest"`    // "" when GitHub unreachable / source has no releases
	Available bool   `json:"available"` // installed < latest (always false when !Fetchable)
	// Fetchable is false when this component's latest version could not be
	// resolved from its source — either a transient GitHub failure OR a source
	// that has no releases at all (e.g. tjst-t/gwq → 404 no releases). Such a
	// component must NOT light the "update available" badge (Sa8e7d0-2-2); the
	// GUI shows it as "取得不可" rather than "最新" or "更新あり".
	Fetchable bool `json:"fetchable"`

	// transientFail (unexported, not serialized) records that THIS cycle's fetch
	// failed transiently (rate-limit / network), as opposed to a stable "no
	// releases" 404. Only transient failures flag the snapshot Degraded, so a
	// permanently release-less source does not show the rate-limit banner forever
	// (Sa8e7d0-2-2).
	transientFail bool
}

// Snapshot is the aggregate detection result.
type Snapshot struct {
	Components []ComponentStatus `json:"components"`
	Available  bool              `json:"available"`  // any component has an update
	NixManaged bool              `json:"nixManaged"` // ~/update-palmux2.sh present (one-click possible)
	// NixOSHost is true when palmux2 runs on a NixOS system (the palmuxOS
	// appliance, Sb14caa). On NixOS the in-app one-click "Update all" does NOT
	// apply — the host is updated by `nixos-rebuild switch` (atomic generation
	// swap + free rollback). S673a42 wires a GUI-kicked version of that switch, so
	// the badge now maps to an update BUTTON on the appliance too (not just
	// guidance).
	NixOSHost bool `json:"nixOSHost"`
	// ApplianceFlakeTarget is the exact `nixos-rebuild --flake <target>` argument
	// for THIS appliance (e.g. "/persist/palmux/nixos#appliance"). Non-empty only
	// on a NixOS host. The GUI/README display this instead of hardcoding the path
	// (S673a42-1): a single backend-sourced value that cannot drift from the real
	// on-appliance flake dir. MUST stay in sync with nixos/modules/appliance.nix
	// `flakeDir` (see ApplianceFlakeDir below).
	ApplianceFlakeTarget string `json:"applianceFlakeTarget"`
	CheckedAt            string `json:"checkedAt"` // RFC3339; "" if never checked
	Degraded             bool   `json:"degraded"`  // GitHub unreachable / rate-limited this cycle
	// Forced is true when this snapshot's "update available" was synthesized by
	// the env-gated force-update test affordance (force.go) rather than a real
	// newer release. Lets the GUI mark the badge/panel as a test run. Always false
	// in production (PALMUX_SELFUPDATE_FORCE unset).
	Forced bool `json:"forced"`
}

// ApplianceFlakeDir is the on-appliance flake directory the palmuxOS box updates
// itself from: `cd <dir> && nixos-rebuild switch --flake .#appliance`. It is the
// single source of truth the GUI/README render (via Snapshot.ApplianceFlakeTarget)
// so the displayed command is always copy-paste-correct. This constant MUST equal
// `flakeDir` in nixos/modules/appliance.nix (which actually runs the switch); the
// two live in different languages (Go vs Nix) so they cannot be one literal, but
// a change to one without the other is a bug. The historical hardcoded FE string
// was the wrong `/etc/palmux#appliance` — the exact drift this const removes.
const (
	ApplianceFlakeDir    = "/persist/palmux/nixos"
	ApplianceFlakeTarget = ApplianceFlakeDir + "#appliance"
)

// nixosMarkers are the paths whose presence identifies a NixOS host. /etc/NIXOS
// is the canonical NixOS marker (also what nixos-rebuild itself checks);
// /etc/os-release carrying `ID=nixos` is the secondary signal. Both are package
// vars so tests can point them at a fixture without touching the real /etc.
var (
	nixosMarkerPath = "/etc/NIXOS"
	osReleasePath   = "/etc/os-release"
)

// IsNixOSHost reports whether this machine is a NixOS system. Exported so other
// packages (e.g. the deploy plane) can switch the privileged apply path from
// `palmux reconcile-system` to a GUI-kicked `nixos-rebuild switch` without
// duplicating the marker-file detection.
func IsNixOSHost() bool { return detectNixOSHost() }

// detectNixOSHost reports whether this machine is a NixOS system.
func detectNixOSHost() bool {
	if _, err := os.Stat(nixosMarkerPath); err == nil {
		return true
	}
	b, err := os.ReadFile(osReleasePath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "ID=nixos" || line == `ID="nixos"` {
			return true
		}
	}
	return false
}

// InstalledProbes injects how installed versions are read. This keeps the
// selfupdate package free of cmd/palmux internals (no import cycle). BinVersion
// returns the running palmux binary's version (resolveVersion()); ImageVersion
// returns the installed palmux-ws image version (installedImageVersion()).
type InstalledProbes struct {
	BinVersion   func() string
	ImageVersion func() string
}

// detectInstalled resolves the installed version for one component.
func detectInstalled(c Component, probes InstalledProbes) string {
	switch c.Kind {
	case KindCoreBinary:
		if probes.BinVersion != nil {
			return probes.BinVersion()
		}
	case KindCoreImage:
		if probes.ImageVersion != nil {
			return probes.ImageVersion()
		}
	case KindTool:
		return toolVersion(c.Bin, c.VersionArgs)
	}
	return ""
}

// toolVersionTokenRe extracts the first dotted version token (optionally
// v-prefixed) from a tool's `--version` output, e.g. "v1.2.3" / "0.8.0". It
// requires at least one dot so a leading bare integer (a date/year/banner like
// "2024 ..." or "0 errors") is not mistaken for the version. (S6ab0ed)
var toolVersionTokenRe = regexp.MustCompile(`v?\d+\.\d+(?:\.\d+)?`)

// toolVersion runs `<bin> <args...>` and returns the first dotted version token
// from its output (e.g. "gwq version v0.8.0" → "v0.8.0"), or "" if the binary
// is absent, errors, or prints no version-looking token.
func toolVersion(bin string, args []string) string {
	if bin == "" {
		return ""
	}
	if _, err := exec.LookPath(bin); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput() //nolint:gosec // bin/args are from the embedded manifest, not user input
	if err != nil && len(out) == 0 {
		return ""
	}
	if tok := toolVersionTokenRe.FindString(string(out)); tok != "" {
		return tok
	}
	return strings.TrimSpace(string(out))
}

// latestTagFn resolves a repo's latest release tag. It is a var so tests can
// inject deterministic results (no live GitHub dependency, which is rate-limited
// for unauthenticated callers and would make classification tests flaky).
var latestTagFn = LatestTag

// Detect computes a fresh detection Snapshot for every manifest component by
// probing installed versions and resolving each component's latest GitHub
// release tag. GitHub failures degrade gracefully per-component (Latest left
// "", Degraded set) rather than failing the whole snapshot (decisions PD-3).
func Detect(ctx context.Context, m Manifest, probes InstalledProbes, nixManaged bool) Snapshot {
	snap := Snapshot{
		NixManaged: nixManaged,
		NixOSHost:  detectNixOSHost(),
		CheckedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	// Surface the exact appliance flake target for the GUI/README to render
	// (S673a42-1), but only on a NixOS host — off-appliance it is meaningless.
	if snap.NixOSHost {
		snap.ApplianceFlakeTarget = ApplianceFlakeTarget
	}
	// Probe installed versions + resolve each component's latest tag. The
	// GitHub fetches are independent and I/O-bound, so fan them out — one slow
	// repo no longer serialises behind the others (worst case = one timeout, not
	// N × timeout). Results are written by index so order is preserved.
	snap.Components = make([]ComponentStatus, len(m.Components))
	var wg sync.WaitGroup
	for i, c := range m.Components {
		snap.Components[i] = ComponentStatus{
			Name:      c.Name,
			Display:   c.Display,
			Source:    c.Source,
			Kind:      string(c.Kind),
			Installed: detectInstalled(c, probes),
		}
		wg.Add(1)
		go func(i int, repo string) {
			defer wg.Done()
			tag, err := latestTagFn(ctx, repo)
			switch {
			case err == nil && strings.TrimSpace(tag) != "":
				snap.Components[i].Latest = tag
				snap.Components[i].Fetchable = true
				snap.Components[i].Available = UpdateAvailable(snap.Components[i].Installed, tag)
			case errors.As(err, new(*NoReleasesError)):
				// Stable "no releases" fact (e.g. gwq): un-fetchable, but NOT a
				// transient degrade. Leave hasTransientFailure unset so the
				// rate-limit/unreachable banner is not falsely shown forever.
			default:
				// Transient failure (rate-limit / network / decode): un-fetchable
				// AND degrade this cycle so the banner explains it.
				snap.Components[i].transientFail = true
			}
		}(i, c.GithubRepo)
	}
	wg.Wait()
	for _, cs := range snap.Components {
		if cs.transientFail {
			snap.Degraded = true
		}
		if cs.Available {
			snap.Available = true
		}
	}
	return snap
}
