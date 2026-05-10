// palmux-agent is the in-container agent binary for palmux workspace-runtime.
// It exposes a JSON-RPC 2.0 server over a Unix Domain Socket (UDS),
// providing host → container communication for port listing, file access, etc.
//
// Usage:
//
//	palmux-agent --socket /tmp/palmux-agent.sock
//
// The agent is designed to run inside an LXD container (or VM, or via SSH remote).
// It is a fully static binary (CGO_ENABLED=0) with no runtime dependencies.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/tjst-t/palmux2/internal/agent"
	"github.com/tjst-t/palmux2/internal/agent/proto"
)

// Version is set by the Makefile via -ldflags.
var Version = "dev"

func main() {
	socketPath := "/tmp/palmux-agent.sock"

	// Simple flag parsing (avoid importing flag to keep binary small).
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--socket", "-socket":
			if i+1 < len(args) {
				i++
				socketPath = args[i]
			}
		case "--version", "-version":
			fmt.Printf("palmux-agent %s (protocol %s)\n", Version, proto.Version)
			os.Exit(0)
		case "--help", "-help", "-h":
			fmt.Fprintln(os.Stderr, "Usage: palmux-agent [--socket <path>] [--version]")
			os.Exit(0)
		}
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	srv := agent.NewServer(socketPath, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := srv.Serve(ctx); err != nil {
		logger.Error("agent exited with error", "err", err)
		os.Exit(1)
	}
}
