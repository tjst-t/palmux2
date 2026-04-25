package domain

import (
	"fmt"
	"strings"
)

// PalmuxSessionPrefix is the prefix that identifies tmux sessions managed by
// Palmux. Anything *without* this prefix is treated as an "Orphan Session"
// (compat mode). Changing this prefix breaks orphan detection for existing
// installs — do not change without a migration story.
const PalmuxSessionPrefix = "_palmux_"

// SessionGroupSeparator separates a session name from the connection-group
// suffix used while a client is attached: `_palmux_..._main--abcd__grp_xyz`.
const SessionGroupSeparator = "__grp_"

// WindowPrefix is the prefix for every tmux window managed by Palmux.
const WindowPrefix = "palmux:"

// SessionName returns the canonical tmux session name for a (repoID, branchID)
// pair: `_palmux_{repoID}_{branchID}`.
func SessionName(repoID, branchID string) string {
	return PalmuxSessionPrefix + repoID + "_" + branchID
}

// ParseSessionName extracts (repoID, branchID) from a session name. Returns
// ok=false for sessions that don't match the Palmux prefix.
func ParseSessionName(name string) (repoID, branchID string, ok bool) {
	if !strings.HasPrefix(name, PalmuxSessionPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, PalmuxSessionPrefix)
	// Strip an optional connection-group suffix.
	if idx := strings.Index(rest, SessionGroupSeparator); idx >= 0 {
		rest = rest[:idx]
	}
	idx := strings.Index(rest, "_")
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

// IsPalmuxSession reports whether the tmux session is managed by Palmux.
func IsPalmuxSession(name string) bool {
	return strings.HasPrefix(name, PalmuxSessionPrefix)
}

// GroupSessionName returns the per-connection tmux session name used when a
// client attaches: `{base}__grp_{connID}`.
func GroupSessionName(baseSession, connID string) string {
	return baseSession + SessionGroupSeparator + connID
}

// WindowName returns the canonical tmux window name for a (type, name) pair:
// `palmux:{type}:{name}`. For terminal singletons (Claude), pass type==name.
func WindowName(tabType, name string) string {
	if name == "" {
		name = tabType
	}
	return WindowPrefix + tabType + ":" + name
}

// ParseWindowName extracts (type, name) from a Palmux window name. Returns
// ok=false for windows that don't match the prefix.
func ParseWindowName(window string) (tabType, name string, ok bool) {
	if !strings.HasPrefix(window, WindowPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(window, WindowPrefix)
	idx := strings.Index(rest, ":")
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

// NextBashWindowName picks the next available `palmux:bash:bash[-N]` name
// given the set of already-existing window names.
func NextBashWindowName(existing map[string]bool) string {
	if !existing[WindowName("bash", "bash")] {
		return WindowName("bash", "bash")
	}
	for i := 2; i < 1_000_000; i++ {
		name := WindowName("bash", fmt.Sprintf("bash-%d", i))
		if !existing[name] {
			return name
		}
	}
	// Practically unreachable.
	return WindowName("bash", "overflow")
}
