package claudetui

import (
	"os/exec"
	"runtime"
)

// goCmd builds an exec.Cmd for the `go` tool with the given arguments.
func goCmd(args ...string) *exec.Cmd {
	goExe := "go"
	if runtime.GOOS == "windows" {
		goExe = "go.exe"
	}
	return exec.Command(goExe, args...)
}
