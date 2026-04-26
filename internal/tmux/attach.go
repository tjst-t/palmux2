package tmux

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// Attach starts `tmux attach-session -t {session}:{windowIdx} -d` under a
// pty and returns an io.ReadWriteCloser plus a resize callback. Closing the
// returned conn kills the underlying tmux client process.
//
// `-d` (detach others) is intentionally omitted here; multi-client viewing
// is handled at a higher level via session groups.
//
// When opts.Cols/Rows are non-zero, the pty is created at that size before
// tmux starts. Without this, pty.Start uses the platform default (typically
// 80x24) and tmux shrinks the session to match — which is what causes the
// "small screen first, then resize" flash on attach.
func (c *execClient) Attach(ctx context.Context, session, windowName string, opts AttachOpts) (io.ReadWriteCloser, ResizeFunc, error) {
	idx, err := c.WindowIndexByName(ctx, session, windowName)
	if err != nil {
		return nil, nil, err
	}
	return c.AttachByIndex(ctx, session, idx, opts)
}

// AttachByIndex is the same as Attach but takes a pre-resolved window index.
// Used by the orphan-session WS endpoint, whose windows aren't Palmux-named.
func (c *execClient) AttachByIndex(ctx context.Context, session string, idx int, opts AttachOpts) (io.ReadWriteCloser, ResizeFunc, error) {
	target := fmt.Sprintf("%s:%d", session, idx)

	cmd := exec.CommandContext(ctx, c.bin, "attach-session", "-t", target)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	var (
		f   *os.File
		err error
	)
	if opts.Cols > 0 && opts.Rows > 0 {
		f, err = pty.StartWithSize(cmd, &pty.Winsize{
			Cols: uint16(opts.Cols),
			Rows: uint16(opts.Rows),
		})
	} else {
		f, err = pty.Start(cmd)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("pty start: %w", err)
	}

	conn := &ptyConn{cmd: cmd, f: f}
	resize := func(cols, rows int) error {
		return pty.Setsize(f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	}
	return conn, resize, nil
}

// NewGroupSession creates a new session that belongs to the same tmux
// session-group as `target`. Group sessions share the underlying windows
// but allow independent attach lifecycles, which is how Palmux supports
// multiple browser clients viewing the same branch terminal.
func (c *execClient) NewGroupSession(ctx context.Context, target, groupName string) error {
	if target == "" || groupName == "" {
		return fmt.Errorf("NewGroupSession: empty argument")
	}
	_, err := c.run(ctx, "new-session", "-d", "-t", target, "-s", groupName)
	return err
}

// ptyConn wraps the pty file + tmux client process so closing the conn also
// terminates the client.
type ptyConn struct {
	cmd *exec.Cmd
	f   *os.File
}

func (p *ptyConn) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *ptyConn) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *ptyConn) Close() error {
	_ = p.f.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_, _ = p.cmd.Process.Wait()
	return nil
}
