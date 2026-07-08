package claudetui

import (
	"encoding/json"
	"strings"
)

// Package claudetui — hooks.go
//
// Builds the Claude Code hook configuration injected into each claude-tui
// subprocess so palmux receives reliable Activity Inbox notifications without
// screen-scraping the terminal.
//
// We pass the config per-process via `claude --settings '<json>'` rather than
// editing the user's global ~/.claude/settings.json or the repo's .claude/
// directory, so palmux-spawned claude sessions get the hooks and nothing else
// is touched. Identity (repo/branch/tab) and the callback URL/token travel as
// PALMUX_* env vars (see hookEnv) which the hook command inherits; the command
// itself is `palmux hook`, handled by cmd/palmux/hook.go.

// palmuxSkillDir is the base directory passed to `claude --add-dir` when
// spawning claude-tui subprocesses. Claude auto-loads any skills found under
// <dir>/.claude/skills/, so placing the palmux-browser skill at
// /usr/local/share/palmux/.claude/skills/palmux-browser/SKILL.md makes it
// available in every claude-tui session without touching ~/.claude or the
// project's .claude directory.
const palmuxSkillDir = "/usr/local/share/palmux"

// buildClaudeSettings returns the JSON string for `claude --settings`, injected
// into every claude-tui subprocess. It ALWAYS sets `disableRemoteControl: true`
// so a palmux-spawned session can never be steered by Claude's Remote Control
// feature (the session is local-only). When withHooks is true it also wires the
// Notification / Stop / UserPromptSubmit lifecycle hooks to the palmux hook
// handler. Passing settings per-process via --settings means the user's global
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

// buildHookSettings is the withHooks=true form (kept for tests / callers that
// specifically want the hook wiring). Always includes disableRemoteControl.
func buildHookSettings(hookBinPath string) (string, error) {
	return buildClaudeSettings(hookBinPath, true)
}

// hookEntries builds the Notification/Stop/UserPromptSubmit → `palmux hook` map.
// The hook command reads the event name from the JSON Claude Code writes to the
// command's stdin, so a single `palmux hook` command serves all three events.
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

// hookEnv returns the PALMUX_* environment variables the hook command reads to
// route a notification back to this exact tab. token is omitted when empty
// (open-access servers).
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
