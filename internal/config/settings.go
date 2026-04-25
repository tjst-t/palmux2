package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Settings is the global, shared-across-devices configuration. Per-device
// settings live in localStorage on the frontend.
//
// Toolbar is left as json.RawMessage in Phase 1 so the schema can evolve in
// Phase 7 (Toolbar implementation) without churning the rest of the system.
type Settings struct {
	BranchSortOrder  string          `json:"branchSortOrder,omitempty"`  // "name" | "activity"
	LastActiveBranch string          `json:"lastActiveBranch,omitempty"` // "{repoId}/{branchId}"
	ImageUploadDir   string          `json:"imageUploadDir,omitempty"`
	Toolbar          json.RawMessage `json:"toolbar,omitempty"`
}

// DefaultSettings returns a Settings populated with built-in defaults.
func DefaultSettings() Settings {
	return Settings{
		BranchSortOrder: "name",
		ImageUploadDir:  "/tmp/palmux-uploads/",
	}
}

// SettingsStore wraps settings.json with the same atomic-write discipline as
// RepoStore.
type SettingsStore struct {
	path string

	mu       sync.RWMutex
	settings Settings
}

// NewSettingsStore loads (or initialises) settings.json under dir.
func NewSettingsStore(dir string) (*SettingsStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	s := &SettingsStore{path: filepath.Join(dir, "settings.json")}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SettingsStore) load() error {
	defaults := DefaultSettings()
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.settings = defaults
			return nil
		}
		return fmt.Errorf("config: read %s: %w", s.path, err)
	}
	var settings Settings
	if err := json.Unmarshal(b, &settings); err != nil {
		return fmt.Errorf("config: parse %s: %w", s.path, err)
	}
	mergeWithDefaults(&settings, defaults)
	s.settings = settings
	return nil
}

func (s *SettingsStore) save() error {
	b, err := json.MarshalIndent(s.settings, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns a copy of the current settings.
func (s *SettingsStore) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// Patch shallow-merges in fields from `update` (non-zero strings overwrite,
// non-nil RawMessage overwrites). Returns the resulting settings.
func (s *SettingsStore) Patch(update Settings) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if update.BranchSortOrder != "" {
		s.settings.BranchSortOrder = update.BranchSortOrder
	}
	if update.LastActiveBranch != "" {
		s.settings.LastActiveBranch = update.LastActiveBranch
	}
	if update.ImageUploadDir != "" {
		s.settings.ImageUploadDir = update.ImageUploadDir
	}
	if update.Toolbar != nil {
		s.settings.Toolbar = update.Toolbar
	}
	if err := s.save(); err != nil {
		return Settings{}, err
	}
	return s.settings, nil
}

// mergeWithDefaults fills empty fields in s from defaults. Toolbar deep-merge
// is deferred to Phase 7 — for Phase 1, an absent Toolbar key inherits the
// default (currently nil) and a present one is left untouched.
func mergeWithDefaults(s *Settings, d Settings) {
	if s.BranchSortOrder == "" {
		s.BranchSortOrder = d.BranchSortOrder
	}
	if s.ImageUploadDir == "" {
		s.ImageUploadDir = d.ImageUploadDir
	}
}
