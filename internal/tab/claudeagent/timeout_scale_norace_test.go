//go:build !race

package claudeagent

// raceTimeoutMultiplier folds into [permTestTimeout] (testutil_test.go). See
// timeout_scale_race_test.go's doc comment — this is the non-`-race` build's
// multiplier (no extra slack needed).
const raceTimeoutMultiplier = 1
