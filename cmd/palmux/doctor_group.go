package main

// doctor_group.go — Sfef725-1-2: the runtime doctor's incus-admin group check
// based on the REAL running palmux service process's effective groups, not an
// `sg incus-admin` subshell.
//
// Why this exists: install.sh historically ran `palmux runtime doctor` under
// `sg incus-admin -c`, which activates the group in that subshell, so the
// doctor saw the group and reported a FALSE PASS even when the actual running
// palmux service did not have it (stale-group bug, ndev 2026-06-18). The doctor
// must look at the running service's /proc/<pid>/status Groups, falling back to
// its own process groups when no separate service process is found.

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/tjst-t/palmux2/internal/incusgroup"
)

// runningServicePID returns the pid of the running `systemd --user` palmux2
// service (the long-lived server process), or (0,false) if it cannot be found.
// It is best-effort: `systemctl --user show -p MainPID palmux2`.
func runningServicePID() (int, bool) {
	c := exec.Command("systemctl", "--user", "show", "-p", "MainPID", "--value", "palmux2") //nolint:gosec
	c.Stdin = nil
	out, err := c.Output()
	if err != nil {
		return 0, false
	}
	pidStr := strings.TrimSpace(string(out))
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// detectServiceGroupState builds an incusgroup.Detector that inspects the
// RUNNING palmux2 service process's groups (via /proc/<pid>/status) when such a
// process exists, otherwise the doctor's own process groups. This is what makes
// the doctor immune to the `sg incus-admin` subshell false-positive: even if
// the doctor itself has the group activated, it reports on the actual service.
func detectServiceGroupState() (incusgroup.Result, int, bool) {
	d := incusgroup.NewDefaultDetector()
	pid, found := runningServicePID()
	if found {
		d.ProcessGroups = func() ([]int, error) { return incusgroup.ReadProcessGroups(pid) }
	}
	return d.Detect(), pid, found
}

// doctorGroupCheck prints the incus-admin group section of `palmux runtime
// doctor` and returns whether it passed (OK or n/a count as pass; stale and
// not-member fail). It writes state-specific remedies.
func doctorGroupCheck() bool {
	res, pid, found := detectServiceGroupState()
	uid := fixServiceUID()

	switch res.State {
	case incusgroup.StateOK:
		if found {
			fmt.Printf("  ✓ incus-admin group: active on the running palmux2 service (pid %d)\n", pid)
		} else {
			fmt.Println("  ✓ incus-admin group: active on this process")
		}
		return true

	case incusgroup.StateStale:
		if found {
			fmt.Printf("  ✗ incus-admin group: the running palmux2 service (pid %d) does NOT have it yet\n", pid)
		} else {
			fmt.Println("  ✗ incus-admin group: the running palmux process does NOT have it yet")
		}
		fmt.Printf("    %s is in the incus-admin group, but the user systemd manager cached the old groups.\n", displayUserName(res.User))
		fmt.Println("    A plain `systemctl --user restart palmux2` is NOT enough (the USER MANAGER holds the stale groups).")
		fmt.Printf("    Fix (restarts the user manager — ends running tmux/Claude sessions, claude resumes with --resume):\n")
		fmt.Printf("      sudo systemctl restart user@%d\n", uid)
		fmt.Println("    or reboot / log out of all sessions and back in.")
		return false

	case incusgroup.StateNotMember:
		fmt.Println("  ✗ incus-admin group: the user is NOT a member")
		fmt.Printf("    Fix (root): sudo usermod -aG incus-admin %s\n", displayUserName(res.User))
		fmt.Println("    Then restart the user manager (or re-run install.sh):")
		fmt.Printf("      sudo systemctl restart user@%d\n", uid)
		return false

	default: // StateNotApplicable
		fmt.Println("  ✓ incus-admin group: n/a (incus not installed or no incus-admin group)")
		return true
	}
}

// displayUserName mirrors incusgroup.displayUser without exporting it.
func displayUserName(u string) string {
	if u == "" {
		return "$USER"
	}
	return u
}
