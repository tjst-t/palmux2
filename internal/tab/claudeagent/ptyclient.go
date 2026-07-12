package claudeagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
)

// This file is the MECHANISM half of S862203-2 (ADR-0004): a generic,
// claude-agnostic line-oriented replay client for a pipe-mode ptyhost. It
// mirrors internal/tab/claudetui/ptyclient.go's dial/HELLO/ATTACH pattern,
// extended for pipe mode's needs:
//
//   - stdout bytes are reassembled into NDJSON lines across DATA frame
//     boundaries (a line may straddle two frames) and delivered to a
//     per-line callback together with the ABSOLUTE offset to resume from on
//     the next ATTACH (i.e. the offset of the first byte NOT yet processed —
//     the byte immediately after that line's trailing '\n').
//   - stderr bytes (MsgStderrData — the ADR-0004 §6 sanctioned protocol
//     addition) are delivered raw, unreassembled, to a separate callback.
//   - ring-overflow (the requested ATTACH offset was evicted) is detected by
//     comparing the requested offset against ptyhost's clamped return value
//     and surfaced as [AttachResult.Overflowed] — never silently swallowed
//     into a gapped replay.
//
// WHAT THIS FILE DELIBERATELY DOES NOT DO (ADR-0002/ADR-0004 boundary):
//   - It does not persist anything. Persistence is [OffsetStore]
//     (offsetstore.go), driven by the caller from inside the LineHandler
//     callback.
//   - It does not reconstruct transcript/permstate/MCP state. That's
//     Story 3 (client.go), which will feed replayed lines through the
//     UNCHANGED processStreamMessage path (decisions.json PD-5).
//   - It sends ACK to ptyhost purely as an informational courtesy (per
//     ADR-0002/PD-4, ptyhost does not persist it) — the client's own ACK
//     failing/being lost is harmless, since the palmux2-side OffsetStore is
//     the actual source of truth for what to request on the next ATTACH.

// LineHandler is invoked once per fully-reassembled NDJSON line (the raw
// bytes, WITHOUT the trailing '\n'). endOffset is the absolute ptyhost
// offset of the byte immediately after that line's '\n' — i.e. exactly the
// offset a caller should persist and request via ATTACH on the next
// reconnect to resume right after this line. A non-nil return stops
// [PipeClient.Run] (propagated as its error) without acking/persisting that
// line, so it will be redelivered on the next successful reconnect.
type LineHandler func(line []byte, endOffset int64) error

// StderrHandler is invoked with each raw stderr chunk ptyhost delivers (no
// line reassembly — stderr is diagnostic-only). May be nil to discard
// stderr entirely.
type StderrHandler func(offset int64, data []byte)

// AttachResult reports the outcome of the ATTACH handshake performed at the
// start of [PipeClient.Run].
type AttachResult struct {
	// Requested is the offset that was asked for (as passed to Run).
	Requested int64
	// StartOffset is the actual absolute offset ptyhost's replay began at
	// (Ring.readFromLocked's clamped return value).
	StartOffset int64
	// Overflowed is true when Requested >= 0 and StartOffset > Requested —
	// i.e. the ring evicted bytes between Requested and StartOffset before
	// this ATTACH, so a gap exists: lossless replay is impossible and the
	// caller must treat this as a NEW session (AC-S862203-2-3), never as a
	// silently-truncated continuation. Always false when Requested < 0
	// ("from oldest retained" has no prior expectation to violate).
	Overflowed bool
}

// PipeClient is a socket client for a pipe-mode ptyhost (see
// internal/ptyhost's Server/protocol). One PipeClient serves one ATTACH
// session; call [PipeClient.Run] once, then discard it (mirrors ptyhost's
// own "one active connection at a time" model — see docs/no-halt-agent-design.md §2).
type PipeClient struct {
	conn     net.Conn
	onLine   LineHandler
	onStderr StderrHandler
}

// DialPipeClient dials a ptyhost unix socket. onLine is required; onStderr
// may be nil (stderr is then silently discarded).
func DialPipeClient(ctx context.Context, socketPath string, onLine LineHandler, onStderr StderrHandler) (*PipeClient, error) {
	if onLine == nil {
		return nil, errors.New("claudeagent: DialPipeClient: onLine is required")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("claudeagent: dial ptyhost socket %s: %w", socketPath, err)
	}
	return &PipeClient{conn: conn, onLine: onLine, onStderr: onStderr}, nil
}

// Hello performs a HELLO round-trip, returning the ptyhost's reported
// protocol version/mode/pid/argv hash.
func (c *PipeClient) Hello() (ptyhost.HelloPayload, error) {
	if err := ptyhost.WriteFrame(c.conn, ptyhost.MsgHello, ptyhost.EncodeHello(ptyhost.HelloPayload{
		ProtocolVersion: ptyhost.ProtocolVersion,
	})); err != nil {
		return ptyhost.HelloPayload{}, fmt.Errorf("claudeagent: write HELLO: %w", err)
	}
	t, payload, err := ptyhost.ReadFrame(c.conn)
	if err != nil {
		return ptyhost.HelloPayload{}, fmt.Errorf("claudeagent: read HELLO reply: %w", err)
	}
	if t != ptyhost.MsgHello {
		return ptyhost.HelloPayload{}, fmt.Errorf("claudeagent: expected HELLO reply, got %v", t)
	}
	hello, err := ptyhost.DecodeHello(payload)
	if err != nil {
		return ptyhost.HelloPayload{}, fmt.Errorf("claudeagent: decode HELLO reply: %w", err)
	}
	return hello, nil
}

// Run writes ATTACH{offset} and then blocks, reassembling stdout NDJSON
// lines and dispatching them (+ any stderr chunks) to the configured
// handlers, until the connection errors/closes or ctx is done. onAttach (if
// non-nil) is invoked exactly once, synchronously from within this loop, as
// soon as the overflow verdict is known (the first MsgData frame) — BEFORE
// any line from that frame is dispatched to onLine, so a caller that wants
// to bail out on overflow can do so via onAttach without processing a
// partial/gapped transcript.
//
// offset semantics match [ptyhost.EncodeAttach]: -1 means "from the oldest
// byte still retained"; a value >= 0 requests resuming exactly there and
// enables overflow detection.
func (c *PipeClient) Run(ctx context.Context, offset int64, onAttach func(AttachResult)) error {
	if err := ptyhost.WriteFrame(c.conn, ptyhost.MsgAttach, ptyhost.EncodeAttach(offset)); err != nil {
		return fmt.Errorf("claudeagent: write ATTACH: %w", err)
	}

	// ctx cancellation closes the connection to unblock the ReadFrame loop
	// below (net.Conn has no ctx-aware Read).
	if ctx != nil {
		stop := context.AfterFunc(ctx, func() { _ = c.conn.Close() })
		defer stop()
	}

	var lineBuf bytes.Buffer
	lineBufStart := int64(-1) // absolute offset of lineBuf's first byte; -1 = empty/unset
	attached := false

	for {
		t, payload, err := ptyhost.ReadFrame(c.conn)
		if err != nil {
			if ctx != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("claudeagent: read frame: %w", err)
		}
		switch t {
		case ptyhost.MsgData:
			frameOffset, data, derr := ptyhost.DecodeData(payload)
			if derr != nil {
				return fmt.Errorf("claudeagent: decode DATA: %w", derr)
			}
			if !attached {
				attached = true
				result := AttachResult{
					Requested:   offset,
					StartOffset: frameOffset,
					Overflowed:  offset >= 0 && frameOffset > offset,
				}
				if onAttach != nil {
					onAttach(result)
				}
				lineBufStart = frameOffset
			}
			if lineBufStart < 0 {
				lineBufStart = frameOffset
			}
			lineBuf.Write(data)
			if err := c.drainLines(&lineBuf, &lineBufStart); err != nil {
				return err
			}

		case ptyhost.MsgStderrData:
			chunkOffset, data, derr := ptyhost.DecodeData(payload)
			if derr != nil {
				return fmt.Errorf("claudeagent: decode STDERR_DATA: %w", derr)
			}
			if c.onStderr != nil {
				// DecodeData aliases payload; copy since the handler may
				// retain it beyond this frame's lifetime.
				out := make([]byte, len(data))
				copy(out, data)
				c.onStderr(chunkOffset, out)
			}

		default:
			// Unknown/irrelevant frame type for this client's purposes
			// (e.g. a STATUS response to a request this client never made)
			// — ignore, forward-compat.
		}
	}
}

// drainLines extracts every complete '\n'-terminated line currently in buf,
// invoking c.onLine for each with its end offset, and leaves any trailing
// partial line in buf (with *bufStart updated to that partial line's
// starting offset) for the next call to complete.
func (c *PipeClient) drainLines(buf *bytes.Buffer, bufStart *int64) error {
	for {
		b := buf.Bytes()
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return nil
		}
		line := make([]byte, i)
		copy(line, b[:i])
		endOffset := *bufStart + int64(i) + 1
		buf.Next(i + 1)
		*bufStart = endOffset
		if err := c.onLine(line, endOffset); err != nil {
			return fmt.Errorf("claudeagent: line handler: %w", err)
		}
		// Informational-only per ADR-0002/PD-4 — ptyhost does not persist
		// this; failures are deliberately not fatal to Run().
		_ = c.Ack(endOffset)
	}
}

// Ack sends an informational ACK{offset} to ptyhost. Per ADR-0004/PD-4 this
// is a courtesy only — ptyhost treats it as a no-op and does not persist it.
// The actual source of truth for "where to resume" is the caller's
// [OffsetStore], updated from inside the [LineHandler] callback BEFORE this
// is called (see drainLines).
func (c *PipeClient) Ack(offset int64) error {
	if err := ptyhost.WriteFrame(c.conn, ptyhost.MsgAck, ptyhost.EncodeAck(offset)); err != nil {
		return fmt.Errorf("claudeagent: write ACK: %w", err)
	}
	return nil
}

// WriteInput sends b to the child's stdin over MsgInput.
func (c *PipeClient) WriteInput(b []byte) error {
	if err := ptyhost.WriteFrame(c.conn, ptyhost.MsgInput, ptyhost.EncodeInput(b)); err != nil {
		return fmt.Errorf("claudeagent: write INPUT: %w", err)
	}
	return nil
}

// Status requests and returns ptyhost's current STATUS.
func (c *PipeClient) Status(timeout time.Duration) (ptyhost.StatusPayload, error) {
	if err := ptyhost.WriteFrame(c.conn, ptyhost.MsgStatus, ptyhost.EncodeStatusRequest()); err != nil {
		return ptyhost.StatusPayload{}, fmt.Errorf("claudeagent: write STATUS request: %w", err)
	}
	if timeout > 0 {
		_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
		defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
	}
	for {
		t, payload, err := ptyhost.ReadFrame(c.conn)
		if err != nil {
			return ptyhost.StatusPayload{}, fmt.Errorf("claudeagent: read STATUS reply: %w", err)
		}
		if t != ptyhost.MsgStatus {
			continue // a live DATA/STDERR_DATA frame arrived first; keep waiting
		}
		st, derr := ptyhost.DecodeStatusResponse(payload)
		if derr != nil {
			return ptyhost.StatusPayload{}, fmt.Errorf("claudeagent: decode STATUS response: %w", derr)
		}
		return st, nil
	}
}

// Close closes the underlying connection WITHOUT sending SHUTDOWN. This is
// exactly what a palmux2 kill -9 (or any ungraceful process death) looks
// like from ptyhost's side: the socket connection simply drops while the
// held child keeps running untouched. Used by tests to simulate that
// scenario, and by production code on a clean palmux2 shutdown that
// intentionally leaves the agent running (the normal no-halt-agent case).
func (c *PipeClient) Close() error {
	return c.conn.Close()
}

// Shutdown sends MsgShutdown (asking ptyhost to terminate the held child)
// then closes the connection. Used when the tab/branch is actually being
// deleted (orphan GC / explicit close), NOT on a routine palmux2 restart.
func (c *PipeClient) Shutdown(sp ptyhost.ShutdownPayload) error {
	err := ptyhost.WriteFrame(c.conn, ptyhost.MsgShutdown, ptyhost.EncodeShutdown(sp))
	_ = c.conn.Close()
	if err != nil {
		return fmt.Errorf("claudeagent: write SHUTDOWN: %w", err)
	}
	return nil
}
