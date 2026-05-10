// CLI helpers for the `palmux port` subcommand. The actual command tree
// lives in cmd/palmux/cmd_port.go; this file holds the small support pieces
// that don't pull in main's globals.

package port

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
)

// PlaceholderPort is the marker palmux port exec replaces with the real
// allocated port number, mirroring portman's `--` convention.
const PlaceholderPort = "{}"

// ExecOptions configure ExecCommand below.
type ExecOptions struct {
	// Allocator is the live allocator (file path + flock).
	Allocator *Allocator

	// Scope, Name identify the lease (and so the port assignment).
	Scope string
	Name  string

	// Project / Worktree / Hostname / Expose are persisted alongside the
	// lease but are not consulted to pick the port.
	Project  string
	Worktree string
	Hostname string
	Expose   bool

	// Argv is the command + args to exec. Every occurrence of "{}" is
	// replaced with the allocated port number as a decimal string.
	Argv []string

	// Stdin / Stdout / Stderr are inherited unless explicitly set (CLI
	// callers use os.Stdin/Stdout/Stderr).
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// FreeOnExit, when true, removes the (Scope, Name) lease from
	// ports.json after the child exits. When false the lease is left in
	// place — useful for `make serve` where we want the same port across
	// re-runs (matching portman semantics).
	FreeOnExit bool
}

// ExecCommand allocates a port for (Scope, Name), substitutes {} in Argv with
// the port number, and runs the command. It returns the child's exit code
// (0 on clean exit, nonzero otherwise) and any error encountered before exec.
//
// Signals (SIGINT, SIGTERM, SIGHUP) are forwarded to the child so that
// `make serve-stop` (which sends SIGTERM via PID file) cleans up properly.
func ExecCommand(ctx context.Context, opts ExecOptions) (int, error) {
	if len(opts.Argv) == 0 {
		return 0, errors.New("exec: empty command")
	}
	m, err := opts.Allocator.Allocate(ctx, Request{
		Scope:    opts.Scope,
		Name:     opts.Name,
		Project:  opts.Project,
		Worktree: opts.Worktree,
		Hostname: opts.Hostname,
		Expose:   opts.Expose,
	})
	if err != nil {
		return 0, fmt.Errorf("allocate: %w", err)
	}

	portStr := fmt.Sprintf("%d", m.Port)
	expanded := make([]string, len(opts.Argv))
	for i, a := range opts.Argv {
		expanded[i] = strings.ReplaceAll(a, PlaceholderPort, portStr)
	}

	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	cmd := exec.CommandContext(ctx, expanded[0], expanded[1:]...)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	// Run the child in its own process group so we can forward signals
	// without re-killing ourselves and so a `tmux kill-session` style
	// stop only takes down our subtree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Surface the chosen port to the child via env, mirroring portman's
	// PORT-name pattern. Callers can use {} substitution OR $PORT.
	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", m.Port))

	if err := cmd.Start(); err != nil {
		if opts.FreeOnExit {
			_ = opts.Allocator.Free(ctx, opts.Scope, opts.Name)
		}
		return 0, fmt.Errorf("start: %w", err)
	}

	// Forward signals to the child.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case s := <-sigCh:
				_ = syscall.Kill(-cmd.Process.Pid, s.(syscall.Signal))
			}
		}
	}()

	exitCode := 0
	werr := cmd.Wait()
	close(done)
	signal.Stop(sigCh)

	if werr != nil {
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			if opts.FreeOnExit {
				_ = opts.Allocator.Free(ctx, opts.Scope, opts.Name)
			}
			return 0, fmt.Errorf("wait: %w", werr)
		}
	}

	if opts.FreeOnExit {
		if err := opts.Allocator.Free(ctx, opts.Scope, opts.Name); err != nil {
			fmt.Fprintf(opts.Stderr, "palmux port: free %s/%s: %v\n", opts.Scope, opts.Name, err)
		}
	}
	return exitCode, nil
}

// PrintListTable writes a portman-list-style table to w.
func PrintListTable(w io.Writer, mappings []Mapping) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SCOPE\tNAME\tPORT\tPROJECT\tWORKTREE\tSTATUS"); err != nil {
		return err
	}
	for _, m := range mappings {
		project := m.Project
		if project == "" {
			project = "-"
		}
		wt := m.Worktree
		if wt == "" {
			wt = "-"
		}
		status := m.Status
		if status == "" {
			status = "-"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n",
			m.Scope, m.Name, m.Port, project, wt, status); err != nil {
			return err
		}
	}
	return tw.Flush()
}
