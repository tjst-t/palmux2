package claudeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// controlMux multiplexes control_request / control_response pairs between
// Palmux and the CLI. Each outgoing request gets a fresh request_id; the
// matching response is delivered to the goroutine waiting on that channel.
//
// CLI-initiated control_requests (canUseTool) are NOT handled here — those
// flow through a separate handler registered on the client.
type controlMux struct {
	mu      sync.Mutex
	pending map[string]chan json.RawMessage
	nextID  atomic.Uint64
	closed  bool
}

func newControlMux() *controlMux {
	return &controlMux{pending: make(map[string]chan json.RawMessage)}
}

// allocate registers a response channel and returns the new request_id and
// the channel the caller should select on.
func (m *controlMux) allocate() (string, chan json.RawMessage) {
	id := fmt.Sprintf("palmux_%d", m.nextID.Add(1))
	ch := make(chan json.RawMessage, 1)
	m.mu.Lock()
	if !m.closed {
		m.pending[id] = ch
	} else {
		close(ch)
	}
	m.mu.Unlock()
	return id, ch
}

// resolve delivers the response body to the goroutine waiting on requestID.
// Unknown IDs are silently dropped (the CLI may emit responses we no longer
// care about, e.g. after we cancelled the wait).
func (m *controlMux) resolve(requestID string, response json.RawMessage) {
	m.mu.Lock()
	ch, ok := m.pending[requestID]
	if ok {
		delete(m.pending, requestID)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- response:
	default:
	}
}

// resolveResponse routes a parsed control_response inner object to the
// matching pending caller. `success` delivers the response payload; `error`
// closes the channel without delivery so controlCall returns the spec'd
// errControlClosed (the actual error string is logged).
func (m *controlMux) resolveResponse(inner controlResponseInner) {
	m.mu.Lock()
	ch, ok := m.pending[inner.RequestID]
	if ok {
		delete(m.pending, inner.RequestID)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	if inner.Subtype == "error" {
		// Surface the error string via a sentinel marshal so the caller
		// (controlCall) can detect it. We reuse the channel: send a payload
		// that's clearly identifiable — namely a JSON object with `__error`.
		body, _ := json.Marshal(map[string]string{"__error": inner.Error})
		select {
		case ch <- body:
		default:
		}
		return
	}
	select {
	case ch <- inner.Response:
	default:
	}
}

// abandon removes a pending entry without delivering — used when the caller
// cancels its context.
func (m *controlMux) abandon(requestID string) {
	m.mu.Lock()
	delete(m.pending, requestID)
	m.mu.Unlock()
}

// closeAll wakes every pending caller with an empty payload so they can exit.
// Called when the client shuts down.
func (m *controlMux) closeAll() {
	m.mu.Lock()
	m.closed = true
	pending := m.pending
	m.pending = map[string]chan json.RawMessage{}
	m.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
}

// errControlTimeout / errControlCancelled / errControlClosed are returned by
// (*Client).controlCall on the corresponding shutdown / timeout paths.
var (
	errControlTimeout   = errors.New("claudeagent: control request timed out")
	errControlCancelled = errors.New("claudeagent: control request cancelled")
	errControlClosed    = errors.New("claudeagent: control channel closed")
)

// controlCall is the high-level "send-and-await" used by Client. It runs on
// the caller's goroutine and times out per controlRequestTimeout.
func (c *Client) controlCall(ctx context.Context, requestBody any) (json.RawMessage, error) {
	id, ch := c.mux.allocate()
	body, err := json.Marshal(requestBody)
	if err != nil {
		c.mux.abandon(id)
		return nil, err
	}
	frame, err := json.Marshal(streamMsg{
		Type:      "control_request",
		RequestID: id,
		Request:   body,
	})
	if err != nil {
		c.mux.abandon(id)
		return nil, err
	}
	if err := c.writeLine(frame); err != nil {
		c.mux.abandon(id)
		return nil, err
	}
	timer := time.NewTimer(controlRequestTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		c.mux.abandon(id)
		return nil, errControlCancelled
	case <-timer.C:
		c.mux.abandon(id)
		return nil, errControlTimeout
	case resp, ok := <-ch:
		if !ok {
			return nil, errControlClosed
		}
		// Did resolveResponse hand us back an error stub?
		var probe struct {
			Err string `json:"__error"`
		}
		if json.Unmarshal(resp, &probe) == nil && probe.Err != "" {
			return nil, fmt.Errorf("claudeagent: %s", probe.Err)
		}
		return resp, nil
	}
}

const controlRequestTimeout = 60 * time.Second
