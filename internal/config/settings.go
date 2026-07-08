package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// UserCommandTarget specifies where a user-defined palette command is
// dispatched when selected.
type UserCommandTarget string

const (
	UserCommandTargetBash  UserCommandTarget = "bash"
	UserCommandTargetURL   UserCommandTarget = "url"
	UserCommandTargetFiles UserCommandTarget = "files"
)

// UserCommand is a single entry in palette.userCommands. The payload field
// (Command / URL / Path) is determined by Target:
//   - bash  → Command (shell string sent to the MRU Bash tab)
//   - url   → URL (opened in a new browser tab)
//   - files → Path (relative path navigated to in the Files tab)
type UserCommand struct {
	Name    string            `json:"name"`
	Target  UserCommandTarget `json:"target"`
	Command string            `json:"command,omitempty"`
	URL     string            `json:"url,omitempty"`
	Path    string            `json:"path,omitempty"`
	Notes   string            `json:"notes,omitempty"`
}

// Validate checks that a UserCommand is self-consistent.
func (u UserCommand) Validate() error {
	if u.Name == "" {
		return fmt.Errorf("userCommand: name is required")
	}
	switch u.Target {
	case UserCommandTargetBash:
		if u.Command == "" {
			return fmt.Errorf("userCommand %q: target 'bash' requires command", u.Name)
		}
	case UserCommandTargetURL:
		if u.URL == "" {
			return fmt.Errorf("userCommand %q: target 'url' requires url", u.Name)
		}
	case UserCommandTargetFiles:
		if u.Path == "" {
			return fmt.Errorf("userCommand %q: target 'files' requires path", u.Name)
		}
	default:
		return fmt.Errorf("userCommand %q: unknown target %q (must be bash|url|files)", u.Name, u.Target)
	}
	return nil
}

// PaletteSettings holds palette-specific user configuration.
type PaletteSettings struct {
	// UserCommands lists user-defined commands shown in the ⌘K '>' mode.
	UserCommands []UserCommand `json:"userCommands,omitempty"`
}

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

	// SubagentStaleAfterDays (S021) is the threshold in days used by the
	// "Clean up subagent worktrees" Drawer action: a subagent worktree is
	// considered stale when it has no `.claude/autopilot-*.lock` AND its
	// last commit is older than this many days. 0 → fall through to
	// DefaultSubagentStaleAfterDays. Negative values are coerced to the
	// default at PATCH time.
	SubagentStaleAfterDays int `json:"subagentStaleAfterDays,omitempty"`

	Toolbar json.RawMessage `json:"toolbar,omitempty"`

	// Palette (S032) holds palette-specific settings, currently just
	// user-defined commands shown in ⌘K '>' mode.
	Palette *PaletteSettings `json:"palette,omitempty"`

	// Claude (S1f75ec-2) holds global claude-tab settings. Currently only
	// DefaultMode controls which implementation new branches default to.
	// Existing branches (= those already in repos.json without a
	// claude_mode entry) always default to "agent" regardless of this
	// setting, to preserve backwards compatibility.
	Claude *ClaudeGlobalSettings `json:"claude,omitempty"`

	// DefaultRuntime (S8478ca-3) is the global runtime default applied when
	// neither a per-Workspace nor a per-repo override is set. When Kind is
	// empty or invalid the field is treated as "unset" (resolver falls
	// through to host fallback). An explicit PATCH with kind="" clears it.
	DefaultRuntime *runtime.Config `json:"defaultRuntime,omitempty"`
}

// ClaudeGlobalSettings (S1f75ec-2) is the sub-object under settings.json
// `claude`. Only DefaultMode is defined for now.
type ClaudeGlobalSettings struct {
	// DefaultMode is the mode assigned to newly-opened branches. Valid values
	// are "agent" and "tui". Absent / empty defaults to "tui".
	DefaultMode string `json:"default_mode,omitempty"`
	// PermissionMode is the claude --permission-mode launched sessions start in.
	// Valid: default, auto, plan, acceptEdits, dontAsk, bypassPermissions, manual.
	// Absent / empty resolves to "auto" (see ClaudePermissionMode). bypassPermissions
	// disables all prompts — fine for the sandboxed container / non-root user.
	PermissionMode string `json:"permission_mode,omitempty"`
}

// validClaudePermissionModes is the set accepted for claude.permission_mode
// (mirrors Claude Code's --permission-mode values).
var validClaudePermissionModes = map[string]bool{
	"default": true, "auto": true, "plan": true, "acceptEdits": true,
	"dontAsk": true, "bypassPermissions": true, "manual": true,
}

// DefaultClaudePermissionMode is the value used when unset.
const DefaultClaudePermissionMode = "auto"

// ClaudePermissionMode resolves the effective claude --permission-mode, applying
// the "auto" default when unset.
func (s Settings) ClaudePermissionMode() string {
	if s.Claude != nil && s.Claude.PermissionMode != "" {
		return s.Claude.PermissionMode
	}
	return DefaultClaudePermissionMode
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

// DefaultSubagentStaleAfterDays is the default age threshold (in days)
// for the S021 subagent-cleanup action. 7 days follows the spec
// guidance: long enough that an in-progress sub-agent run isn't
// targeted by accident, short enough that orphaned worktrees from
// completed runs surface promptly.
const DefaultSubagentStaleAfterDays = 7

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
		SubagentStaleAfterDays:   DefaultSubagentStaleAfterDays,
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
		if update.BranchSortOrder != "name" && update.BranchSortOrder != "activity" {
			return Settings{}, fmt.Errorf("config: patch: invalid branchSortOrder %q (must be name or activity)", update.BranchSortOrder)
		}
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
	if update.SubagentStaleAfterDays > 0 {
		s.settings.SubagentStaleAfterDays = update.SubagentStaleAfterDays
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
	// S032: palette.userCommands — validate each entry before persisting.
	if update.Palette != nil {
		for _, uc := range update.Palette.UserCommands {
			if err := uc.Validate(); err != nil {
				return Settings{}, fmt.Errorf("config: patch: %w", err)
			}
		}
		if s.settings.Palette == nil {
			s.settings.Palette = &PaletteSettings{}
		}
		s.settings.Palette.UserCommands = update.Palette.UserCommands
	}
	// S1f75ec-2: claude.default_mode — allow "" to clear back to the default.
	if update.Claude != nil {
		m := update.Claude.DefaultMode
		if m != "" && m != "agent" && m != "tui" {
			return Settings{}, fmt.Errorf("config: patch: invalid claude.default_mode %q (must be agent or tui)", m)
		}
		pm := update.Claude.PermissionMode
		if pm != "" && !validClaudePermissionModes[pm] {
			return Settings{}, fmt.Errorf("config: patch: invalid claude.permission_mode %q", pm)
		}
		if s.settings.Claude == nil {
			s.settings.Claude = &ClaudeGlobalSettings{}
		}
		// Field-wise merge: an empty field leaves the existing value alone so a
		// patch of one field never clobbers the other.
		if m != "" {
			s.settings.Claude.DefaultMode = m
		}
		if pm != "" {
			s.settings.Claude.PermissionMode = pm
		}
	}
	// S8478ca-3: defaultRuntime — a non-nil pointer triggers validation.
	// An explicit {kind:""} clears the field (nil-vs-empty: nil = leave
	// alone, non-nil = replace, empty kind = clear).
	if update.DefaultRuntime != nil {
		if update.DefaultRuntime.Kind != "" && !update.DefaultRuntime.Kind.IsValid() {
			return Settings{}, fmt.Errorf("config: patch: invalid defaultRuntime.kind %q", update.DefaultRuntime.Kind)
		}
		if update.DefaultRuntime.Kind == "" {
			// Explicit clear.
			s.settings.DefaultRuntime = nil
		} else {
			cfg := *update.DefaultRuntime
			s.settings.DefaultRuntime = &cfg
		}
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
	if s.SubagentStaleAfterDays <= 0 {
		s.SubagentStaleAfterDays = d.SubagentStaleAfterDays
	}
	// S015: only inherit defaults when the key is *absent* from the file
	// (decoded as nil). An explicit empty slice — `"autoWorktreePathPatterns": []`
	// — is honoured as "user opted out of auto detection".
	if s.AutoWorktreePathPatterns == nil {
		s.AutoWorktreePathPatterns = append([]string(nil), d.AutoWorktreePathPatterns...)
	}
}

// ClaudeDefaultMode (S1f75ec-2) returns the global default mode for new
// branches. Falls through to "tui" when the setting is unset or invalid.
func (s *SettingsStore) ClaudeDefaultMode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings.Claude != nil {
		m := s.settings.Claude.DefaultMode
		if m == "agent" || m == "tui" {
			return m
		}
	}
	return "tui"
}

// DefaultRuntime (S8478ca-3) returns the global default runtime, or the zero
// value (runtime.Config{}) when unset. Callers should treat a zero-value
// Config (Kind == "") as "unset" and fall through to the host fallback.
func (s *SettingsStore) DefaultRuntime() runtime.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings.DefaultRuntime != nil && s.settings.DefaultRuntime.Kind.IsValid() {
		return *s.settings.DefaultRuntime
	}
	return runtime.Config{}
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

// SubagentStaleAfterDays returns the configured threshold (in days) for
// the S021 subagent-cleanup action, falling back to the default when
// unset or non-positive.
func (s *SettingsStore) SubagentStaleAfterDays() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings.SubagentStaleAfterDays > 0 {
		return s.settings.SubagentStaleAfterDays
	}
	return DefaultSubagentStaleAfterDays
}

// Reload re-reads settings.json from disk and replaces the in-memory copy.
// On parse failure the previous value is preserved (Sa53137-1: 不正時は前値維持)
// and an error is returned so the caller can log it. On success it returns the
// freshly loaded Settings. Used by the fsnotify watcher (and could be called
// by `palmux apply`) so direct disk edits take effect without a restart.
func (s *SettingsStore) Reload() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.settings
	if err := s.load(); err != nil {
		// load() leaves s.settings untouched on read error, but on a JSON
		// parse error it returns before assigning, so prev is still intact.
		s.settings = prev
		return prev, err
	}
	return s.settings, nil
}

// SettingsWatcher watches the directory containing settings.json with fsnotify
// and invokes onChange after the file is created/written. It reuses the
// dir-watch + basename-filter pattern from claudetui.SessionWatcher: because
// SettingsStore.save() writes a temp file then os.Rename()s it over
// settings.json, watching the parent directory (not the file directly) and
// filtering on the basename is the robust way to catch atomic-rename saves
// done by both palmux itself and external editors.
type SettingsWatcher struct {
	fsw       *fsnotify.Watcher
	dir       string
	base      string
	onChange  func(Settings)
	logger    *slog.Logger
	store     *SettingsStore
	done      chan struct{}
	closeOnce sync.Once
}

// WatchFile starts an fsnotify watcher on the directory holding settings.json.
// Whenever the file changes on disk, the store is reloaded and onChange is
// invoked with the new Settings (skipped when the reload fails validation —
// previous value is kept and the error is logged). Returns a *SettingsWatcher
// whose Close() releases the fd.
func (s *SettingsStore) WatchFile(onChange func(Settings), logger *slog.Logger) (*SettingsWatcher, error) {
	if logger == nil {
		logger = slog.Default()
	}
	dir := filepath.Dir(s.path)
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("config: settings watcher: %w", err)
	}
	if err := fsw.Add(dir); err != nil {
		fsw.Close()
		return nil, fmt.Errorf("config: settings watcher add %s: %w", dir, err)
	}
	w := &SettingsWatcher{
		fsw:      fsw,
		dir:      dir,
		base:     filepath.Base(s.path),
		onChange: onChange,
		logger:   logger,
		store:    s,
		done:     make(chan struct{}),
	}
	go w.loop()
	return w, nil
}

// Close stops the watcher. Idempotent and safe to call concurrently.
func (w *SettingsWatcher) Close() error {
	w.closeOnce.Do(func() { close(w.done) })
	return w.fsw.Close()
}

func (w *SettingsWatcher) loop() {
	// Coalesce the write+rename burst that a single save produces into one
	// reload. 400ms is long enough to swallow editor multi-write saves while
	// still feeling immediate.
	const debounce = 400 * time.Millisecond
	// A single Timer reused across events. Reset is always preceded by a
	// Stop+drain so a stale tick cannot fire early (the documented Go pitfall).
	timer := time.NewTimer(debounce)
	timer.Stop()
	drain := func() {
		// Stop returns false when the timer already fired or was stopped; in the
		// fired case a value may still be buffered in the channel — drain it.
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	for {
		select {
		case <-w.done:
			timer.Stop()
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) != w.base {
				continue
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
				continue
			}
			drain()
			timer.Reset(debounce)
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
		case <-timer.C:
			// Snapshot the in-memory settings BEFORE reloading. If the on-disk
			// content matches what memory already held, the change was applied
			// in-process (a PATCH that already published settings.updated), so
			// the watcher must NOT publish a duplicate WS event. Only a genuine
			// external edit (disk != memory) fires onChange.
			before, beErr := json.Marshal(w.store.Get())
			updated, err := w.store.Reload()
			if err != nil {
				w.logger.Warn("settings reload from disk failed; keeping previous values", "err", err)
				continue
			}
			after, afErr := json.Marshal(updated)
			if beErr == nil && afErr == nil && string(before) == string(after) {
				continue // no external change → skip duplicate publish
			}
			if w.onChange != nil {
				w.onChange(updated)
			}
		}
	}
}
