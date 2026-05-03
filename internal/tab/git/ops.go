// Package git: S012 — review-and-commit flow.
//
// This file extends the basic read/stage operations in git.go with the day-to-day
// write operations that close the gap between "view diffs" and "ship work":
// commit, push, pull, fetch, branch CRUD, and line-range staging.
//
// All operations honour the surrounding shell environment so SSH agent
// (`SSH_AUTH_SOCK`) and HTTPS credential helpers (osxkeychain / libsecret)
// work without configuration. Where we explicitly want to avoid an
// interactive terminal prompt (e.g. detecting a missing credential rather
// than blocking forever) we pass `GIT_TERMINAL_PROMPT=0`.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CommitOptions controls a `git commit`.
type CommitOptions struct {
	Message  string `json:"message"`
	Amend    bool   `json:"amend,omitempty"`
	Signoff  bool   `json:"signoff,omitempty"`
	NoVerify bool   `json:"noVerify,omitempty"`
	// AllowEmpty mirrors `git commit --allow-empty`. Useful for pure
	// merge commits when nothing else is staged.
	AllowEmpty bool `json:"allowEmpty,omitempty"`
}

// CommitResult is the JSON returned by /git/commit.
type CommitResult struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

// Commit runs `git commit` with the requested options. When opts.Amend is
// set and opts.Message is empty, the previous commit message is preserved
// (`--no-edit`).
func Commit(ctx context.Context, repoDir string, opts CommitOptions) (CommitResult, error) {
	args := []string{"commit"}
	if opts.Amend {
		args = append(args, "--amend")
		if strings.TrimSpace(opts.Message) == "" {
			args = append(args, "--no-edit")
		}
	}
	if opts.Signoff {
		args = append(args, "-s")
	}
	if opts.NoVerify {
		args = append(args, "--no-verify")
	}
	if opts.AllowEmpty {
		args = append(args, "--allow-empty")
	}
	if strings.TrimSpace(opts.Message) != "" {
		// Use -F /dev/stdin to avoid argv-length limits and to keep
		// multi-line messages intact. We pipe via stdin below.
		args = append(args, "-F", "-")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	if strings.TrimSpace(opts.Message) != "" {
		cmd.Stdin = strings.NewReader(opts.Message)
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return CommitResult{}, fmt.Errorf("git commit: %s", strings.TrimSpace(stderr.String()))
	}
	// Pick up the new HEAD.
	out, err := runGit(ctx, repoDir, "log", "-1", "--pretty=format:%H%x09%s")
	if err != nil {
		return CommitResult{}, err
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	res := CommitResult{}
	if len(parts) >= 1 {
		res.Hash = parts[0]
	}
	if len(parts) >= 2 {
		res.Subject = parts[1]
	}
	return res, nil
}

// HeadCommitMessage returns the full message of HEAD. Used to prefill the
// amend form on the FE so the user sees what they're rewriting.
func HeadCommitMessage(ctx context.Context, repoDir string) (string, error) {
	out, err := runGit(ctx, repoDir, "log", "-1", "--pretty=format:%B")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// PushOptions controls `git push`.
type PushOptions struct {
	Remote         string `json:"remote,omitempty"`         // defaults to "origin"
	Branch         string `json:"branch,omitempty"`         // defaults to current
	SetUpstream    bool   `json:"setUpstream,omitempty"`    // -u
	Force          bool   `json:"force,omitempty"`          // --force (dangerous, FE confirms twice)
	ForceWithLease bool   `json:"forceWithLease,omitempty"` // --force-with-lease (preferred over --force)
	Tags           bool   `json:"tags,omitempty"`
}

// Push runs `git push` with the requested options. Returns combined
// stdout+stderr so the FE can show progress info.
func Push(ctx context.Context, repoDir string, opts PushOptions) (string, error) {
	args := []string{"push"}
	if opts.SetUpstream {
		args = append(args, "-u")
	}
	if opts.ForceWithLease {
		args = append(args, "--force-with-lease")
	} else if opts.Force {
		args = append(args, "--force")
	}
	if opts.Tags {
		args = append(args, "--tags")
	}
	remote := strings.TrimSpace(opts.Remote)
	if remote == "" {
		remote = "origin"
	}
	args = append(args, remote)
	if br := strings.TrimSpace(opts.Branch); br != "" {
		args = append(args, br)
	}
	return runGitCombined(ctx, repoDir, args...)
}

// PullOptions controls `git pull`.
type PullOptions struct {
	Remote  string `json:"remote,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Rebase  bool   `json:"rebase,omitempty"`
	FFOnly  bool   `json:"ffOnly,omitempty"`
	NoCommit bool  `json:"noCommit,omitempty"`
}

// Pull runs `git pull`.
func Pull(ctx context.Context, repoDir string, opts PullOptions) (string, error) {
	args := []string{"pull"}
	switch {
	case opts.Rebase:
		args = append(args, "--rebase")
	case opts.FFOnly:
		args = append(args, "--ff-only")
	}
	if opts.NoCommit {
		args = append(args, "--no-commit")
	}
	if r := strings.TrimSpace(opts.Remote); r != "" {
		args = append(args, r)
		if b := strings.TrimSpace(opts.Branch); b != "" {
			args = append(args, b)
		}
	}
	return runGitCombined(ctx, repoDir, args...)
}

// FetchOptions controls `git fetch`.
type FetchOptions struct {
	Remote string `json:"remote,omitempty"`
	Prune  bool   `json:"prune,omitempty"`
	All    bool   `json:"all,omitempty"`
}

// Fetch runs `git fetch`.
func Fetch(ctx context.Context, repoDir string, opts FetchOptions) (string, error) {
	args := []string{"fetch"}
	if opts.All {
		args = append(args, "--all")
	}
	if opts.Prune {
		args = append(args, "--prune")
	}
	if r := strings.TrimSpace(opts.Remote); r != "" && !opts.All {
		args = append(args, r)
	}
	return runGitCombined(ctx, repoDir, args...)
}

// CreateBranchOptions controls `git branch <name> [<start>]` or `switch -c`.
type CreateBranchOptions struct {
	Name      string `json:"name"`
	StartFrom string `json:"startFrom,omitempty"` // sha or ref; empty = current HEAD
	Checkout  bool   `json:"checkout,omitempty"`  // also switch to it
}

// CreateBranch creates a new local branch.
func CreateBranch(ctx context.Context, repoDir string, opts CreateBranchOptions) error {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return errors.New("branch name required")
	}
	if opts.Checkout {
		args := []string{"switch", "-c", name}
		if opts.StartFrom != "" {
			args = append(args, opts.StartFrom)
		}
		_, err := runGit(ctx, repoDir, args...)
		return err
	}
	args := []string{"branch", name}
	if opts.StartFrom != "" {
		args = append(args, opts.StartFrom)
	}
	_, err := runGit(ctx, repoDir, args...)
	return err
}

// SwitchBranch checks out an existing branch via `git switch`.
func SwitchBranch(ctx context.Context, repoDir, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("branch name required")
	}
	_, err := runGit(ctx, repoDir, "switch", name)
	return err
}

// DeleteBranchOptions controls `git branch -d / -D`.
type DeleteBranchOptions struct {
	Name  string `json:"name"`
	Force bool   `json:"force,omitempty"` // -D vs -d
}

// DeleteBranch deletes a local branch. With Force=true, uses `-D` which
// allows deleting an unmerged branch.
func DeleteBranch(ctx context.Context, repoDir string, opts DeleteBranchOptions) error {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return errors.New("branch name required")
	}
	flag := "-d"
	if opts.Force {
		flag = "-D"
	}
	_, err := runGit(ctx, repoDir, "branch", flag, name)
	return err
}

// SetUpstreamOptions controls `git branch --set-upstream-to=<upstream> <branch>`.
type SetUpstreamOptions struct {
	Branch   string `json:"branch"`
	Upstream string `json:"upstream"` // e.g. "origin/main"
}

// SetUpstream wires a tracking branch.
func SetUpstream(ctx context.Context, repoDir string, opts SetUpstreamOptions) error {
	if strings.TrimSpace(opts.Branch) == "" || strings.TrimSpace(opts.Upstream) == "" {
		return errors.New("branch and upstream required")
	}
	_, err := runGit(ctx, repoDir, "branch", "--set-upstream-to="+opts.Upstream, opts.Branch)
	return err
}

// runGitCombined runs git with a merged stdout+stderr stream (useful for
// push/pull/fetch where the user wants to see progress text).
//
// `GIT_TERMINAL_PROMPT=0` is set so a missing credential helper fails fast
// with a recognisable error rather than blocking on tty input the server
// can't see. Detected credential failures are wrapped in
// ErrCredentialRequired so the HTTP handler can map them to a 401 + a
// `git.credentialRequest` WS event for the FE dialog (S012-1-7).
//
// SSH agent (`SSH_AUTH_SOCK`) and system credential helpers
// (osxkeychain / libsecret etc.) are inherited via os.Environ() so the
// server picks up whatever the user's shell session already provides.
func runGitCombined(ctx context.Context, repoDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		body := combined.String()
		if isCredentialError(body) {
			return body, fmt.Errorf("%w: %s", ErrCredentialRequired, strings.TrimSpace(firstLine(body)))
		}
		return body, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(body))
	}
	return combined.String(), nil
}

// ErrCredentialRequired is returned by Push/Pull/Fetch when git fails for
// a reason the FE can recover from by prompting the user (e.g. missing
// HTTPS credentials when GIT_TERMINAL_PROMPT=0 prevented git from asking
// directly).
var ErrCredentialRequired = errors.New("git: credential required")

func isCredentialError(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, needle := range []string{
		"terminal prompts disabled",
		"could not read username",
		"could not read password",
		"authentication failed",
		"permission denied (publickey)",
		"host key verification failed",
		"fatal: authentication",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

// === Line-range staging (S012-1-4) =========================================

// LineRange is a 1-based inclusive line range in the *new* (working tree)
// version of a file.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// StageLines stages only the lines in `ranges` of `path`. We do this by
// rebuilding the unified diff with hunks restricted to those ranges and
// piping it through `git apply --cached`. Lines outside any range stay
// unstaged.
//
// Implementation note: we reuse the existing diff parser (so we can
// match the exact patch format `git apply` expects), filter each hunk's
// "+"/" " lines, and emit a synthesized hunk whose @@ header reflects
// the post-filter counts.
func StageLines(ctx context.Context, repoDir, path string, ranges []LineRange) error {
	if path == "" {
		return errors.New("path required")
	}
	if len(ranges) == 0 {
		return errors.New("at least one range required")
	}
	raw, err := RawDiff(ctx, repoDir, DiffWorking, path)
	if err != nil {
		return err
	}
	files := ParseUnifiedDiff(raw)
	if len(files) == 0 {
		return errors.New("no working-tree changes for " + path)
	}
	patch := buildLineRangePatch(files[0], ranges)
	if patch == "" {
		return errors.New("selection covers no changed lines")
	}
	return ApplyHunk(ctx, repoDir, patch, true /*cached*/, false /*reverse*/)
}

// buildLineRangePatch constructs a unified diff containing one hunk per
// region of consecutive added/removed lines that fall inside `ranges`.
// Lines outside the range are preserved as context so the patch applies
// cleanly. Pure-deletion regions are kept whenever any of their context
// neighbours fall inside the range.
func buildLineRangePatch(f DiffFile, ranges []LineRange) string {
	// Resolve ranges into a quick "is this new-file line in range?" check.
	inRange := func(line int) bool {
		for _, r := range ranges {
			if line >= r.Start && line <= r.End {
				return true
			}
		}
		return false
	}

	var hunks []DiffHunk
	for _, h := range f.Hunks {
		newLine := h.NewStart
		var kept []DiffLine
		any := false
		for _, ln := range h.Lines {
			switch ln.Kind {
			case "add":
				if inRange(newLine) {
					kept = append(kept, ln)
					any = true
				} else {
					// Drop the addition: rewrite as context using
					// the *old* version (i.e. skip this hunk-line
					// entirely; git apply will not stage it).
				}
				newLine++
			case "del":
				// Deletion: keep if the surrounding new-line index is
				// inside the range. Use newLine (which has not yet
				// advanced for this old-only line) as the anchor.
				if inRange(newLine) || inRange(newLine-1) {
					kept = append(kept, ln)
					any = true
				}
			case "context":
				kept = append(kept, ln)
				newLine++
			case "meta":
				kept = append(kept, ln)
			}
		}
		if !any {
			continue
		}
		// Recompute counts from the kept lines.
		oldCount, newCount := 0, 0
		for _, ln := range kept {
			switch ln.Kind {
			case "add":
				newCount++
			case "del":
				oldCount++
			case "context":
				oldCount++
				newCount++
			}
		}
		// Trim trailing pure-context lines that bridged into removed
		// additions: the @@ counts above are already correct, but we
		// also need to strip leading/trailing context that ends up
		// orphaned. We keep them — `git apply` is happy with extra
		// context as long as the counts match.

		newH := DiffHunk{
			Header:   fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, oldCount, h.NewStart, newCount),
			Lines:    kept,
			OldStart: h.OldStart,
			OldCount: oldCount,
			NewStart: h.NewStart,
			NewCount: newCount,
		}
		hunks = append(hunks, newH)
	}
	if len(hunks) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(f.Header)
	for _, h := range hunks {
		sb.WriteString(h.Header)
		sb.WriteByte('\n')
		for _, ln := range h.Lines {
			switch ln.Kind {
			case "add":
				sb.WriteByte('+')
			case "del":
				sb.WriteByte('-')
			case "context":
				sb.WriteByte(' ')
			case "meta":
				sb.WriteString(ln.Text)
				sb.WriteByte('\n')
				continue
			}
			sb.WriteString(ln.Text)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

