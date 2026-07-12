package claudetui

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

// This file is the socket-client seam between claudetui.Daemon and the
// `palmux ptyhost` process holding its claude subprocess (ADR-0002 — thin
// holder). Daemon builds the same argv/env/cwd it always has (hooks,
// --permission-mode, incus PTYCommander wrapper, --plugin-dir — see
// spawnWithArgs in daemon.go) and hands it here as an opaque
// [PtyHostLaunchRequest]; ptyhost itself has zero claude-specific knowledge.

// ptyHostDialTimeout bounds how long dialAndHello waits for a socket to
// accept connections after a fresh launch (the launched process is
// detached/async — see internal/ptyhost/launch.go — so the socket is not
// guaranteed to exist the instant Launch returns).
const ptyHostDialTimeout = 5 * time.Second

// ptyHostDialRetryInterval is the backoff between dial attempts while
// waiting for a just-launched ptyhost to start listening.
const ptyHostDialRetryInterval = 20 * time.Millisecond

// PtyHostLaunchRequest describes a ptyhost a Daemon wants spawned (or, if one
// is already listening at SocketPath, attached to instead — see
// [Daemon.launchAndAttach]). Argv/Env/Cwd are the fully-assembled, opaque
// command line built by spawnWithArgs; ptyhost interprets none of it
// (ADR-0002).
type PtyHostLaunchRequest struct {
	// PalmuxBin is the palmux binary to re-invoke as `<PalmuxBin> ptyhost
	// ...` (production launch path only; ignored by the in-process
	// dev/test fallback — see [inProcessLaunchPtyHost]).
	PalmuxBin string
	// InstancePrefix isolates concurrent palmux instances (host vs
	// INSTANCE=dev rigs), mirroring domain.PalmuxSessionPrefix.
	InstancePrefix string
	// Seed is hashed into the systemd scope unit name for readability
	// (repoId__branchId__tabId).
	Seed string
	// SocketPath / StatusPath are where the ptyhost must listen / write its
	// status file — precomputed by the caller (Daemon) so the "does a
	// survivor already exist" dial-first check and the actual launch agree
	// on the same location.
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

// PtyHostLaunchFunc starts (or ensures listening) a ptyhost for req. On
// return without error, req.SocketPath must be ready to accept connections
// (implementations block/retry internally as needed). Production uses
// [defaultLaunchPtyHost] (the real ADR-0003 cgroup-escape spawn); tests may
// inject [inProcessLaunchPtyHost] or a custom fake via
// DaemonConfig.PtyHostLaunch.
type PtyHostLaunchFunc func(ctx context.Context, req PtyHostLaunchRequest) error

// defaultLaunchPtyHost is the production implementation: it re-invokes
// `<PalmuxBin> ptyhost ...` via the real [ptyhost.Launcher] (systemd-run
// --user --scope cgroup-escape, falling back to setsid — ADR-0003) so the
// spawned ptyhost (and the claude process/incus-wrapper it holds) survives
// this palmux2 process's own death, then waits for the socket to accept
// connections.
func defaultLaunchPtyHost(ctx context.Context, req PtyHostLaunchRequest) error {
	if req.PalmuxBin == "" {
		return fmt.Errorf("claudetui: ptyhost launch: PalmuxBin is empty")
	}
	args := []string{"--socket", req.SocketPath, "--status", req.StatusPath}
	if req.Seed != "" {
		// Opaque identity label echoed into the status file — see
		// [ptyhost.Config.Seed] doc comment; enables S3f2658-3 discovery/GC
		// to recover (repoId, branchId, tabId) from disk.
		args = append(args, "--seed", req.Seed)
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
		return fmt.Errorf("claudetui: ptyhost launch: %w", err)
	}
	return waitForSocket(ctx, req.SocketPath, ptyHostDialTimeout, nil)
}

// testPtyHostSeq isolates each auto-fallback (PalmuxBin=="") Daemon's ptyhost
// run directory from every other one in the same test binary, regardless of
// whether the owning tests clean up promptly or run with shared/empty
// repoID/branchID/tabID (most unit tests do). See DaemonConfig.PalmuxBin.
var testPtyHostSeq atomic.Int64

// inProcessLaunchPtyHost is the automatic fallback used when
// DaemonConfig.PalmuxBin is empty (the case for virtually all existing unit
// tests, none of which set it): instead of spawning a REAL detached `palmux
// ptyhost` OS process, it runs a real [ptyhost.Server] as a goroutine in the
// CURRENT process. This is still the genuine ptyhost protocol/ring/spawn
// code (AC-S3f2658-2-1's "Integration: with a real ptyhost holding a fake
// child") — only the OS-process-detachment half (Story 1's ADR-0003 spawn
// path, already covered by internal/ptyhost's own tests) is swapped out, so
// the claudetui test suite stays hermetic and fast without needing to build
// and exec a real `palmux` binary.
//
// Deliberately NOT wired to ctx: like the production launcher, the server
// must outlive the calling request/spawn context (ptyhost survives palmux2
// restarts by design) — here that just means it survives until its own
// child exits or a SHUTDOWN message arrives.
func inProcessLaunchPtyHost(ctx context.Context, req PtyHostLaunchRequest) error {
	srv, err := ptyhost.NewServer(ptyhost.Config{
		Argv:       req.Argv,
		Env:        req.Env,
		Cwd:        req.Cwd,
		SocketPath: req.SocketPath,
		StatusPath: req.StatusPath,
		RingSize:   req.RingSize,
		Seed:       req.Seed,
	})
	if err != nil {
		return fmt.Errorf("claudetui: in-process ptyhost: %w", err)
	}
	// Buffered so the goroutine's send never blocks even after this function
	// has long since returned on the success path (Run() only returns once
	// the held child eventually exits, which can be long after launch).
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- srv.Run(context.Background()) }()
	return waitForSocket(ctx, req.SocketPath, ptyHostDialTimeout, runErrCh)
}

// autoTestRunDir returns a fresh, per-call-unique temp directory to hold
// ptyhost sockets for a Daemon that did not opt into production wiring
// (PalmuxBin == ""). Exported-shaped (unexported) helper kept separate from
// [inProcessLaunchPtyHost] so DaemonConfig.RunDirOverride can still win when
// a test explicitly wants two Daemons to share one ptyhost run directory
// (simulating "palmux2 restarted, new Daemon object, same surviving
// ptyhost").
func autoTestRunDir() string {
	return filepath.Join(os.TempDir(), "palmux-ptyhost-dev",
		fmt.Sprintf("d%d-%d", os.Getpid(), testPtyHostSeq.Add(1)))
}

// waitForSocket polls until a unix socket at path accepts a connection (used
// after launching to bound the async-startup window rather than dialing
// exactly once) or ctx/timeout expires. earlyErr, if non-nil, is the launched
// ptyhost's own Run() outcome (in-process launch only) — a value received on
// it BEFORE the socket ever becomes reachable means the spawn failed before
// it got as far as listening (e.g. a nonexistent child binary), which lets
// callers fail fast instead of waiting out the full timeout. earlyErr may be
// nil (production/detached launch has no such channel to observe).
func waitForSocket(ctx context.Context, path string, timeout time.Duration, earlyErr <-chan error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-earlyErr:
			// Give the socket one last, immediate check — Run() can return
			// nil almost instantly for a trivial child (e.g. `true`) without
			// that being a failure.
			if conn, derr := net.DialTimeout("unix", path, ptyHostDialRetryInterval); derr == nil {
				_ = conn.Close()
				return nil
			}
			if err != nil {
				return fmt.Errorf("claudetui: ptyhost exited before listening: %w", err)
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
	return fmt.Errorf("claudetui: ptyhost socket %s not accepting connections after %v: %w", path, timeout, lastErr)
}

// probeExisting reports whether a ptyhost is ALREADY listening at path — a
// single, non-retrying dial. Used before launching to detect a survivor from
// a prior palmux2 lifetime (§3 of docs/no-halt-agent-design.md): a nil error
// here means "attach, don't spawn."
func probeExisting(path string) (net.Conn, bool) {
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err != nil {
		return nil, false
	}
	return conn, true
}

// dialFresh dials path with the standard retry/backoff used right after a
// fresh launch (the process is detached/async, so the socket may not be
// immediately ready even though [waitForSocket] already confirmed it once —
// dial again for the connection we'll actually keep, since waitForSocket's
// probe connection was already closed).
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
	return nil, fmt.Errorf("claudetui: dial ptyhost socket %s: %w", path, lastErr)
}

// sendHello writes a HELLO frame and reads the reply.
func sendHello(conn net.Conn) (ptyhost.HelloPayload, error) {
	if err := ptyhost.WriteFrame(conn, ptyhost.MsgHello, ptyhost.EncodeHello(ptyhost.HelloPayload{
		ProtocolVersion: ptyhost.ProtocolVersion,
	})); err != nil {
		return ptyhost.HelloPayload{}, fmt.Errorf("claudetui: write HELLO: %w", err)
	}
	t, payload, err := ptyhost.ReadFrame(conn)
	if err != nil {
		return ptyhost.HelloPayload{}, fmt.Errorf("claudetui: read HELLO reply: %w", err)
	}
	if t != ptyhost.MsgHello {
		return ptyhost.HelloPayload{}, fmt.Errorf("claudetui: expected HELLO reply, got %v", t)
	}
	hello, err := ptyhost.DecodeHello(payload)
	if err != nil {
		return ptyhost.HelloPayload{}, fmt.Errorf("claudetui: decode HELLO reply: %w", err)
	}
	return hello, nil
}

// sendAttach writes an ATTACH request for offset and reads the first DATA
// reply (the replay). offset == -1 means "from the oldest byte still
// retained" (see ptyhost.EncodeAttach).
func sendAttach(conn net.Conn, offset int64) ([]byte, error) {
	if err := ptyhost.WriteFrame(conn, ptyhost.MsgAttach, ptyhost.EncodeAttach(offset)); err != nil {
		return nil, fmt.Errorf("claudetui: write ATTACH: %w", err)
	}
	t, payload, err := ptyhost.ReadFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("claudetui: read ATTACH reply: %w", err)
	}
	if t != ptyhost.MsgData {
		return nil, fmt.Errorf("claudetui: expected DATA reply to ATTACH, got %v", t)
	}
	_, data, err := ptyhost.DecodeData(payload)
	if err != nil {
		return nil, fmt.Errorf("claudetui: decode ATTACH replay: %w", err)
	}
	// DecodeData aliases payload; copy since the caller may retain this
	// beyond the read buffer's lifetime.
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}
