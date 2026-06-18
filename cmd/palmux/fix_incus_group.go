package main

// fix_incus_group.go — Sfef725-2: `sudo palmux fix-incus-group`.
//
// The single privileged recover verb. Its ONLY action is to restart the user
// systemd manager (`systemctl restart user@<uid>`) so a freshly-added
// incus-admin group is picked up by the palmux service the manager respawns.
//
// Security: this verb takes NO free-form input. The uid is hardcoded to the
// service user (SUDO_UID under sudo, else os.Getuid). install.sh installs a
// verb-limited NOPASSWD sudoers drop-in granting EXACTLY this verb — mirroring
// Sa53137's reconcile-system. Restarting one's own user manager is a
// low-privilege action the user can already perform.
//
// `--check` is a no-op probe that prints the resolved command and exits 0
// without restarting anything (used to verify the sudoers wiring).

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

func runFixIncusGroup(args []string) int {
	check := false
	for _, a := range args {
		switch a {
		case "--check":
			check = true
		case "-h", "--help":
			fmt.Println("Usage: sudo palmux fix-incus-group [--check]")
			fmt.Println("Restarts the user systemd manager (systemctl restart user@<uid>) so a")
			fmt.Println("freshly-added incus-admin group is applied to the palmux service.")
			fmt.Println("The only privileged recover verb; takes no free-form input.")
			return 0
		}
	}

	uid := fixServiceUID()
	target := fmt.Sprintf("user@%d", uid)

	if check {
		fmt.Printf("fix-incus-group: would run: systemctl restart %s\n", target)
		return 0
	}

	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "fix-incus-group: must run as root (use: sudo palmux fix-incus-group).")
		fmt.Fprintf(os.Stderr, "Manual equivalent: sudo systemctl restart %s\n", target)
		return 1
	}

	fmt.Printf("fix-incus-group: restarting %s to apply the incus-admin group ...\n", target)
	cmd := exec.Command("systemctl", "restart", target) //nolint:gosec // fixed verb + computed uid, no user input
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "fix-incus-group: systemctl restart %s failed: %v\n", target, err)
		return 1
	}
	fmt.Printf("fix-incus-group: %s restarted.\n", target)
	return 0
}

// fixServiceUID resolves the uid of the user whose manager must restart. Under
// sudo, SUDO_UID is the real user; otherwise os.Getuid().
func fixServiceUID() int {
	if su := os.Getenv("SUDO_UID"); su != "" {
		if n, err := strconv.Atoi(su); err == nil {
			return n
		}
	}
	return os.Getuid()
}
