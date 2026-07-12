//go:build ignore

// fake_child is a tiny subprocess used by ptyhost tests. It is compiled by
// the test suite via `go build` — it is NOT part of the regular build graph
// (mirrors internal/tab/claudetui/testdata/fake_claude.go).
//
// Behaviour:
//   - Prints "fake_child started\n" to stdout immediately.
//   - Reads stdin line by line; for each line, prints "echo: <line>\n". This
//     is an explicit application-level echo (independent of PTY line
//     discipline) so INPUT/DATA round-trip tests are deterministic.
//   - On SIGWINCH, ioctl-queries its own controlling terminal's winsize and
//     prints "winsize: <cols>x<rows>\n" — used to assert RESIZE frames
//     actually change the PTY the child sees.
//   - A line of exactly "PTYHOST_TEST_EXIT:<code>" makes it os.Exit(<code>)
//     immediately (used by tests that don't want to depend on `sh -c exit`).
//   - Exits 0 on SIGTERM/SIGINT or on stdin EOF.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func main() {
	fmt.Println("fake_child started")

	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGWINCH)
	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGWINCH:
				ws, err := unix.IoctlGetWinsize(int(os.Stdin.Fd()), unix.TIOCGWINSZ)
				if err != nil {
					fmt.Fprintf(os.Stderr, "fake_child: ioctl winsize: %v\n", err)
					continue
				}
				fmt.Printf("winsize: %dx%d\n", ws.Col, ws.Row)
			case syscall.SIGTERM, syscall.SIGINT:
				os.Exit(0)
			}
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.HasPrefix(line, "PTYHOST_TEST_EXIT:") {
			codeStr := strings.TrimPrefix(line, "PTYHOST_TEST_EXIT:")
			code, err := strconv.Atoi(codeStr)
			if err != nil {
				code = 1
			}
			os.Exit(code)
		}
		fmt.Printf("echo: %s\n", line)
	}
	os.Exit(0)
}
