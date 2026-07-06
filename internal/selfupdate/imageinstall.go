package selfupdate

// imageinstall.go — GUI-kicked palmux-ws image fetch (S673a42-3).
//
// On the palmuxOS appliance the host is updated by `nixos-rebuild switch`
// (S673a42-2), but the palmux-ws incus image is a SEPARATE axis: it is a GitHub
// Release asset imported into incus and re-aliased, exactly what `palmux runtime
// install` does. This runs that same command in-process as a background job (the
// running palmux2 binary IS the palmux binary, so we re-exec ourselves) and
// exposes a small status the GUI polls. It deliberately does NOT restart palmux2
// and does NOT touch running containers — container re-creation stays the
// operator-driven S7364e3 "Update container" step, which the drift scan surfaces
// once the new image lands (DESIGN_PRINCIPLES rule 5: don't kill a running claude
// under the user).

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ImageInstallStatus is the GUI-facing state of the image fetch job.
type ImageInstallStatus struct {
	Running   bool   `json:"running"`
	Done      bool   `json:"done"`      // a run finished successfully (badge should clear)
	Error     string `json:"error"`     // non-empty when the last run failed
	Installed string `json:"installed"` // installed image version after a successful run
}

// imageInstallState is the mutex-guarded job state on the Service.
type imageInstallState struct {
	mu     sync.Mutex
	status ImageInstallStatus
}

// imageInstallTimeout bounds a single fetch+import (large tarball download).
const imageInstallTimeout = 30 * time.Minute

// imageInstallCmd builds the command that fetches + imports the latest palmux-ws
// image. A var so tests can stub it with a fake (no real incus / GitHub). Default:
// re-exec THIS binary as `palmux runtime install`, which resolves the latest
// release asset, imports it into incus, re-aliases palmux-ws, and runs the
// Sa8e7d0 completion guard.
var imageInstallCmd = func(ctx context.Context) (*exec.Cmd, error) {
	// E2E-rig seam: PALMUX_IMAGE_INSTALL_CMD overrides the real `runtime install`
	// with a shell command so the dev E2E can exercise the POST→job→GET-done flow
	// against a real backend WITHOUT downloading the ~810 MB image / touching incus.
	// Mirrors the PALMUX_SELFUPDATE_RESTART_CMD seam (service.go). NEVER set in
	// production, where the real re-exec below runs; the green appliance smoke
	// (AC-S673a42-3-3) exercises the real command.
	if seam := os.Getenv("PALMUX_IMAGE_INSTALL_CMD"); seam != "" {
		c := exec.CommandContext(ctx, "sh", "-c", seam) //nolint:gosec // E2E-rig-only seam
		c.Stdin = nil
		return c, nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, self, "runtime", "install") //nolint:gosec // fixed subcommand, no user input
	c.Stdin = nil
	return c, nil
}

// ErrImageInstallInFlight is returned when a fetch is already running.
var ErrImageInstallInFlight = errImageInstallInFlight{}

type errImageInstallInFlight struct{}

func (errImageInstallInFlight) Error() string {
	return "palmux-ws image の取得はすでに実行中です"
}

// ImageInstallStatus returns a copy of the current job state.
func (s *Service) ImageInstallStatus() ImageInstallStatus {
	s.img.mu.Lock()
	defer s.img.mu.Unlock()
	return s.img.status
}

// RunImageInstall starts the image fetch in the background and returns
// immediately. A second call while one is in flight returns ErrImageInstallInFlight.
// On success it refreshes the detection snapshot so the "palmux-ws image 更新あり"
// badge clears; the per-branch drift scan then surfaces the S7364e3 "Update
// container" regenerate affordance.
func (s *Service) RunImageInstall() error {
	s.img.mu.Lock()
	if s.img.status.Running {
		s.img.mu.Unlock()
		return ErrImageInstallInFlight
	}
	s.img.status = ImageInstallStatus{Running: true}
	s.img.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), imageInstallTimeout)
		defer cancel()

		var errMsg string
		cmd, err := imageInstallCmd(ctx)
		if err != nil {
			errMsg = err.Error()
		} else {
			out, runErr := cmd.CombinedOutput()
			if runErr != nil {
				// Surface the tail of the command output so the GUI shows WHY.
				errMsg = strings.TrimSpace(tail(string(out), 800))
				if errMsg == "" {
					errMsg = runErr.Error()
				} else {
					errMsg = runErr.Error() + ": " + errMsg
				}
			}
		}

		installed := ""
		if s.probes.ImageVersion != nil {
			installed = s.probes.ImageVersion()
		}

		s.img.mu.Lock()
		s.img.status = ImageInstallStatus{
			Running:   false,
			Done:      errMsg == "",
			Error:     errMsg,
			Installed: installed,
		}
		s.img.mu.Unlock()

		if errMsg == "" {
			// Clear the badge server-side (recompute Available for the image).
			s.Refresh(context.Background())
			s.logger.Info("selfupdate: palmux-ws image install completed", "installed", installed)
		} else {
			s.logger.Warn("selfupdate: palmux-ws image install failed", "err", errMsg)
		}
	}()
	return nil
}

// tail returns the last n bytes of s (rune-safe-ish; used only for error display).
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
