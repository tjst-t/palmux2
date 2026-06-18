package server

// handler_incusgroup.go — Sfef725: incus-admin stale-group detection + GUI
// click-recover.
//
// Endpoints:
//
//	GET  /api/incus-group        — host-global detection state (Story 1).
//	                               {state, gid, user, remedy, detail,
//	                                fixAvailable, restartCommand}
//	POST /api/incus-group/fix    — trigger the privileged recover verb
//	                               (`sudo palmux fix-incus-group`) which restarts
//	                               the user manager. The server is restarted with
//	                               it; the GUI observes completion via the
//	                               S6ab0ed WS-drop → /api/health reconnect
//	                               handshake (Story 2).
//
// Detection is privilege-free and host-global (not per-branch): the running
// palmux server process IS the process that talks to the incus socket.

import (
	"net/http"
	"os"
	"os/exec"

	"github.com/tjst-t/palmux2/internal/incusgroup"
)

// incusGroupResponse is the body for GET /api/incus-group. It augments the pure
// detection Result with deployment facts the FE needs to decide whether to show
// the recover button vs. a manual command.
type incusGroupResponse struct {
	incusgroup.Result
	// FixAvailable is true when the privileged recover verb is wired (the
	// `sudo palmux fix-incus-group` sudoers drop-in is installed). When false the
	// FE shows the manual `sudo systemctl restart user@<uid>` command instead.
	FixAvailable bool `json:"fixAvailable"`
	// RestartCommand is the exact manual command to apply the group (shown when
	// FixAvailable is false, and as the explanatory subtext otherwise).
	RestartCommand string `json:"restartCommand,omitempty"`
}

// detectIncusGroup runs the default detector against the running process. It is
// a package-level var so tests can stub it deterministically (the 3 states are
// constructed by varying the detector inputs, per test-discipline Rule 7).
//
// E2E seam (dev rig only): PALMUX_INCUS_GROUP_FAKE_STATE forces the detector's
// INPUTS so the real HTTP endpoint + real binary classify a chosen state. It
// does NOT bypass the classifier — it varies the gid/membership/process-group
// inputs exactly as the unit test does, which the sprint prompt explicitly
// endorses ("vary the gid/membership inputs, not faking the whole flow"). Never
// set in production.
var detectIncusGroup = func() incusgroup.Result {
	if forced := os.Getenv("PALMUX_INCUS_GROUP_FAKE_STATE"); forced != "" {
		return fakeStateDetector(forced).Detect()
	}
	return incusgroup.NewDefaultDetector().Detect()
}

// fakeStateDetector builds a Detector whose injected inputs produce the named
// state when run through the real classifier. Used only by the E2E seam.
func fakeStateDetector(state string) *incusgroup.Detector {
	const gid = 994
	switch state {
	case "ok":
		return &incusgroup.Detector{
			IncusInstalled: func() bool { return true },
			ResolveGID:     func() (int, bool) { return gid, true },
			UserInGroup:    func(int) (bool, string) { return true, "ubuntu" },
			ProcessGroups:  func() ([]int, error) { return []int{4, gid, 1000}, nil },
		}
	case "stale":
		return &incusgroup.Detector{
			IncusInstalled: func() bool { return true },
			ResolveGID:     func() (int, bool) { return gid, true },
			UserInGroup:    func(int) (bool, string) { return true, "ubuntu" },
			ProcessGroups:  func() ([]int, error) { return []int{4, 1000}, nil }, // missing gid
		}
	case "not-member":
		return &incusgroup.Detector{
			IncusInstalled: func() bool { return true },
			ResolveGID:     func() (int, bool) { return gid, true },
			UserInGroup:    func(int) (bool, string) { return false, "ubuntu" },
			ProcessGroups:  func() ([]int, error) { return []int{4, 1000}, nil },
		}
	default: // "n/a"
		return &incusgroup.Detector{
			IncusInstalled: func() bool { return false },
			ResolveGID:     func() (int, bool) { return -1, false },
			UserInGroup:    func(int) (bool, string) { return false, "" },
			ProcessGroups:  func() ([]int, error) { return nil, nil },
		}
	}
}

// fixVerbAvailable reports whether the privileged recover verb can be invoked:
// the `sudo` binary exists and a non-interactive `sudo -n palmux fix-incus-group
// --check` (no-op probe) is permitted by the sudoers drop-in. It is a var so
// tests can stub it.
var fixVerbAvailable = func() bool {
	if _, err := exec.LookPath("sudo"); err != nil {
		return false
	}
	if _, err := exec.LookPath("palmux"); err != nil {
		// Resolve the running binary path instead; install.sh registers the
		// sudoers verb against the resolved palmux2/palmux path.
		if _, err2 := exec.LookPath("palmux2"); err2 != nil {
			return false
		}
	}
	// `sudo -n true` confirms NOPASSWD is configured at all. We cannot probe the
	// exact verb without running it, so we use the presence of the sudoers
	// drop-in marker file as the authoritative signal.
	return sudoersDropInPresent()
}

func (h *handlers) getIncusGroup(w http.ResponseWriter, _ *http.Request) {
	res := detectIncusGroup()
	resp := incusGroupResponse{Result: res}
	// The recover button is only meaningful in the stale state; for not-member
	// the fix is usermod (root), not a user-manager restart.
	if res.State == incusgroup.StateStale {
		resp.FixAvailable = fixVerbAvailable()
		resp.RestartCommand = restartUserManagerCommand()
	}
	writeJSON(w, http.StatusOK, resp)
}

// postIncusGroupFix triggers the privileged recover verb. The verb restarts the
// user manager (`systemctl restart user@<uid>`), which restarts palmux itself,
// so this handler cannot wait synchronously — it launches detached and returns
// immediately. The GUI rides the S6ab0ed reconnect handshake to completion.
func (h *handlers) postIncusGroupFix(w http.ResponseWriter, _ *http.Request) {
	res := detectIncusGroup()
	if res.State != incusgroup.StateStale {
		// Only the stale state is recoverable by a user-manager restart.
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":     false,
			"state":  res.State,
			"error":  "the incus-admin group state is not 'stale'; a user-manager restart does not apply here",
			"remedy": res.Remedy,
			"detail": res.Detail,
		})
		return
	}
	if !fixVerbAvailable() {
		// AC-Sfef725-2-4: no privileged verb installed → tell the GUI to show the
		// manual command instead of attempting.
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":             false,
			"fixAvailable":   false,
			"restartCommand": restartUserManagerCommand(),
			"error":          "the fix-incus-group sudoers verb is not installed on this host",
		})
		return
	}
	if err := launchFixIncusGroup(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	// The recover verb now runs detached and will restart the user manager (and
	// us with it). The GUI observes completion via the WS-drop → /api/health
	// reconnect handshake (AC-Sfef725-2-3).
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "incus-admin を適用しています。user マネージャ再起動後 palmux は自動で再起動し、この画面は数秒で再接続します。",
	})
}
