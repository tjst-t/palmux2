package ptyhost

import (
	"os"
	"testing"
)

// requireSurvivalSmoke gates the heavy real-machine tests in this package
// (they spawn real detached processes and/or transient systemd --user units,
// which cause CPU contention and process churn under the default parallel
// `go test ./...`). They are opt-in smoke, invoked explicitly during
// `sprint verify` with PALMUX_SURVIVAL_SMOKE=1, matching the project's
// existing convention for real-machine verification (PALMUX_REALINCUS_SMOKE
// and the tests/acceptance/*.py suites). The fast branching/fallback logic is
// still covered unconditionally by the injected-runner unit tests in
// launch_test.go, so gating these does not reduce logic coverage in the
// default gate.
func requireSurvivalSmoke(t *testing.T) {
	t.Helper()
	if os.Getenv("PALMUX_SURVIVAL_SMOKE") == "" {
		t.Skip("real-machine survival smoke — spawns real detached processes / systemd units; set PALMUX_SURVIVAL_SMOKE=1 to run (kept out of the default `go test ./...` to avoid CPU contention + process churn; run explicitly in sprint verify)")
	}
}
