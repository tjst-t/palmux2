package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/tjst-t/palmux2/internal/ptyhost"
)

// runPtyHost implements the `palmux ptyhost` subcommand — the thin, generic
// process holder described by ADR-0001/0002. It has ZERO claude-specific
// knowledge: --socket/--status/--cwd/--env/--seed are opaque plumbing, and
// the trailing argv (after "--") is whatever command palmux2 wants held
// under a PTY. Do not add claude/incus/tmux-specific flags or logic here —
// that belongs on the palmux2 side (see internal/ptyhost/doc.go).
//
// Usage:
//
//	palmux ptyhost --socket <path> --status <path> [--cwd <dir>] \
//	  [--env KEY=VALUE ...] [--ring-size <bytes>] [--seed <label>] -- <argv...>
func runPtyHost(args []string) int {
	fs := pflag.NewFlagSet("ptyhost", pflag.ContinueOnError)
	socketPath := fs.String("socket", "", "unix socket path to serve the ptyhost protocol on (required)")
	statusPath := fs.String("status", "", "path to write the JSON status file (required)")
	cwd := fs.String("cwd", "", "working directory for the spawned child (empty = inherit)")
	envFlags := fs.StringArray("env", nil, "KEY=VALUE environment variable for the spawned child (repeatable); if omitted entirely, the child inherits palmux ptyhost's own environment")
	ringSize := fs.Int("ring-size", ptyhost.DefaultRingSize, "ring buffer capacity in bytes")
	seed := fs.String("seed", "", "opaque identity label echoed into the status file for palmux2-side discovery/GC (S3f2658-3); ptyhost does not interpret it")

	if err := fs.Parse(args); err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		fmt.Fprintln(os.Stderr, "palmux ptyhost:", err)
		return 2
	}

	argv := fs.Args()
	if *socketPath == "" || *statusPath == "" || len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "palmux ptyhost: --socket, --status and a child argv (after --) are required")
		return 2
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	srv, err := ptyhost.NewServer(ptyhost.Config{
		Argv:       argv,
		Env:        []string(*envFlags),
		Cwd:        *cwd,
		SocketPath: *socketPath,
		StatusPath: *statusPath,
		RingSize:   *ringSize,
		Seed:       *seed,
		Logger:     logger,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "palmux ptyhost:", err)
		return 1
	}

	// ptyhost is a detached process; SIGTERM/SIGINT are still an intentional,
	// explicit way to stop it (e.g. an operator killing it directly, or the
	// non-systemd setsid fallback host actually delivering the signal). This
	// is handled the same way as a socket SHUTDOWN: forward to the child,
	// grace period, SIGKILL, then exit — never respawn (ADR-0002).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := srv.Run(ctx); err != nil && ctx.Err() == nil {
		// ctx.Err() != nil means Run returned ctx.Err() itself (signal-driven
		// shutdown) — that's the expected exit path, not a failure.
		fmt.Fprintln(os.Stderr, "palmux ptyhost:", err)
		return 1
	}
	return 0
}
