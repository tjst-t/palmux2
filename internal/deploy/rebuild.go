// rebuild.go — GUI/CLI-kickable `nixos-rebuild switch` for the NixOS appliance.
//
// palmux2 runs as the non-root `palmux` user, which on the appliance has no
// password and is not in `wheel` (the image ships key-zero / password-auth-off),
// so it cannot run `nixos-rebuild` or `sudo` directly. The appliance NixOS module
// instead defines a ROOT system oneshot `palmux-rebuild.service` (which runs
// `nixos-rebuild switch --flake /persist/palmux/nixos#appliance`) and grants the
// palmux user permission to START it over the system bus via a polkit rule. So the
// privileged "apply public domain / TLS" step is a plain `systemctl start` here —
// no sudo, no password. Running in its OWN cgroup (not as a palmux2 child) means
// the switch restarting palmux2.service does not kill the rebuild mid-flight (the
// Sa8e7d0 self-update lesson, applied to the OS).
package deploy

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// systemctlBin drives the system-level palmux-rebuild unit. A var so tests can
// stub it with a fake systemctl.
var systemctlBin = "systemctl"

// rebuildUnit is the fixed system oneshot defined by nixos/modules/appliance.nix.
// It applies the CURRENT flake pin (domain/TLS drop-in projection + switch) —
// used by the deploy panel's "apply public domain/TLS" button.
const rebuildUnit = "palmux-rebuild.service"

// rebuildUpdateUnit is the SIBLING fixed oneshot (also nixos/modules/appliance.nix)
// used for VERSION updates: it runs `nix flake update palmux` (bump the pin to
// latest main) BEFORE the same switch logic. It is a distinct fixed unit — not an
// argument to rebuildUnit — so the polkit authorization stays verb-limited (the
// palmux user may only `start` these two named units; no arbitrary command reaches
// root). S673a42-2.
const rebuildUpdateUnit = "palmux-rebuild-update.service"

// RebuildStatus is the live state of the palmux-rebuild oneshot, parsed from
// `systemctl show`. Running is a convenience = ActiveState in {activating,reloading}.
type RebuildStatus struct {
	Active  string `json:"active"`  // systemd ActiveState: inactive|activating|active|deactivating|failed
	Result  string `json:"result"`  // systemd Result: success|exit-code|... ("" while running)
	Running bool   `json:"running"` // true while the switch is in progress
}

// TriggerRebuild starts palmux-rebuild.service WITHOUT blocking (the switch can
// take minutes and restarts palmux2 mid-way). It first clears any prior failed
// state so a re-trigger after a failed run is not refused by systemd.
func TriggerRebuild(ctx context.Context) error { return startUnit(ctx, rebuildUnit) }

// TriggerRebuildUpdate starts the VERSION-update oneshot (rebuildUpdateUnit),
// which bumps the palmux pin (`nix flake update palmux`) before switching. Same
// no-block + reset-failed semantics as TriggerRebuild. S673a42-2.
func TriggerRebuildUpdate(ctx context.Context) error { return startUnit(ctx, rebuildUpdateUnit) }

// RebuildUpdateLoaded reports whether palmux-rebuild-update.service (the GUI
// version-update oneshot) is defined in the running NixOS generation. It is false
// on the "bootstrap gap": when the running palmux binary is NEWER than the
// deployed generation, that generation predates S673a42 and defines neither this
// unit nor the polkit rule that lets the non-root palmux user start it — so a
// `systemctl start` would fail with an opaque polkit "Access denied". The GUI /
// handler pre-flight this so they can surface actionable guidance (do one manual
// `nixos-rebuild switch` first) instead of the raw denial. A non-nil error means
// the LoadState could not be read (e.g. no systemd) — callers treat that as
// "unknown" and do NOT block on it.
func RebuildUpdateLoaded(ctx context.Context) (bool, error) {
	return unitLoaded(ctx, rebuildUpdateUnit)
}

// RebuildLoaded is the RebuildUpdateLoaded counterpart for palmux-rebuild.service
// (the domain/TLS apply oneshot). Same bootstrap-gap semantics.
func RebuildLoaded(ctx context.Context) (bool, error) { return unitLoaded(ctx, rebuildUnit) }

// unitLoaded reads `systemctl show -p LoadState <unit>` and reports whether the
// unit is present (LoadState=loaded). A "not-found" LoadState → false, no error;
// only a systemctl exec failure returns an error.
func unitLoaded(ctx context.Context, unit string) (bool, error) {
	out, err := exec.CommandContext(ctx, systemctlBin, "show", "-p", "LoadState", unit).Output() //nolint:gosec // fixed unit name
	if err != nil {
		return false, fmt.Errorf("show %s LoadState: %w", unit, err)
	}
	_, v, _ := strings.Cut(strings.TrimSpace(string(out)), "=")
	return v == "loaded", nil
}

// startUnit resets any prior failed state and starts a fixed unit --no-block.
func startUnit(ctx context.Context, unit string) error {
	// Best-effort reset of a prior failed run; ignore its error (unit may be clean).
	_ = exec.CommandContext(ctx, systemctlBin, "reset-failed", unit).Run()                           //nolint:errcheck // best-effort
	out, err := exec.CommandContext(ctx, systemctlBin, "start", "--no-block", unit).CombinedOutput() //nolint:gosec // fixed unit name
	if err != nil {
		return fmt.Errorf("start %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// QueryRebuild reports the current state of palmux-rebuild.service. While the
// switch is running the GUI polls this; when palmux2 itself is restarted by the
// switch the existing reconnect handshake (WS drop → /health → reconnect) covers
// the gap and the next poll lands on the post-switch palmux2.
func QueryRebuild(ctx context.Context) (RebuildStatus, error) { return queryUnit(ctx, rebuildUnit) }

// QueryRebuildUpdate reports the state of the version-update oneshot. The GUI
// polls this to catch a failure that does NOT restart palmux2 (e.g. a `nix flake
// update` / eval error aborts before the switch), which the WS-drop reconnect
// handshake alone could only surface via timeout. S673a42-2.
func QueryRebuildUpdate(ctx context.Context) (RebuildStatus, error) {
	return queryUnit(ctx, rebuildUpdateUnit)
}

func queryUnit(ctx context.Context, unit string) (RebuildStatus, error) {
	out, err := exec.CommandContext(ctx, systemctlBin, "show", "-p", "ActiveState", "-p", "Result", unit).Output() //nolint:gosec // fixed unit name
	if err != nil {
		return RebuildStatus{}, fmt.Errorf("show %s: %w", unit, err)
	}
	return parseRebuildShow(string(out)), nil
}

// parseRebuildShow parses `systemctl show -p ActiveState -p Result` key=value lines.
func parseRebuildShow(s string) RebuildStatus {
	var st RebuildStatus
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			st.Active = v
		case "Result":
			st.Result = v
		}
	}
	st.Running = st.Active == "activating" || st.Active == "reloading" || st.Active == "deactivating"
	return st
}
