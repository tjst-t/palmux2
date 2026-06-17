package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ghRelease is the subset of the GitHub Releases API response we consume. Its
// shape mirrors cmd/palmux/runtime.go's ghRelease (priority_rule 6).
type ghRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// LatestTag queries the GitHub Releases API for the newest stable release tag
// of owner/repo (e.g. "tjst-t/palmux2"). It is GITHUB_TOKEN-aware to avoid the
// unauthenticated 60 req/h rate limit (decisions PD-2). Returns the tag name
// (e.g. "v0.11.0").
//
// The HTTP/auth/header plumbing is the same shape as
// cmd/palmux/runtime.go:latestReleaseAssetURL — reused rather than reinvented.
func LatestTag(ctx context.Context, repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", fmt.Errorf("empty repo")
	}
	endpoint := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		// Rate-limited. Surface a typed error so the poller can graceful-degrade
		// (decisions PD-3) without flapping the badge.
		return "", &RateLimitError{Status: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("GitHub API returned %d for %s: %s", resp.StatusCode, repo, string(body))
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("decode release JSON: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return "", fmt.Errorf("no tag in latest release of %s", repo)
	}
	return strings.TrimSpace(rel.TagName), nil
}

// RateLimitError marks a GitHub rate-limit / forbidden response so callers can
// graceful-degrade (keep prior state, log only) instead of clearing the badge.
type RateLimitError struct{ Status int }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("GitHub rate-limited (HTTP %d); set GITHUB_TOKEN for higher limits", e.Status)
}
