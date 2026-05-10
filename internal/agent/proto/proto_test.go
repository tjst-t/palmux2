package proto_test

import (
	"encoding/json"
	"testing"

	"github.com/tjst-t/palmux2/internal/agent/proto"
)

// [AC-S98156b-1-2] JSON-RPC 2.0 over UDS の最小プロトコル定義の検証
// - 5 method の struct が定義されている
// - JSON marshal/unmarshal が往復できる
// - version negotiation field (AgentVersion) が含まれる

func TestRequestMarshal(t *testing.T) {
	req := proto.Request{
		JSONRPC: "2.0",
		Method:  "Echo",
		Params:  proto.EchoParams{Msg: "hello"},
		ID:      1,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal Request: %v", err)
	}
	var req2 proto.Request
	if err := json.Unmarshal(b, &req2); err != nil {
		t.Fatalf("unmarshal Request: %v", err)
	}
	if req2.Method != "Echo" {
		t.Errorf("expected method Echo, got %q", req2.Method)
	}
}

func TestResponseMarshal(t *testing.T) {
	resp := proto.Response{
		JSONRPC:      "2.0",
		Result:       proto.EchoResult{Msg: "hello", AgentVersion: proto.Version},
		ID:           1,
		AgentVersion: proto.Version,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal Response: %v", err)
	}
	var resp2 proto.Response
	if err := json.Unmarshal(b, &resp2); err != nil {
		t.Fatalf("unmarshal Response: %v", err)
	}
	if resp2.AgentVersion != proto.Version {
		t.Errorf("expected AgentVersion %q, got %q", proto.Version, resp2.AgentVersion)
	}
}

func TestErrorCodes(t *testing.T) {
	// Verify standard error codes are present (JSON-RPC 2.0 spec compliance).
	codes := map[string]int{
		"ParseError":     proto.ErrCodeParseError,
		"InvalidRequest": proto.ErrCodeInvalidRequest,
		"MethodNotFound": proto.ErrCodeMethodNotFound,
		"InvalidParams":  proto.ErrCodeInvalidParams,
		"Internal":       proto.ErrCodeInternal,
		"Forbidden":      proto.ErrCodeForbidden,
	}
	for name, code := range codes {
		if code >= 0 {
			t.Errorf("error code %s must be negative, got %d", name, code)
		}
	}
}

func TestListListeningPortsResult(t *testing.T) {
	result := proto.ListListeningPortsResult{
		Ports: []proto.PortEntry{
			{Port: 8080, Protocol: "tcp", LocalAddress: "0.0.0.0"},
			{Port: 8443, Protocol: "tcp6", LocalAddress: "::"},
		},
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal ListListeningPortsResult: %v", err)
	}
	var result2 proto.ListListeningPortsResult
	if err := json.Unmarshal(b, &result2); err != nil {
		t.Fatalf("unmarshal ListListeningPortsResult: %v", err)
	}
	if len(result2.Ports) != 2 {
		t.Errorf("expected 2 ports, got %d", len(result2.Ports))
	}
}

func TestReadFileResult(t *testing.T) {
	result := proto.ReadFileResult{
		Content:  "aGVsbG8=", // base64("hello")
		Encoding: "base64",
		Size:     5,
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal ReadFileResult: %v", err)
	}
	var result2 proto.ReadFileResult
	if err := json.Unmarshal(b, &result2); err != nil {
		t.Fatalf("unmarshal ReadFileResult: %v", err)
	}
	if result2.Encoding != "base64" {
		t.Errorf("expected encoding base64, got %q", result2.Encoding)
	}
}

func TestStatResult(t *testing.T) {
	result := proto.StatResult{
		Name:    "test.go",
		Size:    1024,
		Mode:    "-rw-r--r--",
		IsDir:   false,
		ModTime: "2026-05-09T00:00:00Z",
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal StatResult: %v", err)
	}
	var result2 proto.StatResult
	if err := json.Unmarshal(b, &result2); err != nil {
		t.Fatalf("unmarshal StatResult: %v", err)
	}
	if result2.Name != "test.go" {
		t.Errorf("expected Name test.go, got %q", result2.Name)
	}
}

func TestWalkResult(t *testing.T) {
	result := proto.WalkResult{
		Entries: []proto.WalkEntry{
			{RelPath: ".", Name: "root", IsDir: true},
			{RelPath: "sub/file.go", Name: "file.go", IsDir: false, Size: 512, Mode: "-rw-r--r--"},
		},
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal WalkResult: %v", err)
	}
	var result2 proto.WalkResult
	if err := json.Unmarshal(b, &result2); err != nil {
		t.Fatalf("unmarshal WalkResult: %v", err)
	}
	if len(result2.Entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(result2.Entries))
	}
}

func TestVersion(t *testing.T) {
	if proto.Version == "" {
		t.Error("Version must not be empty")
	}
}
