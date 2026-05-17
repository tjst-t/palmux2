// Command poc-pty is the Track B PoC PTY daemon for Sprint S1d2278-2.
//
// It spawns an interactive subprocess (default: `claude`) under a Go-owned
// PTY and exposes bidirectional WebSocket access over HTTP.  This is
// intentionally throwaway PoC code — the production integration will be a
// dedicated Sprint.
//
// Usage:
//
//	poc-pty --port <int> [flags]
//
// Key flags:
//
//	--port          (required) listen port
//	--claude-bin    path to subprocess binary (default: claude)
//	--claude-args   space-separated args to pass to claude-bin
//	--probe         one-shot probe mode (see below)
//	--ring-size     ring buffer bytes (default: 1048576)
//	--probe-prompt  bytes sent in probe mode (default: "hello\n")
//
// Probe mode (--probe):
//
//	Spawns claude-bin, sends --probe-prompt, reads for up to 5s or
//	until 2s of inactivity, prints observed bytes, exits 0 if any
//	bytes received, non-zero otherwise.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	creackpty "github.com/creack/pty"
	"os/exec"

	pocpty "github.com/tjst-t/palmux2/internal/poc/pty"
)

const (
	// ProbeMaxDuration is the maximum wall-clock time for --probe mode.
	ProbeMaxDuration = 5 * time.Second
	// ProbeInactivityTimeout is how long --probe waits without new bytes
	// before declaring "done".
	ProbeInactivityTimeout = 2 * time.Second
)

func main() {
	fs := pflag.NewFlagSet("poc-pty", pflag.ContinueOnError)

	port := fs.Int("port", 0, "HTTP listen port (required unless --probe)")
	claudeBin := fs.String("claude-bin", "claude", "subprocess binary path")
	claudeArgsRaw := fs.String("claude-args", "", "space-separated args for claude-bin")
	probeMode := fs.Bool("probe", false, "one-shot probe: spawn, send prompt, read, exit")
	ringSize := fs.Int("ring-size", pocpty.DefaultRingSize, "ring buffer size in bytes")
	probePrompt := fs.String("probe-prompt", "hello\n", "bytes sent by --probe")

	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "poc-pty: flag parse: %v\n", err)
		os.Exit(2)
	}

	claudeArgs := splitArgs(*claudeArgsRaw)

	if *probeMode {
		if err := runProbe(*claudeBin, claudeArgs, *probePrompt); err != nil {
			fmt.Fprintf(os.Stderr, "poc-pty probe: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if *port == 0 {
		fmt.Fprintln(os.Stderr, "poc-pty: --port is required (pass 0 only in probe mode)")
		os.Exit(2)
	}

	if err := runServer(*port, *claudeBin, claudeArgs, *ringSize); err != nil {
		fmt.Fprintf(os.Stderr, "poc-pty: %v\n", err)
		os.Exit(1)
	}
}

// splitArgs splits a space-separated argument string into a slice.
// An empty string returns nil.
func splitArgs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return strings.Fields(raw)
}

// runProbe implements --probe mode.
// It spawns claude-bin under a PTY, sends probe-prompt, reads until
// ProbeInactivityTimeout seconds of inactivity or ProbeMaxDuration total,
// then kills the subprocess and exits.
func runProbe(bin string, args []string, prompt string) error {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := creackpty.Start(cmd)
	if err != nil {
		return fmt.Errorf("pty.Start: %w", err)
	}
	// Always clean up.
	defer func() {
		cmd.Process.Kill()
		ptmx.Close()
		cmd.Wait()
	}()

	// Send probe prompt.
	if _, err := ptmx.Write([]byte(prompt)); err != nil {
		return fmt.Errorf("write probe prompt: %w", err)
	}

	// Read loop runs in a goroutine so we can apply wall-clock timeouts
	// without relying on SetReadDeadline (PTY fds on Linux are raw OS files
	// and do not support net.Conn-style deadlines reliably).
	type readResult struct {
		data []byte
	}
	ch := make(chan []byte, 256)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				ch <- chunk
			}
			if err != nil {
				close(ch)
				return
			}
		}
	}()

	var received []byte
	totalDeadline := time.After(ProbeMaxDuration)
	inactivity := time.NewTimer(ProbeInactivityTimeout)
	defer inactivity.Stop()

loop:
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				break loop
			}
			received = append(received, chunk...)
			// Reset inactivity timer on each received chunk.
			if !inactivity.Stop() {
				select {
				case <-inactivity.C:
				default:
				}
			}
			inactivity.Reset(ProbeInactivityTimeout)
		case <-inactivity.C:
			break loop
		case <-totalDeadline:
			break loop
		}
	}

	if len(received) == 0 {
		return fmt.Errorf("probe received 0 bytes")
	}

	display := received
	if len(display) > 200 {
		display = display[:200]
	}
	fmt.Printf("pty: ok, claude: alive, sent %d byte(s), recv %d bytes\n",
		len(prompt), len(received))
	fmt.Printf("first bytes: %q\n", display)
	return nil
}

// runServer starts the HTTP+WS server in daemon mode.
func runServer(port int, claudeBin string, claudeArgs []string, ringSize int) error {
	daemon := pocpty.NewDaemon(claudeBin, claudeArgs, ringSize)
	srv := pocpty.NewServer(daemon)

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("listen :%d: %w", port, err)
	}
	// Print exactly this line so test fixtures can parse the port.
	fmt.Printf("listening on :%d\n", listener.Addr().(*net.TCPAddr).Port)

	httpSrv := &http.Server{Handler: srv.Handler()}

	// Graceful shutdown on SIGTERM/SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpSrv.Shutdown(ctx)
		daemon.Shutdown()
	}()

	if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http serve: %w", err)
	}
	return nil
}

// Ensure io.Reader is used (avoids import elimination).
var _ io.Reader = (*os.File)(nil)
