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
//	--query-burst <minBytes>     emit realistic scrollback filler interleaved
//	                             with real DA1/DA2/CPR ANSI device queries
//	                             until at least minBytes have been written,
//	                             then print "QUERY_BURST_DONE" and loop
//	                             normally. Used by the Sfeed64-1 reattach
//	                             deadlock regression test to produce a large,
//	                             query-heavy ptyhost ring for a survivor
//	                             ATTACH replay (see reattach_deadlock_test.go).
//	--settings <json-or-file>    accepted (ignored) — real claude consumes it
//	--system-prompt <s>          accepted (ignored)
//	--foo, --bar, etc.           any other flags are silently accepted
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	emitOsc52 := ""       // non-empty → emit OSC 52 with this text as the payload
	dumpInvocation := ""  // non-empty → write argv + PALMUX_* env as JSON to this path
	counterWinch := false // S3f2658-2: incrementing counter + SIGWINCH trap, for restart/reconnect + screen-restore-jiggle tests
	queryBurstBytes := 0  // Sfeed64-1: >0 → emit a query-heavy burst of at least this many bytes

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
		case "--counter-winch":
			counterWinch = true
		case "--query-burst":
			if i+1 < len(args) {
				if n, perr := strconv.Atoi(args[i+1]); perr == nil {
					queryBurstBytes = n
				}
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

	if queryBurstBytes > 0 {
		// Let the owning daemon's OWN initial spawn/ATTACH(-1) complete first
		// (normally sub-millisecond) before this burst starts landing in the
		// ptyhost ring. Without this delay a fast enough burst can race that
		// very first ATTACH and become part of THIS spawn's own replay
		// instead of accumulating as ordinary live output — which still
		// reproduces the underlying bug, just nondeterministically and on
		// the wrong daemon (the test wants a clean "fresh spawn, empty
		// replay" baseline so the large replay is deterministically what a
		// SECOND, independently-constructed Daemon receives on reconnect —
		// see reattach_deadlock_test.go).
		time.Sleep(500 * time.Millisecond)
		emitQueryBurst(queryBurstBytes)
		fmt.Println("QUERY_BURST_DONE")
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

	if counterWinch {
		// S3f2658-2: a distinctive, fast-incrementing counter line (for
		// "no gap across a palmux2 restart" assertions) that also traps
		// SIGWINCH and echoes a marker — proving a RESIZE frame sent over the
		// ptyhost socket genuinely reaches this process as a real terminal
		// resize (the §5 screen-restore jiggle's convergence mechanism).
		winchCh := make(chan os.Signal, 8)
		signal.Notify(winchCh, syscall.SIGWINCH)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		n := 0
		for {
			select {
			case <-sigCh:
				os.Exit(0)
			case <-winchCh:
				if _, err := fmt.Fprintln(os.Stdout, "WINCH_MARKER"); err != nil {
					os.Exit(0)
				}
			case <-ticker.C:
				n++
				if _, err := fmt.Fprintf(os.Stdout, "COUNTER %d\n", n); err != nil {
					os.Exit(0)
				}
			}
		}
	}

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

// emitQueryBurst writes realistic scrollback-shaped filler text interleaved
// with REAL ANSI device query sequences (DA1 "\x1b[c", DA2 "\x1b[>c", cursor
// position report "\x1b[6n" — the exact three sequences
// third_party/charmbracelet-x-vt-racefix/handlers.go answers by writing into
// the emulator's response pipe) until at least minBytes have been written to
// stdout. This is NOT toy/synthetic data: it is the same shape of content a
// real, long-running claude TUI session accumulates in its ptyhost ring — a
// busy session's screen repaints routinely provoke a real terminal emulator
// (and, symmetrically, palmux2's server-side one) into emitting these same
// three query kinds. See docs/handoff/reattach-deadlock-handoff.md and
// reattach_deadlock_test.go [AC-Sfeed64-1-2].
func emitQueryBurst(minBytes int) {
	queries := []string{"\x1b[c", "\x1b[>c", "\x1b[6n"}
	// Build the whole burst in memory and emit it as ONE write, rather than
	// one small fmt.Fprint call per line/query (thousands of tiny write
	// syscalls). The DATA is identical either way; batching only avoids
	// syscall-count overhead that made this flaky under heavy CPU contention
	// (e.g. `go test ./...` running many packages' real-subprocess tests
	// concurrently on a small core count) — content realism (size, line
	// shape, interleaved real ANSI queries) is unchanged.
	var buf bytes.Buffer
	buf.Grow(minBytes + 4096)
	for line := 1; buf.Len() < minBytes; line++ {
		fmt.Fprintf(&buf, "line %d: realistic scrollback filler content for the reattach-deadlock regression test\n", line)
		// Real terminal sessions probe device attributes / cursor position
		// occasionally (e.g. once per redraw), not on every single line of
		// output — interleave queries periodically rather than after every
		// line. This keeps the LIVE-forwarding side (the attached daemon's
		// own drainer answering these as they stream in, which is a
		// correctly-behaving, unrelated code path) from being swamped with
		// thousands of responses, while still comfortably exceeding the
		// handful of queries needed to prove the replay-time deadlock (any
		// single unread response blocks the unbuffered emulator pipe).
		if line%25 == 0 {
			for _, q := range queries {
				buf.WriteString(q)
			}
		}
	}
	_, _ = os.Stdout.Write(buf.Bytes())
}
