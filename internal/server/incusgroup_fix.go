package server

// incusgroup_fix.go — the privileged-recover plumbing for Sfef725-2.
//
// The recover path mirrors Sa53137's verb-limited NOPASSWD sudoers pattern:
// `sudo palmux fix-incus-group` is a single allowlisted verb whose ONLY action
// is `systemctl restart user@<uid>` (uid hardcoded to the service user). It
// takes no free-form input. install.sh installs the matching sudoers drop-in.
//
// Launching is detached (like selfupdate.RunUpdate): the verb restarts the user
// manager, which terminates this process, so the HTTP handler must not Wait.

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// sudoersDropInMarker is the file install.sh writes for the fix-incus-group
// verb. Its presence is the authoritative signal that the recover button can be
// shown (the verb is permitted without a password).
const sudoersDropInMarker = "/etc/sudoers.d/palmux-fix-incus-group"

// sudoersDropInPresent reports whether the fix-incus-group sudoers drop-in is
// installed. It is a var so tests can stub the deployment fact.
//
// E2E seam (dev rig only): PALMUX_INCUS_GROUP_FAKE_VERB = "1" forces present,
// "0" forces absent — lets the GUI E2E cover both the recover-button and the
// manual-command fallback without touching /etc/sudoers.d. Never set in prod.
var sudoersDropInPresent = func() bool {
	switch os.Getenv("PALMUX_INCUS_GROUP_FAKE_VERB") {
	case "1":
		return true
	case "0":
		return false
	}
	fi, err := os.Stat(sudoersDropInMarker)
	return err == nil && !fi.IsDir()
}

// serviceUID returns the uid the palmux server runs as (os.Getuid).
func serviceUID() int { return os.Getuid() }

// restartUserManagerCommand is the exact manual command a user can run to apply
// a freshly-added incus-admin group (shown when the privileged verb is absent).
func restartUserManagerCommand() string {
	return fmt.Sprintf("sudo systemctl restart user@%d", serviceUID())
}

// fixCommand is the privileged verb invocation. It is a var so tests can stub
// it to a harmless command instead of really restarting the user manager.
//
// E2E seam (dev rig only): PALMUX_INCUS_GROUP_FAKE_FIX_CMD replaces the real
// `sudo palmux fix-incus-group` with a harmless command (e.g. a marker-writing
// shell snippet) so the GUI E2E can verify the endpoint→verb WIRING is real
// without killing the dev rig's user manager. The wiring (handler → detached
// exec) stays real; only the leaf command is swapped. Never set in production.
var fixCommand = func() *exec.Cmd {
	if fake := os.Getenv("PALMUX_INCUS_GROUP_FAKE_FIX_CMD"); fake != "" {
		return exec.Command("sh", "-c", fake) //nolint:gosec // dev-rig seam only
	}
	bin := palmuxBinPath()
	return exec.Command("sudo", "-n", bin, "fix-incus-group") //nolint:gosec // fixed verb, no free-form input
}

// palmuxBinPath resolves the palmux binary path the sudoers verb is registered
// against (palmux2 preferred, then palmux, then the running executable).
func palmuxBinPath() string {
	for _, name := range []string{"palmux2", "palmux"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "palmux"
}

// launchFixIncusGroup starts the privileged recover verb DETACHED and returns
// immediately. The verb restarts the user manager (and palmux with it); the GUI
// observes completion via the reconnect handshake.
func launchFixIncusGroup() error {
	cmd := fixCommand()
	// Detach: new session so the restart survives our exit; discard output to a
	// log file for diagnostics.
	logPath := os.TempDir() + "/palmux-fix-incus-group.log"
	var logFile *os.File
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil { //nolint:gosec
		logFile = lf
		cmd.Stdout = lf
		cmd.Stderr = lf
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return fmt.Errorf("launch fix-incus-group: %w", err)
	}
	// Release the child and close OUR copy of the log fd: the child inherited its
	// own. Not closing it leaks one fd per recover attempt in the long-lived
	// server (the child usually restarts us, but a denied/stubbed verb returns).
	_ = cmd.Process.Release()
	if logFile != nil {
		_ = logFile.Close()
	}
	return nil
}
