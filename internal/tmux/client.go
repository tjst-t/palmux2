// Package tmux defines an abstraction over the tmux command-line tool. All
// tmux interactions in Palmux must go through Client — never call
// `exec.Command("tmux", ...)` directly. This makes the rest of the codebase
// trivially mockable in tests.
package tmux

import (
	"context"
	"io"
)

// Session is a Palmux-visible tmux session.
type Session struct {
	Name      string
	CreatedAt int64 // unix seconds
	Attached  bool  // any client currently attached
}

// Window is a tmux window inside a session. Index is the tmux assigned
// numeric index; Name is what Palmux uses to look windows up.
type Window struct {
	Index int
	Name  string
}

// NewSessionOpts describes a session to create. Command may be empty for a
// plain shell.
type NewSessionOpts struct {
	Name        string // session name (must include the _palmux_ prefix for managed sessions)
	WindowName  string // initial window name
	Cwd         string // working directory
	Command     string // optional command to run in the initial window
	Env         []string
	WindowWidth int // optional initial pty size; 0 = tmux default
	WindowHeight int
}

// NewWindowOpts describes a window to create.
type NewWindowOpts struct {
	Name    string
	Cwd     string
	Command string // optional
	Env     []string
}

// ResizeFunc resizes the pty associated with an Attach. cols/rows are character
// dimensions.
type ResizeFunc func(cols, rows int) error

// Client is the abstraction. All methods take a Context for cancellation.
type Client interface {
	// Sessions
	ListSessions(ctx context.Context) ([]Session, error)
	NewSession(ctx context.Context, opts NewSessionOpts) error
	KillSession(ctx context.Context, name string) error
	HasSession(ctx context.Context, name string) (bool, error)

	// Windows
	ListWindows(ctx context.Context, session string) ([]Window, error)
	NewWindow(ctx context.Context, session string, opts NewWindowOpts) error
	KillWindowByName(ctx context.Context, session, windowName string) error
	RenameWindow(ctx context.Context, session, oldName, newName string) error
	WindowIndexByName(ctx context.Context, session, windowName string) (int, error)
	SendKeys(ctx context.Context, session, windowName, keys string) error

	// Attach (terminal pty I/O)
	Attach(ctx context.Context, session, windowName string) (io.ReadWriteCloser, ResizeFunc, error)
	NewGroupSession(ctx context.Context, target, groupName string) error
}
