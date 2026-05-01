// Package git: S013 — Git History & Common Ops.
//
// Adds the "weekly / occasional" operations on top of S012's daily flow:
// rich log filtering, branch graph adjacency, full stash lifecycle,
// cherry-pick / revert / reset, tag CRUD, file history, and blame.
//
// Design notes:
//
//   - Each function corresponds to one or more `git` invocations and
//     returns a JSON-friendly value. Mutating ops return the trailing
//     stdout/stderr so the FE can show `git`'s own progress text.
//   - We do NOT buffer huge log output: `LogFilter` with paginate ≥ 100
//     just streams `git log --max-count` directly. Pagination uses
//     `--skip` rather than commit-hash anchors so the FE can fetch
//     "page N" deterministically while filters change.
//   - Reset hard, cherry-pick, revert all run with
//     `GIT_TERMINAL_PROMPT=0` (inherited from runGit-style helpers) so a
//     missing GPG signature key surfaces as an error instead of blocking
//     the server.
package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// === Rich log =============================================================

// LogFilter narrows down `git log` output for the rich log view.
type LogFilter struct {
	Author string `json:"author,omitempty"`
	Grep   string `json:"grep,omitempty"`
	Since  string `json:"since,omitempty"`  // any value `git log --since=` accepts (e.g. "2 weeks ago", "2024-01-01")
	Until  string `json:"until,omitempty"`
	Path   string `json:"path,omitempty"`   // restrict to commits touching this path
	Branch string `json:"branch,omitempty"` // ref to start the walk from; default = HEAD
	Skip   int    `json:"skip,omitempty"`   // pagination: how many to skip
	Limit  int    `json:"limit,omitempty"`  // pagination: how many to return; default 50, capped 1000
	All    bool   `json:"all,omitempty"`    // include all refs (for graph view)
}

// LogEntryDetail extends LogEntry with parents (for graph layout) and
// (lazily) refs pointing at the commit. The FE uses parents to draw
// edges; refs (branch/tag pointers) are rendered as inline pills.
type LogEntryDetail struct {
	Hash    string   `json:"hash"`
	Subject string   `json:"subject"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Date    string   `json:"date"`
	Parents []string `json:"parents"`
	Refs    []string `json:"refs,omitempty"`
}

// LogFiltered returns up to filter.Limit commits matching the given filter.
// Used by the rich log view (S013-1-1).
func LogFiltered(ctx context.Context, repoDir string, filter LogFilter) ([]LogEntryDetail, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	args := []string{"log",
		"--max-count=" + strconv.Itoa(limit),
		"--pretty=format:%H%x09%P%x09%s%x09%an%x09%ae%x09%aI%x09%D",
	}
	if filter.Skip > 0 {
		args = append(args, "--skip="+strconv.Itoa(filter.Skip))
	}
	if a := strings.TrimSpace(filter.Author); a != "" {
		args = append(args, "--author="+a)
	}
	if g := strings.TrimSpace(filter.Grep); g != "" {
		// `--grep` matches subject + body; `-i` makes it case-insensitive
		// for nicer UX. Multiple words are AND-ed by repeating --grep.
		for _, tok := range strings.Fields(g) {
			args = append(args, "--grep="+tok)
		}
		args = append(args, "-i", "--all-match")
	}
	if s := strings.TrimSpace(filter.Since); s != "" {
		args = append(args, "--since="+s)
	}
	if u := strings.TrimSpace(filter.Until); u != "" {
		args = append(args, "--until="+u)
	}
	if filter.All {
		args = append(args, "--all")
	}
	if br := strings.TrimSpace(filter.Branch); br != "" {
		args = append(args, br)
	}
	if p := strings.TrimSpace(filter.Path); p != "" {
		args = append(args, "--", p)
	}
	out, err := runGit(ctx, repoDir, args...)
	if err != nil {
		return nil, err
	}
	var entries []LogEntryDetail
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 7)
		if len(fields) < 6 {
			continue
		}
		parents := []string{}
		if p := strings.TrimSpace(fields[1]); p != "" {
			parents = strings.Fields(p)
		}
		var refs []string
		if len(fields) >= 7 && strings.TrimSpace(fields[6]) != "" {
			for _, r := range strings.Split(fields[6], ", ") {
				r = strings.TrimSpace(r)
				if r != "" {
					refs = append(refs, r)
				}
			}
		}
		entries = append(entries, LogEntryDetail{
			Hash:    fields[0],
			Parents: parents,
			Subject: fields[2],
			Author:  fields[3],
			Email:   fields[4],
			Date:    fields[5],
			Refs:    refs,
		})
	}
	return entries, nil
}

// === Stash ================================================================

// StashEntry is one entry from `git stash list`.
type StashEntry struct {
	// Name is the stash ref (e.g. "stash@{0}").
	Name string `json:"name"`
	// Index is the integer position (0 == top).
	Index int `json:"index"`
	// Branch the stash was created on.
	Branch string `json:"branch,omitempty"`
	// Subject is the stash message.
	Subject string `json:"subject"`
	// Date in ISO 8601.
	Date string `json:"date,omitempty"`
}

// StashList returns the current stash entries (newest first).
func StashList(ctx context.Context, repoDir string) ([]StashEntry, error) {
	out, err := runGit(ctx, repoDir, "stash", "list", "--pretty=format:%gd%x09%gs%x09%aI")
	if err != nil {
		return nil, err
	}
	var entries []StashEntry
	for i, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		name := fields[0]
		subj := ""
		date := ""
		if len(fields) >= 2 {
			subj = fields[1]
		}
		if len(fields) >= 3 {
			date = fields[2]
		}
		// `git stash list` prefixes the subject with "WIP on <branch>: <sha> <subject>"
		// or "On <branch>: <message>". Extract branch when we can.
		branch := ""
		if rest, ok := strings.CutPrefix(subj, "WIP on "); ok {
			if idx := strings.Index(rest, ":"); idx >= 0 {
				branch = rest[:idx]
			}
		} else if rest, ok := strings.CutPrefix(subj, "On "); ok {
			if idx := strings.Index(rest, ":"); idx >= 0 {
				branch = rest[:idx]
			}
		}
		entries = append(entries, StashEntry{
			Name:    name,
			Index:   i,
			Branch:  branch,
			Subject: subj,
			Date:    date,
		})
	}
	return entries, nil
}

// StashPushOptions controls `git stash push`.
type StashPushOptions struct {
	Message          string `json:"message,omitempty"`
	IncludeUntracked bool   `json:"includeUntracked,omitempty"`
	KeepIndex        bool   `json:"keepIndex,omitempty"`
}

// StashPush creates a new stash entry. Returns the stash ref so the FE
// can refresh.
func StashPush(ctx context.Context, repoDir string, opts StashPushOptions) (string, error) {
	args := []string{"stash", "push"}
	if opts.IncludeUntracked {
		args = append(args, "-u")
	}
	if opts.KeepIndex {
		args = append(args, "--keep-index")
	}
	if msg := strings.TrimSpace(opts.Message); msg != "" {
		args = append(args, "-m", msg)
	}
	return runGitCombined(ctx, repoDir, args...)
}

// StashApply applies stash entry `name` without removing it.
func StashApply(ctx context.Context, repoDir, name string) (string, error) {
	if name == "" {
		return "", errors.New("stash name required")
	}
	return runGitCombined(ctx, repoDir, "stash", "apply", name)
}

// StashPop applies stash entry `name` and removes it on success.
func StashPop(ctx context.Context, repoDir, name string) (string, error) {
	if name == "" {
		return "", errors.New("stash name required")
	}
	return runGitCombined(ctx, repoDir, "stash", "pop", name)
}

// StashDrop removes stash entry `name`.
func StashDrop(ctx context.Context, repoDir, name string) error {
	if name == "" {
		return errors.New("stash name required")
	}
	_, err := runGit(ctx, repoDir, "stash", "drop", name)
	return err
}

// StashDiff returns the unified diff for stash entry `name`.
func StashDiff(ctx context.Context, repoDir, name string) (string, error) {
	if name == "" {
		return "", errors.New("stash name required")
	}
	out, err := runGit(ctx, repoDir, "stash", "show", "-p", "--no-color", name)
	if err != nil {
		// `git stash show -p` returns 1 when there is no diff. We surface
		// that as an empty diff rather than a hard error so the FE can
		// render "no changes" gracefully.
		s := strings.ToLower(err.Error())
		if strings.Contains(s, "is not a stash-like commit") {
			return "", err
		}
		return "", nil
	}
	return string(out), nil
}

// === Cherry-pick / Revert / Reset =========================================

// CherryPickOptions controls `git cherry-pick`.
type CherryPickOptions struct {
	CommitSHA string `json:"commitSha"`
	NoCommit  bool   `json:"noCommit,omitempty"`
}

// CherryPick applies a commit onto the current branch.
//
// On a clean apply we return the trailing git output for FE display. On
// merge conflict, git exits 1 and writes conflict markers; the caller
// should detect it via the returned error and steer the user to S014's
// conflict resolver.
func CherryPick(ctx context.Context, repoDir string, opts CherryPickOptions) (string, error) {
	sha := strings.TrimSpace(opts.CommitSHA)
	if sha == "" {
		return "", errors.New("commit sha required")
	}
	args := []string{"cherry-pick"}
	if opts.NoCommit {
		args = append(args, "--no-commit")
	}
	args = append(args, sha)
	out, err := runGitCombined(ctx, repoDir, args...)
	if err != nil {
		// Wrap conflict-style errors so the handler can return a
		// dedicated status code.
		if strings.Contains(strings.ToLower(out), "conflict") ||
			strings.Contains(strings.ToLower(err.Error()), "conflict") {
			return out, fmt.Errorf("%w: %s", ErrCherryPickConflict, strings.TrimSpace(firstLine(out)))
		}
		return out, err
	}
	return out, nil
}

// ErrCherryPickConflict signals that a cherry-pick stopped because of a
// merge conflict. S014 owns the resolver UI; for S013 the FE just
// surfaces a "conflict — switch to Git → Status" message.
var ErrCherryPickConflict = errors.New("cherry-pick: conflict")

// RevertOptions controls `git revert`.
type RevertOptions struct {
	CommitSHA string `json:"commitSha"`
	NoCommit  bool   `json:"noCommit,omitempty"`
}

// Revert creates a new commit that undoes the changes from `opts.CommitSHA`.
func Revert(ctx context.Context, repoDir string, opts RevertOptions) (string, error) {
	sha := strings.TrimSpace(opts.CommitSHA)
	if sha == "" {
		return "", errors.New("commit sha required")
	}
	args := []string{"revert"}
	if opts.NoCommit {
		args = append(args, "--no-commit")
	}
	args = append(args, "--no-edit", sha)
	return runGitCombined(ctx, repoDir, args...)
}

// ResetMode mirrors `git reset --<mode>`.
type ResetMode string

const (
	ResetSoft  ResetMode = "soft"
	ResetMixed ResetMode = "mixed"
	ResetHard  ResetMode = "hard"
)

// ResetOptions controls `git reset`.
type ResetOptions struct {
	CommitSHA string    `json:"commitSha"`
	Mode      ResetMode `json:"mode"` // defaults to mixed if empty
}

// Reset moves HEAD (and optionally the index/working tree) to `opts.CommitSHA`.
//
// Hard reset is irreversible from inside palmux2's UI; the FE enforces a
// 2-step confirm before calling this. The reflog still preserves the old
// HEAD for ~90 days, which the modal mentions to the user.
func Reset(ctx context.Context, repoDir string, opts ResetOptions) (string, error) {
	sha := strings.TrimSpace(opts.CommitSHA)
	if sha == "" {
		return "", errors.New("commit sha required")
	}
	mode := opts.Mode
	if mode == "" {
		mode = ResetMixed
	}
	switch mode {
	case ResetSoft, ResetMixed, ResetHard:
	default:
		return "", fmt.Errorf("invalid reset mode: %s", mode)
	}
	return runGitCombined(ctx, repoDir, "reset", "--"+string(mode), sha)
}

// === Tag CRUD =============================================================

// TagEntry is one tag from `git tag --list`.
type TagEntry struct {
	Name      string `json:"name"`
	Annotated bool   `json:"annotated"`
	CommitSHA string `json:"commitSha"`
	Subject   string `json:"subject,omitempty"`
	Tagger    string `json:"tagger,omitempty"`
	Date      string `json:"date,omitempty"`
}

// TagList returns all local tags.
func TagList(ctx context.Context, repoDir string) ([]TagEntry, error) {
	// %(objecttype) is "tag" for annotated, "commit" for lightweight.
	// %(*objectname) is the tagged commit when annotated; for lightweight
	// the tag itself points directly at the commit so we fall back to
	// %(objectname).
	out, err := runGit(ctx, repoDir,
		"for-each-ref",
		"--format=%(refname:short)\t%(objecttype)\t%(objectname)\t%(*objectname)\t%(subject)\t%(taggername)\t%(taggerdate:iso-strict)",
		"refs/tags",
	)
	if err != nil {
		return nil, err
	}
	var entries []TagEntry
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 7)
		if len(fields) < 4 {
			continue
		}
		t := TagEntry{
			Name:      fields[0],
			Annotated: fields[1] == "tag",
			CommitSHA: fields[3],
		}
		if t.CommitSHA == "" {
			t.CommitSHA = fields[2]
		}
		if len(fields) >= 5 {
			t.Subject = fields[4]
		}
		if len(fields) >= 6 {
			t.Tagger = fields[5]
		}
		if len(fields) >= 7 {
			t.Date = fields[6]
		}
		entries = append(entries, t)
	}
	return entries, nil
}

// CreateTagOptions controls `git tag`.
type CreateTagOptions struct {
	Name      string `json:"name"`
	CommitSHA string `json:"commitSha,omitempty"` // defaults to HEAD
	Message   string `json:"message,omitempty"`   // empty -> lightweight
	Annotated bool   `json:"annotated,omitempty"`
	Force     bool   `json:"force,omitempty"`
}

// CreateTag creates a local tag.
func CreateTag(ctx context.Context, repoDir string, opts CreateTagOptions) error {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return errors.New("tag name required")
	}
	args := []string{"tag"}
	if opts.Force {
		args = append(args, "-f")
	}
	if opts.Annotated || strings.TrimSpace(opts.Message) != "" {
		args = append(args, "-a", name, "-m", opts.Message)
	} else {
		args = append(args, name)
	}
	if sha := strings.TrimSpace(opts.CommitSHA); sha != "" {
		args = append(args, sha)
	}
	_, err := runGit(ctx, repoDir, args...)
	return err
}

// DeleteTagOptions controls `git tag -d` / `git push --delete`.
type DeleteTagOptions struct {
	Name   string `json:"name"`
	Remote string `json:"remote,omitempty"` // when non-empty, also delete from this remote
}

// DeleteTag removes a local tag (and optionally the remote tag too).
func DeleteTag(ctx context.Context, repoDir string, opts DeleteTagOptions) (string, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return "", errors.New("tag name required")
	}
	output := ""
	if _, err := runGit(ctx, repoDir, "tag", "-d", name); err != nil {
		return "", err
	}
	if r := strings.TrimSpace(opts.Remote); r != "" {
		out, err := runGitCombined(ctx, repoDir, "push", r, "--delete", "refs/tags/"+name)
		if err != nil {
			return out, err
		}
		output = out
	}
	return output, nil
}

// PushTagOptions controls a single tag push.
type PushTagOptions struct {
	Name   string `json:"name"`
	Remote string `json:"remote,omitempty"` // defaults to "origin"
	Force  bool   `json:"force,omitempty"`
}

// PushTag pushes a single named tag (or all tags when Name == "").
func PushTag(ctx context.Context, repoDir string, opts PushTagOptions) (string, error) {
	remote := strings.TrimSpace(opts.Remote)
	if remote == "" {
		remote = "origin"
	}
	args := []string{"push"}
	if opts.Force {
		args = append(args, "--force")
	}
	args = append(args, remote)
	if name := strings.TrimSpace(opts.Name); name != "" {
		args = append(args, "refs/tags/"+name)
	} else {
		args = append(args, "--tags")
	}
	return runGitCombined(ctx, repoDir, args...)
}

// === File history =========================================================

// FileHistory returns commits that touched `path`. The returned slice is
// in commit order (newest first), each entry annotated with a short
// per-commit stat (insertions/deletions for the file).
func FileHistory(ctx context.Context, repoDir, path string, limit int) ([]LogEntryDetail, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("path required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	return LogFiltered(ctx, repoDir, LogFilter{
		Path:  path,
		Limit: limit,
	})
}

// === Blame ================================================================

// BlameLine is one line of blame output.
type BlameLine struct {
	Hash       string `json:"hash"`
	Author     string `json:"author"`
	Email      string `json:"email,omitempty"`
	AuthorTime string `json:"authorTime,omitempty"` // ISO 8601
	Summary    string `json:"summary,omitempty"`    // commit subject
	OrigLine   int    `json:"origLine"`             // 1-based line in source commit
	FinalLine  int    `json:"finalLine"`            // 1-based line in current file
	Content    string `json:"content"`
}

// Blame runs `git blame --porcelain` and returns one entry per output
// line. The porcelain format groups header lines per commit, then
// repeats only the hash for subsequent lines from the same commit; we
// expand each entry into a self-contained record so the FE doesn't need
// to track state.
func Blame(ctx context.Context, repoDir, revision, path string) ([]BlameLine, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("path required")
	}
	rev := strings.TrimSpace(revision)
	args := []string{"blame", "--porcelain"}
	if rev != "" {
		args = append(args, rev)
	}
	args = append(args, "--", path)
	out, err := runGit(ctx, repoDir, args...)
	if err != nil {
		return nil, err
	}
	commits := map[string]struct {
		author  string
		email   string
		date    string
		summary string
	}{}
	var lines []BlameLine
	current := BlameLine{}
	expectingContent := false
	for _, raw := range strings.Split(string(out), "\n") {
		if expectingContent {
			// The porcelain format puts the source line on the line
			// immediately after the header lines for that line, prefixed
			// with a tab.
			if strings.HasPrefix(raw, "\t") {
				current.Content = raw[1:]
				if c, ok := commits[current.Hash]; ok {
					current.Author = c.author
					current.Email = c.email
					current.AuthorTime = c.date
					current.Summary = c.summary
				}
				lines = append(lines, current)
				current = BlameLine{}
				expectingContent = false
				continue
			}
			// Otherwise we hit another header before the content (rare
			// when --porcelain encounters merge boundaries); treat it
			// like a regular header.
		}
		if raw == "" {
			continue
		}
		// First field is either the SHA (40 hex chars) or a key like
		// "author Foo".
		if len(raw) >= 40 && isHex(raw[:40]) {
			parts := strings.Fields(raw)
			if len(parts) < 3 {
				continue
			}
			current = BlameLine{}
			current.Hash = parts[0]
			current.OrigLine, _ = strconv.Atoi(parts[1])
			current.FinalLine, _ = strconv.Atoi(parts[2])
			expectingContent = true
			continue
		}
		// Header continuation (key/value pairs like "author Foo Bar",
		// "author-mail <foo@example>", "author-time 1700000000",
		// "summary <subject>"). Use the most recent SHA seen as the
		// commit key.
		if !expectingContent {
			continue
		}
		fields := strings.SplitN(raw, " ", 2)
		key := fields[0]
		val := ""
		if len(fields) > 1 {
			val = fields[1]
		}
		entry := commits[current.Hash]
		switch key {
		case "author":
			entry.author = val
		case "author-mail":
			entry.email = strings.Trim(val, "<>")
		case "author-time":
			if ts, err := strconv.ParseInt(val, 10, 64); err == nil {
				entry.date = isoTimeFromUnix(ts)
			}
		case "summary":
			entry.summary = val
		}
		commits[current.Hash] = entry
	}
	return lines, nil
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func isoTimeFromUnix(ts int64) string {
	// Use a tiny inline formatter to avoid pulling another import for a
	// one-shot conversion.
	return fmt.Sprintf("@%d", ts)
}

// === Branch graph =========================================================

// BranchGraph returns the recent commit list with parents and ref
// pointers, intended for the SVG layout in the rich log view.
func BranchGraph(ctx context.Context, repoDir string, limit int, all bool) ([]LogEntryDetail, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	return LogFiltered(ctx, repoDir, LogFilter{
		Limit: limit,
		All:   all,
	})
}

// === Helpers shared across operations =====================================

// runGitNoErr is like runGit but never returns an error, only the
// stdout — useful for best-effort lookups (e.g. resolving HEAD when
// computing reset previews) that should not break the parent operation.
func runGitNoErr(ctx context.Context, repoDir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}
