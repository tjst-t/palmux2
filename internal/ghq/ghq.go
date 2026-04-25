// Package ghq wraps the `ghq` CLI tool.
package ghq

import (
	"bytes"
	"context"
	"fmt"
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
