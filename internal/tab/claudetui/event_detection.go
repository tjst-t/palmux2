package claudetui

// Package claudetui — event_detection.go
//
// Scans the headless emulator grid for claude-tui-specific patterns and emits
// Activity Inbox notifications via the notify Hub.
//
// # Detection strategy
//
// ## permission_prompt
//
// After each Feed() call, the Daemon calls scanPermissionPrompt(), which reads
// the bottom 8 rows of the emulator grid and matches a regex against each row's
// text content.  The regex targets claude's interactive TUI prompt format.
//
// // MVP: pattern matching against claude's TUI output, will break if claude
// // changes its prompt format.  The brittleness is intentional — we document it
// // here and accept it in exchange for simplicity.
//
// ## task_complete (BEL)
//
// The Daemon's readLoop scans incoming bytes for the ASCII BEL character
// (\x07).  On detection, it emits a TaskCompleteEvent.  A 2-second throttle
// window prevents spam from sessions that BEL repeatedly.
//
// Both events are published via notify.Hub.IngestInternal, reusing the same
// pipeline that claudeagent uses, so the frontend's Activity Inbox receives
// them over the existing /api/events WebSocket.

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tjst-t/palmux2/internal/notify"
)

// permissionPromptRe matches claude's TUI permission prompts.
//
// MVP: pattern matching against claude's TUI output, will break if claude
// changes its prompt format. The regex intentionally accepts slight variations
// (leading box-drawing characters, leading spaces, bare prompt lines).
//
// Patterns observed in claude's TUI:
//   - "Do you want me to ..."
//   - "Allow ...?"
//   - "│ Allow ...?" (box-drawing border on the left)
var permissionPromptRe = regexp.MustCompile(
	`(?i)(Do you want me to|Allow\s+.{1,120}\?)`)

const (
	// permissionScanRows is the number of rows from the bottom of the grid to
	// scan for permission prompts.
	permissionScanRows = 8

	// belThrottleWindow is the minimum interval between successive
	// task_complete events for the same daemon instance.
	belThrottleWindow = 2 * time.Second
)

// rowToString converts a GridRow to a plain string by concatenating cell runes.
func rowToString(row GridRow) string {
	var b strings.Builder
	b.Grow(len(row.Cells))
	for _, c := range row.Cells {
		b.WriteRune(c.Ch)
	}
	return b.String()
}

// scanPermissionPrompt scans the grid rows for a known permission-prompt
// pattern.  It checks the bottom permissionScanRows first (most likely
// location in a real TUI), then falls back to all rows so that simpler
// test binaries that write to the first few rows are also detected.
// Returns the matched question text (trimmed), or "" if no match.
func scanPermissionPrompt(g Grid) string {
	scanRange := func(start, end int) string {
		for y := start; y < end && y < len(g.Lines); y++ {
			// rowToString produces a string with spaces for empty cells.
			// TrimRight("0") removes only trailing null bytes; TrimSpace
			// removes leading/trailing whitespace including carriage returns.
			line := strings.TrimSpace(
				strings.TrimRight(rowToString(g.Lines[y]), "\x00 "),
			)
			if permissionPromptRe.MatchString(line) {
				return line
			}
		}
		return ""
	}

	// Try bottom rows first (real TUI prompts appear there).
	start := g.Rows - permissionScanRows
	if start < 0 {
		start = 0
	}
	if hit := scanRange(start, g.Rows); hit != "" {
		return hit
	}
	// Fall back to all rows (handles simple test binaries).
	return scanRange(0, start)
}

// maybeEmitPermissionPrompt checks the current emulator grid for a permission
// prompt.  If a new (distinct-from-last) prompt is detected, it publishes a
// notification via hub and updates lastQuestion.  Returns the updated question
// string.
func maybeEmitPermissionPrompt(
	hub *notify.Hub,
	repoID, branchID string,
	g Grid,
	lastQuestion string,
) string {
	if hub == nil {
		return lastQuestion
	}
	question := scanPermissionPrompt(g)
	if question == "" || question == lastQuestion {
		return lastQuestion
	}
	// New distinct question detected.
	hub.IngestInternal(repoID, branchID, notify.InternalRequest{
		RequestID: fmt.Sprintf("claudetui-perm-%x", time.Now().UnixNano()),
		Type:      "claudetui.permission_prompt",
		Title:     "claude-tui permission request",
		Message:   question,
	})
	return question
}

// maybeEmitTaskComplete checks whether p contains a BEL (\x07) byte. When it
// does and the throttle window has elapsed since the last emission, it
// publishes a TaskComplete notification and returns the current time.
// Returns the unchanged lastBEL time if no emission occurred.
func maybeEmitTaskComplete(
	hub *notify.Hub,
	repoID, branchID string,
	p []byte,
	lastBEL time.Time,
) time.Time {
	if hub == nil {
		return lastBEL
	}
	found := false
	for _, b := range p {
		if b == 0x07 {
			found = true
			break
		}
	}
	if !found {
		return lastBEL
	}
	now := time.Now()
	if now.Sub(lastBEL) < belThrottleWindow {
		return lastBEL
	}
	hub.IngestInternal(repoID, branchID, notify.InternalRequest{
		RequestID: fmt.Sprintf("claudetui-bel-%x", now.UnixNano()),
		Type:      "claudetui.task_complete",
		Title:     "claude-tui task complete",
		Message:   "Task finished",
	})
	return now
}
