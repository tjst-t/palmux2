package tmux

import (
	"errors"
	"testing"
)

func TestIsNoServerErr(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"no server running (host socket)", "tmux list-windows -t x: no server running on /tmp/tmux-1000/default", true},
		{"error connecting (missing socket)", "tmux has-session -t x: error connecting to /tmp/tmux-1000/default (No such file or directory)", true},
		{"wrapped by incus exec", "incus exec tmux list-windows: exit 1: no server running on /tmp/tmux-1000/default", true},
		{"session absent but server up", "can't find session: _palmux_x", false},
		{"duplicate session", "duplicate session: _palmux_x", false},
		{"nil error", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var err error
			if c.msg != "" {
				err = errors.New(c.msg)
			}
			if got := IsNoServerErr(err); got != c.want {
				t.Errorf("IsNoServerErr(%q) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}
