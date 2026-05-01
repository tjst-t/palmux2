package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/tjst-t/palmux2/internal/domain"
)

// ErrTooManyConnections is returned by AddConnection when the per-branch
// cap configured via --max-connections is already reached.
var ErrTooManyConnections = errors.New("too many connections for this branch")

// AddConnection registers an active terminal client. The returned connection
// ID is also used as the suffix for the per-client tmux session group.
//
// Returns ErrTooManyConnections when MaxConnsPerBranch is set and the branch
// already has that many live connections — the WS handler turns this into
// a 429 response.
func (s *Store) AddConnection(repoID, branchID, tabID string) (*domain.Connection, error) {
	id := newConnID()
	c := &domain.Connection{
		ID:        id,
		RepoID:    repoID,
		BranchID:  branchID,
		TabID:     tabID,
		StartedAt: time.Now(),
	}
	s.mu.Lock()
	if cap := s.deps.MaxConnsPerBranch; cap > 0 {
		count := 0
		for _, existing := range s.conns {
			if existing.RepoID == repoID && existing.BranchID == branchID {
				count++
			}
		}
		if count >= cap {
			s.mu.Unlock()
			return nil, ErrTooManyConnections
		}
	}
	s.conns[id] = c
	// S009-fix-2: remember every conn ID we've ever issued so sync_tmux's
	// zombie-kill pass leaves group sessions belonging to OTHER palmux
	// instances (sharing the same tmux server) alone. Without this guard,
	// running a host + dev instance side-by-side trampled each other's
	// `__grp_xxx` sessions, causing the user-reported 3-second WS
	// reconnect loop. Capacity is bounded — group sessions are short-
	// lived, so this set stays small in practice.
	s.knownConnIDs[id] = struct{}{}
	s.mu.Unlock()
	return c, nil
}

// RemoveConnection drops a connection from the registry.
func (s *Store) RemoveConnection(id string) {
	s.mu.Lock()
	delete(s.conns, id)
	s.mu.Unlock()
}

// Connections returns a snapshot.
func (s *Store) Connections() []domain.Connection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Connection, 0, len(s.conns))
	for _, c := range s.conns {
		out = append(out, *c)
	}
	return out
}

func newConnID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
