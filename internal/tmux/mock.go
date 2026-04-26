package tmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// MockClient is an in-memory Client implementation for tests.
// It is concurrency-safe and tracks every call for assertions.
type MockClient struct {
	mu       sync.Mutex
	sessions map[string]*mockSession
	calls    []string

	// AttachFn lets tests override Attach behaviour. If nil, Attach returns
	// an error.
	AttachFn func(ctx context.Context, session, windowName string, opts AttachOpts) (io.ReadWriteCloser, ResizeFunc, error)
}

type mockSession struct {
	name      string
	createdAt int64
	windows   []Window
	nextIdx   int
}

// NewMockClient returns an empty MockClient.
func NewMockClient() *MockClient {
	return &MockClient{sessions: map[string]*mockSession{}}
}

// Calls returns a copy of recorded call descriptions in order.
func (m *MockClient) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.calls))
	copy(out, m.calls)
	return out
}

func (m *MockClient) record(format string, args ...any) {
	m.calls = append(m.calls, fmt.Sprintf(format, args...))
}

func (m *MockClient) ListSessions(_ context.Context) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("ListSessions")
	out := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, Session{Name: s.name, CreatedAt: s.createdAt})
	}
	return out, nil
}

func (m *MockClient) NewSession(_ context.Context, opts NewSessionOpts) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("NewSession %s window=%s cmd=%q", opts.Name, opts.WindowName, opts.Command)
	if _, ok := m.sessions[opts.Name]; ok {
		return fmt.Errorf("session %s exists", opts.Name)
	}
	s := &mockSession{name: opts.Name, createdAt: time.Now().Unix()}
	if opts.WindowName != "" {
		s.windows = append(s.windows, Window{Index: 0, Name: opts.WindowName})
		s.nextIdx = 1
	}
	m.sessions[opts.Name] = s
	return nil
}

func (m *MockClient) KillSession(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("KillSession %s", name)
	if _, ok := m.sessions[name]; !ok {
		return fmt.Errorf("session %s not found", name)
	}
	delete(m.sessions, name)
	return nil
}

func (m *MockClient) HasSession(_ context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("HasSession %s", name)
	_, ok := m.sessions[name]
	return ok, nil
}

func (m *MockClient) ListWindows(_ context.Context, session string) ([]Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("ListWindows %s", session)
	s, ok := m.sessions[session]
	if !ok {
		return nil, fmt.Errorf("session %s not found", session)
	}
	out := make([]Window, len(s.windows))
	copy(out, s.windows)
	return out, nil
}

func (m *MockClient) NewWindow(_ context.Context, session string, opts NewWindowOpts) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("NewWindow %s window=%s cmd=%q", session, opts.Name, opts.Command)
	s, ok := m.sessions[session]
	if !ok {
		return fmt.Errorf("session %s not found", session)
	}
	for _, w := range s.windows {
		if w.Name == opts.Name {
			return fmt.Errorf("window %s exists in %s", opts.Name, session)
		}
	}
	s.windows = append(s.windows, Window{Index: s.nextIdx, Name: opts.Name})
	s.nextIdx++
	return nil
}

func (m *MockClient) KillWindowByName(_ context.Context, session, windowName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("KillWindowByName %s/%s", session, windowName)
	s, ok := m.sessions[session]
	if !ok {
		return fmt.Errorf("session %s not found", session)
	}
	for i, w := range s.windows {
		if w.Name == windowName {
			s.windows = append(s.windows[:i], s.windows[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("window %s not found in %s", windowName, session)
}

func (m *MockClient) RenameWindow(_ context.Context, session, oldName, newName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("RenameWindow %s/%s->%s", session, oldName, newName)
	s, ok := m.sessions[session]
	if !ok {
		return fmt.Errorf("session %s not found", session)
	}
	for i, w := range s.windows {
		if w.Name == oldName {
			s.windows[i].Name = newName
			return nil
		}
	}
	return fmt.Errorf("window %s not found in %s", oldName, session)
}

func (m *MockClient) WindowIndexByName(_ context.Context, session, windowName string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("WindowIndexByName %s/%s", session, windowName)
	s, ok := m.sessions[session]
	if !ok {
		return 0, fmt.Errorf("session %s not found", session)
	}
	for _, w := range s.windows {
		if w.Name == windowName {
			return w.Index, nil
		}
	}
	return 0, fmt.Errorf("window %s not found in %s", windowName, session)
}

func (m *MockClient) SendKeys(_ context.Context, session, windowName, keys string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("SendKeys %s/%s %q", session, windowName, keys)
	s, ok := m.sessions[session]
	if !ok {
		return fmt.Errorf("session %s not found", session)
	}
	for _, w := range s.windows {
		if w.Name == windowName {
			return nil
		}
	}
	return fmt.Errorf("window %s not found in %s", windowName, session)
}

func (m *MockClient) Attach(ctx context.Context, session, windowName string, opts AttachOpts) (io.ReadWriteCloser, ResizeFunc, error) {
	m.mu.Lock()
	fn := m.AttachFn
	m.calls = append(m.calls, fmt.Sprintf("Attach %s/%s %dx%d", session, windowName, opts.Cols, opts.Rows))
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, session, windowName, opts)
	}
	return nil, nil, errors.New("MockClient.Attach: not configured")
}

func (m *MockClient) AttachByIndex(_ context.Context, session string, idx int, opts AttachOpts) (io.ReadWriteCloser, ResizeFunc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, fmt.Sprintf("AttachByIndex %s:%d %dx%d", session, idx, opts.Cols, opts.Rows))
	return nil, nil, errors.New("MockClient.AttachByIndex: not configured")
}

func (m *MockClient) NewGroupSession(_ context.Context, target, groupName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("NewGroupSession %s -> %s", target, groupName)
	if _, ok := m.sessions[target]; !ok {
		return fmt.Errorf("session %s not found", target)
	}
	if _, ok := m.sessions[groupName]; ok {
		return fmt.Errorf("group session %s exists", groupName)
	}
	// Group sessions share windows with the target — we mirror them here.
	src := m.sessions[target]
	clone := &mockSession{name: groupName, createdAt: time.Now().Unix()}
	clone.windows = append(clone.windows, src.windows...)
	clone.nextIdx = src.nextIdx
	m.sessions[groupName] = clone
	return nil
}

// SeedSession injects a session for tests (e.g. to simulate an already-running
// tmux session before the Store starts).
func (m *MockClient) SeedSession(name string, windows ...Window) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &mockSession{name: name, createdAt: time.Now().Unix(), windows: append([]Window(nil), windows...)}
	if len(windows) > 0 {
		s.nextIdx = windows[len(windows)-1].Index + 1
	}
	m.sessions[name] = s
}

// SessionNames returns the names of all currently-tracked sessions.
func (m *MockClient) SessionNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.sessions))
	for name := range m.sessions {
		out = append(out, name)
	}
	return out
}
