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
const rebuildUnit = "palmux-rebuild.service"

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
func TriggerRebuild(ctx context.Context) error {
	// Best-effort reset of a prior failed run; ignore its error (unit may be clean).
	_ = exec.CommandContext(ctx, systemctlBin, "reset-failed", rebuildUnit).Run()                           //nolint:errcheck // best-effort
	out, err := exec.CommandContext(ctx, systemctlBin, "start", "--no-block", rebuildUnit).CombinedOutput() //nolint:gosec // fixed unit name
	if err != nil {
		return fmt.Errorf("start %s: %w: %s", rebuildUnit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// QueryRebuild reports the current state of palmux-rebuild.service. While the
// switch is running the GUI polls this; when palmux2 itself is restarted by the
// switch the existing reconnect handshake (WS drop → /health → reconnect) covers
// the gap and the next poll lands on the post-switch palmux2.
func QueryRebuild(ctx context.Context) (RebuildStatus, error) {
	out, err := exec.CommandContext(ctx, systemctlBin, "show", "-p", "ActiveState", "-p", "Result", rebuildUnit).Output() //nolint:gosec // fixed unit name
	if err != nil {
		return RebuildStatus{}, fmt.Errorf("show %s: %w", rebuildUnit, err)
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
