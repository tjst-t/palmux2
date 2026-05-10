// Package agent implements the palmux-agent JSON-RPC 2.0 server over Unix Domain Socket.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"

	"github.com/tjst-t/palmux2/internal/agent/proto"
)

// Handler is the function signature for a JSON-RPC method handler.
// It receives the raw params JSON and returns (result, error).
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Server is a JSON-RPC 2.0 server over Unix Domain Socket.
type Server struct {
	socketPath string
	mu         sync.RWMutex
	methods    map[string]Handler
	logger     *slog.Logger
}

// NewServer creates a new Server listening on the given UDS path.
func NewServer(socketPath string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		socketPath: socketPath,
		methods:    make(map[string]Handler),
		logger:     logger,
	}
	// Register built-in methods.
	s.Register("Echo", s.handleEcho)
	s.Register("ListListeningPorts", s.handleListListeningPorts)
	s.Register("ReadFile", s.handleReadFile)
	s.Register("Stat", s.handleStat)
	s.Register("Walk", s.handleWalk)
	return s
}

// Register adds or replaces a method handler. Thread-safe.
func (s *Server) Register(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.methods[method] = h
}

// Serve starts accepting connections. It blocks until ctx is cancelled.
// It removes the socket file before listening, so callers should clean up
// any leftover socket from a previous run.
func (s *Server) Serve(ctx context.Context) error {
	// Remove stale socket file.
	if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agent: remove stale socket: %w", err)
	}

	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("agent: listen unix %s: %w", s.socketPath, err)
	}
	defer l.Close()

	s.logger.Info("palmux-agent listening", "socket", s.socketPath, "version", proto.Version)

	// Close listener when context is cancelled.
	go func() {
		<-ctx.Done()
		l.Close()
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // graceful shutdown
			}
			return fmt.Errorf("agent: accept: %w", err)
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(bufio.NewReader(conn))
	enc := json.NewEncoder(conn)

	for {
		var req proto.Request
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return
			}
			// Parse error — respond if possible.
			_ = enc.Encode(errResponse(nil, proto.ErrCodeParseError, "parse error: "+err.Error()))
			return
		}

		resp := s.dispatch(ctx, &req)
		if err := enc.Encode(resp); err != nil {
			s.logger.Warn("encode response failed", "err", err)
			return
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req *proto.Request) *proto.Response {
	if req.JSONRPC != "2.0" {
		return errResponse(req.ID, proto.ErrCodeInvalidRequest, "jsonrpc must be \"2.0\"")
	}
	s.mu.RLock()
	h, ok := s.methods[req.Method]
	s.mu.RUnlock()
	if !ok {
		return errResponse(req.ID, proto.ErrCodeMethodNotFound, "method not found: "+req.Method)
	}

	// Marshal params to raw JSON for handler.
	var rawParams json.RawMessage
	if req.Params != nil {
		b, err := json.Marshal(req.Params)
		if err != nil {
			return errResponse(req.ID, proto.ErrCodeInvalidParams, "params marshal: "+err.Error())
		}
		rawParams = b
	}

	result, err := h(ctx, rawParams)
	if err != nil {
		// Check if it's a typed RPC error.
		var rpcErr *proto.RPCErr
		if errors.As(err, &rpcErr) {
			return &proto.Response{
				JSONRPC:      "2.0",
				Error:        rpcErr,
				ID:           req.ID,
				AgentVersion: proto.Version,
			}
		}
		return errResponse(req.ID, proto.ErrCodeInternal, err.Error())
	}
	return &proto.Response{
		JSONRPC:      "2.0",
		Result:       result,
		ID:           req.ID,
		AgentVersion: proto.Version,
	}
}

func errResponse(id any, code int, msg string) *proto.Response {
	return &proto.Response{
		JSONRPC:      "2.0",
		Error:        &proto.RPCErr{Code: code, Message: msg},
		ID:           id,
		AgentVersion: proto.Version,
	}
}
