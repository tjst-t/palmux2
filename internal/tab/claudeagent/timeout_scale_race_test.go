//go:build race

package claudeagent

// raceTimeoutMultiplier folds into [permTestTimeout] (testutil_test.go).
// cmd/go automatically adds the "race" build constraint whenever a test
// binary is built with `go test -race`, so this needs no runtime detection
// at all — it is simply the multiplier this build (vs
// timeout_scale_norace_test.go's build) compiles in.
//
// 3x: -race instrumentation (red-zone checks, shadow memory bookkeeping on
// every memory access) measurably slows goroutine scheduling and channel
// operations — exactly the operations the permission integration tests'
// bounded waits are timing (AC-S64c835-2-2 / backlog #6).
const raceTimeoutMultiplier = 3
