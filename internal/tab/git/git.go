// Package git implements the Git tab — a singleton, protected, REST-backed
// tab that exposes status / log / diff / stage / unstage / discard for the
// branch's worktree.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// FileStatus reflects the porcelain-v1 two-letter status code for one file.
type FileStatus struct {
	Path        string `json:"path"`
	OldPath     string `json:"oldPath,omitempty"` // for renames
	StagedCode  string `json:"stagedCode"`        // first letter of XY (e.g. "M", "A", "D", "R", "?")
	WorkingCode string `json:"workingCode"`       // second letter
}

// StatusReport bundles staged / unstaged / untracked files.
type StatusReport struct {
	Branch    string       `json:"branch"`
	Staged    []FileStatus `json:"staged"`
	Unstaged  []FileStatus `json:"unstaged"`
	Untracked []FileStatus `json:"untracked"`
	Conflicts []FileStatus `json:"conflicts,omitempty"`
}

// Status runs `git status --porcelain=v1 -b -uall` and parses the output.
func Status(ctx context.Context, repoDir string) (StatusReport, error) {
	out, err := runGit(ctx, repoDir, "status", "--porcelain=v1", "-b", "-uall")
	if err != nil {
		return StatusReport{}, err
	}
	return parseStatus(string(out)), nil
}

func parseStatus(s string) StatusReport {
	var rep StatusReport
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			rep.Branch = strings.SplitN(strings.TrimPrefix(line, "## "), "...", 2)[0]
			continue
		}
		if len(line) < 3 {
			continue
		}
		x, y := line[0], line[1]
		path := line[3:]
		oldPath := ""
		// Rename: "R  oldpath -> newpath"
		if x == 'R' || y == 'R' {
			if idx := strings.Index(path, " -> "); idx >= 0 {
				oldPath = path[:idx]
				path = path[idx+len(" -> "):]
			}
		}
		fs := FileStatus{Path: path, OldPath: oldPath, StagedCode: string(x), WorkingCode: string(y)}
		switch {
		case x == 'U' || y == 'U' || (x == 'D' && y == 'D') || (x == 'A' && y == 'A'):
			rep.Conflicts = append(rep.Conflicts, fs)
		case x == '?' && y == '?':
			rep.Untracked = append(rep.Untracked, fs)
		default:
			if x != ' ' && x != '?' {
				rep.Staged = append(rep.Staged, fs)
			}
			if y != ' ' && y != '?' {
				rep.Unstaged = append(rep.Unstaged, fs)
			}
		}
	}
	return rep
}

// LogEntry is one commit in `git log`.
type LogEntry struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Email   string `json:"email"`
	Date    string `json:"date"` // ISO 8601
}

// Log returns up to limit recent commits.
func Log(ctx context.Context, repoDir string, limit int) ([]LogEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	out, err := runGit(ctx, repoDir, "log", "--max-count="+fmt.Sprint(limit),
		"--pretty=format:%H%x09%s%x09%an%x09%ae%x09%aI")
	if err != nil {
		return nil, err
	}
	var entries []LogEntry
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 5)
		if len(fields) != 5 {
			continue
		}
		entries = append(entries, LogEntry{
			Hash:    fields[0],
			Subject: fields[1],
			Author:  fields[2],
			Email:   fields[3],
			Date:    fields[4],
		})
	}
	return entries, nil
}

// BranchEntry is one ref in `git branch -a`.
type BranchEntry struct {
	Name     string `json:"name"`
	IsRemote bool   `json:"isRemote"`
	IsHead   bool   `json:"isHead"`
}

// Branches lists local + remote branches.
func Branches(ctx context.Context, repoDir string) ([]BranchEntry, error) {
	out, err := runGit(ctx, repoDir, "for-each-ref",
		"--format=%(refname:short)\t%(refname)\t%(HEAD)",
		"refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	var entries []BranchEntry
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 {
			continue
		}
		short := fields[0]
		full := fields[1]
		head := false
		if len(fields) > 2 {
			head = fields[2] == "*"
		}
		if strings.HasSuffix(short, "/HEAD") {
			continue
		}
		entries = append(entries, BranchEntry{
			Name:     short,
			IsRemote: strings.HasPrefix(full, "refs/remotes/"),
			IsHead:   head,
		})
	}
	return entries, nil
}

// DiffMode chooses which set of changes Diff returns.
type DiffMode string

const (
	DiffWorking DiffMode = "working" // unstaged (working tree vs index)
	DiffStaged  DiffMode = "staged"  // staged   (index vs HEAD)
)

// Show returns the contents of <ref>:<path> via `git show`. Used by the
// Monaco diff viewer to fetch HEAD-side blob bodies (S012-1-10). For
// missing or newly-added files git returns a non-zero exit; we surface
// that as an empty string + nil error so the diff viewer renders the
// added file as "all-new".
func Show(ctx context.Context, repoDir, ref, path string) (string, error) {
	if path == "" {
		return "", errors.New("show: path required")
	}
	if ref == "" {
		ref = "HEAD"
	}
	out, err := runGit(ctx, repoDir, "show", ref+":"+path)
	if err != nil {
		// `git show HEAD:newfile.txt` errors when the file doesn't
		// exist at that ref. Treat that as empty so the diff editor
		// can render the addition. We use a string match because
		// runGit collapses stderr into the error.
		s := strings.ToLower(err.Error())
		if strings.Contains(s, "exists on disk, but not in") ||
			strings.Contains(s, "does not exist in") ||
			strings.Contains(s, "path '") && strings.Contains(s, "exists") ||
			strings.Contains(s, "ambiguous argument") {
			return "", nil
		}
		return "", err
	}
	return string(out), nil
}

// RawDiff returns the raw unified diff text.
func RawDiff(ctx context.Context, repoDir string, mode DiffMode, path string) (string, error) {
	args := []string{"diff", "--no-color", "--no-ext-diff"}
	if mode == DiffStaged {
		args = append(args, "--cached")
	}
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := runGit(ctx, repoDir, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Stage runs `git add <path>` (or "." for all).
func Stage(ctx context.Context, repoDir, path string) error {
	if path == "" {
		path = "."
	}
	_, err := runGit(ctx, repoDir, "add", "--", path)
	return err
}

// Unstage runs `git reset HEAD -- <path>`.
func Unstage(ctx context.Context, repoDir, path string) error {
	if path == "" {
		path = "."
	}
	_, err := runGit(ctx, repoDir, "reset", "HEAD", "--", path)
	return err
}

// Discard reverts unstaged changes for path. For untracked files we delete
// them outright.
func Discard(ctx context.Context, repoDir, path string) error {
	if path == "" {
		return errors.New("discard: path required")
	}
	// `git restore` works for tracked files; for untracked, `git clean -f`.
	if _, err := runGit(ctx, repoDir, "restore", "--", path); err != nil {
		// Try clean (untracked files).
		if _, err2 := runGit(ctx, repoDir, "clean", "-f", "--", path); err2 != nil {
			return fmt.Errorf("discard %s: %w", path, err)
		}
	}
	return nil
}

// ApplyHunk applies a hunk via `git apply` to the index (--cached) for
// stage-hunk, or to the working tree with --reverse for discard-hunk.
//
// hunkPatch is the partial unified diff (with the `diff --git` and `--- / +++`
// headers plus the chosen hunk). The caller is responsible for assembling
// the patch.
func ApplyHunk(ctx context.Context, repoDir, hunkPatch string, cached, reverse bool) error {
	args := []string{"apply", "--whitespace=nowarn"}
	if cached {
		args = append(args, "--cached", "--index")
	}
	if reverse {
		args = append(args, "--reverse")
	}
	args = append(args, "-")
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	cmd.Stdin = strings.NewReader(hunkPatch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git apply: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

func runGit(ctx context.Context, repoDir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
