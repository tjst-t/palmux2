package store

import (
	"context"
	"strings"

	"github.com/tjst-t/palmux2/internal/domain"
)

// OrphanWindow is one tmux window inside an orphan session.
type OrphanWindow struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
}

// OrphanSession is a tmux session that Palmux doesn't manage. Surfacing them
// lets users attach existing local tmux sessions through Palmux's web UI
// without renaming anything.
type OrphanSession struct {
	Name      string         `json:"name"`
	Attached  bool           `json:"attached"`
	CreatedAt int64          `json:"createdAt,omitempty"`
	Windows   []OrphanWindow `json:"windows"`
}

// OrphanSessions returns every tmux session not managed by Palmux. Filters
// out anything beginning with the Palmux prefix (including the per-attach
// `__grp_` group sessions, which are nested under the prefix).
func (s *Store) OrphanSessions(ctx context.Context) ([]OrphanSession, error) {
	all, err := s.deps.Tmux.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]OrphanSession, 0)
	for _, sess := range all {
		if strings.HasPrefix(sess.Name, domain.PalmuxSessionPrefix) {
			continue
		}
		windows, err := s.deps.Tmux.ListWindows(ctx, sess.Name)
		if err != nil {
			// Don't fail the whole listing because one session is misbehaving.
			continue
		}
		ow := make([]OrphanWindow, 0, len(windows))
		for _, w := range windows {
			ow = append(ow, OrphanWindow{Index: w.Index, Name: w.Name})
		}
		out = append(out, OrphanSession{
			Name:      sess.Name,
			Attached:  sess.Attached,
			CreatedAt: sess.CreatedAt,
			Windows:   ow,
		})
	}
	return out, nil
}
