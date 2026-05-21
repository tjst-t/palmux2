//go:build ignore

// fake_claude is a tiny subprocess used by claudetui tests.  It is compiled by
// the test suite via `go build` — it is NOT part of the regular build graph.
//
// Behaviour:
//   - Prints "fake_claude started\n" to stdout immediately.
//   - If "--resume <id>" is passed, prints "resume: <id>".
//   - If "--exit-immediately" is passed, exits right away (exit 0).
//   - If "--write-session <dir> <session_id>" is passed, writes a stub
//     .jsonl file at <dir>/<session_id>.jsonl after startup so the
//     SessionWatcher / Manager can detect it.
//   - Otherwise it writes a heartbeat line once per second until SIGTERM/SIGINT.
//
// Flags recognised:
//
//	--exit-immediately          exit 0 immediately after the startup line
//	--resume <id>               print "resume: <id>" then loop normally
//	--write-session <dir> <id>  write <dir>/<id>.jsonl then loop normally
//	--print-cwd                 print "cwd: <os.Getwd()>" then loop normally
//	--system-prompt <s>         accepted (ignored)
//	--foo, --bar, etc.          any other flags are silently accepted
package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	args := os.Args[1:]

	exitImmediately := false
	resumeID := ""
	writeSessionDir := ""
	writeSessionID := ""
	printCwd := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--exit-immediately":
			exitImmediately = true
		case "--resume":
			if i+1 < len(args) {
				resumeID = args[i+1]
				i++
			}
		case "--write-session":
			// Consumes next two args: <dir> <session_id>
			if i+2 < len(args) {
				writeSessionDir = args[i+1]
				writeSessionID = args[i+2]
				i += 2
			}
		case "--print-cwd":
			printCwd = true
		}
	}

	fmt.Println("fake_claude started")

	if resumeID != "" {
		fmt.Printf("resume: %s\n", resumeID)
	}

	if printCwd {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake_claude: getwd: %v\n", err)
		} else {
			fmt.Printf("cwd: %s\n", cwd)
		}
	}

	if exitImmediately {
		os.Exit(0)
	}

	// Write a stub .jsonl to simulate claude creating a session file.
	if writeSessionDir != "" && writeSessionID != "" {
		if err := os.MkdirAll(writeSessionDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "fake_claude: mkdir %s: %v\n", writeSessionDir, err)
		} else {
			jsonlPath := filepath.Join(writeSessionDir, writeSessionID+".jsonl")
			content := `{"type":"user","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n"
			if err := os.WriteFile(jsonlPath, []byte(content), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "fake_claude: write jsonl: %v\n", err)
			} else {
				fmt.Printf("wrote-session: %s\n", writeSessionID)
			}
		}
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
