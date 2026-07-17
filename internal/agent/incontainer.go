package agent

import (
	"os"
	"os/exec"
	"path/filepath"
)

// InContainerProvider is an optional Adapter capability (S339021): an
// adapter that can resolve a HOST binary usable inside a workspace container
// via the same same-path bind-mount approach claude's own binary already
// relies on (containerClaudeBin + the ~/.local/bin shared device — see
// internal/runtime/incus/shared_profile.go) rather than baking the binary
// into the palmux-ws image. Implemented by CodexAdapter/OpencodeAdapter,
// whose binaries live at a system path (e.g. /usr/bin, or an npm-global
// install) that is NOT already covered by the static shared-profile device
// list. GenericAdapter does not implement this — its `container_command` is
// an explicit, user-declared config path (config.toml), not something to
// auto-resolve and auto-share.
type InContainerProvider interface {
	// ContainerBinary resolves the fully-resolved (symlinks followed)
	// absolute host path of the binary that should be exec'd — both when
	// invoked directly on the host, and (once bind-mounted at the identical
	// path by SharedContainerPaths) inside a workspace container. ok is
	// false when the binary cannot be resolved on this host, in which case
	// Capabilities().InContainer must also report false (no silent
	// fallback — see agenttui.Daemon's D12 guard).
	ContainerBinary() (hostPath string, ok bool)
	// SharedContainerPaths returns every additional absolute host path
	// (directory, or a standalone binary file) that must be bind-mounted at
	// the SAME path into every workspace container for this adapter to
	// actually run and authenticate in-container: e.g. the npm package tree
	// a wrapper script's own relative module resolution depends on, auth/
	// credential state directories, and any palmux-generated config the
	// adapter writes via SpawnSpec's PreFiles. Only existing paths are
	// returned — a caller does not need to re-check existence (mirrors
	// internal/runtime/incus/shared_profile.go's declaredDevices()
	// existing-source convention, which also silently skips an absent
	// source as defense-in-depth).
	SharedContainerPaths() []string
}

// resolveHostBinary resolves bin — a bare command name looked up on PATH, or
// an already-absolute path — to its REAL, symlink-fully-resolved absolute
// file path. Returns ("", false) when bin is empty, cannot be found, or
// resolves to something other than a regular file.
func resolveHostBinary(bin string) (string, bool) {
	if bin == "" {
		return "", false
	}
	p := bin
	if !filepath.IsAbs(p) {
		found, err := exec.LookPath(bin)
		if err != nil {
			return "", false
		}
		p = found
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || resolved == "" {
		return "", false
	}
	fi, statErr := os.Stat(resolved)
	if statErr != nil || fi.IsDir() {
		return "", false
	}
	return resolved, true
}

// npmGlobalRoot walks up the ancestors of resolvedBin looking for a
// directory literally named "node_modules". A binary installed as an npm
// global package (e.g. `npm install -g @openai/codex`) commonly ships as a
// thin wrapper whose own relative module resolution depends on the REST of
// that package tree being present at the same relative layout — bind-
// mounting only the leaf binary file breaks it (verified live: codex's
// wrapper throws "Missing optional dependency @openai/codex-linux-x64" when
// isolated from its package root, because it require()s a sibling
// optionalDependency package for the real native binary). Sharing the whole
// node_modules root preserves that layout identically in-container.
//
// Returns ("", false) for a binary that isn't npm-installed (e.g. a
// standalone binary at /usr/local/bin or ~/.local/bin) — callers fall back
// to sharing just the binary file itself in that case.
func npmGlobalRoot(resolvedBin string) (string, bool) {
	dir := filepath.Dir(resolvedBin)
	for {
		if filepath.Base(dir) == "node_modules" {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// containerSharesForBinary is the common "how do I share this resolved
// binary into a container" policy used by both CodexAdapter and
// OpencodeAdapter's SharedContainerPaths.
//
//   - npm-global install (a "node_modules/…" ancestor): share the whole
//     package tree AND the node runtime the wrapper's `#!/usr/bin/env node`
//     shebang needs. A bare workspace image has no node, so sharing only the
//     package tree makes the wrapper exit 127 (command not found). Verified
//     live: codex is a Node script and dies with status 127 in-container when
//     node is not also shared.
//   - standalone self-contained executable (bun/go/rust single-file build,
//     e.g. opencode.exe is a bun single-exe ELF): the binary alone suffices.
//
// resolver resolves a bare command name to its real host path (the adapter's
// binResolver, defaulting to resolveHostBinary) — used here to locate the node
// runtime; injectable so tests stay host-independent.
func containerSharesForBinary(resolvedBin string, resolver func(string) (string, bool)) []string {
	if root, ok := npmGlobalRoot(resolvedBin); ok {
		shares := []string{root}
		if resolver != nil {
			if node, ok := resolver("node"); ok {
				shares = append(shares, node)
			}
		}
		return shares
	}
	return []string{resolvedBin}
}

// existingDir returns p if it exists and is a directory, else "".
func existingDir(p string) string {
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return p
	}
	return ""
}
