package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
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
func runHook(args []string) int {
	eventOverride, urlOverride := parseHookArgs(args)

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

// parseHookArgs extracts the optional --event and --url overrides. Unknown
// flags are ignored (fail-open).
func parseHookArgs(args []string) (event, url string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--event":
			if i+1 < len(args) {
				event = args[i+1]
				i++
			}
		case "--url":
			if i+1 < len(args) {
				url = args[i+1]
				i++
			}
		}
	}
	return event, url
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
