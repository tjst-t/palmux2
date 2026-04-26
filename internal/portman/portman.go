// Package portman is a thin wrapper around the `portman list --json` CLI.
// We use it to surface published service URLs in the Palmux UI for the
// currently focused repo / branch.
package portman

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
)

// Lease is one entry from `portman list --json`.
type Lease struct {
	Name     string `json:"name"`
	Project  string `json:"project"`
	Worktree string `json:"worktree"`
	Port     int    `json:"port"`
	Hostname string `json:"hostname"`
	Expose   bool   `json:"expose"`
	Status   string `json:"status"`
	URL      string `json:"url"`
}

// Client runs portman to enumerate active leases.
type Client struct {
	bin string
}

// New returns a Client. `bin` is the binary path; "" → look up "portman"
// on $PATH.
func New(bin string) *Client {
	if bin == "" {
		bin = "portman"
	}
	return &Client{bin: bin}
}

// Available reports whether the configured binary is on PATH (or, if an
// absolute path was given, that it exists). Used to skip the API entirely
// in environments without portman installed.
func (c *Client) Available() bool {
	_, err := exec.LookPath(c.bin)
	return err == nil
}

// List returns every lease portman knows about. Errors from the CLI bubble
// up to the caller as-is.
func (c *Client) List(ctx context.Context) ([]Lease, error) {
	if !c.Available() {
		return nil, errors.New("portman: binary not found on PATH")
	}
	cmd := exec.CommandContext(ctx, c.bin, "list", "--json")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var leases []Lease
	if err := json.Unmarshal(out, &leases); err != nil {
		return nil, err
	}
	return leases, nil
}

// ForRepo filters the lease list by project + worktree. We accept any
// project string the portman CLI emits — typically `<owner>/<repo>` for a
// ghq-rooted layout — and match it against the repo's ghq path with the
// `github.com/` host prefix stripped.
//
// Pass `worktree == ""` to match every worktree.
func ForRepo(leases []Lease, ghqPath, worktree string) []Lease {
	repoKey := stripHostPrefix(ghqPath)
	out := make([]Lease, 0, 4)
	for _, l := range leases {
		if l.Project != repoKey {
			continue
		}
		if worktree != "" && l.Worktree != worktree {
			continue
		}
		out = append(out, l)
	}
	return out
}

func stripHostPrefix(ghqPath string) string {
	// "github.com/owner/repo" → "owner/repo"
	if i := strings.Index(ghqPath, "/"); i > 0 && strings.Contains(ghqPath[:i], ".") {
		return ghqPath[i+1:]
	}
	return ghqPath
}
