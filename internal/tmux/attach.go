package tmux

import (
	"context"
	"errors"
	"io"
)

// Attach and NewGroupSession are implemented in Phase 2. The interface is
// declared here so the rest of the codebase compiles against the full Client.

func (c *execClient) Attach(ctx context.Context, session, windowName string) (io.ReadWriteCloser, ResizeFunc, error) {
	_ = ctx
	_ = session
	_ = windowName
	return nil, nil, errors.New("tmux.Attach: implemented in Phase 2")
}

func (c *execClient) NewGroupSession(ctx context.Context, target, groupName string) error {
	_ = ctx
	_ = target
	_ = groupName
	return errors.New("tmux.NewGroupSession: implemented in Phase 2")
}
