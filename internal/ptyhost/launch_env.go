package ptyhost

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ensureUserBusEnv makes sure cmd's environment carries XDG_RUNTIME_DIR and
// DBUS_SESSION_BUS_ADDRESS so `systemd-run --user` can reach the caller's
// systemd --user manager over D-Bus.
//
// The S3f2658-1-1 spike (docs/sprint-logs/S3f2658/spike-S3f2658-1-1.json)
// found that an ambient shell can lack these even when the user's systemd
// --user session and D-Bus socket both exist at the conventional
// /run/user/<uid> location — so we do not assume inheritance and instead
// derive sane defaults when they are missing.
func ensureUserBusEnv(cmd *exec.Cmd) {
	env := os.Environ()
	hasRuntimeDir, hasBus := false, false
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "XDG_RUNTIME_DIR="):
			hasRuntimeDir = true
		case strings.HasPrefix(e, "DBUS_SESSION_BUS_ADDRESS="):
			hasBus = true
		}
	}
	uid := os.Getuid()
	if !hasRuntimeDir {
		env = append(env, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", uid))
	}
	if !hasBus {
		env = append(env, fmt.Sprintf("DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%d/bus", uid))
	}
	cmd.Env = env
}
