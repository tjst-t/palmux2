package pty

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	creackpty "github.com/creack/pty"
)

const (
	// DefaultRingSize is the default ring buffer capacity (1 MiB).
	DefaultRingSize = 1 << 20
	// ptyReadBufSize is the size of the read buffer for PTY output.
	ptyReadBufSize = 4096
	// gracefulShutdownTimeout is how long we wait for the subprocess to
	// exit after sending SIGTERM before escalating to SIGKILL.
	gracefulShutdownTimeout = 5 * time.Second
)

// State represents the lifecycle state of the daemon's subprocess.
type State int32

const (
	StateIdle     State = iota // claude not yet spawned
	StateRunning               // claude is alive
	StateDead                  // claude exited unexpectedly
	StateShutdown              // daemon is shutting down intentionally
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateRunning:
		return "running"
	case StateDead:
		return "dead"
	case StateShutdown:
		return "shutdown"
	default:
		return "unknown"
	}
}

// subscriber is a write-only sink that receives PTY output chunks in real-time.
type subscriber struct {
	ch   chan []byte
	done chan struct{}
}

// Daemon owns the PTY subprocess (claude or substitute) and multiplexes
// output to an arbitrary number of WebSocket clients via a fan-out mechanism.
//
// Lifecycle:
//  1. NewDaemon() — allocate, no subprocess yet.
//  2. first WS attach calls EnsureStarted() which spawns the subprocess lazily.
//  3. SIGTERM/SIGINT → Shutdown().
type Daemon struct {
	// configuration
	claudeBin  string
	claudeArgs []string
	ring       *RingBuffer

	// daemonCtx is owned by the daemon and lives until Shutdown().
	// IMPORTANT: subprocess is spawned under daemonCtx, NOT the
	// per-request HTTP context, so that WS client disconnects do NOT
	// kill the subprocess (priority_rule 4 + "keep daemon alive").
	daemonCtx    context.Context
	daemonCancel context.CancelFunc

	// process state
	stateMu   sync.Mutex
	state     atomic.Int32 // holds State; read without lock for perf
	proc      *os.Process
	ptmx      *os.File // master side of PTY
	sessionID string   // session ID parsed from subprocess output (best-effort)

	// fan-out
	subMu sync.Mutex
	subs  map[*subscriber]struct{}

	// shutdown signalling
	shutdownCh chan struct{}
	shutdownWg sync.WaitGroup

	// spawn guard — prevents concurrent spawn
	spawnOnce sync.Once

	// for tests / stats
	attachedCount atomic.Int32
}

// NewDaemon creates a daemon with the given subprocess and ring buffer settings.
func NewDaemon(claudeBin string, claudeArgs []string, ringSize int) *Daemon {
	ctx, cancel := context.WithCancel(context.Background())
	d := &Daemon{
		claudeBin:    claudeBin,
		claudeArgs:   claudeArgs,
		ring:         NewRingBuffer(ringSize),
		subs:         make(map[*subscriber]struct{}),
		shutdownCh:   make(chan struct{}),
		daemonCtx:    ctx,
		daemonCancel: cancel,
	}
	d.state.Store(int32(StateIdle))
	return d
}

// Stats returns the current daemon statistics snapshot.
type Stats struct {
	PID             int    `json:"pid"`
	RingBytes       int    `json:"ring_bytes"`
	AttachedClients int32  `json:"attached_clients"`
	Alive           bool   `json:"alive"`
	State           string `json:"state"`
}

// CurrentStats returns a Stats snapshot (no lock held on long paths).
func (d *Daemon) CurrentStats() Stats {
	st := State(d.state.Load())
	alive := st == StateRunning
	pid := 0
	d.stateMu.Lock()
	if d.proc != nil {
		pid = d.proc.Pid
	}
	d.stateMu.Unlock()
	return Stats{
		PID:             pid,
		RingBytes:       d.ring.Len(),
		AttachedClients: d.attachedCount.Load(),
		Alive:           alive,
		State:           st.String(),
	}
}

// EnsureStarted spawns the subprocess under a PTY on the first call.
// Subsequent calls are no-ops (sync.Once).  If the daemon is already
// shutting down, EnsureStarted returns an error.
//
// The subprocess is spawned under the daemon's own context (daemonCtx),
// not the caller's context.  This ensures that a WS client disconnect
// does NOT kill the subprocess.
func (d *Daemon) EnsureStarted(_ context.Context) error {
	if State(d.state.Load()) == StateShutdown {
		return fmt.Errorf("daemon is shutting down")
	}
	var spawnErr error
	d.spawnOnce.Do(func() {
		spawnErr = d.spawn(d.daemonCtx)
	})
	return spawnErr
}

// spawn creates the PTY pair, starts the subprocess, and launches the
// read loop in a background goroutine.  Must be called exactly once.
func (d *Daemon) spawn(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, d.claudeBin, d.claudeArgs...)
	// Inherit a minimal environment so interactive TUIs work.
	cmd.Env = os.Environ()
	// Set TERM so TUI programs like claude render correctly.
	cmd.Env = appendOrReplace(cmd.Env, "TERM=xterm-256color")

	ptmx, err := creackpty.Start(cmd)
	if err != nil {
		return fmt.Errorf("pty.Start: %w", err)
	}

	d.stateMu.Lock()
	d.ptmx = ptmx
	d.proc = cmd.Process
	d.state.Store(int32(StateRunning))
	d.stateMu.Unlock()

	slog.Info("poc-pty: subprocess spawned",
		"bin", d.claudeBin,
		"args", d.claudeArgs,
		"pid", cmd.Process.Pid,
	)

	// Background: read PTY output, write to ring, fan-out to subscribers.
	d.shutdownWg.Add(1)
	go func() {
		defer d.shutdownWg.Done()
		d.readLoop()
	}()

	// Background: wait for subprocess to exit.
	d.shutdownWg.Add(1)
	go func() {
		defer d.shutdownWg.Done()
		err := cmd.Wait()
		d.stateMu.Lock()
		cur := State(d.state.Load())
		d.stateMu.Unlock()
		if cur != StateShutdown {
			// Unexpected exit.
			slog.Warn("poc-pty: claude died, will resume",
				"pid", cmd.Process.Pid,
				"err", err,
			)
			d.state.Store(int32(StateDead))
		}
	}()

	return nil
}

// readLoop pumps bytes from the PTY master into the ring buffer and
// broadcasts them to all subscribers.
func (d *Daemon) readLoop() {
	buf := make([]byte, ptyReadBufSize)
	for {
		select {
		case <-d.shutdownCh:
			return
		default:
		}
		n, err := d.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			d.ring.Write(chunk)
			d.broadcast(chunk)
		}
		if err != nil {
			if err == io.EOF {
				return
			}
			// PTY closed (e.g. subprocess exited)
			return
		}
	}
}

// broadcast sends a chunk to every registered subscriber.
// If a subscriber's channel is full, the chunk is dropped for that
// subscriber (non-blocking send) to avoid back-pressure on the PTY read loop.
func (d *Daemon) broadcast(chunk []byte) {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	for sub := range d.subs {
		select {
		case sub.ch <- chunk:
		default:
			// Subscriber is lagging; drop this chunk for it.
		}
	}
}

// Subscribe registers a new subscriber and returns its channel.  Call
// Unsubscribe when done.
func (d *Daemon) Subscribe() *subscriber {
	sub := &subscriber{
		ch:   make(chan []byte, 256),
		done: make(chan struct{}),
	}
	d.subMu.Lock()
	d.subs[sub] = struct{}{}
	d.subMu.Unlock()
	d.attachedCount.Add(1)
	return sub
}

// Unsubscribe removes a subscriber.
func (d *Daemon) Unsubscribe(sub *subscriber) {
	d.subMu.Lock()
	delete(d.subs, sub)
	d.subMu.Unlock()
	d.attachedCount.Add(-1)
	close(sub.done)
}

// WriteInput writes bytes to the PTY master (stdin of the subprocess).
func (d *Daemon) WriteInput(p []byte) error {
	d.stateMu.Lock()
	ptmx := d.ptmx
	d.stateMu.Unlock()
	if ptmx == nil {
		return fmt.Errorf("subprocess not started")
	}
	_, err := ptmx.Write(p)
	return err
}

// Resize sends a SIGWINCH-equivalent resize notification to the PTY.
func (d *Daemon) Resize(cols, rows uint16) error {
	d.stateMu.Lock()
	ptmx := d.ptmx
	d.stateMu.Unlock()
	if ptmx == nil {
		return fmt.Errorf("subprocess not started")
	}
	return creackpty.Setsize(ptmx, &creackpty.Winsize{
		Rows: rows,
		Cols: cols,
	})
}

// ReplayRing sends the entire ring buffer to dst in a single write.
// Used to populate a newly attached WS client.
func (d *Daemon) ReplayRing(dst io.Writer) error {
	data := d.ring.Bytes()
	if len(data) == 0 {
		return nil
	}
	_, err := dst.Write(data)
	return err
}

// Shutdown performs a graceful shutdown:
//  1. Sets state to StateShutdown.
//  2. Cancels the daemon context (stops readLoop).
//  3. Sends SIGTERM to the subprocess.
//  4. Waits up to gracefulShutdownTimeout; escalates to SIGKILL.
//  5. Closes the PTY master fd.
//  6. Closes shutdownCh so the read loop exits.
func (d *Daemon) Shutdown() {
	d.state.Store(int32(StateShutdown))
	d.daemonCancel()
	close(d.shutdownCh)

	d.stateMu.Lock()
	proc := d.proc
	ptmx := d.ptmx
	d.stateMu.Unlock()

	if proc != nil {
		slog.Info("poc-pty: sending SIGTERM to subprocess", "pid", proc.Pid)
		proc.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			proc.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(gracefulShutdownTimeout):
			slog.Warn("poc-pty: subprocess did not exit; sending SIGKILL", "pid", proc.Pid)
			proc.Signal(syscall.SIGKILL)
		}
	}
	if ptmx != nil {
		ptmx.Close()
	}

	d.shutdownWg.Wait()
	slog.Info("poc-pty: daemon shutdown complete")
}

// SessionID returns the session ID that was recorded during the first run
// (may be empty if not yet parsed).
func (d *Daemon) SessionID() string {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	return d.sessionID
}

// appendOrReplace either appends "KEY=value" to env or replaces the
// existing entry with matching key.
func appendOrReplace(env []string, kv string) []string {
	key := strings.SplitN(kv, "=", 2)[0] + "="
	for i, e := range env {
		if strings.HasPrefix(e, key) {
			env[i] = kv
			return env
		}
	}
	return append(env, kv)
}
