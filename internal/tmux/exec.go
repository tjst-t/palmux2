package tmux

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// NewExecClient returns a Client backed by the system `tmux` binary on PATH.
func NewExecClient() Client {
	return &execClient{bin: "tmux"}
}

type execClient struct {
	bin string
}

// run executes a tmux subcommand and returns combined stdout/stderr on error.
func (c *execClient) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("tmux %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

func (c *execClient) ListSessions(ctx context.Context) ([]Session, error) {
	out, err := c.run(ctx, "list-sessions", "-F", "#{session_name}\t#{session_created}\t#{session_attached}")
	if err != nil {
		// `no server running` is not an error — it just means zero sessions.
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return nil, nil
		}
		return nil, err
	}
	var sessions []Session
	for line := range linesOf(out) {
		fields := strings.Split(line, "\t")
		if len(fields) < 1 || fields[0] == "" {
			continue
		}
		s := Session{Name: fields[0]}
		if len(fields) > 1 {
			s.CreatedAt, _ = strconv.ParseInt(fields[1], 10, 64)
		}
		if len(fields) > 2 {
			s.Attached = fields[2] == "1"
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

func (c *execClient) NewSession(ctx context.Context, opts NewSessionOpts) error {
	if opts.Name == "" {
		return fmt.Errorf("NewSession: empty name")
	}
	args := []string{"new-session", "-d", "-s", opts.Name}
	if opts.WindowName != "" {
		args = append(args, "-n", opts.WindowName)
	}
	if opts.Cwd != "" {
		args = append(args, "-c", opts.Cwd)
	}
	if opts.WindowWidth > 0 && opts.WindowHeight > 0 {
		args = append(args, "-x", strconv.Itoa(opts.WindowWidth), "-y", strconv.Itoa(opts.WindowHeight))
	}
	if opts.Command != "" {
		args = append(args, opts.Command)
	}
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Env = appendEnv(opts.Env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux new-session %s: %s", opts.Name, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *execClient) KillSession(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("KillSession: empty name")
	}
	_, err := c.run(ctx, "kill-session", "-t", name)
	return err
}

func (c *execClient) HasSession(ctx context.Context, name string) (bool, error) {
	if name == "" {
		return false, fmt.Errorf("HasSession: empty name")
	}
	cmd := exec.CommandContext(ctx, c.bin, "has-session", "-t", name)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if asExitError(err, &ee) {
			// Exit 1 = no such session. Anything else propagates.
			if ee.ExitCode() == 1 {
				return false, nil
			}
		}
		return false, fmt.Errorf("tmux has-session %s: %w", name, err)
	}
	return true, nil
}

func (c *execClient) ListWindows(ctx context.Context, session string) ([]Window, error) {
	if session == "" {
		return nil, fmt.Errorf("ListWindows: empty session")
	}
	out, err := c.run(ctx, "list-windows", "-t", session, "-F", "#{window_index}\t#{window_name}")
	if err != nil {
		return nil, err
	}
	var windows []Window
	for line := range linesOf(out) {
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			continue
		}
		idx, perr := strconv.Atoi(fields[0])
		if perr != nil {
			continue
		}
		windows = append(windows, Window{Index: idx, Name: fields[1]})
	}
	return windows, nil
}

func (c *execClient) NewWindow(ctx context.Context, session string, opts NewWindowOpts) error {
	if session == "" || opts.Name == "" {
		return fmt.Errorf("NewWindow: empty session or name")
	}
	args := []string{"new-window", "-d", "-t", session, "-n", opts.Name}
	if opts.Cwd != "" {
		args = append(args, "-c", opts.Cwd)
	}
	if opts.Command != "" {
		args = append(args, opts.Command)
	}
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Env = appendEnv(opts.Env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux new-window %s/%s: %s", session, opts.Name, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *execClient) KillWindowByName(ctx context.Context, session, windowName string) error {
	idx, err := c.WindowIndexByName(ctx, session, windowName)
	if err != nil {
		return err
	}
	target := fmt.Sprintf("%s:%d", session, idx)
	_, err = c.run(ctx, "kill-window", "-t", target)
	return err
}

func (c *execClient) RenameWindow(ctx context.Context, session, oldName, newName string) error {
	idx, err := c.WindowIndexByName(ctx, session, oldName)
	if err != nil {
		return err
	}
	target := fmt.Sprintf("%s:%d", session, idx)
	_, err = c.run(ctx, "rename-window", "-t", target, newName)
	return err
}

func (c *execClient) WindowIndexByName(ctx context.Context, session, windowName string) (int, error) {
	windows, err := c.ListWindows(ctx, session)
	if err != nil {
		return 0, err
	}
	for _, w := range windows {
		if w.Name == windowName {
			return w.Index, nil
		}
	}
	return 0, fmt.Errorf("window %q not found in session %q", windowName, session)
}

func (c *execClient) SendKeys(ctx context.Context, session, windowName, keys string) error {
	idx, err := c.WindowIndexByName(ctx, session, windowName)
	if err != nil {
		return err
	}
	target := fmt.Sprintf("%s:%d", session, idx)
	_, err = c.run(ctx, "send-keys", "-t", target, keys)
	return err
}

// RespawnWindow kills the current process inside the named window and runs
// `command` in its place. The window itself stays — clients attached to it
// get the new program seamlessly.
func (c *execClient) RespawnWindow(ctx context.Context, session, windowName, command string) error {
	idx, err := c.WindowIndexByName(ctx, session, windowName)
	if err != nil {
		return err
	}
	target := fmt.Sprintf("%s:%d", session, idx)
	_, err = c.run(ctx, "respawn-window", "-t", target, "-k", command)
	return err
}

// appendEnv merges extra environment variables on top of the current process
// environment. tmux inherits these for the new session/window.
func appendEnv(extra []string) []string {
	if len(extra) == 0 {
		return nil
	}
	return append([]string(nil), extra...)
}

// linesOf yields non-empty lines from b without allocating a slice up front.
func linesOf(b []byte) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" {
				continue
			}
			if !yield(line) {
				return
			}
		}
	}
}

// asExitError unwraps err into *exec.ExitError if possible.
func asExitError(err error, target **exec.ExitError) bool {
	if err == nil {
		return false
	}
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}
