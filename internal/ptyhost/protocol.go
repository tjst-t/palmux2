package ptyhost

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ProtocolVersion is the current wire protocol version, carried in HELLO. A
// version mismatch is NOT treated as fatal by design (see
// docs/no-halt-agent-design.md §2) — palmux2 degrades the UI rather than
// killing an old ptyhost.
const ProtocolVersion = 1

// MsgType identifies a frame's payload shape. This set is intentionally
// frozen-minimal (ADR-0002): adding a message type here should be treated as
// a signal to reconsider whether the behavior belongs in palmux2 instead.
type MsgType uint8

const (
	// MsgHello is sent by the client to request the server's identity, and by
	// the server in reply. Payload: JSON-encoded [HelloPayload].
	MsgHello MsgType = iota + 1
	// MsgAttach requests replay from an absolute offset plus a live
	// subscription. Payload: 8-byte big-endian offset (see [EncodeAttach]).
	MsgAttach
	// MsgData carries ring bytes (replay or live) with their absolute
	// starting offset. Payload: 8-byte big-endian offset + raw bytes (see
	// [EncodeData]).
	MsgData
	// MsgInput carries raw bytes to write to the child's PTY/stdin. Payload:
	// raw bytes, no framing beyond the outer frame.
	MsgInput
	// MsgResize carries a PTY winsize change. Payload: 2-byte cols + 2-byte
	// rows, both big-endian (see [EncodeResize]).
	MsgResize
	// MsgAck acknowledges a processed offset (used by the lossless agent-pipe
	// replay design in Sprint 2; accepted as a no-op in pty mode). Payload:
	// 8-byte big-endian offset.
	MsgAck
	// MsgStatus is a status request (empty payload) from the client, or a
	// status response (JSON-encoded [StatusPayload]) from the server.
	MsgStatus
	// MsgShutdown asks the server to terminate the child (SIGTERM, then
	// SIGKILL after a grace period) and exit. Payload: JSON-encoded
	// [ShutdownPayload] (may be empty for defaults).
	MsgShutdown
)

// String implements fmt.Stringer for readable logs/test failures.
func (t MsgType) String() string {
	switch t {
	case MsgHello:
		return "HELLO"
	case MsgAttach:
		return "ATTACH"
	case MsgData:
		return "DATA"
	case MsgInput:
		return "INPUT"
	case MsgResize:
		return "RESIZE"
	case MsgAck:
		return "ACK"
	case MsgStatus:
		return "STATUS"
	case MsgShutdown:
		return "SHUTDOWN"
	default:
		return fmt.Sprintf("MsgType(%d)", uint8(t))
	}
}

// maxFrameSize bounds the length header to guard against unbounded
// allocation from a corrupt or malicious peer.
const maxFrameSize = 16 << 20 // 16 MiB

// frameHeaderLen is the encoded size of the length-prefix field.
const frameHeaderLen = 4

// WriteFrame writes one frame to w: a 4-byte big-endian length (covering the
// type byte + payload), the 1-byte type, then the payload.
func WriteFrame(w io.Writer, t MsgType, payload []byte) error {
	if len(payload) > maxFrameSize-1 {
		return fmt.Errorf("ptyhost: frame payload too large: %d bytes", len(payload))
	}
	hdr := make([]byte, frameHeaderLen+1)
	binary.BigEndian.PutUint32(hdr[:frameHeaderLen], uint32(len(payload)+1))
	hdr[frameHeaderLen] = byte(t)
	if _, err := w.Write(hdr); err != nil {
		return fmt.Errorf("ptyhost: write frame header: %w", err)
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return fmt.Errorf("ptyhost: write frame payload: %w", err)
		}
	}
	return nil
}

// ReadFrame reads one frame from r. A clean peer close surfaces as io.EOF
// (checkable with errors.Is) so callers can distinguish "connection closed"
// from a genuine protocol error.
func ReadFrame(r io.Reader) (MsgType, []byte, error) {
	var hdr [frameHeaderLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(hdr[:])
	if length == 0 || length > maxFrameSize {
		return 0, nil, fmt.Errorf("ptyhost: invalid frame length %d", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, fmt.Errorf("ptyhost: read frame body: %w", err)
	}
	return MsgType(body[0]), body[1:], nil
}

// HelloPayload is the JSON body of a HELLO frame.
type HelloPayload struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Mode            string `json:"mode"` // "pty" (pipe mode is Sprint 2 / ADR-0004)
	Pid             int    `json:"pid"`
	ArgvHash        string `json:"argvHash"`
}

// EncodeHello marshals a HelloPayload to its frame payload bytes.
func EncodeHello(p HelloPayload) []byte {
	b, _ := json.Marshal(p) // HelloPayload has no unmarshalable fields
	return b
}

// DecodeHello unmarshals a HELLO frame payload.
func DecodeHello(payload []byte) (HelloPayload, error) {
	var p HelloPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return HelloPayload{}, fmt.Errorf("ptyhost: decode HELLO: %w", err)
	}
	return p, nil
}

// EncodeAttach encodes an ATTACH request for the given absolute offset.
// offset == -1 means "replay from the oldest byte still retained".
func EncodeAttach(offset int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(offset))
	return b
}

// DecodeAttach decodes an ATTACH frame payload.
func DecodeAttach(payload []byte) (int64, error) {
	if len(payload) != 8 {
		return 0, fmt.Errorf("ptyhost: invalid ATTACH payload length %d", len(payload))
	}
	return int64(binary.BigEndian.Uint64(payload)), nil
}

// EncodeData encodes a DATA frame payload: absolute offset + raw bytes.
func EncodeData(offset int64, data []byte) []byte {
	b := make([]byte, 8+len(data))
	binary.BigEndian.PutUint64(b[:8], uint64(offset))
	copy(b[8:], data)
	return b
}

// DecodeData decodes a DATA frame payload into its absolute offset and raw
// bytes. The returned slice aliases payload; copy it if retained beyond the
// caller's read buffer's lifetime.
func DecodeData(payload []byte) (int64, []byte, error) {
	if len(payload) < 8 {
		return 0, nil, fmt.Errorf("ptyhost: invalid DATA payload length %d", len(payload))
	}
	offset := int64(binary.BigEndian.Uint64(payload[:8]))
	return offset, payload[8:], nil
}

// EncodeInput encodes an INPUT frame payload (raw passthrough — kept as a
// named function for symmetry/readability at call sites).
func EncodeInput(data []byte) []byte {
	return data
}

// DecodeInput decodes an INPUT frame payload (raw passthrough).
func DecodeInput(payload []byte) []byte {
	return payload
}

// EncodeResize encodes a RESIZE frame payload.
func EncodeResize(cols, rows uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:2], cols)
	binary.BigEndian.PutUint16(b[2:4], rows)
	return b
}

// DecodeResize decodes a RESIZE frame payload.
func DecodeResize(payload []byte) (cols, rows uint16, err error) {
	if len(payload) != 4 {
		return 0, 0, fmt.Errorf("ptyhost: invalid RESIZE payload length %d", len(payload))
	}
	return binary.BigEndian.Uint16(payload[0:2]), binary.BigEndian.Uint16(payload[2:4]), nil
}

// EncodeAck encodes an ACK frame payload for the given absolute offset.
func EncodeAck(offset int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(offset))
	return b
}

// DecodeAck decodes an ACK frame payload.
func DecodeAck(payload []byte) (int64, error) {
	if len(payload) != 8 {
		return 0, fmt.Errorf("ptyhost: invalid ACK payload length %d", len(payload))
	}
	return int64(binary.BigEndian.Uint64(payload)), nil
}

// StatusPayload is the JSON body of a STATUS response frame. A STATUS
// request frame carries an empty payload (see [IsStatusRequest]).
type StatusPayload struct {
	Pid             int   `json:"pid"`
	Alive           bool  `json:"alive"`
	ExitCode        int   `json:"exitCode"`
	ExitCodeValid   bool  `json:"exitCodeValid"`
	RingBytes       int   `json:"ringBytes"`
	RingHeadOffset  int64 `json:"ringHeadOffset"`
	RingTotalOffset int64 `json:"ringTotalOffset"`
}

// EncodeStatusRequest returns the (empty) payload for a STATUS request.
func EncodeStatusRequest() []byte {
	return nil
}

// IsStatusRequest reports whether a received STATUS frame payload is a
// request (empty) as opposed to a response (non-empty JSON body).
func IsStatusRequest(payload []byte) bool {
	return len(payload) == 0
}

// EncodeStatusResponse marshals a StatusPayload to its frame payload bytes.
func EncodeStatusResponse(p StatusPayload) []byte {
	b, _ := json.Marshal(p)
	return b
}

// DecodeStatusResponse unmarshals a STATUS response frame payload.
func DecodeStatusResponse(payload []byte) (StatusPayload, error) {
	var p StatusPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return StatusPayload{}, fmt.Errorf("ptyhost: decode STATUS response: %w", err)
	}
	return p, nil
}

// ShutdownPayload is the JSON body of a SHUTDOWN frame.
type ShutdownPayload struct {
	// Signal names the initial signal to send (informational; the server
	// always escalates to SIGKILL after GraceMillis regardless). Empty means
	// the server's default (SIGTERM).
	Signal string `json:"signal,omitempty"`
	// GraceMillis is how long to wait after the initial signal before
	// SIGKILL. 0 means the server's default.
	GraceMillis int `json:"graceMillis,omitempty"`
}

// EncodeShutdown marshals a ShutdownPayload to its frame payload bytes.
func EncodeShutdown(p ShutdownPayload) []byte {
	b, _ := json.Marshal(p)
	return b
}

// DecodeShutdown unmarshals a SHUTDOWN frame payload. An empty payload
// decodes to the zero value (server defaults apply).
func DecodeShutdown(payload []byte) (ShutdownPayload, error) {
	var p ShutdownPayload
	if len(payload) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return ShutdownPayload{}, fmt.Errorf("ptyhost: decode SHUTDOWN: %w", err)
	}
	return p, nil
}

// ArgvHash returns a short, stable hash of argv, used by HELLO/STATUS so a
// reconnecting client can sanity-check it is talking to the ptyhost it
// expects (a full argv echo would leak secrets that may be embedded in
// arguments, e.g. settings JSON).
func ArgvHash(argv []string) string {
	h := sha256.Sum256([]byte(strings.Join(argv, "\x00")))
	return hex.EncodeToString(h[:])[:16]
}
