// Package ghq wraps the `ghq` CLI tool.
package ghq

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Client wraps a `ghq` binary on PATH.
type Client struct {
	bin string
}

// New returns a Client.
func New() *Client { return &Client{bin: "ghq"} }

// Repository is one entry from `ghq list`.
type Repository struct {
	GHQPath  string // "github.com/tjst-t/palmux"
	FullPath string // absolute path on disk
}

// Root returns the absolute path of the ghq root directory.
func (c *Client) Root(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, c.bin, "root").Output()
	if err != nil {
		return "", fmt.Errorf("ghq root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Get clones a repository using `ghq get <url>`. Both stdout and stderr are
// captured; on failure the stderr text is returned as the error message so the
// caller can surface it to the user verbatim. The ctx must remain alive for the
// duration of the clone — cancelling it kills the subprocess.
func (c *Client) Get(ctx context.Context, url string) (string, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return "", fmt.Errorf("ghq.Get: empty url")
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin, "get", url)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		se := strings.TrimSpace(stderr.String())
		if se == "" {
			se = err.Error()
		}
		return "", fmt.Errorf("%s\nghq exit code: %d", se, cmd.ProcessState.ExitCode())
	}
	// ghq get outputs the destination path on stdout in recent versions.
	// If not, derive it from `ghq root` + normalised url.
	dest := strings.TrimSpace(stdout.String())
	if dest == "" {
		root, rerr := c.Root(ctx)
		if rerr != nil {
			return "", nil // best-effort — let caller use `ghq list` to discover
		}
		dest = filepath.Join(root, ghqPathFromURL(url))
	}
	return dest, nil
}

// Rm removes a repository directory managed by ghq.
// It first tries `ghq rm <ghqPath>`; if ghq does not support the rm subcommand
// (older versions), it falls back to os.RemoveAll(fullPath).
func (c *Client) Rm(ctx context.Context, ghqPath, fullPath string) error {
	ghqPath = strings.TrimSpace(ghqPath)
	fullPath = strings.TrimSpace(fullPath)
	if ghqPath == "" && fullPath == "" {
		return fmt.Errorf("ghq.Rm: both ghqPath and fullPath are empty")
	}

	// Attempt `ghq rm <path>` — available in ghq >= 1.x.
	if ghqPath != "" {
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, c.bin, "rm", "--force", ghqPath)
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil {
			return nil
		}
		// If the error suggests the subcommand is unknown, fall through to os.RemoveAll.
		se := stderr.String()
		if !strings.Contains(se, "unknown command") && !strings.Contains(se, "not a known") &&
			!strings.Contains(se, "flag provided but not defined") {
			// A real error (not "subcommand missing").
			return fmt.Errorf("ghq rm: %s", strings.TrimSpace(se))
		}
	}

	// Fallback: direct filesystem removal.
	if fullPath == "" {
		return fmt.Errorf("ghq.Rm: ghq rm unavailable and fullPath is empty")
	}
	if err := os.RemoveAll(fullPath); err != nil {
		return fmt.Errorf("ghq.Rm os.RemoveAll: %w", err)
	}
	return nil
}

// ghqPathFromURL converts a clone URL into the ghq-relative path
// (host/owner/repo). Handles https://, git@host:owner/repo.git, and
// bare owner/repo shorthands.
func ghqPathFromURL(url string) string {
	url = strings.TrimSuffix(url, ".git")
	// git@github.com:owner/repo  →  github.com/owner/repo
	if strings.HasPrefix(url, "git@") {
		url = strings.TrimPrefix(url, "git@")
		url = strings.Replace(url, ":", "/", 1)
		return url
	}
	// https://github.com/owner/repo  →  github.com/owner/repo
	for _, pf := range []string{"https://", "http://"} {
		if strings.HasPrefix(url, pf) {
			return strings.TrimPrefix(url, pf)
		}
	}
	// owner/repo shorthand  →  github.com/owner/repo
	if parts := strings.Split(url, "/"); len(parts) == 2 {
		return "github.com/" + url
	}
	return url
}

// List returns all repositories tracked by ghq, with their absolute paths
// resolved using `ghq root`.
func (c *Client) List(ctx context.Context) ([]Repository, error) {
	root, err := c.Root(ctx)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, c.bin, "list")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ghq list: %s", strings.TrimSpace(stderr.String()))
	}
	var repos []Repository
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		repos = append(repos, Repository{
			GHQPath:  line,
			FullPath: filepath.Join(root, line),
		})
	}
	return repos, nil
}
