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

// buildHookSettings returns the JSON string for `claude --settings` that wires
// the Notification / Stop / UserPromptSubmit lifecycle hooks to the palmux hook
// handler. hookBinPath is the absolute path to the palmux binary.
//
// The hook command reads the event name from the JSON Claude Code writes to the
// command's stdin, so a single `palmux hook` command serves all three events.
func buildHookSettings(hookBinPath string) (string, error) {
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
	settings := map[string]any{
		"hooks": map[string]any{
			"Notification":     entry,
			"Stop":             entry,
			"UserPromptSubmit": entry,
		},
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return "", err
	}
	return string(b), nil
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
