package claudeagent

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// PermissionModes is the discovered list of `--permission-mode` choices the
// installed `claude` CLI supports, plus the canonical default.
//
// The CLI's --help text exposes them like:
//
//	--permission-mode <mode>  Permission mode to use for the session
//	  (choices: "acceptEdits", "auto", "bypassPermissions", "default",
//	  "dontAsk", "plan")
//
// We probe once at server startup and cache the result. If the probe fails
// (CLI missing, output shape changed) we fall back to a built-in list.
type PermissionModes struct {
	Modes   []string `json:"modes"`
	Default string   `json:"default"`
	Source  string   `json:"source"` // "cli" | "fallback"
}

// fallbackModes is what we report when the CLI probe doesn't yield anything
// usable. Matches Claude Code 2.x at the time of writing.
var fallbackModes = []string{"default", "acceptEdits", "auto", "plan", "bypassPermissions"}

var modeChoicesPattern = regexp.MustCompile(`(?s)--permission-mode\s+<mode>.*?\(choices:\s*([^)]*)\)`)
var quotedString = regexp.MustCompile(`"([^"]+)"`)

// DetectPermissionModes runs `<binary> --help` and extracts the supported
// modes. Caches per-binary so we don't re-shell-out on every API request.
func DetectPermissionModes(binary string) PermissionModes {
	if binary == "" {
		binary = "claude"
	}
	if cached, ok := modeCacheGet(binary); ok {
		return cached
	}
	out := probeModes(binary)
	modeCacheSet(binary, out)
	return out
}

func probeModes(binary string) PermissionModes {
	defaultMode := "acceptEdits"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "--help")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Run(); err != nil {
		return PermissionModes{Modes: append([]string{}, fallbackModes...), Default: defaultMode, Source: "fallback"}
	}

	match := modeChoicesPattern.FindStringSubmatch(stdout.String())
	if len(match) < 2 {
		return PermissionModes{Modes: append([]string{}, fallbackModes...), Default: defaultMode, Source: "fallback"}
	}
	var modes []string
	for _, m := range quotedString.FindAllStringSubmatch(match[1], -1) {
		modes = append(modes, m[1])
	}
	if len(modes) == 0 {
		return PermissionModes{Modes: append([]string{}, fallbackModes...), Default: defaultMode, Source: "fallback"}
	}
	// Order modes for the UI: safest → most permissive. The CLI reports
	// alphabetically; we re-sort by our preferred axis.
	modes = orderModes(modes)
	if !contains(modes, defaultMode) {
		defaultMode = modes[0]
	}
	return PermissionModes{Modes: modes, Default: defaultMode, Source: "cli"}
}

// orderModes sorts known modes by ascending permissiveness; unknown modes
// land at the end in original order so future CLI additions still surface.
func orderModes(modes []string) []string {
	preferred := []string{"default", "plan", "acceptEdits", "dontAsk", "auto", "bypassPermissions"}
	rank := map[string]int{}
	for i, m := range preferred {
		rank[m] = i
	}
	out := make([]string, len(modes))
	copy(out, modes)
	// Stable: for unknowns, push past the end of the preferred list.
	const unknownRank = 1 << 16
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && lessRank(rank, unknownRank, out[j], out[j-1]) {
			out[j], out[j-1] = out[j-1], out[j]
			j--
		}
	}
	return out
}

func lessRank(rank map[string]int, unknown int, a, b string) bool {
	ra := rank[a]
	if _, ok := rank[a]; !ok {
		ra = unknown
	}
	rb := rank[b]
	if _, ok := rank[b]; !ok {
		rb = unknown
	}
	if ra != rb {
		return ra < rb
	}
	return strings.Compare(a, b) < 0
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ── per-binary cache (immutable values, so trivial sync.Map suffices) ──

var (
	modeCacheMu sync.Mutex
	modeCache   = map[string]PermissionModes{}
)

func modeCacheGet(binary string) (PermissionModes, bool) {
	modeCacheMu.Lock()
	defer modeCacheMu.Unlock()
	v, ok := modeCache[binary]
	return v, ok
}

func modeCacheSet(binary string, v PermissionModes) {
	modeCacheMu.Lock()
	defer modeCacheMu.Unlock()
	modeCache[binary] = v
}
