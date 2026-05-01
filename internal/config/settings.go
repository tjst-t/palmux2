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
//
// S008 renamed `imageUploadDir` to `attachmentUploadDir` and added
// `attachmentTtlDays` (TTL cleanup window). The legacy key is read on
// load for backward compatibility and migrated to the new one on the
// next save. We deliberately keep an `imageUploadDir` Go field too so
// older client code (if any) that PATCHes that key still works — load()
// folds it into AttachmentUploadDir.
type Settings struct {
	BranchSortOrder     string `json:"branchSortOrder,omitempty"`  // "name" | "activity"
	LastActiveBranch    string `json:"lastActiveBranch,omitempty"` // "{repoId}/{branchId}"
	AttachmentUploadDir string `json:"attachmentUploadDir,omitempty"`
	AttachmentTtlDays   int    `json:"attachmentTtlDays,omitempty"`
	// ImageUploadDir is the legacy key (pre-S008). Kept on the struct so
	// older PATCH payloads still parse; load()/Patch() copy it into
	// AttachmentUploadDir so the rest of the codebase only reads the new
	// name. Marshalling skips it once migrated (always written as the
	// new key on next save).
	ImageUploadDir string `json:"imageUploadDir,omitempty"`

	// MaxClaudeTabsPerBranch caps how many parallel Claude tabs a branch
	// may host (S009). Each Claude tab spawns its own claude CLI
	// subprocess, so the cap protects against runaway resource use. 0 →
	// fall through to DefaultMaxClaudeTabsPerBranch.
	MaxClaudeTabsPerBranch int `json:"maxClaudeTabsPerBranch,omitempty"`

	// MaxBashTabsPerBranch caps how many tmux Bash windows a branch may
	// host (S009). Same shape as the Claude cap; defaults higher because
	// Bash tabs are cheap (idle shells).
	MaxBashTabsPerBranch int `json:"maxBashTabsPerBranch,omitempty"`

	Toolbar json.RawMessage `json:"toolbar,omitempty"`
}

// DefaultAttachmentUploadDir is the fallback when the user has not
// configured one. Server-side helpers may resolve this at runtime.
const DefaultAttachmentUploadDir = "/tmp/palmux-uploads/"

// DefaultAttachmentTtlDays is the default cleanup window for files
// under AttachmentUploadDir.
const DefaultAttachmentTtlDays = 30

// DefaultMaxClaudeTabsPerBranch is the default cap on parallel Claude tabs
// per branch. 3 keeps a single user's API quota from going wild while
// still permitting "main agent + 2 helpers" patterns.
const DefaultMaxClaudeTabsPerBranch = 3

// DefaultMaxBashTabsPerBranch is the default cap on Bash tabs per branch.
// 5 covers typical "build + watcher + scratch + repl + spare" without
// inviting tab-spam.
const DefaultMaxBashTabsPerBranch = 5

// DefaultSettings returns a Settings populated with built-in defaults.
func DefaultSettings() Settings {
	return Settings{
		BranchSortOrder:        "name",
		AttachmentUploadDir:    DefaultAttachmentUploadDir,
		AttachmentTtlDays:      DefaultAttachmentTtlDays,
		MaxClaudeTabsPerBranch: DefaultMaxClaudeTabsPerBranch,
		MaxBashTabsPerBranch:   DefaultMaxBashTabsPerBranch,
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
	migrateLegacyAttachmentDir(&settings)
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

// MaxClaudeTabsPerBranch implements tab.SettingsView. Falls through to
// the package default when the persisted value is unset/non-positive.
func (s *SettingsStore) MaxClaudeTabsPerBranch() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings.MaxClaudeTabsPerBranch > 0 {
		return s.settings.MaxClaudeTabsPerBranch
	}
	return DefaultMaxClaudeTabsPerBranch
}

// MaxBashTabsPerBranch implements tab.SettingsView.
func (s *SettingsStore) MaxBashTabsPerBranch() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings.MaxBashTabsPerBranch > 0 {
		return s.settings.MaxBashTabsPerBranch
	}
	return DefaultMaxBashTabsPerBranch
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
	// Accept either the new key or the legacy one in patches; the new
	// key wins if both are sent (the migration removes the old key).
	if update.AttachmentUploadDir != "" {
		s.settings.AttachmentUploadDir = update.AttachmentUploadDir
		s.settings.ImageUploadDir = ""
	} else if update.ImageUploadDir != "" {
		s.settings.AttachmentUploadDir = update.ImageUploadDir
		s.settings.ImageUploadDir = ""
	}
	if update.AttachmentTtlDays > 0 {
		s.settings.AttachmentTtlDays = update.AttachmentTtlDays
	}
	if update.MaxClaudeTabsPerBranch > 0 {
		s.settings.MaxClaudeTabsPerBranch = update.MaxClaudeTabsPerBranch
	}
	if update.MaxBashTabsPerBranch > 0 {
		s.settings.MaxBashTabsPerBranch = update.MaxBashTabsPerBranch
	}
	if update.Toolbar != nil {
		s.settings.Toolbar = update.Toolbar
	}
	if err := s.save(); err != nil {
		return Settings{}, err
	}
	return s.settings, nil
}

// migrateLegacyAttachmentDir folds a legacy `imageUploadDir` key into the
// new `attachmentUploadDir` field when the new field is empty. The old
// field is then cleared so subsequent saves write the new key only.
// Settings files written before S008 only have `imageUploadDir`; this
// keeps them working without forcing the user to edit the file.
func migrateLegacyAttachmentDir(s *Settings) {
	if s.AttachmentUploadDir == "" && s.ImageUploadDir != "" {
		s.AttachmentUploadDir = s.ImageUploadDir
	}
	// Always drop the legacy field once read so it doesn't get written
	// back. Subsequent saves serialise only the canonical key.
	s.ImageUploadDir = ""
}

// mergeWithDefaults fills empty fields in s from defaults. Toolbar deep-merge
// is deferred to Phase 7 — for Phase 1, an absent Toolbar key inherits the
// default (currently nil) and a present one is left untouched.
func mergeWithDefaults(s *Settings, d Settings) {
	if s.BranchSortOrder == "" {
		s.BranchSortOrder = d.BranchSortOrder
	}
	if s.AttachmentUploadDir == "" {
		s.AttachmentUploadDir = d.AttachmentUploadDir
	}
	if s.AttachmentTtlDays <= 0 {
		s.AttachmentTtlDays = d.AttachmentTtlDays
	}
	if s.MaxClaudeTabsPerBranch <= 0 {
		s.MaxClaudeTabsPerBranch = d.MaxClaudeTabsPerBranch
	}
	if s.MaxBashTabsPerBranch <= 0 {
		s.MaxBashTabsPerBranch = d.MaxBashTabsPerBranch
	}
}
