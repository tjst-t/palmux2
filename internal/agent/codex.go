package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// KindCodex is the built-in Codex CLI adapter kind (S339021-1).
const KindCodex Kind = "codex"

// CodexAdapter is the built-in Adapter for the `codex` CLI (OpenAI's Codex
// coding agent). It differs from ClaudeAdapter in two structural ways spiked
// live against codex-cli 0.144.1 (see docs/multi-agent-framework-design.md
// and this Sprint's decisions.json for the raw findings):
//
//   - Resume is NOT session-ID-driven the way claude's `--resume <uuid>` is.
//     `codex resume <uuid>` still works when an ID is known, but the CLI's
//     own crash-recovery idiom is `codex resume --last`, which resumes
//     "whatever session was most recently active in this cwd" without palmux
//     ever having to learn an ID. CodexAdapter does not implement
//     [SessionDiscoverer] — instead, on a respawn (SpawnIntent.IsRespawn),
//     SpawnSpec itself scans ~/.codex/sessions for the newest rollout whose
//     session_meta.cwd matches the worktree, falling back to `--last` (which
//     is cwd-scoped without --all) when no local match is found.
//   - Notify is turn-end only ([NotifyTurnEnd]): codex's `notify` mechanism
//     fires exactly one event, `agent-turn-complete`, with no
//     permission-wait signal in the TUI (unlike claude's Notification +
//     Stop). It is wired per-process via `-c
//     notify=["<hookbin>","hook","--agent=codex"]`, a TOML config override
//     that never touches ~/.codex/config.toml, mirroring how claude's
//     `--settings` JSON never touches ~/.claude/settings.json.
//
// In-container support (S339021): codex runs INSIDE a workspace container by
// bind-mounting the HOST codex install (binary + npm package tree, if any,
// plus ~/.codex auth) into the container at the identical path — no image
// bake required, mirroring claude's containerClaudeBin approach. See
// [CodexAdapter.ContainerBinary] / [CodexAdapter.SharedContainerPaths].
type CodexAdapter struct {
	mu   sync.RWMutex
	bin  string
	args []string

	// sessionsRoot overrides the default ~/.codex/sessions lookup root for
	// tests. Empty means "use the real default" (sessionsRootOrDefault).
	sessionsRoot string

	// binResolver overrides resolveHostBinary for tests (host-independent:
	// production code must not depend on whether codex happens to be
	// installed on the machine running `go test`). Defaults to
	// resolveHostBinary in NewCodexAdapter.
	binResolver func(string) (string, bool)
}

var (
	_ Adapter             = (*CodexAdapter)(nil)
	_ Configurable        = (*CodexAdapter)(nil)
	_ InContainerProvider = (*CodexAdapter)(nil)
)

// NewCodexAdapter creates a CodexAdapter. bin defaults to "codex" when empty;
// args are extra arguments appended to every spawn (after the resume/notify
// flags this adapter injects itself).
func NewCodexAdapter(bin string, args []string) *CodexAdapter {
	if bin == "" {
		bin = "codex"
	}
	return &CodexAdapter{bin: bin, args: append([]string(nil), args...), binResolver: resolveHostBinary}
}

// Kind returns "codex".
func (a *CodexAdapter) Kind() Kind { return KindCodex }

// DisplayName returns "Codex" (a fixed built-in label, like claude's
// "Claude" — a custom [AgentConfigEntry.DisplayName] for the reserved
// "codex" section name is intentionally ignored, mirroring how claude
// handles the same case).
func (a *CodexAdapter) DisplayName() string { return "Codex" }

// Capabilities reports resume + turn-end notify support. InContainer
// (S339021) is true exactly when the configured codex binary resolves on
// this host (ContainerBinary()) — codex's own host install (binary + npm
// package tree, if any) is bind-mounted into the workspace container at the
// identical path, no palmux-ws image bake required, mirroring how claude's
// binary rides the ~/.local/bin shared device. When it does not resolve
// (codex not installed on this host), the agenttui daemon's D12 guard
// (Sdec0a7-2) refuses to spawn a codex tab on an incus workspace with an
// explicit error rather than silently falling back to a host exec.
// PermissionMode is false: codex has no palmux-recognized permission-mode
// flag equivalent to claude's --permission-mode.
func (a *CodexAdapter) Capabilities() Capabilities {
	_, ok := a.ContainerBinary()
	return Capabilities{
		Resume:         true,
		Notify:         NotifyTurnEnd,
		InContainer:    ok,
		PermissionMode: false,
	}
}

// ContainerBinary resolves the configured codex bin to its real, symlink-
// resolved absolute host path (see resolveHostBinary). This is the exact
// path SpawnSpec uses as Argv[0] when intent.InContainer is true, and the
// exact path SharedContainerPaths bind-mounts into every workspace
// container.
func (a *CodexAdapter) ContainerBinary() (string, bool) {
	bin, _, _, resolver := a.snapshot()
	return resolver(bin)
}

// SharedContainerPaths returns the codex binary's own share (the whole npm
// package tree when codex was installed as an npm global package, else just
// the binary file — see binaryShareForContainer) plus ~/.codex (auth +
// session transcripts) when present, so an in-container codex is already
// logged in with the host's credentials and its resume-by-cwd-match scan
// (latestCodexSessionForCwd) sees the same sessions whether it runs on the
// host or inside a container.
func (a *CodexAdapter) SharedContainerPaths() []string {
	_, _, _, resolver := a.snapshot()
	var out []string
	if resolved, ok := a.ContainerBinary(); ok {
		out = append(out, containerSharesForBinary(resolved, resolver)...)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if dir := existingDir(filepath.Join(home, ".codex")); dir != "" {
			out = append(out, dir)
		}
	}
	return out
}

// SetBin hot-swaps the codex binary path used for spawns after this call
// (mirrors [ClaudeAdapter.SetBin]). Empty bin is a no-op.
func (a *CodexAdapter) SetBin(bin string) {
	if bin == "" {
		return
	}
	a.mu.Lock()
	a.bin = bin
	a.mu.Unlock()
}

// SetArgs hot-swaps the extra args passed to codex on every spawn.
func (a *CodexAdapter) SetArgs(args []string) {
	a.mu.Lock()
	a.args = append([]string(nil), args...)
	a.mu.Unlock()
}

func (a *CodexAdapter) snapshot() (bin string, args []string, sessionsRoot string, binResolver func(string) (string, bool)) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	resolver := a.binResolver
	if resolver == nil {
		resolver = resolveHostBinary
	}
	return a.bin, append([]string(nil), a.args...), a.sessionsRoot, resolver
}

// SpawnSpec builds the codex argv:
//
//	fresh:            <bin> [-c notify=[...]] <args...>
//	resume by ID:     <bin> [-c notify=[...]] resume <id> <args...>
//	respawn (no ID):  <bin> [-c notify=[...]] resume <uuid-found-by-cwd|--last> <args...>
//
// The notify `-c` flag, when present, is prepended before the resume
// subcommand/args (both forms accept -c per `codex --help` / `codex resume
// --help` — it is a global option, not subcommand-specific).
//
// In-container (intent.InContainer): argv[0] becomes the resolved
// ContainerBinary() path rather than the plain bin. A binary that fails to
// resolve here returns an explicit error (defense-in-depth — the agenttui
// daemon's D12 guard, driven by Capabilities().InContainer, is the primary
// enforcement point and should already prevent SpawnSpec from ever being
// called this way).
func (a *CodexAdapter) SpawnSpec(intent SpawnIntent) (SpawnSpec, error) {
	bin, base, sessionsRoot, binResolver := a.snapshot()

	var subArgs []string
	switch {
	case intent.ResumeSessionID != "":
		subArgs = []string{"resume", intent.ResumeSessionID}
	case intent.IsRespawn:
		if id, ok := latestCodexSessionForCwd(codexSessionsRootOrDefault(sessionsRoot), intent.Worktree); ok {
			subArgs = []string{"resume", id}
		} else {
			// cwd-scoped fallback (no --all): the most recent session
			// started in this worktree, or codex's own picker/no-op if
			// there truly is none.
			subArgs = []string{"resume", "--last"}
		}
	}

	argvBin := bin
	if intent.InContainer {
		resolved, ok := binResolver(bin)
		if !ok {
			return SpawnSpec{}, fmt.Errorf("agent: codex adapter: cannot resolve binary %q on this host; no in-container support", bin)
		}
		argvBin = resolved
	}

	argv := []string{argvBin}
	var env []string
	if intent.Hook.HookBinPath != "" && intent.Hook.NotifyURL != "" {
		argv = append(argv, "-c", codexNotifyConfigValue(intent.Hook.HookBinPath))
		env = hookEnv(intent.Hook.NotifyURL, intent.Hook.Token, intent.Hook.RepoID, intent.Hook.BranchID, intent.Hook.TabID)
		if intent.Hook.TabName != "" {
			env = append(env, "PALMUX_TAB_NAME="+intent.Hook.TabName)
		}
	}
	argv = append(argv, subArgs...)
	argv = append(argv, base...)

	return SpawnSpec{
		Argv:        argv,
		Env:         env,
		KillPattern: argvBin,
	}, nil
}

// codexNotifyConfigValue builds the `-c` value that injects codex's `notify`
// program override: `notify=["<hookBin>","hook","--agent=codex"]`. This is a
// per-process TOML config override (like claude's --settings JSON) — it
// never touches ~/.codex/config.toml.
func codexNotifyConfigValue(hookBin string) string {
	return "notify=" + tomlStringArray([]string{hookBin, "hook", "--agent=codex"})
}

// tomlStringArray renders items as a TOML array of basic strings, suitable
// for embedding in a `-c key=<value>` argv element.
func tomlStringArray(items []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(tomlQuoteString(it))
	}
	b.WriteByte(']')
	return b.String()
}

// tomlQuoteString renders s as a TOML basic string (double-quoted, with `"`
// and `\` escaped). Codex CLI argv elements are passed via exec's argv array
// (never a shell), so no shell-quoting is needed — only TOML string
// escaping, since `-c key=value`'s value is parsed as TOML.
func tomlQuoteString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// codexSessionsRootOrDefault returns override if non-empty, else
// ~/.codex/sessions (or "" if $HOME cannot be resolved, in which case
// latestCodexSessionForCwd's WalkDir simply finds nothing).
func codexSessionsRootOrDefault(override string) string {
	if override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// codexSessionMeta mirrors the first line of a codex rollout-*.jsonl
// transcript: {"type":"session_meta","payload":{"session_id":"...","cwd":"..."}}.
type codexSessionMeta struct {
	Type    string `json:"type"`
	Payload struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
	} `json:"payload"`
}

// latestCodexSessionForCwd scans sessionsRoot (recursively — codex lays
// transcripts out under YYYY/MM/DD/) for rollout-*.jsonl files whose first
// line's session_meta.cwd matches worktree, and returns the session_id of
// the one with the newest file mtime. Returns ("", false) when sessionsRoot
// doesn't exist, is unreadable, or no file matches — all treated as
// best-effort misses (the caller falls back to `resume --last`), never an
// error.
func latestCodexSessionForCwd(sessionsRoot, worktree string) (string, bool) {
	if sessionsRoot == "" || worktree == "" {
		return "", false
	}
	absWorktree, err := filepath.Abs(worktree)
	if err != nil {
		absWorktree = worktree
	}

	var bestID string
	var bestMTime time.Time
	_ = filepath.WalkDir(sessionsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort: skip unreadable entries, don't abort the walk
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		// Skip files that can't possibly beat the current best — but only
		// once we HAVE a best, since we haven't parsed this file's cwd yet.
		if bestID != "" && !info.ModTime().After(bestMTime) {
			return nil
		}
		id, cwd, ok := readCodexSessionMetaFirstLine(path)
		if !ok || id == "" || cwd == "" {
			return nil
		}
		cwdAbs, aerr := filepath.Abs(cwd)
		if aerr != nil {
			cwdAbs = cwd
		}
		if cwdAbs != absWorktree {
			return nil
		}
		if bestID == "" || info.ModTime().After(bestMTime) {
			bestID = id
			bestMTime = info.ModTime()
		}
		return nil
	})
	return bestID, bestID != ""
}

// readCodexSessionMetaFirstLine reads only the first line of path (a codex
// rollout transcript) and parses it as a session_meta record. Returns
// ok=false for anything that doesn't parse as the expected shape (a
// non-transcript file, an empty file, a truncated/corrupt first line, or a
// first line whose "type" isn't "session_meta") — all best-effort misses.
func readCodexSessionMetaFirstLine(path string) (sessionID, cwd string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer func() { _ = f.Close() }()

	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && line == "" {
		return "", "", false
	}
	var meta codexSessionMeta
	if jerr := json.Unmarshal([]byte(strings.TrimRight(line, "\n")), &meta); jerr != nil {
		return "", "", false
	}
	if meta.Type != "session_meta" || meta.Payload.SessionID == "" {
		return "", "", false
	}
	return meta.Payload.SessionID, meta.Payload.Cwd, true
}
