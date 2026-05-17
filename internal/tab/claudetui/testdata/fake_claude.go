//go:build ignore

// fake_claude is a tiny subprocess used by claudetui tests.  It is compiled by
// the test suite via `go build` — it is NOT part of the regular build graph.
//
// Behaviour:
//   - Prints "fake_claude started\n" to stdout immediately.
//   - If "--resume <id>" is passed, prints "resume: <id>".
//   - If "--exit-immediately" is passed, exits right away (exit 0).
//   - Otherwise it writes a heartbeat line once per second until SIGTERM/SIGINT.
//
// Flags recognised:
//
//	--exit-immediately   exit 0 immediately after the startup line
//	--resume <id>        print "resume: <id>" then loop normally
//	--system-prompt <s>  accepted (ignored — tests just verify the arg was passed)
//	--foo, --bar, etc.   any other flags are silently accepted
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	args := os.Args[1:]

	exitImmediately := false
	resumeID := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--exit-immediately":
			exitImmediately = true
		case "--resume":
			if i+1 < len(args) {
				resumeID = args[i+1]
				i++
			}
		}
	}

	fmt.Println("fake_claude started")

	if resumeID != "" {
		fmt.Printf("resume: %s\n", resumeID)
	}

	if exitImmediately {
		os.Exit(0)
	}

	// Handle SIGTERM/SIGINT gracefully so tests do not need to wait for
	// the gracefulShutdownTimeout.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sigCh:
			os.Exit(0)
		case <-ticker.C:
			if _, err := fmt.Fprintln(os.Stdout, "heartbeat"); err != nil {
				// stdout closed (PTY master closed) — exit cleanly.
				os.Exit(0)
			}
		}
	}
}
