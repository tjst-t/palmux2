// Package netns manages Linux network namespace lifecycle for per-worktree
// isolation. The Manager creates a rootless user+net namespace (unshare -Urn)
// and attaches a slirp4netns subprocess for outbound connectivity.
//
// This package is Linux-only and is a no-op (all methods succeed silently)
// when slirp4netns is not found or the OS is not Linux.
package netns

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PortMapping describes an active host↔netns port forward.
type PortMapping struct {
	HostPort     int       `json:"hostPort"`
	InternalPort int       `json:"internalPort"`
	CreatedAt    time.Time `json:"createdAt"`
	PublicURL    string    `json:"publicUrl,omitempty"` // set when Caddy integration is active
}

// WorktreeState is the live state for one worktree's netns.
type WorktreeState struct {
	WorktreeID       string        `json:"worktreeId"`
	NetnsPath        string        `json:"netnsPath"`
	// AnchorPID is the PID of the anchor process that keeps the netns alive.
	// The ns is accessible at /proc/<AnchorPID>/ns/net.
	AnchorPID        int           `json:"anchorPid,omitempty"`
	SlirpPID         int           `json:"slirpPid,omitempty"`
	SlirpSocketPath  string        `json:"slirpSocketPath,omitempty"`
	IsolateNetwork   bool          `json:"isolateNetwork"`
	ParentWorktreeID string        `json:"parentWorktreeId,omitempty"` // non-empty = inherit parent's netns
	Ports            []PortMapping `json:"ports,omitempty"`
}

// StateFile is the on-disk representation of tmp/netns-state.json.
type StateFile struct {
	Worktrees []WorktreeState `json:"worktrees"`
}

// state manages the in-memory + on-disk state for all worktrees.
type state struct {
	mu   sync.Mutex
	path string
	data StateFile
}

func newState(path string) (*state, error) {
	s := &state{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *state) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.data = StateFile{}
			return nil
		}
		return fmt.Errorf("netns state: read %s: %w", s.path, err)
	}
	var sf StateFile
	if err := json.Unmarshal(b, &sf); err != nil {
		return fmt.Errorf("netns state: parse %s: %w", s.path, err)
	}
	s.data = sf
	return nil
}

func (s *state) save() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("netns state: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("netns state: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("netns state: rename %s: %w", s.path, err)
	}
	return nil
}

// Get returns a copy of the WorktreeState for the given ID.
func (s *state) Get(worktreeID string) (WorktreeState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.data.Worktrees {
		if w.WorktreeID == worktreeID {
			return copyWorktreeState(w), true
		}
	}
	return WorktreeState{}, false
}

// All returns a copy of all WorktreeStates.
func (s *state) All() []WorktreeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WorktreeState, len(s.data.Worktrees))
	for i, w := range s.data.Worktrees {
		out[i] = copyWorktreeState(w)
	}
	return out
}

// Upsert adds or replaces the state for a worktree and persists.
func (s *state) Upsert(ws WorktreeState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, w := range s.data.Worktrees {
		if w.WorktreeID == ws.WorktreeID {
			s.data.Worktrees[i] = ws
			return s.save()
		}
	}
	s.data.Worktrees = append(s.data.Worktrees, ws)
	return s.save()
}

// Delete removes a worktree's state and persists.
func (s *state) Delete(worktreeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, w := range s.data.Worktrees {
		if w.WorktreeID == worktreeID {
			s.data.Worktrees = append(s.data.Worktrees[:i], s.data.Worktrees[i+1:]...)
			return s.save()
		}
	}
	return nil
}

// AddPort adds a port mapping to a worktree's state.
func (s *state) AddPort(worktreeID string, pm PortMapping) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, w := range s.data.Worktrees {
		if w.WorktreeID == worktreeID {
			s.data.Worktrees[i].Ports = append(s.data.Worktrees[i].Ports, pm)
			return s.save()
		}
	}
	return fmt.Errorf("netns state: worktree %q not found", worktreeID)
}

// RemovePort removes a port mapping from a worktree's state.
func (s *state) RemovePort(worktreeID string, hostPort int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, w := range s.data.Worktrees {
		if w.WorktreeID == worktreeID {
			ports := s.data.Worktrees[i].Ports
			for j, p := range ports {
				if p.HostPort == hostPort {
					s.data.Worktrees[i].Ports = append(ports[:j], ports[j+1:]...)
					return s.save()
				}
			}
			return fmt.Errorf("netns state: port %d not found for worktree %q", hostPort, worktreeID)
		}
	}
	return fmt.Errorf("netns state: worktree %q not found", worktreeID)
}

// UpdatePortPublicURL updates the publicUrl field for a specific port mapping.
func (s *state) UpdatePortPublicURL(worktreeID string, hostPort int, publicURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, w := range s.data.Worktrees {
		if w.WorktreeID == worktreeID {
			for j, p := range s.data.Worktrees[i].Ports {
				if p.HostPort == hostPort {
					s.data.Worktrees[i].Ports[j].PublicURL = publicURL
					return s.save()
				}
			}
			return fmt.Errorf("netns state: port %d not found for worktree %q", hostPort, worktreeID)
		}
	}
	return fmt.Errorf("netns state: worktree %q not found", worktreeID)
}

// IsHostPortInUse returns true if any worktree has mapped the given hostPort.
func (s *state) IsHostPortInUse(hostPort int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.data.Worktrees {
		for _, p := range w.Ports {
			if p.HostPort == hostPort {
				return true
			}
		}
	}
	return false
}

// StatePath returns the canonical path for netns-state.json inside a data dir.
func StatePath(dataDir string) string {
	return filepath.Join(dataDir, "netns-state.json")
}

func copyWorktreeState(w WorktreeState) WorktreeState {
	cp := w
	if w.Ports != nil {
		cp.Ports = make([]PortMapping, len(w.Ports))
		copy(cp.Ports, w.Ports)
	}
	return cp
}
