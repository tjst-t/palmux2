package claudetui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// tuiSessionsFileName is the filename for claudetui's own session persistence.
// Separate from claudeagent's sessions.json to keep failure modes isolated
// and avoid tight cross-package coupling.
const tuiSessionsFileName = "claudetui-sessions.json"

// tuiPersistedShape is the on-disk layout for claudetui-sessions.json.
type tuiPersistedShape struct {
	// Active maps "{repoId}/{branchId}" → session_id.
	Active map[string]string `json:"active"`
}

// SessionStore persists claudetui's active session IDs using atomic
// write-via-rename discipline identical to claudeagent.Store.
//
// The file is stored under the same config directory as sessions.json, but
// with filename "claudetui-sessions.json" to keep the two tabs isolated.
type SessionStore struct {
	path string

	mu   sync.RWMutex
	data tuiPersistedShape
}

// NewSessionStore loads (or initialises) claudetui-sessions.json under dir.
// dir is typically the same configDir passed to claudeagent.NewStore.
func NewSessionStore(dir string) (*SessionStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("claudetui store: mkdir %s: %w", dir, err)
	}
	s := &SessionStore{path: filepath.Join(dir, tuiSessionsFileName)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SessionStore) load() error {
	s.data.Active = map[string]string{}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("claudetui store: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		// Corruption recovery: log and start fresh.
		s.data.Active = map[string]string{}
		return nil
	}
	if s.data.Active == nil {
		s.data.Active = map[string]string{}
	}
	return nil
}

func (s *SessionStore) save() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("claudetui store: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("claudetui store: write tmp: %w", err)
	}
	return os.Rename(tmp, s.path)
}

// storeKey builds the persistence key for (repoID, branchID).
func storeKey(repoID, branchID string) string { return repoID + "/" + branchID }

// LoadActive returns the last-known session ID for (repoID, branchID), or
// ("", false) if none has been persisted.
func (s *SessionStore) LoadActive(repoID, branchID string) (sessionID string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, exists := s.data.Active[storeKey(repoID, branchID)]
	if !exists || v == "" {
		return "", false
	}
	return v, true
}

// SetActive records sessionID as the active session for (repoID, branchID)
// and persists immediately.
func (s *SessionStore) SetActive(repoID, branchID, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("claudetui store: SetActive: empty sessionID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Active[storeKey(repoID, branchID)] = sessionID
	return s.save()
}

// ClearActive removes the active session pointer for (repoID, branchID).
func (s *SessionStore) ClearActive(repoID, branchID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Active, storeKey(repoID, branchID))
	return s.save()
}
