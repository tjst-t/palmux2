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
//	--exit-immediately           exit 0 immediately after the startup line
//	--resume <id>                print "resume: <id>" then loop normally
//	--write-session <dir> <id>   write <dir>/<id>.jsonl then loop normally
//	--print-cwd                  print "cwd: <os.Getwd()>" then loop normally
//	--emit-osc52 <text>          emit OSC 52 clipboard-write sequence
//	--settings <json-or-file>    accepted (ignored) — real claude consumes it
//	--system-prompt <s>          accepted (ignored)
//	--foo, --bar, etc.           any other flags are silently accepted
package main

import (
	"encoding/base64"
	"encoding/json"
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
	emitOsc52 := ""      // non-empty → emit OSC 52 with this text as the payload
	dumpInvocation := "" // non-empty → write argv + PALMUX_* env as JSON to this path

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dump-invocation":
			// Consumes next arg: path to write the invocation JSON to.
			if i+1 < len(args) {
				dumpInvocation = args[i+1]
				i++
			}
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
		case "--emit-osc52":
			// Consumes next arg: the text to put into the OSC 52 clipboard.
			if i+1 < len(args) {
				emitOsc52 = args[i+1]
				i++
			}
		}
	}

	// Record how palmux invoked us (argv + injected PALMUX_* env) so spawn-time
	// hook-injection can be asserted by tests.
	if dumpInvocation != "" {
		rec := map[string]any{
			"argv": os.Args,
			"env": map[string]string{
				"PALMUX_NOTIFY_URL": os.Getenv("PALMUX_NOTIFY_URL"),
				"PALMUX_TOKEN":      os.Getenv("PALMUX_TOKEN"),
				"PALMUX_REPO_ID":    os.Getenv("PALMUX_REPO_ID"),
				"PALMUX_BRANCH_ID":  os.Getenv("PALMUX_BRANCH_ID"),
				"PALMUX_TAB_ID":     os.Getenv("PALMUX_TAB_ID"),
			},
		}
		if b, err := json.Marshal(rec); err == nil {
			_ = os.WriteFile(dumpInvocation, b, 0o644)
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

	if emitOsc52 != "" {
		// Emit OSC 52 clipboard-write sequence.
		b64 := base64.StdEncoding.EncodeToString([]byte(emitOsc52))
		// Write to stdout: ESC ] 52 ; c ; <b64> BEL
		fmt.Printf("\x1b]52;c;%s\x07", b64)
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
