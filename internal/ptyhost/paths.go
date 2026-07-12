package ptyhost

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// RunDir returns the directory palmux2-side clients should place ptyhost
// sockets/status files in for a given instancePrefix (see
// docs/no-halt-agent-design.md §3): normally
// "$XDG_RUNTIME_DIR/palmux/<instancePrefix>/", falling back to a
// subdirectory of os.TempDir() when XDG_RUNTIME_DIR is unset.
//
// The TempDir fallback is intentionally NOT scoped to survive a host
// reboot — ptyhost survival is explicitly scoped to palmux2 process
// restarts, not host reboots (a non-goal per the design doc), so an
// ephemeral-but-boot-stable location is sufficient and avoids requiring
// every caller to plumb a --config-dir through just for this.
//
// instancePrefix is sanitized the same way [ScopeUnitName] sanitizes it
// (leading/trailing underscores trimmed, empty falls back to "palmux") so
// the run directory and the systemd scope unit name agree on instance
// identity.
func RunDir(instancePrefix string) string {
	prefix := sanitizeInstancePrefix(instancePrefix)
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "palmux", prefix)
	}
	return filepath.Join(os.TempDir(), "palmux-ptyhost", prefix)
}

// FileKey derives a short, filesystem/AF_UNIX-safe, deterministic filename
// component from an arbitrary-length seed string (e.g.
// "<repoId>__<branchId>__<tabId>", per docs/no-halt-agent-design.md §3).
//
// AF_UNIX socket paths are capped at ~108 bytes on Linux (sun_path). A
// literal repoId/branchId/tabId seed can exceed that once joined under
// RunDir — Repository/Workspace IDs are normally short slugs, but tab IDs
// like "claude:claude" and, especially, test/CI-generated repo IDs
// (timestamp+pid-suffixed) can push the joined path well past the limit,
// which fails at listen() with "bind: invalid argument" — a real bug this
// hashing fixes, not just a test-hygiene one. The hash is stable across
// calls with the same seed, which is required: it's how a restarted
// palmux2's freshly-reconstructed Daemon rediscovers the SAME ptyhost socket
// path a surviving ptyhost is still listening on (see [Daemon.launchAndAttach]
// equivalent — the reconnect-or-spawn decision in internal/tab/claudetui).
func FileKey(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(h[:])[:20]
}

// SocketPath and StatusPath return the deterministic socket/status file
// paths for a given run directory (see [RunDir]) and seed (see [FileKey]).
func SocketPath(runDir, seed string) string {
	return filepath.Join(runDir, FileKey(seed)+".sock")
}

func StatusPath(runDir, seed string) string {
	return filepath.Join(runDir, FileKey(seed)+".json")
}
