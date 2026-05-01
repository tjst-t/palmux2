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

	// PreviewMaxBytes is the soft cap on file size for in-browser preview
	// in the Files tab (S010). Files above this threshold render a "too
	// large to preview" placeholder instead of being shipped to the
	// frontend Monaco / image / drawio viewers. 0 → fall through to
	// DefaultPreviewMaxBytes.
	PreviewMaxBytes int64 `json:"previewMaxBytes,omitempty"`

	// AutoWorktreePathPatterns (S015) lists glob patterns that mark a
	// worktree as auto-generated (subagent / autopilot output). When a
	// worktree's absolute path matches any of these patterns AND the
	// branch isn't in `repos.json#userOpenedBranches`, the Drawer
	// classifies it under the "subagent / autopilot" section so the user's
	// hand-managed branches stay visible. Default
	// (`DefaultAutoWorktreePathPatterns`) catches claude-skills sub-agent
	// output. The field is `omitempty` because an empty slice in JSON
	// means "no auto patterns" — distinct from "use the default" which is
	// signalled by the absence of the key.
	AutoWorktreePathPatterns []string `json:"autoWorktreePathPatterns,omitempty"`

	// ReadPreviewLineCount (S017) controls how many leading lines of a
	// Read tool result are rendered before the "Show all (X lines)"
	// toggle is offered. The FE consults this on each tool_result block
	// and slices the body to `[:N]`. 0 → fall through to
	// DefaultReadPreviewLineCount. Negative values are coerced to the
	// default at PATCH time.
	ReadPreviewLineCount int `json:"readPreviewLineCount,omitempty"`

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

// DefaultPreviewMaxBytes caps Files-tab preview at 10 MiB. Above this we
// skip the bandwidth round-trip and render a placeholder client-side.
// 10 MiB matches the S010 acceptance criterion. Configurable via
// `previewMaxBytes` in settings.json.
const DefaultPreviewMaxBytes int64 = 10 * 1024 * 1024

// DefaultAutoWorktreePathPatterns matches the worktree directory layout
// claude-skills sub-agents create (`.claude/worktrees/<id>`). Users with
// custom autopilot tooling can override via `autoWorktreePathPatterns`
// in settings.json. The literal `*` is interpreted as `[^/]*` and the
// pattern as a substring of the worktree's absolute path (the matcher
// itself lives in internal/store).
var DefaultAutoWorktreePathPatterns = []string{".claude/worktrees/*"}

// DefaultReadPreviewLineCount caps Read tool result preview at 50
// leading lines. Above this we render a "Show all (X lines)" toggle
// (S017). Configurable via `readPreviewLineCount` in settings.json.
const DefaultReadPreviewLineCount = 50

// DefaultSettings returns a Settings populated with built-in defaults.
func DefaultSettings() Settings {
	return Settings{
		BranchSortOrder:          "name",
		AttachmentUploadDir:      DefaultAttachmentUploadDir,
		AttachmentTtlDays:        DefaultAttachmentTtlDays,
		MaxClaudeTabsPerBranch:   DefaultMaxClaudeTabsPerBranch,
		MaxBashTabsPerBranch:     DefaultMaxBashTabsPerBranch,
		PreviewMaxBytes:          DefaultPreviewMaxBytes,
		AutoWorktreePathPatterns: append([]string(nil), DefaultAutoWorktreePathPatterns...),
		ReadPreviewLineCount:     DefaultReadPreviewLineCount,
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
	if update.PreviewMaxBytes > 0 {
		s.settings.PreviewMaxBytes = update.PreviewMaxBytes
	}
	if update.ReadPreviewLineCount > 0 {
		s.settings.ReadPreviewLineCount = update.ReadPreviewLineCount
	}
	// S015: a nil slice in the patch means "leave alone"; an explicit
	// empty slice (provided by the FE as `[]`) clears all patterns;
	// otherwise overwrite. We can't distinguish nil from `[]` after
	// json.Unmarshal directly, so PATCH semantics here are "any non-nil
	// slice replaces". A future FE that wants to reset to defaults
	// should DELETE the key entirely (which Go decodes as nil — leave
	// alone) — that's a UI rather than API distinction.
	if update.AutoWorktreePathPatterns != nil {
		clean := make([]string, 0, len(update.AutoWorktreePathPatterns))
		for _, p := range update.AutoWorktreePathPatterns {
			if p == "" {
				continue
			}
			clean = append(clean, p)
		}
		s.settings.AutoWorktreePathPatterns = clean
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
	if s.PreviewMaxBytes <= 0 {
		s.PreviewMaxBytes = d.PreviewMaxBytes
	}
	if s.ReadPreviewLineCount <= 0 {
		s.ReadPreviewLineCount = d.ReadPreviewLineCount
	}
	// S015: only inherit defaults when the key is *absent* from the file
	// (decoded as nil). An explicit empty slice — `"autoWorktreePathPatterns": []`
	// — is honoured as "user opted out of auto detection".
	if s.AutoWorktreePathPatterns == nil {
		s.AutoWorktreePathPatterns = append([]string(nil), d.AutoWorktreePathPatterns...)
	}
}

// AutoWorktreePathPatterns implements the SettingsView slice accessor
// used by category derivation. Always returns a defensive copy so callers
// can iterate without holding the lock.
func (s *SettingsStore) AutoWorktreePathPatterns() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings.AutoWorktreePathPatterns == nil {
		return append([]string(nil), DefaultAutoWorktreePathPatterns...)
	}
	return append([]string(nil), s.settings.AutoWorktreePathPatterns...)
}
