package ptyhost

import (
	"bytes"
	"testing"
)

// TestFrame_RoundTrip_AllMessageTypes round-trips every message type through
// WriteFrame/ReadFrame and asserts byte-exact framing (AC-S3f2658-1-1).
func TestFrame_RoundTrip_AllMessageTypes(t *testing.T) {
	cases := []struct {
		name    string
		msgType MsgType
		payload []byte
	}{
		{"HELLO", MsgHello, EncodeHello(HelloPayload{ProtocolVersion: ProtocolVersion, Mode: "pty", Pid: 4242, ArgvHash: "deadbeef"})},
		{"ATTACH", MsgAttach, EncodeAttach(-1)},
		{"ATTACH-positive", MsgAttach, EncodeAttach(123456789)},
		{"DATA", MsgData, EncodeData(42, []byte("some pty output\x00with a nul\x1b[31m"))},
		{"DATA-empty", MsgData, EncodeData(0, nil)},
		{"INPUT", MsgInput, EncodeInput([]byte("ls -la\n"))},
		{"RESIZE", MsgResize, EncodeResize(120, 40)},
		{"ACK", MsgAck, EncodeAck(999)},
		{"STATUS-request", MsgStatus, EncodeStatusRequest()},
		{"STATUS-response", MsgStatus, EncodeStatusResponse(StatusPayload{
			Pid: 1, Alive: true, ExitCode: 0, ExitCodeValid: false,
			RingBytes: 100, RingHeadOffset: 0, RingTotalOffset: 100,
		})},
		{"SHUTDOWN", MsgShutdown, EncodeShutdown(ShutdownPayload{Signal: "TERM", GraceMillis: 3000})},
		{"SHUTDOWN-empty", MsgShutdown, EncodeShutdown(ShutdownPayload{})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteFrame(&buf, tc.msgType, tc.payload); err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}

			// Byte-exact framing: 4-byte BE length (payload+1) + type byte + payload.
			wire := buf.Bytes()
			if len(wire) != 4+1+len(tc.payload) {
				t.Fatalf("frame length = %d, want %d", len(wire), 4+1+len(tc.payload))
			}
			gotLen := uint32(wire[0])<<24 | uint32(wire[1])<<16 | uint32(wire[2])<<8 | uint32(wire[3])
			if gotLen != uint32(len(tc.payload)+1) {
				t.Fatalf("length header = %d, want %d", gotLen, len(tc.payload)+1)
			}
			if MsgType(wire[4]) != tc.msgType {
				t.Fatalf("type byte = %d, want %d", wire[4], tc.msgType)
			}
			if !bytes.Equal(wire[5:], tc.payload) {
				t.Fatalf("payload = %q, want %q", wire[5:], tc.payload)
			}

			gotType, gotPayload, err := ReadFrame(bytes.NewReader(wire))
			if err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			if gotType != tc.msgType {
				t.Fatalf("ReadFrame type = %v, want %v", gotType, tc.msgType)
			}
			if !bytes.Equal(gotPayload, tc.payload) {
				t.Fatalf("ReadFrame payload = %q, want %q", gotPayload, tc.payload)
			}
		})
	}
}

// TestFrame_MultipleFramesOnOneStream asserts frames read back in order from
// a stream carrying several concatenated frames (the real socket usage
// pattern).
func TestFrame_MultipleFramesOnOneStream(t *testing.T) {
	var buf bytes.Buffer
	want := []struct {
		t MsgType
		p []byte
	}{
		{MsgHello, EncodeHello(HelloPayload{ProtocolVersion: 1, Mode: "pty", Pid: 1})},
		{MsgData, EncodeData(0, []byte("chunk1"))},
		{MsgData, EncodeData(6, []byte("chunk2"))},
		{MsgStatus, EncodeStatusRequest()},
	}
	for _, w := range want {
		if err := WriteFrame(&buf, w.t, w.p); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}

	r := bytes.NewReader(buf.Bytes())
	for i, w := range want {
		gotType, gotPayload, err := ReadFrame(r)
		if err != nil {
			t.Fatalf("frame %d: ReadFrame: %v", i, err)
		}
		if gotType != w.t {
			t.Fatalf("frame %d: type = %v, want %v", i, gotType, w.t)
		}
		if !bytes.Equal(gotPayload, w.p) {
			t.Fatalf("frame %d: payload = %q, want %q", i, gotPayload, w.p)
		}
	}
}

// TestHello_VersionField asserts the ProtocolVersion is carried through
// encode/decode intact (used for the version-mismatch UI-degrade policy).
func TestHello_VersionField(t *testing.T) {
	p := HelloPayload{ProtocolVersion: ProtocolVersion, Mode: "pty", Pid: 777, ArgvHash: ArgvHash([]string{"claude", "--foo"})}
	encoded := EncodeHello(p)
	decoded, err := DecodeHello(encoded)
	if err != nil {
		t.Fatalf("DecodeHello: %v", err)
	}
	if decoded != p {
		t.Fatalf("decoded HELLO = %+v, want %+v", decoded, p)
	}
	if decoded.ProtocolVersion != ProtocolVersion {
		t.Fatalf("ProtocolVersion = %d, want %d", decoded.ProtocolVersion, ProtocolVersion)
	}
}

// TestStatus_RequestVsResponse asserts IsStatusRequest correctly
// distinguishes an empty request payload from a populated response payload.
func TestStatus_RequestVsResponse(t *testing.T) {
	if !IsStatusRequest(EncodeStatusRequest()) {
		t.Fatal("EncodeStatusRequest() should be a request")
	}
	resp := EncodeStatusResponse(StatusPayload{Pid: 1, Alive: true})
	if IsStatusRequest(resp) {
		t.Fatal("a populated STATUS response should not be classified as a request")
	}
}

// TestReadFrame_RejectsOversizedLength guards against unbounded allocation
// from a corrupt/malicious length header.
func TestReadFrame_RejectsOversizedLength(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0x7f, 0xff, 0xff, 0xff}) // huge bogus length
	_, _, err := ReadFrame(&buf)
	if err == nil {
		t.Fatal("expected an error for an oversized frame length")
	}
}

// TestArgvHash_StableAndDistinguishing asserts ArgvHash is deterministic for
// the same argv and differs for different argv.
func TestArgvHash_StableAndDistinguishing(t *testing.T) {
	a := ArgvHash([]string{"claude", "--foo", "bar"})
	b := ArgvHash([]string{"claude", "--foo", "bar"})
	c := ArgvHash([]string{"claude", "--foo", "baz"})
	if a != b {
		t.Fatalf("ArgvHash not stable: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("ArgvHash did not distinguish different argv: %q", a)
	}
}
