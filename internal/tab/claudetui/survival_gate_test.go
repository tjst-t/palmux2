package claudetui

import (
	"os"
	"testing"
)

// requireSurvivalSmoke gates the heavy real-machine tests in this package
// (they build the real palmux binary and spawn real detached ptyhost
// processes, causing CPU contention + process churn under the default
// parallel `go test ./...`). They are opt-in smoke, invoked explicitly during
// `sprint verify` with PALMUX_SURVIVAL_SMOKE=1, matching the project's
// existing real-machine verification convention (PALMUX_REALINCUS_SMOKE and
// tests/acceptance/*.py). The daemon<->ptyhost attach logic also has fast
// in-process-fallback coverage in the default gate (daemon/manager tests).
func requireSurvivalSmoke(t *testing.T) {
	t.Helper()
	if os.Getenv("PALMUX_SURVIVAL_SMOKE") == "" {
		t.Skip("real-machine survival smoke — builds the real binary and spawns real detached ptyhosts; set PALMUX_SURVIVAL_SMOKE=1 to run (kept out of the default `go test ./...` to avoid CPU contention + process churn; run explicitly in sprint verify)")
	}
}
