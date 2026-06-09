// Package incus — Caddy integration for incus-container port routing.
//
// For each detected listening port in a container, palmux writes a per-port
// Caddyfile snippet in /etc/caddy/conf.d/<inst>-<port>.caddy that reverse-
// proxies a per-workspace subdomain (<inst>.palmux.local:<port>) to
// <containerIP>:<port>.  After writing the snippet, `caddy reload` is run.
//
// Design (docs/workspace-runtime-design.md §5.2):
//   - No host-side port is consumed (bridge-direct path, containerIP is
//     reachable from the host via incusbr0).
//   - If caddy binary is not on PATH the write is skipped (graceful degrade)
//     and the caller surfaces <containerIP>:<port> instead.
//   - caddy reload is idempotent; stale snippets from crashed sessions are
//     cleaned up by removeSnippet / clearSnippets.
//
// The runner dependency is injectable so tests can assert exact caddy args
// without touching the filesystem or running a real Caddy.
package incus

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// CaddyConfDir is the directory where palmux writes per-workspace Caddy
	// snippets.  This must match the glob in the main Caddyfile.
	CaddyConfDir = "/etc/caddy/conf.d"

	// CaddyfileDefault is the path of the main Caddyfile that palmux ensures
	// exists (includes conf.d/*.caddy).
	CaddyfileDefault = "/etc/caddy/Caddyfile"

	// caddyfileContent is the minimal Caddyfile written by palmux when it
	// does not already exist.  It imports every snippet in conf.d.
	caddyfileContent = "# Palmux managed — imports workspace snippets\nimport conf.d/*.caddy\n"
)

// caddyRunner is an injectable function for running the caddy binary.
// Signature mirrors runner: (ctx, args...) → stdout, stderr, code, err.
type caddyRunner func(ctx context.Context, args ...string) (stdout, stderr string, code int, err error)

// defaultCaddyRunner executes `caddy <args>` via exec.CommandContext.
func defaultCaddyRunner(ctx context.Context, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, "caddy", args...) //nolint:gosec
	var so, se strings.Builder
	cmd.Stdout = &so
	cmd.Stderr = &se
	if err := cmd.Run(); err != nil {
		if xe, ok := err.(*exec.ExitError); ok {
			return so.String(), se.String(), xe.ExitCode(), nil
		}
		return so.String(), se.String(), -1, err
	}
	return so.String(), se.String(), 0, nil
}

// caddyAvailable returns true if caddy is on PATH.
func caddyAvailable() bool {
	_, err := exec.LookPath("caddy")
	return err == nil
}

// snippetPath returns the path for the per-workspace-port snippet.
// Example: /etc/caddy/conf.d/ws-foo-ab12-5173.caddy
func snippetPath(instName string, port int) string {
	name := fmt.Sprintf("%s-%d.caddy", instName, port)
	return filepath.Join(CaddyConfDir, name)
}

// caddySubdomain returns the virtual host label used in the snippet.
// Format: <instName>-<port>.palmux.local  (plain HTTP — no TLS for local dev)
func caddySubdomain(instName string, port int) string {
	return fmt.Sprintf("http://%s-%d.palmux.local", instName, port)
}

// ensureCaddyfile creates the main Caddyfile if it does not exist.
// This is called once before the first snippet write.
func ensureCaddyfile() error {
	if _, err := os.Stat(CaddyfileDefault); err == nil {
		return nil // already exists
	}
	if err := os.MkdirAll(CaddyConfDir, 0o755); err != nil {
		return fmt.Errorf("caddy: mkdir conf.d: %w", err)
	}
	return os.WriteFile(CaddyfileDefault, []byte(caddyfileContent), 0o644)
}

// writeSnippet writes a Caddy reverse_proxy snippet for containerIP:port
// and runs caddy reload.  If caddy is not on PATH it logs a warning and
// returns the direct address instead of an error.
//
// Returns the public URL written to the snippet (e.g.
// "http://<instName>-<port>.palmux.local") or "" on graceful degrade.
//
// [AC-S8478ca-4-2]
func writeSnippet(
	ctx context.Context,
	instName, containerIP string,
	port int,
	run caddyRunner,
	log *slog.Logger,
) (publicURL string, err error) {
	if run == nil {
		run = defaultCaddyRunner
	}
	if !caddyAvailable() {
		log.Warn("caddy: not on PATH — skipping snippet write; serve via direct IP",
			"inst", instName, "addr", fmt.Sprintf("%s:%d", containerIP, port))
		return "", nil
	}

	if err := os.MkdirAll(CaddyConfDir, 0o755); err != nil {
		return "", fmt.Errorf("caddy: mkdir %s: %w", CaddyConfDir, err)
	}

	if err := ensureCaddyfile(); err != nil {
		return "", fmt.Errorf("caddy: ensure Caddyfile: %w", err)
	}

	vhost := caddySubdomain(instName, port)
	snippet := fmt.Sprintf(
		"# palmux workspace %s port %d — auto-generated, do not edit\n%s {\n\treverse_proxy %s:%d\n}\n",
		instName, port, vhost, containerIP, port,
	)

	path := snippetPath(instName, port)
	if writeErr := os.WriteFile(path, []byte(snippet), 0o644); writeErr != nil {
		return "", fmt.Errorf("caddy: write snippet %s: %w", path, writeErr)
	}
	log.Debug("caddy: wrote snippet", "path", path, "vhost", vhost)

	// caddy reload — gracefully reload with the updated config.
	// Use --config so caddy picks up CaddyfileDefault even if it wasn't
	// launched from /etc/caddy.
	_, stderr, code, runErr := run(ctx, "reload", "--config", CaddyfileDefault)
	if runErr != nil {
		log.Warn("caddy: reload OS error", "err", runErr, "inst", instName, "port", port)
	} else if code != 0 {
		// caddy may not be running yet; try starting it.
		log.Info("caddy: reload returned non-zero, attempting caddy start", "code", code, "stderr", stderr)
		_, startStderr, startCode, startErr := run(ctx, "start", "--config", CaddyfileDefault)
		if startErr != nil || startCode != 0 {
			log.Warn("caddy: start also failed — snippet written but caddy not running",
				"startCode", startCode, "startStderr", startStderr, "startErr", startErr,
				"inst", instName, "port", port)
			// Non-fatal: snippet is on disk; caddy will pick it up when (re)started.
		} else {
			log.Info("caddy: started", "inst", instName)
		}
	} else {
		log.Info("caddy: reloaded", "inst", instName, "port", port)
	}

	return vhost, nil
}

// removeSnippet deletes the snippet file for the given instance+port and
// reloads Caddy.  Idempotent — no error if the file does not exist.
//
// [AC-S8478ca-4-2]
func removeSnippet(
	ctx context.Context,
	instName string,
	port int,
	run caddyRunner,
	log *slog.Logger,
) {
	path := snippetPath(instName, port)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Warn("caddy: remove snippet", "path", path, "err", err)
	}
	if !caddyAvailable() {
		return
	}
	if run == nil {
		run = defaultCaddyRunner
	}
	_, _, _, _ = run(ctx, "reload", "--config", CaddyfileDefault)
}

// clearSnippets removes all conf.d/*.caddy files matching the given instName
// prefix and reloads Caddy.  Called on container stop.
//
// [AC-S8478ca-4-2]
func clearSnippets(ctx context.Context, instName string, run caddyRunner, log *slog.Logger) {
	pattern := filepath.Join(CaddyConfDir, instName+"-*.caddy")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		log.Warn("caddy: glob snippets", "pattern", pattern, "err", err)
		return
	}
	removed := 0
	for _, m := range matches {
		if removeErr := os.Remove(m); removeErr != nil && !os.IsNotExist(removeErr) {
			log.Warn("caddy: remove snippet", "path", m, "err", removeErr)
		} else {
			removed++
		}
	}
	if removed == 0 || !caddyAvailable() {
		return
	}
	if run == nil {
		run = defaultCaddyRunner
	}
	_, _, _, _ = run(ctx, "reload", "--config", CaddyfileDefault)
	log.Info("caddy: cleared snippets and reloaded", "inst", instName, "count", removed)
}
