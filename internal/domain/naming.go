package domain

import (
	"fmt"
	"strings"
)

// PalmuxSessionPrefix is the prefix that identifies tmux sessions managed
// by *this* palmux process. Anything *without* this prefix is treated as
// an "Orphan Session" (compat mode).
//
// Default `_palmux_`. Mutable so palmux can be launched with a per-
// instance prefix (`--tmux-prefix=_palmux_dev_`) when several palmux
// processes share one tmux server — e.g. host palmux2 plus a dev palmux2
// in a `gwq add -b dev` worktree. Without isolation each process's
// `sync_tmux` loop would treat the other's `_palmux_*` sessions as
// zombies / "missing" and the user sees Bash terminals oscillate
// between "usable" and "Reconnecting…" on a 5-second cycle (S009-fix-3).
//
// Set ONCE at process start via [Configure] before any goroutine reads
// from it; treat as read-only thereafter.
var PalmuxSessionPrefix = "_palmux_"

// DefaultPalmuxSessionPrefix is the value used by `palmux` when no
// `--tmux-prefix` is given. Public so tests / integration code can refer
// to the wire-default without hardcoding a literal.
const DefaultPalmuxSessionPrefix = "_palmux_"

// Configure sets process-global naming state. Pass an empty `prefix` to
// reset to the default. Validates that the prefix is non-empty and ends
// with `_` so [ParseSessionName] can reliably split <prefix><repo>_<branch>.
//
// Must be called before any other palmux code reads the prefix (i.e. very
// early in main). Calling Configure after sessions have been created with
// a different prefix will silently orphan them — the previous sessions
// stop being recognised as ours.
func Configure(prefix string) {
	if prefix == "" {
		PalmuxSessionPrefix = DefaultPalmuxSessionPrefix
		return
	}
	if !strings.HasSuffix(prefix, "_") {
		prefix += "_"
	}
	PalmuxSessionPrefix = prefix
}

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
// ok=false for sessions that don't match the Palmux prefix or whose
// post-prefix shape doesn't fit `<repoID>_<branchID>` where repoID
// contains the slug+hash separator `--` (S009-fix-3).
//
// The `--` requirement is what keeps the canonical `_palmux_` prefix
// from accidentally claiming an instance-suffixed peer's session: a
// `_palmux_dev_<repoID>_<branchID>` session has post-prefix
// `dev_<repoID>_<branchID>`, whose first underscore-token is `dev` —
// no `--`, so ParseSessionName rejects it. That makes IsPalmuxSession
// return false for the peer's sessions and the host's sync_tmux loop
// stops touching them.
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
	repo := rest[:idx]
	if !strings.Contains(repo, "--") {
		// repoID is always slug+hash (`owner--repo--hash4`), so the
		// first underscore-token MUST contain `--`. Anything else is a
		// peer instance using `_palmux_<word>_…` as its prefix.
		return "", "", false
	}
	return repo, rest[idx+1:], true
}

// IsPalmuxSession reports whether the tmux session is managed by *this*
// Palmux process. It is stricter than a plain `strings.HasPrefix`:
// `_palmux_dev_<repo>_<branch>` is NOT considered a `_palmux_` session
// because its first repoID-shaped segment (`dev`) doesn't contain the
// `--` slug separator. See [ParseSessionName] for the rule.
func IsPalmuxSession(name string) bool {
	if !strings.HasPrefix(name, PalmuxSessionPrefix) {
		return false
	}
	_, _, ok := ParseSessionName(name)
	return ok
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
