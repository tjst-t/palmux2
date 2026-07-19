package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// runHook implements the `palmux hook` subcommand. It is the handler that
// Claude Code invokes for the Notification / Stop / UserPromptSubmit lifecycle
// hooks configured (per claude-tui subprocess) via `claude --settings`. See
// internal/tab/claudetui/hooks.go for the settings JSON that wires it up.
//
// Design notes (why a subcommand instead of the v1 curl+tmux approach):
//   - Identity travels via env vars injected onto the claude process at spawn
//     (PALMUX_REPO_ID / PALMUX_BRANCH_ID / PALMUX_TAB_ID), so the hook never has
//     to introspect tmux — claude-tui isn't even in a tmux window.
//   - The notify endpoint URL + token are injected too (PALMUX_NOTIFY_URL /
//     PALMUX_TOKEN), so there is no env.* file scanning / multi-instance loop.
//   - Doing the POST in Go avoids the shell/curl/jq quoting that made the v1
//     hook brittle.
//
// It is deliberately fail-open: any error (no env, unreachable server, bad
// stdin) results in a silent exit 0 so a misconfigured palmux never blocks or
// disrupts the user's claude session.
//
// stdin carries Claude Code's hook JSON payload (hook_event_name, message,
// notification_type, last_assistant_message, …). The event is taken from that
// payload, or from an explicit `--event` flag (used by tests).
//
// S2b5691: an explicit `--agent=codex` flag switches to the codex notify wire
// format instead — see runCodexHook. Codex passes its JSON payload as the
// LAST argv element (not stdin), matching `-c
// notify=["<hookbin>","hook","--agent=codex"]` in internal/agent/codex.go.
//
// `--agent=opencode` switches to the opencode notify wire format — see
// runOpencodeHook. Unlike codex, opencode's notify plugin
// (internal/agent/opencode.go's embedded palmux-notify.js) POSTs its JSON
// payload on the hook subprocess's STDIN (it spawns `<hookbin> hook
// --agent=opencode` directly via child_process.spawn, not through a shell
// command string), so runOpencodeHook reads stdin like the claude path does.
//
// Any other/unrecognized --agent value (including the default, absent flag)
// falls through to the claude/stdin path so a misconfiguration fails open
// rather than silently dropping notifications.
func runHook(args []string) int {
	eventOverride, urlOverride, agentKind := parseHookArgs(args)

	if agentKind == "codex" {
		return runCodexHook(args, urlOverride)
	}
	if agentKind == "opencode" {
		return runOpencodeHook(urlOverride)
	}

	// Best-effort read of the hook payload. Cap the read so a huge transcript
	// echoed onto stdin can't blow up memory; we only need the small header.
	raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	var payload struct {
		HookEventName        string `json:"hook_event_name"`
		Message              string `json:"message"`
		NotificationType     string `json:"notification_type"`
		LastAssistantMessage string `json:"last_assistant_message"`
	}
	_ = json.Unmarshal(raw, &payload)

	event := eventOverride
	if event == "" {
		event = payload.HookEventName
	}

	url := urlOverride
	if url == "" {
		url = os.Getenv("PALMUX_NOTIFY_URL")
	}
	repoID := os.Getenv("PALMUX_REPO_ID")
	branchID := os.Getenv("PALMUX_BRANCH_ID")
	tabID := os.Getenv("PALMUX_TAB_ID")
	tabName := os.Getenv("PALMUX_TAB_NAME")
	token := os.Getenv("PALMUX_TOKEN")

	// Nothing to call, or no idea where to route — fail open.
	if url == "" || repoID == "" || branchID == "" {
		return 0
	}

	body, ok := buildHookBody(event, repoID, branchID, tabID, tabName, payload.NotificationType, payload.Message, payload.LastAssistantMessage)
	if !ok {
		return 0
	}

	postHookNotification(url, token, body)
	return 0
}

// hookFlagsWithValues lists the hook subcommand's known flags that consume
// the FOLLOWING argv element as their value (as opposed to an inline
// `--flag=value` token). parseHookArgs and lastNonFlagHookArg both need this
// same knowledge — parseHookArgs to consume the value, lastNonFlagHookArg to
// skip past it so it can never be mistaken for codex's JSON payload (e.g.
// `--url http://x/api/notify` placed after the payload) — so it is kept as a
// single shared table rather than two separately-maintained heuristics.
// Extend this alongside any new such flag.
var hookFlagsWithValues = map[string]bool{
	"--event": true,
	"--url":   true,
}

// parseHookArgs extracts the optional --event, --url, and --agent=<kind>
// overrides. Unknown flags are ignored (fail-open).
func parseHookArgs(args []string) (event, url, agentKind string) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--event":
			if i+1 < len(args) {
				event = args[i+1]
				i++
			}
		case args[i] == "--url":
			if i+1 < len(args) {
				url = args[i+1]
				i++
			}
		case strings.HasPrefix(args[i], "--agent="):
			agentKind = strings.TrimPrefix(args[i], "--agent=")
		}
	}
	return event, url, agentKind
}

// runCodexHook implements the `--agent=codex` branch of runHook. Codex's own
// `notify` mechanism (see internal/agent/codex.go's `-c notify=[...]`
// injection) invokes the configured program with a single JSON payload as
// the LAST command-line argument — never on stdin. Today codex only ever
// fires `"type":"agent-turn-complete"` (there is no permission-wait signal in
// the TUI), which maps to the same "your turn" notification shape Claude's
// Stop hook produces, so it reuses the "claudetui.*" type prefix the FE
// Activity Inbox already recognizes and the same "claudetui-hook-<tabId>"
// RequestID scheme (hookRequestID) so an Inbox entry updates in place rather
// than piling up.
func runCodexHook(args []string, urlOverride string) int {
	raw := lastNonFlagHookArg(args)
	var payload struct {
		Type                 string `json:"type"`
		LastAssistantMessage string `json:"last-assistant-message"`
	}
	_ = json.Unmarshal([]byte(raw), &payload)

	// Only agent-turn-complete is mapped today; anything else (a future codex
	// notify event type we don't yet understand) fails open silently.
	if payload.Type != "agent-turn-complete" {
		return 0
	}

	url := urlOverride
	if url == "" {
		url = os.Getenv("PALMUX_NOTIFY_URL")
	}
	repoID := os.Getenv("PALMUX_REPO_ID")
	branchID := os.Getenv("PALMUX_BRANCH_ID")
	tabID := os.Getenv("PALMUX_TAB_ID")
	tabName := os.Getenv("PALMUX_TAB_NAME")
	token := os.Getenv("PALMUX_TOKEN")

	if url == "" || repoID == "" || branchID == "" {
		return 0
	}

	body := map[string]any{
		"repoId":    repoID,
		"branchId":  branchID,
		"tabId":     tabID,
		"tabName":   tabName,
		"requestId": hookRequestID(tabID),
		"type":      "claudetui.task_complete",
		"message":   firstNonEmpty(payload.LastAssistantMessage, "Codex finished — your turn"),
	}

	postHookNotification(url, token, body)
	return 0
}

// runOpencodeHook implements the `--agent=opencode` branch of runHook.
// opencode's own notify mechanism is a palmux-authored plugin
// (internal/agent/opencode.go's embedded palmux-notify.js, injected
// per-process via OPENCODE_CONFIG_CONTENT — see OpencodeAdapter.SpawnSpec)
// that spawns `<hookbin> hook --agent=opencode` via Node's child_process and
// writes its JSON payload to that child's STDIN — never argv, since the
// plugin controls the spawn directly rather than going through opencode's
// own CLI-driven notify wiring the way codex's `-c notify=[...]` does.
//
// The plugin drives BOTH of opencode's notify-worthy signals off a single
// bus event — event.type "session.idle" (turn ended) and "permission.asked"
// (a tool call needs approval) — see OpencodeAdapter's type doc comment.
// "session.idle" maps to the same "claudetui.task_complete" shape Claude's
// Stop hook / codex's agent-turn-complete produce; "permission.asked" maps
// to "claudetui.permission_prompt", the same shape Claude's
// Notification/permission_prompt hook produces — both reuse the
// "claudetui.*" type prefix the FE Activity Inbox already recognizes and the
// same "claudetui-hook-<tabId>" RequestID scheme so an Inbox entry updates
// in place rather than piling up.
func runOpencodeHook(urlOverride string) int {
	raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	var payload struct {
		Type      string `json:"type"`
		SessionID string `json:"sessionID"`
		Message   string `json:"message"`
	}
	_ = json.Unmarshal(raw, &payload)

	url := urlOverride
	if url == "" {
		url = os.Getenv("PALMUX_NOTIFY_URL")
	}
	repoID := os.Getenv("PALMUX_REPO_ID")
	branchID := os.Getenv("PALMUX_BRANCH_ID")
	tabID := os.Getenv("PALMUX_TAB_ID")
	tabName := os.Getenv("PALMUX_TAB_NAME")
	token := os.Getenv("PALMUX_TOKEN")

	if url == "" || repoID == "" || branchID == "" {
		return 0
	}

	body := map[string]any{
		"repoId":    repoID,
		"branchId":  branchID,
		"tabId":     tabID,
		"tabName":   tabName,
		"requestId": hookRequestID(tabID),
	}

	switch payload.Type {
	case "session.idle":
		body["type"] = "claudetui.task_complete"
		body["message"] = firstNonEmpty(payload.Message, "opencode finished — your turn")
	case "permission.asked":
		body["type"] = "claudetui.permission_prompt"
		body["message"] = firstNonEmpty(payload.Message, "opencode needs your permission")
	default:
		// A future/unrecognized opencode event type — fail open silently.
		return 0
	}

	postHookNotification(url, token, body)
	return 0
}

// lastNonFlagHookArg returns the last element of args that is neither a
// known flag nor a known flag's VALUE (codex's JSON payload always starts
// with `{`, which never collides with a flag spelling). Value-aware via the
// shared hookFlagsWithValues table: `--url http://x/api/notify` is a single
// flag+value pair, not two independent tokens, so `http://x/api/notify`
// (which doesn't start with `--` either) must never be picked up as the
// payload just because it happens to be the trailing argv element — walking
// forward and tracking "the next token belongs to the flag we just saw" is
// what a naive `!strings.HasPrefix(a, "--")` backward scan cannot do. An
// inline `--agent=<kind>` token and any other unrecognized `--`-prefixed
// flag are also excluded, conservatively, from ever being the payload.
// Empty when no such element exists.
func lastNonFlagHookArg(args []string) string {
	var last string
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if hookFlagsWithValues[a] {
			skipNext = true
			continue
		}
		if strings.HasPrefix(a, "--") {
			continue
		}
		last = a
	}
	return last
}

// hookRequestID is the stable per-tab RequestID for claude-tui hook
// notifications. Reusing one ID per tab makes the notify Hub refresh a single
// inbox entry in place (Notification/Stop) and lets UserPromptSubmit resolve it.
func hookRequestID(tabID string) string {
	return "claudetui-hook-" + tabID
}

// buildHookBody maps a Claude Code hook event to a notify.IngestRequest-shaped
// payload. Returns ok=false for events we ignore.
func buildHookBody(event, repoID, branchID, tabID, tabName, notificationType, message, lastAssistant string) (map[string]any, bool) {
	base := func() map[string]any {
		return map[string]any{
			"repoId":    repoID,
			"branchId":  branchID,
			"tabId":     tabID,
			"tabName":   tabName,
			"requestId": hookRequestID(tabID),
		}
	}

	switch event {
	case "UserPromptSubmit":
		// User replied — dismiss the pending "your turn" notification.
		b := map[string]any{
			"repoId":    repoID,
			"branchId":  branchID,
			"requestId": hookRequestID(tabID),
			"resolve":   true,
		}
		return b, true

	case "Stop":
		b := base()
		b["type"] = "claudetui.task_complete"
		b["message"] = firstNonEmpty(message, lastAssistant, "Claude finished — your turn")
		return b, true

	case "Notification":
		b := base()
		switch notificationType {
		case "permission_prompt":
			b["type"] = "claudetui.permission_prompt"
			b["message"] = firstNonEmpty(message, "Claude needs your permission")
		case "idle_prompt":
			b["type"] = "claudetui.idle"
			b["message"] = firstNonEmpty(message, "Claude is waiting for your input")
		default:
			b["type"] = "claudetui.notification"
			b["message"] = firstNonEmpty(message, "Claude needs your attention")
		}
		return b, true

	default:
		return nil, false
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// postHookNotification fires the notification at the local palmux server. It is
// best-effort: a short timeout, errors swallowed.
func postHookNotification(url, token string, body map[string]any) {
	buf, err := json.Marshal(body)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
