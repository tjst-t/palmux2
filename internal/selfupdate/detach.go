package selfupdate

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the child in its own session so it survives palmux being
// restarted/killed by the very update it launched (decisions PD-4/PD-7). palmux
// targets Linux (VISION tech_constraints), so Setsid is always available.
func detachProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
