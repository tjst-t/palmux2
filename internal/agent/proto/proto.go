// Package proto defines the JSON-RPC 2.0 protocol types for palmux-agent.
//
// All messages follow the JSON-RPC 2.0 specification (https://www.jsonrpc.org/specification).
// The agent exposes the following methods over a Unix Domain Socket:
//
//   - Echo         — smoke test (round-trip)
//   - ListListeningPorts — list ports from /proc/net/tcp(6) (no lsof/ss dependency)
//   - ReadFile     — read file contents (path-traversal protected)
//   - Stat         — file/dir metadata (path-traversal protected)
//   - Walk         — directory listing (path-traversal protected)
package proto

import "fmt"

// Version is the protocol version string included in all responses.
// It follows semver; the agent rejects requests with a mismatched major version.
const Version = "1.0.0"

// -------- JSON-RPC 2.0 envelope --------

// Request is a JSON-RPC 2.0 request object.
type Request struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
	// ID is omitted for notifications; set to a number or string for calls.
	ID any `json:"id"`
}

// Response is a JSON-RPC 2.0 response object.
type Response struct {
	JSONRPC  string  `json:"jsonrpc"`
	Result   any     `json:"result,omitempty"`
	Error    *RPCErr `json:"error,omitempty"`
	ID       any     `json:"id"`
	// AgentVersion is added by palmux-agent to every response for version negotiation.
	AgentVersion string `json:"agent_version"`
}

// RPCErr is a JSON-RPC 2.0 error object.
// It implements the error interface so handlers can return it directly.
type RPCErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *RPCErr) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// Standard JSON-RPC 2.0 error codes.
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
	// Application-specific range: -32000 to -32099.
	ErrCodeForbidden = -32000
)

// -------- Echo --------

// EchoParams is the params object for the Echo method.
type EchoParams struct {
	Msg string `json:"msg"`
}

// EchoResult is the result object for the Echo method.
type EchoResult struct {
	Msg          string `json:"msg"`
	AgentVersion string `json:"agent_version"`
}

// -------- ListListeningPorts --------

// ListListeningPortsParams is the params object for ListListeningPorts (currently no options).
type ListListeningPortsParams struct {
	// IPv4Only restricts results to IPv4. Default false = include both.
	IPv4Only bool `json:"ipv4_only,omitempty"`
}

// PortEntry represents a single listening port.
type PortEntry struct {
	Port     uint16 `json:"port"`
	Protocol string `json:"protocol"` // "tcp" or "tcp6"
	// LocalAddress is the bound local IP (e.g. "0.0.0.0" or "::").
	LocalAddress string `json:"local_address"`
}

// ListListeningPortsResult is the result for ListListeningPorts.
type ListListeningPortsResult struct {
	Ports []PortEntry `json:"ports"`
}

// -------- ReadFile --------

// ReadFileParams is the params object for ReadFile.
type ReadFileParams struct {
	// Root is the sandbox root — the agent refuses paths that escape it.
	Root string `json:"root"`
	// Path is a relative path inside Root.
	Path string `json:"path"`
}

// ReadFileResult is the result for ReadFile.
type ReadFileResult struct {
	Content  string `json:"content"`   // base64-encoded bytes
	Encoding string `json:"encoding"`  // always "base64"
	Size     int64  `json:"size"`
}

// -------- Stat --------

// StatParams is the params object for Stat.
type StatParams struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

// StatResult is the result for Stat.
type StatResult struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`    // e.g. "-rw-r--r--"
	IsDir   bool   `json:"is_dir"`
	ModTime string `json:"mod_time"` // RFC3339
}

// -------- Walk --------

// WalkParams is the params object for Walk.
type WalkParams struct {
	Root     string `json:"root"`
	Path     string `json:"path"`
	MaxDepth int    `json:"max_depth,omitempty"` // 0 = unlimited
}

// WalkEntry is one entry in a Walk result.
type WalkEntry struct {
	RelPath string `json:"rel_path"`
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
}

// WalkResult is the result for Walk.
type WalkResult struct {
	Entries []WalkEntry `json:"entries"`
}
