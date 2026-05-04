// Package commands sniffs out runnable commands declared in a repository's
// Makefile or package.json. Results are cached for 30s per worktree path.
package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Command describes one runnable target.
type Command struct {
	Name    string `json:"name"`
	Source  string `json:"source"` // "make" | "npm" | "yarn" | "pnpm" | "bun"
	Command string `json:"command"`
	Line    int    `json:"line,omitempty"`
}

// Detector caches detection results per worktree.
type Detector struct {
	ttl time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	at       time.Time
	commands []Command
}

// New returns a Detector with the default 30s TTL.
func New() *Detector {
	return &Detector{
		ttl:   30 * time.Second,
		cache: map[string]cacheEntry{},
	}
}

// Detect scans dir for command sources. Cached results shorter than ttl are
// returned without re-reading the filesystem.
func (d *Detector) Detect(ctx context.Context, dir string) ([]Command, error) {
	if dir == "" {
		return nil, errors.New("commands: empty directory")
	}
	d.mu.Lock()
	if entry, ok := d.cache[dir]; ok && time.Since(entry.at) < d.ttl {
		d.mu.Unlock()
		return entry.commands, nil
	}
	d.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out := []Command{}
	out = append(out, scanMakefile(dir)...)
	out = append(out, scanPackageJSON(dir)...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Name < out[j].Name
	})

	d.mu.Lock()
	d.cache[dir] = cacheEntry{at: time.Now(), commands: out}
	d.mu.Unlock()
	return out, nil
}

// Invalidate drops the cached entry for dir (if any). Useful after the user
// edits a Makefile and wants the change to surface immediately.
func (d *Detector) Invalidate(dir string) {
	d.mu.Lock()
	delete(d.cache, dir)
	d.mu.Unlock()
}

// makeTargetRE matches lines like "build: deps" or "build:". The first capture
// group is the target name; we filter out targets that look like file paths
// (contain '/' or '.') and special targets (start with '.').
//
// hotfix: also reject Makefile variable assignments — `VAR := value`,
// `VAR ?= value`, `VAR += value` start with `name:` so the regex matches,
// but they aren't runnable targets. We detect the second `:` / `=` after
// the colon manually because RE2 lacks lookahead.
var makeTargetRE = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_\-]*)\s*:`)

func scanMakefile(dir string) []Command {
	path := filepath.Join(dir, "Makefile")
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return nil
	}
	defer f.Close()

	out := []Command{}
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") {
			continue
		}
		idx := makeTargetRE.FindStringSubmatchIndex(line)
		if idx == nil {
			continue
		}
		// idx[1] is the byte position right after the matched colon.
		// If the next char is '=' the line is a `name :=` variable
		// assignment, not a target — skip it.
		if idx[1] < len(line) && line[idx[1]] == '=' {
			continue
		}
		name := line[idx[2]:idx[3]]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, Command{
			Name:    name,
			Source:  "make",
			Command: "make " + name,
			Line:    lineNo,
		})
	}
	return out
}

// packageJSON is the minimal subset we read.
type packageJSON struct {
	Scripts        map[string]string `json:"scripts"`
	PackageManager string            `json:"packageManager"`
}

func scanPackageJSON(dir string) []Command {
	path := filepath.Join(dir, "package.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pkg packageJSON
	if err := json.Unmarshal(b, &pkg); err != nil {
		return nil
	}
	if len(pkg.Scripts) == 0 {
		return nil
	}
	pm := detectPackageManager(dir, pkg.PackageManager)
	out := make([]Command, 0, len(pkg.Scripts))
	for name, cmd := range pkg.Scripts {
		out = append(out, Command{
			Name:    name,
			Source:  pm,
			Command: pm + " run " + name,
		})
		_ = cmd
	}
	return out
}

func detectPackageManager(dir, declared string) string {
	if declared != "" {
		// "pnpm@8.0.0" → "pnpm"
		if i := strings.Index(declared, "@"); i > 0 {
			return declared[:i]
		}
		return declared
	}
	for _, candidate := range []struct{ file, name string }{
		{"pnpm-lock.yaml", "pnpm"},
		{"yarn.lock", "yarn"},
		{"bun.lockb", "bun"},
		{"bun.lock", "bun"},
	} {
		if _, err := os.Stat(filepath.Join(dir, candidate.file)); err == nil {
			return candidate.name
		}
	}
	return "npm"
}
