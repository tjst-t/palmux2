package store

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/tjst-t/palmux2/internal/domain"
)

// AddConnection registers an active terminal client. The returned connection
// ID is also used as the suffix for the per-client tmux session group.
func (s *Store) AddConnection(repoID, branchID, tabID string) *domain.Connection {
	id := newConnID()
	c := &domain.Connection{
		ID:        id,
		RepoID:    repoID,
		BranchID:  branchID,
		TabID:     tabID,
		StartedAt: time.Now(),
	}
	s.mu.Lock()
	s.conns[id] = c
	s.mu.Unlock()
	return c
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
