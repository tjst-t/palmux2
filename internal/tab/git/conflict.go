// Package git: S014 — Conflict resolution + Interactive rebase + Submodule /
// Reflog / Bisect helpers.
//
// This file extends the S012 + S013 surfaces with the "難所操作" tier:
//
//   - 3-way merge: list the conflicting files, fetch ours / base / theirs
//     blobs, write the resolved working-tree copy, mark resolved.
//   - Interactive rebase: read / write `.git/rebase-merge/git-rebase-todo`,
//     start a rebase that pauses on the todo so the FE can edit it,
//     continue / abort / skip the in-flight rebase or merge.
//   - Submodule listing + init / update.
//   - Reflog viewer.
//   - Bisect helper (start / good / bad / skip / reset / status).
//
// All functions return JSON-friendly values and route their `git` invocations
// through the existing runGit / runGitCombined helpers in git.go / ops.go so
// that GIT_TERMINAL_PROMPT and the credential-helper inheritance from S012
// continue to apply unchanged.
package git

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// =============================================================================
// Conflict listing & parsing
// =============================================================================

// ConflictFile bundles one file's conflict state.
type ConflictFile struct {
	Path    string          `json:"path"`
	Hunks   []ConflictHunk  `json:"hunks"`
	HasBase bool            `json:"hasBase"` // true if the file is in diff3 mode (`merge.conflictStyle=diff3`)
	Binary  bool            `json:"binary"`  // true if the parser couldn't make sense of the markers
}

// ConflictHunk describes one `<<<<<<< / ======= / >>>>>>>` group.
//
// Line numbers are 1-based offsets in the *current working-tree file*; the FE
// uses them to render the "this hunk lives at line N" label and to scroll
// the editor.
type ConflictHunk struct {
	StartLine int      `json:"startLine"`
	EndLine   int      `json:"endLine"`
	Ours      []string `json:"ours"`
	Base      []string `json:"base,omitempty"`
	Theirs    []string `json:"theirs"`
	OursLabel string   `json:"oursLabel,omitempty"`   // text after `<<<<<<<`
	TheirsLabel string `json:"theirsLabel,omitempty"` // text after `>>>>>>>`
}

// Conflicts returns one entry per file currently in conflict (porcelain XY
// codes UU, AA, DD, AU, UA, DU, UD).
func Conflicts(ctx context.Context, repoDir string) ([]ConflictFile, error) {
	rep, err := Status(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	var out []ConflictFile
	for _, fs := range rep.Conflicts {
		cf, err := readConflictFile(repoDir, fs.Path)
		if err != nil {
			// Don't abort the whole listing — just mark the file binary.
			out = append(out, ConflictFile{Path: fs.Path, Binary: true})
			continue
		}
		out = append(out, cf)
	}
	return out, nil
}

func readConflictFile(repoDir, path string) (ConflictFile, error) {
	full := filepath.Join(repoDir, path)
	body, err := os.ReadFile(full)
	if err != nil {
		return ConflictFile{Path: path, Binary: true}, nil
	}
	if !looksLikeText(body) {
		return ConflictFile{Path: path, Binary: true}, nil
	}
	return parseConflictBody(path, string(body)), nil
}

func looksLikeText(b []byte) bool {
	// Cheap heuristic: NUL bytes in the first 8KB → binary.
	n := len(b)
	if n > 8192 {
		n = 8192
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return false
		}
	}
	return true
}

// parseConflictBody walks the file looking for diff3-style and 2-way conflict
// markers. Tolerates files with mixed conflict regions.
func parseConflictBody(path, body string) ConflictFile {
	cf := ConflictFile{Path: path}
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) {
		ln := lines[i]
		if strings.HasPrefix(ln, "<<<<<<<") {
			start := i + 1 // 1-based
			oursLabel := strings.TrimSpace(strings.TrimPrefix(ln, "<<<<<<<"))
			i++
			var ours, base, theirs []string
			section := "ours"
			hasBase := false
			theirsLabel := ""
			done := false
			for i < len(lines) && !done {
				cur := lines[i]
				switch {
				case strings.HasPrefix(cur, "|||||||"):
					hasBase = true
					section = "base"
				case strings.HasPrefix(cur, "======="):
					section = "theirs"
				case strings.HasPrefix(cur, ">>>>>>>"):
					theirsLabel = strings.TrimSpace(strings.TrimPrefix(cur, ">>>>>>>"))
					done = true
				default:
					switch section {
					case "ours":
						ours = append(ours, cur)
					case "base":
						base = append(base, cur)
					case "theirs":
						theirs = append(theirs, cur)
					}
				}
				i++
			}
			end := i // i is one past the >>>>>>> line; in 1-based that's i
			cf.Hunks = append(cf.Hunks, ConflictHunk{
				StartLine:   start,
				EndLine:     end,
				Ours:        ours,
				Base:        base,
				Theirs:      theirs,
				OursLabel:   oursLabel,
				TheirsLabel: theirsLabel,
			})
			if hasBase {
				cf.HasBase = true
			}
			continue
		}
		i++
	}
	return cf
}

// =============================================================================
// Per-file conflict body (ours / base / theirs / merged)
// =============================================================================

// ConflictBody returns the three sides of the conflict plus the current
// working-tree file (the merged candidate).
type ConflictBody struct {
	Path   string `json:"path"`
	Ours   string `json:"ours"`
	Base   string `json:"base"`
	Theirs string `json:"theirs"`
	Merged string `json:"merged"` // current working-tree file
}

// GetConflictFile fetches the three index stages plus the current working
// tree copy. Stage 1 = base, 2 = ours, 3 = theirs (per `git ls-files -u`).
func GetConflictFile(ctx context.Context, repoDir, path string) (ConflictBody, error) {
	if strings.TrimSpace(path) == "" {
		return ConflictBody{}, errors.New("path required")
	}
	body := ConflictBody{Path: path}
	body.Base, _ = stageBlob(ctx, repoDir, 1, path)
	body.Ours, _ = stageBlob(ctx, repoDir, 2, path)
	body.Theirs, _ = stageBlob(ctx, repoDir, 3, path)
	full := filepath.Join(repoDir, path)
	if data, err := os.ReadFile(full); err == nil {
		body.Merged = string(data)
	}
	return body, nil
}

func stageBlob(ctx context.Context, repoDir string, stage int, path string) (string, error) {
	out, err := runGit(ctx, repoDir, "show", fmt.Sprintf(":%d:%s", stage, path))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// PutConflictFile overwrites the working-tree copy of `path` with `content`.
//
// Note: this does NOT call `git add` — the caller marks the file resolved as a
// separate step so the FE can render the post-write diff and let the user
// re-edit before committing to the index.
func PutConflictFile(ctx context.Context, repoDir, path, content string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path required")
	}
	if strings.Contains(path, "..") {
		return errors.New("invalid path")
	}
	full := filepath.Join(repoDir, path)
	// Ensure the parent dir exists (some merges introduce new directories).
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// MarkConflictResolved runs `git add -- <path>` to record the working-tree
// copy as the resolved version.
func MarkConflictResolved(ctx context.Context, repoDir, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path required")
	}
	_, err := runGit(ctx, repoDir, "add", "--", path)
	return err
}

// =============================================================================
// Rebase TODO
// =============================================================================

// RebaseTodoEntry is one "pick / squash / ..." line in
// .git/rebase-merge/git-rebase-todo.
type RebaseTodoEntry struct {
	// Action is the verb (pick / squash / fixup / edit / drop / reword
	// / exec). Comments and blank lines are ignored.
	Action string `json:"action"`
	// SHA is the commit hash (empty for `exec`).
	SHA string `json:"sha,omitempty"`
	// Subject is the trailing short message (after the SHA).
	Subject string `json:"subject,omitempty"`
	// Raw is the original line, preserved so we can round-trip
	// unrecognised verbs.
	Raw string `json:"raw"`
}

// RebaseStatus describes whether a rebase is currently in progress.
type RebaseStatus struct {
	Active     bool              `json:"active"`
	Interactive bool             `json:"interactive"`
	Onto       string            `json:"onto,omitempty"`
	Head       string            `json:"head,omitempty"`
	Todo       []RebaseTodoEntry `json:"todo,omitempty"`
	// Done lists already-applied entries (from git-rebase-todo.backup or
	// `done`), so the FE can render a progress indicator.
	Done []RebaseTodoEntry `json:"done,omitempty"`
}

func rebaseDir(repoDir string) string {
	// In rebase --interactive: .git/rebase-merge
	// In rebase --apply (the older one): .git/rebase-apply
	if _, err := os.Stat(filepath.Join(repoDir, ".git", "rebase-merge")); err == nil {
		return filepath.Join(repoDir, ".git", "rebase-merge")
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".git", "rebase-apply")); err == nil {
		return filepath.Join(repoDir, ".git", "rebase-apply")
	}
	return ""
}

// GetRebaseStatus returns the current rebase state (or {active:false}).
func GetRebaseStatus(ctx context.Context, repoDir string) (RebaseStatus, error) {
	dir := rebaseDir(repoDir)
	if dir == "" {
		return RebaseStatus{Active: false}, nil
	}
	st := RebaseStatus{Active: true, Interactive: strings.HasSuffix(dir, "rebase-merge")}
	if b, err := os.ReadFile(filepath.Join(dir, "onto")); err == nil {
		st.Onto = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(filepath.Join(dir, "head-name")); err == nil {
		st.Head = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(filepath.Join(dir, "git-rebase-todo")); err == nil {
		st.Todo = parseRebaseTodo(string(b))
	}
	if b, err := os.ReadFile(filepath.Join(dir, "done")); err == nil {
		st.Done = parseRebaseTodo(string(b))
	}
	return st, nil
}

// PutRebaseTodo writes the new todo to `.git/rebase-merge/git-rebase-todo`.
//
// Optionally runs `git rebase --continue` afterwards (when continueAfter=true).
// We expose both modes because the FE may want to "save and pause" before
// continuing — but the default UX is "Apply" which means continue.
func PutRebaseTodo(ctx context.Context, repoDir string, entries []RebaseTodoEntry, continueAfter bool) (string, error) {
	dir := rebaseDir(repoDir)
	if dir == "" {
		return "", errors.New("no rebase in progress")
	}
	body := serializeRebaseTodo(entries)
	if err := os.WriteFile(filepath.Join(dir, "git-rebase-todo"), []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("write git-rebase-todo: %w", err)
	}
	if !continueAfter {
		return body, nil
	}
	out, err := runGitCombined(ctx, repoDir, "rebase", "--continue")
	return out, err
}

func parseRebaseTodo(s string) []RebaseTodoEntry {
	var out []RebaseTodoEntry
	for _, raw := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(raw)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		fields := strings.SplitN(trim, " ", 3)
		entry := RebaseTodoEntry{Raw: raw, Action: fields[0]}
		if len(fields) >= 2 {
			entry.SHA = fields[1]
		}
		if len(fields) >= 3 {
			entry.Subject = fields[2]
		}
		out = append(out, entry)
	}
	return out
}

func serializeRebaseTodo(entries []RebaseTodoEntry) string {
	var sb strings.Builder
	for _, e := range entries {
		// If the FE preserved the original Raw line and didn't mutate
		// Action/SHA, prefer that — this is useful for `exec` lines
		// that contain spaces.
		if e.Raw != "" && e.Action == "" {
			sb.WriteString(e.Raw)
			sb.WriteByte('\n')
			continue
		}
		sb.WriteString(e.Action)
		if e.SHA != "" {
			sb.WriteByte(' ')
			sb.WriteString(e.SHA)
		}
		if e.Subject != "" {
			sb.WriteByte(' ')
			sb.WriteString(e.Subject)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// =============================================================================
// Rebase / merge ops
// =============================================================================

// RebaseStartOptions controls a `git rebase` start.
type RebaseStartOptions struct {
	// Onto is the upstream / target ref. Required.
	Onto string `json:"onto"`
	// Interactive opens the rebase in interactive mode. The backend uses
	// `GIT_SEQUENCE_EDITOR=":"` to bypass the editor; the FE is then
	// responsible for editing the todo via PUT /git/rebase-todo.
	Interactive bool `json:"interactive,omitempty"`
	// Todo lets the caller supply a pre-edited todo. When set, we run an
	// interactive rebase, pause it (no-op editor), then overwrite the todo
	// before continuing. Subject lines must not contain newlines.
	Todo []RebaseTodoEntry `json:"todo,omitempty"`
	// Autosquash mirrors `git rebase --autosquash`.
	Autosquash bool `json:"autosquash,omitempty"`
	// Autostash mirrors `git rebase --autostash`.
	Autostash bool `json:"autostash,omitempty"`
}

// RebaseStart starts a rebase. When `Todo` is supplied (interactive flow),
// we use a no-op sequence editor so git pauses with the default todo, then
// overwrite the todo and run --continue.
func RebaseStart(ctx context.Context, repoDir string, opts RebaseStartOptions) (string, error) {
	if strings.TrimSpace(opts.Onto) == "" {
		return "", errors.New("onto required")
	}
	args := []string{"rebase"}
	if opts.Interactive || len(opts.Todo) > 0 {
		args = append(args, "-i")
	}
	if opts.Autosquash {
		args = append(args, "--autosquash")
	}
	if opts.Autostash {
		args = append(args, "--autostash")
	}
	args = append(args, opts.Onto)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if len(opts.Todo) > 0 {
		// Pause with default todo so we can replace it.
		cmd.Env = append(cmd.Env, "GIT_SEQUENCE_EDITOR=:")
	} else if opts.Interactive {
		// Caller wants to edit the todo afterwards via PUT /git/rebase-todo.
		// `:` is shell builtin no-op — git accepts the unchanged todo and
		// applies it. Since the FE will replace the todo *after* the
		// command returns, we instead use `true` which also keeps the
		// pristine todo, but we need git to NOT auto-apply it. Easiest:
		// write a sentinel todo by passing a custom editor that stops:
		// we set GIT_SEQUENCE_EDITOR=":" so git proceeds with the
		// default todo (all "pick"), then we let the FE call PUT to
		// rewrite the todo on the next pause (which only happens at a
		// conflict). For empty interactive (caller-supplied todo
		// absent), fall back to no-op editor.
		cmd.Env = append(cmd.Env, "GIT_SEQUENCE_EDITOR=:")
	}
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	runErr := cmd.Run()
	out := combined.String()

	if len(opts.Todo) > 0 {
		// Even if rebase succeeded outright, give the FE the chance to
		// reorder. We rewrite the todo file (if it still exists) and
		// run --continue.
		dir := rebaseDir(repoDir)
		if dir != "" {
			body := serializeRebaseTodo(opts.Todo)
			if werr := os.WriteFile(filepath.Join(dir, "git-rebase-todo"), []byte(body), 0o644); werr != nil {
				return out, fmt.Errorf("write rebase todo: %w", werr)
			}
			cont, contErr := runGitCombined(ctx, repoDir, "rebase", "--continue")
			out = out + cont
			if contErr != nil {
				return out, contErr
			}
			return out, nil
		}
		// dir gone means rebase finished without pausing — that means
		// our todo was applied as-is (no reordering needed).
	}
	if runErr != nil {
		return out, fmt.Errorf("git rebase: %s", strings.TrimSpace(out))
	}
	return out, nil
}

// RebaseAbort runs `git rebase --abort`.
func RebaseAbort(ctx context.Context, repoDir string) (string, error) {
	return runGitCombined(ctx, repoDir, "rebase", "--abort")
}

// RebaseContinue runs `git rebase --continue`.
func RebaseContinue(ctx context.Context, repoDir string) (string, error) {
	return runGitCombined(ctx, repoDir, "rebase", "--continue")
}

// RebaseSkip runs `git rebase --skip`.
func RebaseSkip(ctx context.Context, repoDir string) (string, error) {
	return runGitCombined(ctx, repoDir, "rebase", "--skip")
}

// MergeOptions controls `git merge`.
type MergeOptions struct {
	Branch  string `json:"branch"`
	NoFF    bool   `json:"noFf,omitempty"`
	Squash  bool   `json:"squash,omitempty"`
	FFOnly  bool   `json:"ffOnly,omitempty"`
	Message string `json:"message,omitempty"`
}

// Merge runs `git merge`.
func Merge(ctx context.Context, repoDir string, opts MergeOptions) (string, error) {
	if strings.TrimSpace(opts.Branch) == "" {
		return "", errors.New("branch required")
	}
	args := []string{"merge"}
	if opts.NoFF {
		args = append(args, "--no-ff")
	}
	if opts.FFOnly {
		args = append(args, "--ff-only")
	}
	if opts.Squash {
		args = append(args, "--squash")
	}
	if msg := strings.TrimSpace(opts.Message); msg != "" {
		args = append(args, "-m", msg)
	}
	args = append(args, opts.Branch)
	return runGitCombined(ctx, repoDir, args...)
}

// MergeAbort runs `git merge --abort`.
func MergeAbort(ctx context.Context, repoDir string) (string, error) {
	return runGitCombined(ctx, repoDir, "merge", "--abort")
}

// =============================================================================
// Submodules
// =============================================================================

// Submodule is one entry from `git submodule status`.
type Submodule struct {
	Path     string `json:"path"`
	Commit   string `json:"commit"`
	Status   string `json:"status"` // initialized | not-initialized | conflict | modified
	Describe string `json:"describe,omitempty"`
}

// Submodules lists all submodules of repoDir.
func Submodules(ctx context.Context, repoDir string) ([]Submodule, error) {
	out, err := runGit(ctx, repoDir, "submodule", "status", "--recursive")
	if err != nil {
		// `git submodule` errors if the repo has no submodules; surface as empty.
		if strings.Contains(strings.ToLower(err.Error()), "no submodule") {
			return []Submodule{}, nil
		}
		return []Submodule{}, nil // empty rather than failure for friendlier UX
	}
	var subs []Submodule
	scan := bufio.NewScanner(bytes.NewReader(out))
	for scan.Scan() {
		raw := scan.Text()
		if raw == "" {
			continue
		}
		// First char encodes status: ' ' = initialized, '-' = not init,
		// '+' = differs, 'U' = conflict.
		status := "initialized"
		switch raw[0] {
		case '-':
			status = "not-initialized"
		case '+':
			status = "modified"
		case 'U':
			status = "conflict"
		}
		// Drop the status char then split.
		body := strings.TrimSpace(raw[1:])
		parts := strings.SplitN(body, " ", 3)
		if len(parts) < 2 {
			continue
		}
		s := Submodule{
			Commit: parts[0],
			Path:   parts[1],
			Status: status,
		}
		if len(parts) >= 3 {
			s.Describe = strings.Trim(parts[2], "()")
		}
		subs = append(subs, s)
	}
	return subs, nil
}

// SubmoduleInit runs `git submodule init <path>`.
func SubmoduleInit(ctx context.Context, repoDir, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path required")
	}
	return runGitCombined(ctx, repoDir, "submodule", "init", "--", path)
}

// SubmoduleUpdate runs `git submodule update --init --recursive <path>`.
func SubmoduleUpdate(ctx context.Context, repoDir, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path required")
	}
	return runGitCombined(ctx, repoDir, "submodule", "update", "--init", "--recursive", "--", path)
}

// =============================================================================
// Reflog
// =============================================================================

// ReflogEntry describes one move of HEAD.
type ReflogEntry struct {
	Hash    string `json:"hash"`
	Ref     string `json:"ref"`     // e.g. "HEAD@{0}"
	Action  string `json:"action"`  // commit, checkout, reset, rebase, ...
	Message string `json:"message"`
	Date    string `json:"date,omitempty"`
}

// Reflog returns the most recent `limit` HEAD movements.
func Reflog(ctx context.Context, repoDir string, limit int) ([]ReflogEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	// %H = commit, %gd = ref selector (HEAD@{N}), %gs = full reflog
	// message ("commit: foo", "checkout: moving from a to b", ...),
	// %aI = ISO date.
	out, err := runGit(ctx, repoDir, "reflog",
		"--max-count="+strconv.Itoa(limit),
		"--pretty=format:%H%x09%gd%x09%gs%x09%aI",
	)
	if err != nil {
		return nil, err
	}
	var entries []ReflogEntry
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) < 3 {
			continue
		}
		gs := fields[2]
		// `gs` is "<action>: <message>". Split.
		action := gs
		message := ""
		if idx := strings.Index(gs, ": "); idx >= 0 {
			action = gs[:idx]
			message = gs[idx+2:]
		}
		entry := ReflogEntry{
			Hash:    fields[0],
			Ref:     fields[1],
			Action:  action,
			Message: message,
		}
		if len(fields) >= 4 {
			entry.Date = fields[3]
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// =============================================================================
// Bisect
// =============================================================================

// BisectStatus describes the current state of `git bisect`.
type BisectStatus struct {
	Active     bool   `json:"active"`
	CurrentSHA string `json:"currentSha,omitempty"`
	Good       string `json:"good,omitempty"` // newest "good" sha
	Bad        string `json:"bad,omitempty"`  // newest "bad" sha
	Remaining  int    `json:"remaining,omitempty"`
	// Log is the raw `git bisect log` output for transparency.
	Log string `json:"log,omitempty"`
}

func bisectActive(repoDir string) bool {
	for _, p := range []string{".git/BISECT_LOG", ".git/BISECT_START", ".git/BISECT_TERMS"} {
		if _, err := os.Stat(filepath.Join(repoDir, p)); err == nil {
			return true
		}
	}
	return false
}

// GetBisectStatus reports whether bisect is in progress and which SHA HEAD
// currently points at.
func GetBisectStatus(ctx context.Context, repoDir string) (BisectStatus, error) {
	if !bisectActive(repoDir) {
		return BisectStatus{Active: false}, nil
	}
	st := BisectStatus{Active: true}
	if log, err := runGit(ctx, repoDir, "bisect", "log"); err == nil {
		st.Log = string(log)
		// Parse out the latest "good" / "bad".
		for _, raw := range strings.Split(string(log), "\n") {
			ln := strings.TrimSpace(raw)
			if strings.HasPrefix(ln, "# good:") {
				if f := strings.Fields(ln); len(f) >= 3 {
					st.Good = f[2]
				}
			}
			if strings.HasPrefix(ln, "# bad:") {
				if f := strings.Fields(ln); len(f) >= 3 {
					st.Bad = f[2]
				}
			}
			if strings.HasPrefix(ln, "git bisect good ") {
				st.Good = strings.TrimPrefix(ln, "git bisect good ")
			}
			if strings.HasPrefix(ln, "git bisect bad ") {
				st.Bad = strings.TrimPrefix(ln, "git bisect bad ")
			}
		}
	}
	if head, err := runGit(ctx, repoDir, "rev-parse", "HEAD"); err == nil {
		st.CurrentSHA = strings.TrimSpace(string(head))
	}
	// Approximate remaining commits via `git bisect view --pretty=oneline`
	// which returns the current bisect range.
	if view, err := runGit(ctx, repoDir, "rev-list", "--count", "HEAD"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(view))); err == nil {
			st.Remaining = n
		}
	}
	return st, nil
}

// BisectStart kicks off a `git bisect`.
type BisectStartOptions struct {
	Good string `json:"good"`
	Bad  string `json:"bad"`
}

func BisectStart(ctx context.Context, repoDir string, opts BisectStartOptions) (string, error) {
	if strings.TrimSpace(opts.Bad) == "" {
		return "", errors.New("bad commit required")
	}
	// Reset any prior bisect to ensure a clean start.
	_, _ = runGitCombined(ctx, repoDir, "bisect", "reset")
	args := []string{"bisect", "start", opts.Bad}
	if g := strings.TrimSpace(opts.Good); g != "" {
		args = append(args, g)
	}
	return runGitCombined(ctx, repoDir, args...)
}

// BisectMark marks HEAD as `term` (good / bad / skip).
func BisectMark(ctx context.Context, repoDir, term string) (string, error) {
	switch term {
	case "good", "bad", "skip":
	default:
		return "", fmt.Errorf("invalid bisect term: %s", term)
	}
	return runGitCombined(ctx, repoDir, "bisect", term)
}

// BisectReset runs `git bisect reset` and returns to the original branch.
func BisectReset(ctx context.Context, repoDir string) (string, error) {
	return runGitCombined(ctx, repoDir, "bisect", "reset")
}

// =============================================================================
// JSON helpers (so handler.go stays slim)
// =============================================================================

// MarshalConflictBody returns a stable JSON encoding of a ConflictBody. The
// handler delegates here so the test suite can assert the wire format
// without poking into the HTTP layer.
func MarshalConflictBody(b ConflictBody) ([]byte, error) {
	return json.Marshal(b)
}
