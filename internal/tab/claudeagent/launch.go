package claudeagent

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
)

// This file is the ptyhost-launch seam for claudeagent's thin pipe-mode
// client (S862203-3). It is a deliberate, near-verbatim mirror of
// internal/tab/claudetui/discover.go's launch half — same reconnect-or-spawn
// contract, same ADR-0002 "argv/env/cwd is opaque, ptyhost interprets none
// of it" discipline — with two differences: every launch request sets
// Mode=ptyhost.ModePipe (ADR-0004/S862203-2's stream-json-safe transport,
// not a PTY), and there is no TUI-specific screen-restore jiggle here (that
// concern doesn't exist for a line-oriented NDJSON stream).

// ptyHostDialTimeout bounds how long dialAndHello waits for a socket to
// accept connections after a fresh launch (the launched process is
// detached/async — see internal/ptyhost/launch.go).
const ptyHostDialTimeout = 5 * time.Second

// ptyHostDialRetryInterval is the backoff between dial attempts while
// waiting for a just-launched ptyhost to start listening.
const ptyHostDialRetryInterval = 20 * time.Millisecond

// PtyHostLaunchRequest describes a pipe-mode ptyhost a Client wants spawned
// (or, if one is already listening at SocketPath, attached to instead — see
// [Client.launchAndAttachPipe]). Argv/Env/Cwd are the fully-assembled,
// opaque command line built by NewClient; ptyhost interprets none of it
// (ADR-0002).
type PtyHostLaunchRequest struct {
	// PalmuxBin is the palmux binary to re-invoke as `<PalmuxBin> ptyhost
	// ...` (production launch path only; ignored by the in-process
	// dev/test fallback — see [inProcessLaunchPtyHost]).
	PalmuxBin string
	// InstancePrefix isolates concurrent palmux instances (host vs
	// INSTANCE=dev rigs), mirroring domain.PalmuxSessionPrefix.
	InstancePrefix string
	// Seed is hashed into the socket/status filename + systemd scope unit
	// name (repoId__branchId__tabId) — a LABEL only, not a parseable
	// identity (see [ptyhost.Config.Seed]).
	Seed string
	// RepoID/BranchID/TabID are the opaque workspace-tab identity written
	// DIRECTLY into the ptyhost status file so discovery/GC can recover the
	// exact tuple without parsing Seed. Threaded verbatim into the ptyhost
	// subcommand's --repo-id/--branch-id/--tab-id flags (production) or
	// ptyhost.Config (in-process test fallback).
	RepoID   string
	BranchID string
	TabID    string
	// SocketPath / StatusPath are where the ptyhost must listen / write its
	// status file — precomputed by the caller so the "does a survivor
	// already exist" dial-first check and the actual launch agree on the
	// same location.
	SocketPath string
	StatusPath string
	// Argv is the full opaque child command line (Argv[0] is the
	// executable).
	Argv []string
	// Env is the full opaque environment for the child.
	Env []string
	// Cwd is the child's working directory.
	Cwd string
	// RingSize is the ptyhost ring buffer capacity in bytes (<=0 → ptyhost's
	// own default).
	RingSize int
}

// PtyHostLaunchFunc starts (or ensures listening) a pipe-mode ptyhost for
// req. On return without error, req.SocketPath must be ready to accept
// connections. Production uses [defaultLaunchPtyHost]; tests may inject
// [inProcessLaunchPtyHost] or a custom fake via ClientOptions.PtyHostLaunch.
type PtyHostLaunchFunc func(ctx context.Context, req PtyHostLaunchRequest) error

// defaultLaunchPtyHost is the production implementation: it re-invokes
// `<PalmuxBin> ptyhost --mode pipe ...` via the real [ptyhost.Launcher]
// (systemd-run --user --scope cgroup-escape, falling back to setsid —
// ADR-0003) so the spawned ptyhost (and the claude process/incus-wrapper it
// holds) survives this palmux2 process's own death, then waits for the
// socket to accept connections.
func defaultLaunchPtyHost(ctx context.Context, req PtyHostLaunchRequest) error {
	if req.PalmuxBin == "" {
		return fmt.Errorf("claudeagent: ptyhost launch: PalmuxBin is empty")
	}
	args := []string{"--socket", req.SocketPath, "--status", req.StatusPath, "--mode", ptyhost.ModePipe}
	if req.Seed != "" {
		args = append(args, "--seed", req.Seed)
	}
	if req.RepoID != "" {
		args = append(args, "--repo-id", req.RepoID)
	}
	if req.BranchID != "" {
		args = append(args, "--branch-id", req.BranchID)
	}
	if req.TabID != "" {
		args = append(args, "--tab-id", req.TabID)
	}
	if req.Cwd != "" {
		args = append(args, "--cwd", req.Cwd)
	}
	for _, kv := range req.Env {
		args = append(args, "--env", kv)
	}
	if req.RingSize > 0 {
		args = append(args, "--ring-size", strconv.Itoa(req.RingSize))
	}
	args = append(args, "--")
	args = append(args, req.Argv...)

	l := &ptyhost.Launcher{}
	if _, err := l.Launch(ctx, ptyhost.LaunchConfig{
		PalmuxBin:      req.PalmuxBin,
		InstancePrefix: req.InstancePrefix,
		Seed:           req.Seed,
		Args:           args,
	}); err != nil {
		return fmt.Errorf("claudeagent: ptyhost launch: %w", err)
	}
	return waitForSocket(ctx, req.SocketPath, ptyHostDialTimeout, nil)
}

// testPtyHostSeq isolates each auto-fallback (PalmuxBin=="") Client's
// ptyhost run directory from every other one in the same test binary.
var testPtyHostSeq atomic.Int64

// inProcessLaunchPtyHost is the automatic fallback used when
// ClientOptions.PalmuxBin is empty (every existing unit/integration test,
// none of which set it): instead of spawning a REAL detached `palmux
// ptyhost` OS process, it runs a real [ptyhost.Server] (Mode=Pipe) as a
// goroutine in the CURRENT process. This is still the genuine ptyhost
// protocol/ring/spawn code — only the OS-process-detachment half is
// swapped out, so the claudeagent test suite stays hermetic and fast
// without needing to build and exec a real `palmux` binary.
//
// Deliberately NOT wired to ctx: like the production launcher, the server
// must outlive the calling request/spawn context — here that just means it
// survives until its own child exits or a SHUTDOWN message arrives.
func inProcessLaunchPtyHost(ctx context.Context, req PtyHostLaunchRequest) error {
	srv, err := ptyhost.NewServer(ptyhost.Config{
		Argv:       req.Argv,
		Env:        req.Env,
		Cwd:        req.Cwd,
		Mode:       ptyhost.ModePipe,
		SocketPath: req.SocketPath,
		StatusPath: req.StatusPath,
		RingSize:   req.RingSize,
		Seed:       req.Seed,
		RepoID:     req.RepoID,
		BranchID:   req.BranchID,
		TabID:      req.TabID,
	})
	if err != nil {
		return fmt.Errorf("claudeagent: in-process ptyhost: %w", err)
	}
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- srv.Run(context.Background()) }()
	return waitForSocket(ctx, req.SocketPath, ptyHostDialTimeout, runErrCh)
}

// autoTestRunDir returns a fresh, per-call-unique temp directory to hold
// ptyhost sockets for a Client that did not opt into production wiring
// (PalmuxBin == "").
func autoTestRunDir() string {
	return filepath.Join(os.TempDir(), "palmux-agent-ptyhost-dev",
		fmt.Sprintf("d%d-%d", os.Getpid(), testPtyHostSeq.Add(1)))
}

// waitForSocket polls until a unix socket at path accepts a connection or
// ctx/timeout expires. earlyErr, if non-nil, is the launched ptyhost's own
// Run() outcome (in-process launch only) — a value received on it BEFORE
// the socket ever becomes reachable means the spawn failed before it got as
// far as listening.
func waitForSocket(ctx context.Context, path string, timeout time.Duration, earlyErr <-chan error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-earlyErr:
			if conn, derr := net.DialTimeout("unix", path, ptyHostDialRetryInterval); derr == nil {
				_ = conn.Close()
				return nil
			}
			if err != nil {
				return fmt.Errorf("claudeagent: ptyhost exited before listening: %w", err)
			}
			lastErr = fmt.Errorf("ptyhost exited before listening (no error)")
			continue
		default:
		}
		conn, err := net.DialTimeout("unix", path, ptyHostDialRetryInterval)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(ptyHostDialRetryInterval)
	}
	return fmt.Errorf("claudeagent: ptyhost socket %s not accepting connections after %v: %w", path, timeout, lastErr)
}

// probeExisting reports whether a ptyhost is ALREADY listening at path — a
// single, non-retrying dial. Used before launching to detect a survivor
// from a prior palmux2 lifetime: a nil error here means "attach, don't
// spawn."
func probeExisting(path string) (net.Conn, bool) {
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err != nil {
		return nil, false
	}
	return conn, true
}

// dialFresh dials path with the standard retry/backoff used right after a
// fresh launch (the process is detached/async, so the socket may not be
// immediately ready even though [waitForSocket] already confirmed it once).
func dialFresh(ctx context.Context, path string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("unix", path, ptyHostDialRetryInterval)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(ptyHostDialRetryInterval)
	}
	return nil, fmt.Errorf("claudeagent: dial ptyhost socket %s: %w", path, lastErr)
}
