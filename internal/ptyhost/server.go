package ptyhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	creackpty "github.com/creack/pty"
)

// Config configures a [Server]. Argv/Env/Cwd are OPAQUE to ptyhost (ADR-0002)
// — palmux2 builds the full command line, environment, and working directory
// and hands them over verbatim.
type Config struct {
	// Argv is the child command: Argv[0] is the executable, the rest are its
	// arguments. Required.
	Argv []string
	// Env is the full environment for the child. Empty means "inherit
	// ptyhost's own environment" (os.Environ()) — callers that want a fully
	// specified environment should pass it explicitly.
	Env []string
	// Cwd is the child's working directory. Empty means inherit.
	Cwd string

	// SocketPath is the unix socket path to serve the protocol on. Required.
	SocketPath string
	// StatusPath is the JSON status file path (see [StatusFile]). Required.
	StatusPath string

	// Seed is an OPAQUE label palmux2 hashes into the socket/status FILENAME
	// (see [FileKey]) and the systemd scope-unit name (Story 1). ptyhost does
	// not interpret it (ADR-0002 — it stays claude-agnostic) and it is echoed
	// verbatim into the on-disk [StatusFile] only for human/debug legibility.
	//
	// IMPORTANT: Seed is NOT a parseable identity. It happens to be built as
	// "repoId__branchId__tabId", but a repoId or branchId may itself contain
	// the literal substring "__" (domain IDs permit "_", and two adjacent
	// sanitized-out chars collapse to "__"), so splitting Seed back on "__"
	// does NOT reliably recover the original tuple. A discovery/GC pass must
	// read the explicit [Config.RepoID]/[Config.BranchID]/[Config.TabID]
	// fields below instead (S3f2658-3 orphan-GC data-loss fix).
	Seed string

	// RepoID/BranchID/TabID are the OPAQUE identity of the workspace tab this
	// ptyhost holds a process for, written DIRECTLY (no join, no parse) into
	// the on-disk [StatusFile] so a palmux2-side discovery/orphan-GC pass
	// (internal/tab/claudetui/discover.go, S3f2658-3) can recover exactly
	// which (repoId, branchId, tabId) a live ptyhost belongs to — even when
	// an ID contains "__" — without the ambiguity of parsing [Seed]. ptyhost
	// stores and returns these verbatim; it never acts on them (ADR-0002 —
	// claude-agnostic). All optional (empty is written as "" and simply
	// cannot be resolved back to an identity by a discovery pass).
	RepoID   string
	BranchID string
	TabID    string

	// RingSize is the ring buffer capacity in bytes. <= 0 uses
	// [DefaultRingSize].
	RingSize int
	// GracePeriod is how long to wait after SIGTERM before SIGKILL on
	// shutdown. <= 0 uses a 5s default.
	GracePeriod time.Duration
	// PostExitLinger is how long the socket stays up AFTER the child exits
	// before the server tears down and returns from Run. This is what makes
	// AC-S3f2658-1-4 ("a STATUS request returns alive=false with the exit
	// status... before the ptyhost tears down") observable rather than a
	// race: a connected client gets a bounded window to read the final
	// STATUS/DATA before the socket goes away. <= 0 uses a 1s default; the
	// on-disk status file (which never disappears) is the authoritative,
	// always-available record regardless of this window.
	PostExitLinger time.Duration

	Logger *slog.Logger
}

// StatusFile is the on-disk JSON status record written alongside the socket
// (see docs/no-halt-agent-design.md §3). It lets a discovery process learn a
// ptyhost's identity/liveness without connecting to the socket.
type StatusFile struct {
	Pid           int        `json:"pid"`
	Mode          string     `json:"mode"` // "pty"
	ArgvHash      string     `json:"argvHash"`
	StartedAt     time.Time  `json:"startedAt"`
	Alive         bool       `json:"alive"`
	ExitCode      int        `json:"exitCode"`
	ExitCodeValid bool       `json:"exitCodeValid"`
	ExitedAt      *time.Time `json:"exitedAt,omitempty"`
	// Seed is [Config.Seed] echoed verbatim for debug legibility ONLY — see
	// its doc comment; it is NOT a parseable identity. Use the explicit
	// RepoID/BranchID/TabID fields below for identity recovery.
	Seed string `json:"seed,omitempty"`
	// RepoID/BranchID/TabID are [Config.RepoID]/[Config.BranchID]/[Config.TabID]
	// echoed verbatim — the authoritative, unambiguous identity a discovery/GC
	// pass reads (S3f2658-3 orphan-GC data-loss fix). Written directly (no
	// join/parse) so an ID containing "__" round-trips exactly.
	RepoID   string `json:"repoId,omitempty"`
	BranchID string `json:"branchId,omitempty"`
	TabID    string `json:"tabId,omitempty"`
}

// Server owns one PTY-spawned child process, feeds its output into a
// [Ring], and serves the socket protocol. It is a thin holder (ADR-0002):
// once the child exits, Server records the exit status and Run returns —
// it does NOT respawn. Respawning (spawning a brand new ptyhost) is
// palmux2's responsibility.
type Server struct {
	cfg    Config
	logger *slog.Logger
	ring   *Ring

	mu            sync.Mutex
	ptmx          *os.File
	cmd           *exec.Cmd
	startedAt     time.Time
	alive         bool
	exitCode      int
	exitCodeValid bool

	childExited chan struct{} // closed once the child has exited and status recorded
	shutdownReq chan ShutdownPayload
	wg          sync.WaitGroup

	activeConnMu sync.Mutex
	activeConn   net.Conn
}

// NewServer validates cfg and constructs a Server. The child process is not
// started until [Server.Run].
func NewServer(cfg Config) (*Server, error) {
	if len(cfg.Argv) == 0 {
		return nil, fmt.Errorf("ptyhost: config: argv is empty")
	}
	if cfg.SocketPath == "" {
		return nil, fmt.Errorf("ptyhost: config: socket path is empty")
	}
	if cfg.StatusPath == "" {
		return nil, fmt.Errorf("ptyhost: config: status path is empty")
	}
	if cfg.RingSize <= 0 {
		cfg.RingSize = DefaultRingSize
	}
	if cfg.GracePeriod <= 0 {
		cfg.GracePeriod = 5 * time.Second
	}
	if cfg.PostExitLinger <= 0 {
		cfg.PostExitLinger = 1 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		cfg:         cfg,
		logger:      logger,
		ring:        NewRing(cfg.RingSize),
		childExited: make(chan struct{}),
		shutdownReq: make(chan ShutdownPayload, 1),
	}, nil
}

// Run spawns the child under a PTY, serves the socket protocol until the
// child exits, a SHUTDOWN is received, or ctx is canceled, then returns. Run
// does not respawn on child exit — see the [Server] doc comment.
func (s *Server) Run(ctx context.Context) error {
	if err := s.spawn(); err != nil {
		return fmt.Errorf("ptyhost: spawn: %w", err)
	}
	if err := s.writeStatusFile(false, 0, false, nil); err != nil {
		s.logger.Warn("ptyhost: write initial status file failed", "err", err)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.readLoop()
	}()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.waitChild()
	}()

	ln, err := s.listen()
	if err != nil {
		s.terminateChild(s.cfg.GracePeriod)
		<-s.childExited
		s.wg.Wait()
		return fmt.Errorf("ptyhost: listen: %w", err)
	}

	connCh := make(chan net.Conn)
	acceptErrCh := make(chan error, 1)
	// stopAccept is closed FIRST in teardown (before ln.Close()) so the
	// accept-loop goroutine's connCh send below has a guaranteed escape
	// hatch. Without this, a connection that Accept() returns in the tiny
	// window between the main select loop below choosing its shutdown/ctx
	// branch (which stops servicing connCh — see the loop's `return` paths)
	// and ln.Close() actually taking effect would block that goroutine on
	// `connCh <- conn` FOREVER (nobody left to receive), which in turn hangs
	// s.wg.Wait() in teardown() forever, which hangs Run() forever. This is
	// not a hypothetical: a discovery/orphan-GC pass (S3f2658-3) that
	// liveness-probes (dial+HELLO+close) a ptyhost RIGHT AS it is being
	// SHUTDOWN can land exactly in this window, and does so more often under
	// real scheduling contention (observed as an intermittent goroutine leak
	// / Run()-never-returns hang in internal/tab/claudetui's discovery/GC
	// test suite before this fix).
	stopAccept := make(chan struct{})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				acceptErrCh <- aerr
				return
			}
			select {
			case connCh <- conn:
			case <-stopAccept:
				// Run() is tearing down and will never service connCh again
				// — this straggler connection (accepted in the shutdown
				// race window described above) gets closed instead of
				// leaking the goroutine that's holding it.
				_ = conn.Close()
				return
			}
		}
	}()

	// teardown closes stopAccept (unblocks a straggler connCh send — see
	// above), THEN the listener (which is what unblocks the accept-loop's
	// NEXT Accept() call), THEN the active client connection, all BEFORE
	// waiting on wg — waiting first would deadlock, since those goroutines
	// only return once unblocked by one of these three signals.
	teardown := func() {
		close(stopAccept)
		_ = ln.Close()
		_ = os.Remove(s.cfg.SocketPath)
		s.closeActiveConn()
		s.wg.Wait()
	}

	// childExitedCh mirrors s.childExited but is set to nil after first
	// firing so the select below can't busy-loop on an already-closed
	// channel; lingerCh starts the post-exit linger window described on
	// [Config.PostExitLinger] once the child has exited, keeping the socket
	// (and any connected client) alive long enough for a final STATUS
	// request to observe the exit before teardown (AC-S3f2658-1-4).
	childExitedCh := s.childExited
	var lingerCh <-chan time.Time

	for {
		select {
		case conn := <-connCh:
			s.replaceConn(conn)
		case <-acceptErrCh:
			// The listener is closed only by our own teardown() below, by
			// which point nothing else reads this channel. Disable this arm
			// so a transient/duplicate error can't busy-loop us.
			acceptErrCh = nil
		case <-childExitedCh:
			childExitedCh = nil
			lingerCh = time.After(s.cfg.PostExitLinger)
		case <-lingerCh:
			teardown()
			return nil
		case sp := <-s.shutdownReq:
			grace := s.cfg.GracePeriod
			if sp.GraceMillis > 0 {
				grace = time.Duration(sp.GraceMillis) * time.Millisecond
			}
			s.terminateChild(grace)
			<-s.childExited
			teardown()
			return nil
		case <-ctx.Done():
			s.terminateChild(s.cfg.GracePeriod)
			<-s.childExited
			teardown()
			return ctx.Err()
		}
	}
}

// spawn starts the child under a PTY.
func (s *Server) spawn() error {
	cmd := exec.Command(s.cfg.Argv[0], s.cfg.Argv[1:]...)
	if s.cfg.Cwd != "" {
		cmd.Dir = s.cfg.Cwd
	}
	if len(s.cfg.Env) > 0 {
		cmd.Env = s.cfg.Env
	}
	ptmx, err := creackpty.Start(cmd)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.ptmx = ptmx
	s.cmd = cmd
	s.startedAt = time.Now()
	s.alive = true
	s.mu.Unlock()
	s.logger.Info("ptyhost: child spawned", "argv", s.cfg.Argv, "pid", cmd.Process.Pid)
	return nil
}

// readLoop pumps PTY output into the ring until the PTY master is closed
// (child exit) or a read error occurs.
func (s *Server) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			_, _ = s.ring.Write(chunk)
		}
		if err != nil {
			return
		}
	}
}

// waitChild is the SOLE caller of cmd.Wait() for this child (avoids double-
// Wait races). It records the exit status, updates the status file, and
// closes childExited.
func (s *Server) waitChild() {
	err := s.cmd.Wait()
	code := 0
	valid := true
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			// Wait itself failed (e.g. I/O error) rather than the process
			// exiting non-zero — no reliable exit code.
			valid = false
		}
	}
	exitedAt := time.Now()

	s.mu.Lock()
	s.alive = false
	s.exitCode = code
	s.exitCodeValid = valid
	s.mu.Unlock()

	_ = s.ptmx.Close()

	if werr := s.writeStatusFile(true, code, valid, &exitedAt); werr != nil {
		s.logger.Warn("ptyhost: write exit status file failed", "err", werr)
	}
	s.logger.Info("ptyhost: child exited", "exitCode", code, "exitCodeValid", valid)
	close(s.childExited)
}

// terminateChild sends SIGTERM, waits up to grace for the child to exit
// (observed via childExited), then escalates to SIGKILL. It is a no-op if
// the child is already known to be exited.
func (s *Server) terminateChild(grace time.Duration) {
	s.mu.Lock()
	var proc *os.Process
	if s.cmd != nil {
		proc = s.cmd.Process
	}
	alive := s.alive
	s.mu.Unlock()
	if !alive || proc == nil {
		return
	}
	_ = proc.Signal(syscall.SIGTERM)
	select {
	case <-s.childExited:
		return
	case <-time.After(grace):
	}
	_ = proc.Signal(syscall.SIGKILL)
}

// listen ensures the socket directory exists, removes a stale socket file
// left behind by a crashed previous run, and binds a unix listener.
func (s *Server) listen() (net.Listener, error) {
	dir := filepath.Dir(s.cfg.SocketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ptyhost: mkdir socket dir: %w", err)
	}
	_ = os.Remove(s.cfg.SocketPath)
	ln, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("ptyhost: listen unix %s: %w", s.cfg.SocketPath, err)
	}
	return ln, nil
}

// replaceConn closes any previously active connection (palmux2 reconnecting
// after its own restart supersedes the stale one) and starts handling the
// new one. Only one client connection is meaningfully active at a time (see
// docs/no-halt-agent-design.md §2).
func (s *Server) replaceConn(conn net.Conn) {
	s.activeConnMu.Lock()
	prev := s.activeConn
	s.activeConn = conn
	s.activeConnMu.Unlock()
	if prev != nil {
		_ = prev.Close()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.handleConn(conn)
	}()
}

func (s *Server) closeActiveConn() {
	s.activeConnMu.Lock()
	conn := s.activeConn
	s.activeConn = nil
	s.activeConnMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// handleConn services one client connection: reads frames, dispatches by
// type, and writes frames back (HELLO reply, DATA replay/live, STATUS
// response). Returns when the connection errors/closes or a SHUTDOWN is
// received. Its lifetime is bounded by Run's teardown, which force-closes
// the active connection (see closeActiveConn) rather than by a context
// passed down here.
func (s *Server) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	var writeMu sync.Mutex
	writeFrame := func(t MsgType, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return WriteFrame(conn, t, payload)
	}

	var sub *Subscription
	defer func() {
		if sub != nil {
			s.ring.Unsubscribe(sub)
		}
	}()

	startPump := func(sub *Subscription) {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for {
				select {
				case chunk, ok := <-sub.Ch:
					if !ok {
						return
					}
					if err := writeFrame(MsgData, EncodeData(chunk.Offset, chunk.Data)); err != nil {
						return
					}
				case <-sub.Done:
					return
				}
			}
		}()
	}

	for {
		t, payload, err := ReadFrame(conn)
		if err != nil {
			return
		}
		switch t {
		case MsgHello:
			_ = writeFrame(MsgHello, EncodeHello(s.helloPayload()))

		case MsgAttach:
			offset, derr := DecodeAttach(payload)
			if derr != nil {
				continue
			}
			if sub != nil {
				s.ring.Unsubscribe(sub)
				sub = nil
			}
			data, start, newSub := s.ring.SnapshotAndSubscribe(offset)
			sub = newSub
			if werr := writeFrame(MsgData, EncodeData(start, data)); werr != nil {
				return
			}
			startPump(sub)

		case MsgInput:
			s.writeInput(DecodeInput(payload))

		case MsgResize:
			cols, rows, derr := DecodeResize(payload)
			if derr == nil {
				s.resize(cols, rows)
			}

		case MsgAck:
			// No-op in pty mode; the lossless-replay ack/offset-persistence
			// path is agent (pipe-mode) territory — Sprint 2 / ADR-0004.

		case MsgStatus:
			if IsStatusRequest(payload) {
				_ = writeFrame(MsgStatus, EncodeStatusResponse(s.statusPayload()))
			}

		case MsgShutdown:
			sp, _ := DecodeShutdown(payload)
			select {
			case s.shutdownReq <- sp:
			default:
			}
			return

		default:
			// Unknown message type: ignore (forward-compat with a future
			// protocol version per the HELLO version-mismatch policy).
		}
	}
}

func (s *Server) writeInput(b []byte) {
	s.mu.Lock()
	ptmx := s.ptmx
	s.mu.Unlock()
	if ptmx == nil || len(b) == 0 {
		return
	}
	_, _ = ptmx.Write(b)
}

func (s *Server) resize(cols, rows uint16) {
	s.mu.Lock()
	ptmx := s.ptmx
	s.mu.Unlock()
	if ptmx == nil {
		return
	}
	_ = creackpty.Setsize(ptmx, &creackpty.Winsize{Rows: rows, Cols: cols})
}

func (s *Server) helloPayload() HelloPayload {
	s.mu.Lock()
	pid := 0
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	s.mu.Unlock()
	return HelloPayload{
		ProtocolVersion: ProtocolVersion,
		Mode:            "pty",
		Pid:             pid,
		ArgvHash:        ArgvHash(s.cfg.Argv),
	}
}

func (s *Server) statusPayload() StatusPayload {
	s.mu.Lock()
	pid := 0
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	alive, exitCode, exitCodeValid := s.alive, s.exitCode, s.exitCodeValid
	s.mu.Unlock()
	return StatusPayload{
		Pid:             pid,
		Alive:           alive,
		ExitCode:        exitCode,
		ExitCodeValid:   exitCodeValid,
		RingBytes:       s.ring.Len(),
		RingHeadOffset:  s.ring.OldestOffset(),
		RingTotalOffset: s.ring.TotalWritten(),
	}
}

// writeStatusFile writes the on-disk [StatusFile] atomically (write-then-
// rename) so a discovery process never observes a partially written file.
func (s *Server) writeStatusFile(exited bool, exitCode int, exitCodeValid bool, exitedAt *time.Time) error {
	s.mu.Lock()
	pid := 0
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	sf := StatusFile{
		Pid:           pid,
		Mode:          "pty",
		ArgvHash:      ArgvHash(s.cfg.Argv),
		StartedAt:     s.startedAt,
		Alive:         !exited,
		ExitCode:      exitCode,
		ExitCodeValid: exitCodeValid,
		ExitedAt:      exitedAt,
		Seed:          s.cfg.Seed,
		RepoID:        s.cfg.RepoID,
		BranchID:      s.cfg.BranchID,
		TabID:         s.cfg.TabID,
	}
	s.mu.Unlock()
	return writeStatusFileAtomic(s.cfg.StatusPath, sf)
}

func writeStatusFileAtomic(path string, sf StatusFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("ptyhost: mkdir status dir: %w", err)
	}
	b, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("ptyhost: marshal status file: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("ptyhost: write status tmp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("ptyhost: rename status file: %w", err)
	}
	return nil
}

// ReadStatusFile reads and parses a [StatusFile] from path. Exported for
// palmux2-side discovery (future story) and for tests.
func ReadStatusFile(path string) (StatusFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return StatusFile{}, fmt.Errorf("ptyhost: read status file: %w", err)
	}
	var sf StatusFile
	if err := json.Unmarshal(b, &sf); err != nil {
		return StatusFile{}, fmt.Errorf("ptyhost: parse status file: %w", err)
	}
	return sf, nil
}
