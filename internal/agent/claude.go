package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// KindClaude is the built-in Claude Code adapter kind.
const KindClaude Kind = "claude"

// S4d8b1c: when the workspace runtime can build an in-container PTY command
// (incus), claude runs INSIDE the container at these fixed paths.
const (
	// containerClaudeBin is the absolute path of the (host-mounted) claude
	// binary inside the container. A non-login `incus exec` has no
	// ~/.local/bin on PATH, so claude must be invoked by absolute path.
	containerClaudeBin = "/home/ubuntu/.local/bin/claude"
)

// palmuxSkillDir is the base directory passed to `claude --plugin-dir` when
// spawning claude subprocesses inside a workspace container. Claude
// auto-loads any skills found under <dir>/.claude/skills/, so placing the
// palmux-browser skill at /usr/local/share/palmux/.claude/skills/… makes it
// available in every in-container claude session without touching
// ~/.claude or the project's .claude directory.
const palmuxSkillDir = "/usr/local/share/palmux"

// ClaudeAdapter is the built-in Adapter for the `claude` CLI. It builds the
// spawn argv (--settings hooks JSON / --permission-mode / --resume /
// --plugin-dir / in-container binary path) and implements SessionDiscoverer
// via the `~/.claude/projects/<slug>` transcript layout — this is a
// behavior-preserving extraction of the logic that used to live directly in
// internal/tab/claudetui's daemon.go, hooks.go, and sessions.go (Sdec0a7-1).
type ClaudeAdapter struct {
	mu   sync.RWMutex
	bin  string
	args []string
}

var (
	_ Adapter           = (*ClaudeAdapter)(nil)
	_ SessionDiscoverer = (*ClaudeAdapter)(nil)
	_ Configurable      = (*ClaudeAdapter)(nil)
)

// NewClaudeAdapter creates a ClaudeAdapter. bin defaults to "claude" when
// empty; args are extra arguments appended to every spawn.
func NewClaudeAdapter(bin string, args []string) *ClaudeAdapter {
	if bin == "" {
		bin = "claude"
	}
	return &ClaudeAdapter{bin: bin, args: append([]string(nil), args...)}
}

// Kind returns "claude".
func (a *ClaudeAdapter) Kind() Kind { return KindClaude }

// DisplayName returns "Claude".
func (a *ClaudeAdapter) DisplayName() string { return "Claude" }

// Capabilities reports full resume + full notify + in-container + permission
// mode support — the historical claude-tui behavior.
func (a *ClaudeAdapter) Capabilities() Capabilities {
	return Capabilities{
		Resume:         true,
		Notify:         NotifyFull,
		InContainer:    true,
		PermissionMode: true,
	}
}

// SetBin hot-swaps the claude binary path used for spawns after this call
// (Sa53137-3 hot apply). Existing subprocesses keep their binary until
// respawn. Empty bin is a no-op (mirrors the historical SetClaudeBin guard).
func (a *ClaudeAdapter) SetBin(bin string) {
	if bin == "" {
		return
	}
	a.mu.Lock()
	a.bin = bin
	a.mu.Unlock()
}

// SetArgs hot-swaps the extra args passed to claude on every spawn.
func (a *ClaudeAdapter) SetArgs(args []string) {
	a.mu.Lock()
	a.args = append([]string(nil), args...)
	a.mu.Unlock()
}

func (a *ClaudeAdapter) snapshot() (bin string, args []string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.bin, append([]string(nil), a.args...)
}

// SpawnSpec builds the claude argv, additional env, and container kill
// pattern for intent. The argv assembly order (permission-mode, then
// settings, then plugin-dir, then base args/--resume) exactly matches the
// pre-extraction claudetui daemon so this is a behavior-preserving move —
// see internal/agent/claude_golden_test.go for the equality proof.
func (a *ClaudeAdapter) SpawnSpec(intent SpawnIntent) (SpawnSpec, error) {
	bin, base := a.snapshot()

	args := append([]string(nil), base...)
	if intent.ResumeSessionID != "" {
		args = append(args, "--resume", intent.ResumeSessionID)
	}

	if intent.InContainer {
		bin = containerClaudeBin
		// The palmux-browser skill plugin only exists inside the container
		// image. --plugin-dir is the correct flag to register it
		// (--add-dir merely grants file access and does NOT load skills).
		args = append([]string{"--plugin-dir", palmuxSkillDir}, args...)
	}

	hookBin := intent.Hook.HookBinPath
	notifyURL := intent.Hook.NotifyURL
	hooksAvailable := hookBin != "" && notifyURL != ""

	// Always inject session-scoped settings via --settings:
	// disableRemoteControl is unconditional (no remote steering of a
	// palmux-spawned session), and the notification hooks are added when a
	// notify endpoint is available. This never touches the user's
	// ~/.claude or the repo's .claude.
	settings, err := buildClaudeSettings(hookBin, hooksAvailable)
	if err != nil {
		return SpawnSpec{}, fmt.Errorf("agent: build claude settings: %w", err)
	}
	args = append([]string{"--settings", settings}, args...)

	var env []string
	if hooksAvailable {
		env = hookEnv(notifyURL, intent.Hook.Token, intent.Hook.RepoID, intent.Hook.BranchID, intent.Hook.TabID)
		// S339021-3: thread the human-readable tab name through so a branch
		// with two Claude tabs can be disambiguated in the Activity Inbox,
		// mirroring the codex/opencode adapters. Omitted when empty (keeps
		// the golden-argv test, which never sets Hook.TabName, byte-identical).
		if intent.Hook.TabName != "" {
			env = append(env, "PALMUX_TAB_NAME="+intent.Hook.TabName)
		}
	}

	// Permission mode (global setting, default "auto"). The flag overrides
	// any defaultMode from settings files, so this is authoritative.
	if intent.PermissionMode != "" {
		args = append([]string{"--permission-mode", intent.PermissionMode}, args...)
	}

	return SpawnSpec{
		Argv:        append([]string{bin}, args...),
		Env:         env,
		KillPattern: containerClaudeBin,
	}, nil
}

// TranscriptDir maps a worktree absolute path to the directory where the
// Claude CLI writes per-session .jsonl transcripts.
//
// The algorithm mirrors claudeagent.transcriptDir (read for canonical
// source): replace every '/' and '.' in the absolute path with '-', then
// join under ~/.claude/projects/<slug>. Example:
//
//	/home/ubuntu/ghq/github.com/foo/bar → ~/.claude/projects/-home-ubuntu-ghq-github-com-foo-bar
//
// Pure function — no I/O on worktree itself.
func (a *ClaudeAdapter) TranscriptDir(worktree string) (string, error) {
	if worktree == "" {
		return "", errors.New("agent: claude: empty worktree")
	}
	abs, err := filepath.Abs(worktree)
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(abs)
	return filepath.Join(home, ".claude", "projects", slug), nil
}

// SessionIDFromPath reports whether path names a valid claude session
// transcript (a RFC4122-UUID-named .jsonl file) and, if so, its session ID.
func (a *ClaudeAdapter) SessionIDFromPath(path string) (string, bool) {
	name := filepath.Base(path)
	if !strings.HasSuffix(name, ".jsonl") {
		return "", false
	}
	id := strings.TrimSuffix(name, ".jsonl")
	if !looksLikeSessionID(id) {
		return "", false
	}
	return id, true
}

// looksLikeSessionID guards against random non-uuid files in the projects
// dir. Claude Code session IDs are RFC4122 UUIDs.
func looksLikeSessionID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

// LatestSessionID scans transcriptDir for *.jsonl files and returns the
// session_id (filename without .jsonl extension) of the one with the
// highest modification time. Returns ("", zero, nil) when the directory is
// empty or contains no valid session files.
func LatestSessionID(transcriptDir string) (sessionID string, mtime time.Time, err error) {
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", time.Time{}, nil
		}
		return "", time.Time{}, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if !looksLikeSessionID(id) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if info.ModTime().After(mtime) {
			mtime = info.ModTime()
			sessionID = id
		}
	}
	return sessionID, mtime, nil
}

// buildClaudeSettings returns the JSON string for `claude --settings`,
// injected into every claude subprocess. It ALWAYS sets
// `disableRemoteControl: true` so a palmux-spawned session can never be
// steered by Claude's Remote Control feature (the session is local-only).
// When withHooks is true it also wires the Notification / Stop /
// UserPromptSubmit lifecycle hooks to the palmux hook handler. Passing
// settings per-process via --settings means the user's global
// ~/.claude/settings.json and the repo's .claude/ are left untouched.
func buildClaudeSettings(hookBinPath string, withHooks bool) (string, error) {
	settings := map[string]any{
		"disableRemoteControl": true,
	}
	if withHooks {
		settings["hooks"] = hookEntries(hookBinPath)
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// buildHookSettings is the withHooks=true form (kept for tests / callers
// that specifically want the hook wiring). Always includes
// disableRemoteControl.
func buildHookSettings(hookBinPath string) (string, error) {
	return buildClaudeSettings(hookBinPath, true)
}

// hookEntries builds the Notification/Stop/UserPromptSubmit → `<bin> hook`
// map. The hook command reads the event name from the JSON Claude Code
// writes to the command's stdin, so a single `<bin> hook` command serves
// all three events.
func hookEntries(hookBinPath string) map[string]any {
	command := shellQuote(hookBinPath) + " hook"
	entry := []any{
		map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": command,
					"timeout": 5,
				},
			},
		},
	}
	return map[string]any{
		"Notification":     entry,
		"Stop":             entry,
		"UserPromptSubmit": entry,
	}
}

// hookEnv returns the PALMUX_* environment variables the hook command reads
// to route a notification back to this exact tab. token is omitted when
// empty (open-access servers).
func hookEnv(notifyURL, token, repoID, branchID, tabID string) []string {
	env := []string{
		"PALMUX_NOTIFY_URL=" + notifyURL,
		"PALMUX_REPO_ID=" + repoID,
		"PALMUX_BRANCH_ID=" + branchID,
		"PALMUX_TAB_ID=" + tabID,
	}
	if token != "" {
		env = append(env, "PALMUX_TOKEN="+token)
	}
	return env
}

// shellQuote single-quotes s for safe embedding in the hook command string
// (Claude Code runs `type: command` hooks through a shell).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
