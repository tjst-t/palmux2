package selfupdate

import (
	"context"
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
	Latest    string `json:"latest"`    // "" when GitHub unreachable for this component
	Available bool   `json:"available"` // installed < latest
}

// Snapshot is the aggregate detection result.
type Snapshot struct {
	Components []ComponentStatus `json:"components"`
	Available  bool              `json:"available"`  // any component has an update
	NixManaged bool              `json:"nixManaged"` // ~/update-palmux2.sh present (one-click possible)
	CheckedAt  string            `json:"checkedAt"`  // RFC3339; "" if never checked
	Degraded   bool              `json:"degraded"`   // GitHub unreachable / rate-limited this cycle
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

// Detect computes a fresh detection Snapshot for every manifest component by
// probing installed versions and resolving each component's latest GitHub
// release tag. GitHub failures degrade gracefully per-component (Latest left
// "", Degraded set) rather than failing the whole snapshot (decisions PD-3).
func Detect(ctx context.Context, m Manifest, probes InstalledProbes, nixManaged bool) Snapshot {
	snap := Snapshot{
		NixManaged: nixManaged,
		CheckedAt:  time.Now().UTC().Format(time.RFC3339),
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
			tag, err := LatestTag(ctx, repo)
			if err == nil {
				snap.Components[i].Latest = tag
				snap.Components[i].Available = UpdateAvailable(snap.Components[i].Installed, tag)
			}
		}(i, c.GithubRepo)
	}
	wg.Wait()
	for _, cs := range snap.Components {
		if cs.Latest == "" {
			snap.Degraded = true
		}
		if cs.Available {
			snap.Available = true
		}
	}
	return snap
}
