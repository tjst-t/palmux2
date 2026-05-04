package server

// handler_remote_url.go — S031-5
//
// GET /api/repos/{repoId}/branches/{branchId}/remote-url
//
// Returns the GitHub (or any remote) URL for the branch so the FE can
// open it in a browser tab via `> open on GitHub` builtin command.

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
)

type remoteURLResponse struct {
	URL string `json:"url"`
}

func (h *handlers) remoteURL(w http.ResponseWriter, r *http.Request) {
	branch, err := h.store.Branch(r.PathValue("repoId"), r.PathValue("branchId"))
	if err != nil {
		writeErr(w, err)
		return
	}

	rawURL, err := gitRemoteURL(r.Context(), branch.WorktreePath)
	if err != nil {
		// Not an error from the API's perspective — just return empty URL.
		writeJSON(w, http.StatusOK, remoteURLResponse{URL: ""})
		return
	}

	// Convert SSH remote to HTTPS and append branch path.
	webURL := normaliseRemoteURL(rawURL, branch.Name)
	writeJSON(w, http.StatusOK, remoteURLResponse{URL: webURL})
}

// gitRemoteURL runs `git remote get-url origin` (or falls back to
// `git remote get-url upstream`) in the given directory.
func gitRemoteURL(ctx context.Context, dir string) (string, error) {
	for _, remote := range []string{"origin", "upstream"} {
		out, err := runGit(ctx, dir, "remote", "get-url", remote)
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), nil
		}
	}
	return "", fmt.Errorf("remote-url: no origin/upstream remote found")
}

// runGit runs a git command in dir and returns stdout.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// normaliseRemoteURL converts a raw git remote URL (SSH or HTTPS) to a
// GitHub-style https web URL pointing at the branch tree view.
//
// Examples:
//
//	https://github.com/owner/repo.git    →  https://github.com/owner/repo/tree/<branch>
//	git@github.com:owner/repo.git        →  https://github.com/owner/repo/tree/<branch>
//	ssh://git@github.com/owner/repo.git  →  https://github.com/owner/repo/tree/<branch>
func normaliseRemoteURL(rawURL, branchName string) string {
	u := strings.TrimSpace(rawURL)

	// ssh:// URL: ssh://git@github.com/owner/repo.git  →  https://github.com/owner/repo
	if strings.HasPrefix(u, "ssh://") {
		u = strings.TrimPrefix(u, "ssh://")
		// Remove optional user@ prefix (e.g. git@)
		if atIdx := strings.Index(u, "@"); atIdx >= 0 {
			u = u[atIdx+1:]
		}
		u = "https://" + u
	}

	// SCP-style: git@github.com:owner/repo.git  →  https://github.com/owner/repo
	if strings.HasPrefix(u, "git@") {
		u = strings.TrimPrefix(u, "git@")
		colonIdx := strings.Index(u, ":")
		if colonIdx >= 0 {
			host := u[:colonIdx]
			rest := u[colonIdx+1:]
			u = "https://" + host + "/" + rest
		}
	}

	// Strip .git suffix
	u = strings.TrimSuffix(u, ".git")

	// Append tree/<branch>
	if branchName != "" {
		u = u + "/tree/" + branchName
	}

	return u
}
