//go:build ignore

// fake_ndjson is a tiny subprocess used by ptyhost pipe-mode tests
// (S862203-2). It stands in for `claude --output-format stream-json`: it
// prints newline-delimited JSON lines with a monotonic counter to stdout at
// a controllable pace, and separately prints diagnostic lines to stderr —
// letting tests assert the pipe-mode ptyhost's stdout/stderr ring separation
// and offset-ack replay deterministically, with NO real claude and NO quota
// (docs/sprint-logs/S862203/scenario-S862203-2.json).
//
// It is compiled by the test suite via `go build` — it is NOT part of the
// regular build graph (mirrors internal/ptyhost/testdata/fake_child.go).
//
// Behaviour, controlled entirely by env vars (kept env-driven rather than
// flags so it stays a trivial, dependency-free child the tests can spawn
// with a fixed argv and vary via Env — mirroring how palmux2 spawns claude):
//
//   - FAKE_NDJSON_COUNT (default 5): how many stdout NDJSON lines to emit
//     before going idle (still alive, reading stdin) or exiting — see
//     FAKE_NDJSON_EXIT_AFTER.
//   - FAKE_NDJSON_START (default 0): the first counter value emitted — lets
//     a test simulate "emit N more lines starting where a previous fake
//     process left off" without needing a stateful child.
//   - FAKE_NDJSON_DELAY_MS (default 0): sleep between each stdout line —
//     controllable pace for tests that want to attach mid-stream.
//   - FAKE_NDJSON_STDERR_COUNT (default equal to FAKE_NDJSON_COUNT): how
//     many "diag: <n>" lines to print to stderr, interleaved 1:1 with the
//     stdout lines (same pacing) — used to assert stderr never lands in the
//     stdout ring and vice versa.
//   - FAKE_NDJSON_EXIT_AFTER=1: os.Exit(0) immediately after emitting the
//     configured line counts, instead of staying alive.
//   - A line of exactly "FAKE_NDJSON_EXIT:<code>" read from stdin makes it
//     os.Exit(<code>) immediately (mirrors fake_child's
//     PTYHOST_TEST_EXIT convention) — used to end a still-alive process on
//     demand from a test.
//
// Each stdout line has the shape:
//
//	{"type":"fake_event","seq":<n>}
//
// — a real NDJSON line (1 JSON object per line, like stream-json), so tests
// exercising line-reassembly-across-DATA-frame-boundaries logic operate on
// realistic content shape without depending on the real claude wire format.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func main() {
	count := envInt("FAKE_NDJSON_COUNT", 5)
	start := envInt("FAKE_NDJSON_START", 0)
	delayMS := envInt("FAKE_NDJSON_DELAY_MS", 0)
	stderrCount := envInt("FAKE_NDJSON_STDERR_COUNT", count)
	exitAfter := os.Getenv("FAKE_NDJSON_EXIT_AFTER") == "1"

	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		os.Exit(0)
	}()

	// FAKE_NDJSON_SPAWN_DESCENDANT=1 spawns a long-lived `sleep` child that
	// INHERITS this process's stdout/stderr (the ptyhost pipe write-ends) and
	// outlives it — the S862203-2 HIGH-2 regression scenario: a backgrounded
	// descendant keeps the pipe read-end open so ptyhost's pumpToRing never
	// sees EOF. ptyhost must kill the whole process group on SHUTDOWN to
	// terminate this descendant too, otherwise Run() hangs / the descendant
	// orphans. Not setting a new process group here means the descendant
	// stays in THIS process's group (which ptyhost created via Setpgid).
	if os.Getenv("FAKE_NDJSON_SPAWN_DESCENDANT") == "1" {
		desc := exec.Command("sleep", "30")
		desc.Stdout = os.Stdout // inherit the ptyhost stdout pipe write-end
		desc.Stderr = os.Stderr // inherit the ptyhost stderr pipe write-end
		if err := desc.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "fake_ndjson: spawn descendant: %v\n", err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < stderrCount; i++ {
			fmt.Fprintf(os.Stderr, "diag: %d\n", start+i)
			if delayMS > 0 {
				time.Sleep(time.Duration(delayMS) * time.Millisecond)
			}
		}
	}()

	for i := 0; i < count; i++ {
		fmt.Printf("{\"type\":\"fake_event\",\"seq\":%d}\n", start+i)
		if delayMS > 0 {
			time.Sleep(time.Duration(delayMS) * time.Millisecond)
		}
	}
	wg.Wait()

	if exitAfter {
		os.Exit(0)
	}

	// Stay alive, reading stdin for an explicit exit request (or EOF). Any
	// other line is echoed back over stdout as {"type":"echo","line":...} —
	// a real NDJSON line — so pipe-mode INPUT->stdin wiring is testable the
	// same way fake_child.go's plain "echo: <line>" covers PTY INPUT.
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.HasPrefix(line, "FAKE_NDJSON_EXIT:") {
			code, err := strconv.Atoi(strings.TrimPrefix(line, "FAKE_NDJSON_EXIT:"))
			if err != nil {
				code = 1
			}
			os.Exit(code)
		}
		fmt.Printf("{\"type\":\"echo\",\"line\":%q}\n", line)
	}
	os.Exit(0)
}
