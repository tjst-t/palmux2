// Package incusgroup detects whether the running palmux process can talk to the
// incus daemon socket, which is gated by membership in the incus-admin group.
//
// The real-world bug this addresses (ndev, 2026-06-18): install.sh adds the
// service user to the incus-admin group, but the already-running `systemd
// --user` manager cached the OLD supplementary group set (from before the add),
// so the palmux process it spawned lacks the group. A plain `systemctl --user
// restart palmux2` does NOT fix it — only restarting the user manager
// (`systemctl restart user@<uid>`) / re-login / reboot picks up the new group.
//
// Three states are distinguished:
//
//	StateOK         — the running process has the incus-admin group → switches work.
//	StateStale      — the USER is in incus-admin but the running PROCESS is not
//	                  → needs a user-manager restart (Story 2 recover button).
//	StateNotMember  — the user is not in incus-admin at all → needs `usermod`.
//	StateNotApplicable — incus is not installed, or there is no incus-admin group
//	                  on this host → the condition is meaningless here.
//
// Detection reads only the running process's own effective groups (it IS the
// process that talks to the incus socket) plus the system group database. It is
// host-side and independent of any container.
package incusgroup

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
)

// State is the classified incus-admin group state for the running palmux
// process on this host.
type State string

const (
	// StateOK — the running palmux process holds the incus-admin group.
	StateOK State = "ok"
	// StateStale — the user is a member of incus-admin, but the running process
	// does not have it (cached old groups). Needs a user-manager restart.
	StateStale State = "stale"
	// StateNotMember — the user is not a member of incus-admin at all. Needs
	// `sudo usermod -aG incus-admin <user>`.
	StateNotMember State = "not-member"
	// StateNotApplicable — incus is not installed or no incus-admin group exists
	// on this host; the condition does not apply.
	StateNotApplicable State = "n/a"
)

// Result is the structured detection outcome, suitable for JSON exposure and
// for rendering doctor remedies / the GUI recover affordance.
type Result struct {
	State State `json:"state"`
	// GID is the resolved incus-admin group id, or -1 when the group does not
	// exist / could not be resolved.
	GID int `json:"gid"`
	// User is the username whose membership was checked (the service user).
	User string `json:"user,omitempty"`
	// Remedy is a short, state-specific machine/UX hint key plus human text.
	Remedy string `json:"remedy"`
	// Detail is a longer human explanation for doctor output and tooltips.
	Detail string `json:"detail"`
}

// Detector resolves the three inputs needed to classify the state. The fields
// are injectable so acceptance tests can construct each of the three states
// deterministically against the real binary (varying the gid / membership /
// process-group inputs) rather than faking the whole flow.
type Detector struct {
	// IncusInstalled reports whether the incus binary is on PATH. When false the
	// state is StateNotApplicable.
	IncusInstalled func() bool
	// ResolveGID returns the incus-admin group's gid and whether it exists.
	ResolveGID func() (gid int, exists bool)
	// UserInGroup reports whether the service user is a member of incus-admin
	// per the system group database (getent / os/user), independent of the
	// running process's effective groups.
	UserInGroup func(gid int) (member bool, username string)
	// ProcessGroups returns the running palmux process's effective supplementary
	// group ids (os.Getgroups for self).
	ProcessGroups func() ([]int, error)
}

// NewDefaultDetector returns a Detector wired to the real host: incus on PATH,
// getent group incus-admin for gid, os/user for membership, os.Getgroups for
// the running process's effective groups.
func NewDefaultDetector() *Detector {
	return &Detector{
		IncusInstalled: func() bool {
			_, err := exec.LookPath("incus")
			return err == nil
		},
		ResolveGID:  resolveIncusAdminGID,
		UserInGroup: userInGroup,
		// Include the primary gid alongside the supplementary groups: POSIX does
		// not guarantee os.Getgroups returns the effective/primary gid, so a
		// process whose PRIMARY group is incus-admin would otherwise be
		// misclassified as stale.
		ProcessGroups: func() ([]int, error) {
			g, err := os.Getgroups()
			if err != nil {
				return nil, err
			}
			return append(g, os.Getegid()), nil
		},
	}
}

const groupName = "incus-admin"

// Detect classifies the incus-admin group state for the running process.
func (d *Detector) Detect() Result {
	if d.IncusInstalled != nil && !d.IncusInstalled() {
		return Result{
			State:  StateNotApplicable,
			GID:    -1,
			Remedy: "install-incus",
			Detail: "incus is not installed on this host; the incus-admin group condition does not apply.",
		}
	}

	gid, exists := d.ResolveGID()
	if !exists {
		return Result{
			State:  StateNotApplicable,
			GID:    -1,
			Remedy: "no-group",
			Detail: "no incus-admin group exists on this host (incus may be installed differently); the condition does not apply.",
		}
	}

	member, username := d.UserInGroup(gid)
	if !member {
		return Result{
			State:  StateNotMember,
			GID:    gid,
			User:   username,
			Remedy: "usermod",
			Detail: fmt.Sprintf("%s is not a member of the incus-admin group. Add the user (root): sudo usermod -aG incus-admin %s — then re-run install.sh or apply the user-manager restart. Until then, switching a Workspace to incus-container fails and falls back to host.", displayUser(username), displayUser(username)),
		}
	}

	procGroups, err := d.ProcessGroups()
	if err != nil {
		// Cannot read our own groups — treat conservatively as unknown/stale so
		// the user is prompted rather than silently assuming OK.
		return Result{
			State:  StateStale,
			GID:    gid,
			User:   username,
			Remedy: "restart-user-manager",
			Detail: fmt.Sprintf("could not read the running process's effective groups (%v); assuming the incus-admin group is not yet active. Restart the user manager: sudo systemctl restart user@<uid>.", err),
		}
	}
	for _, g := range procGroups {
		if g == gid {
			return Result{
				State:  StateOK,
				GID:    gid,
				User:   username,
				Remedy: "none",
				Detail: "the running palmux process has the incus-admin group; incus-container switches can reach the incus daemon socket.",
			}
		}
	}

	// User is a member, but the running process does not have the group: stale.
	return Result{
		State:  StateStale,
		GID:    gid,
		User:   username,
		Remedy: "restart-user-manager",
		Detail: fmt.Sprintf("%s is in the incus-admin group, but the running palmux process does not have it yet (the user manager cached the old groups). A plain `systemctl --user restart palmux2` is NOT enough — restart the user manager (`sudo systemctl restart user@<uid>`), re-login, or reboot. Until then, switching a Workspace to incus-container fails and falls back to host.", displayUser(username)),
	}
}

// displayUser returns the username or "$USER" when unknown.
func displayUser(u string) string {
	if u == "" {
		return "$USER"
	}
	return u
}

// resolveIncusAdminGID resolves the incus-admin group's gid via os/user
// (which reads getent / the system group database).
func resolveIncusAdminGID() (int, bool) {
	g, err := user.LookupGroup(groupName)
	if err != nil {
		return -1, false
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return -1, false
	}
	return gid, true
}

// userInGroup reports whether the current (service) user is a member of the
// group with the given gid per the system group database — NOT the running
// process's effective groups. It checks both the user's primary gid and the
// group's listed members.
func userInGroup(gid int) (bool, string) {
	u, err := user.Current()
	if err != nil {
		return false, ""
	}
	username := u.Username
	// Primary gid match.
	if pg, perr := strconv.Atoi(u.Gid); perr == nil && pg == gid {
		return true, username
	}
	// Supplementary groups recorded for the user in the group DB.
	gidStrs, err := u.GroupIds()
	if err == nil {
		want := strconv.Itoa(gid)
		for _, gs := range gidStrs {
			if gs == want {
				return true, username
			}
		}
	}
	return false, username
}

// ProcessGroupsFromStatus parses a /proc/<pid>/status file content and returns
// the process's group ids: the supplementary groups (Groups: line) PLUS the
// effective primary gid (the 2nd field of the Gid: line). Including the primary
// gid avoids misclassifying a process whose primary group is incus-admin as
// stale. Exposed so a detector can be pointed at another process's
// /proc/<pid>/status (e.g. an explicit service pid) instead of the running self.
func ProcessGroupsFromStatus(statusContent string) ([]int, error) {
	var (
		out      []int
		haveSupp bool
	)
	sc := bufio.NewScanner(strings.NewReader(statusContent))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "Groups:"):
			fields := strings.Fields(strings.TrimPrefix(line, "Groups:"))
			for _, f := range fields {
				n, err := strconv.Atoi(f)
				if err != nil {
					return nil, fmt.Errorf("parse group id %q: %w", f, err)
				}
				out = append(out, n)
			}
			haveSupp = true
		case strings.HasPrefix(line, "Gid:"):
			// Gid:\treal\teffective\tsaved\tfs — use the effective gid (index 1).
			fields := strings.Fields(strings.TrimPrefix(line, "Gid:"))
			if len(fields) >= 2 {
				if n, err := strconv.Atoi(fields[1]); err == nil {
					out = append(out, n)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan /proc status: %w", err)
	}
	if !haveSupp {
		return nil, fmt.Errorf("no Groups: line in /proc status")
	}
	return out, nil
}

// ReadProcessGroups reads /proc/<pid>/status and returns the process's
// supplementary group ids. Used when a detector must inspect a specific pid
// (e.g. an explicit running palmux service pid) rather than the calling
// process itself.
func ReadProcessGroups(pid int) ([]int, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return nil, fmt.Errorf("read /proc/%d/status: %w", pid, err)
	}
	return ProcessGroupsFromStatus(string(b))
}
