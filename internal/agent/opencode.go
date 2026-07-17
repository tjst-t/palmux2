package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// KindOpencode is the built-in opencode CLI adapter kind (S339021-2).
const KindOpencode Kind = "opencode"

// opencodeSessionListTimeout bounds the `<bin> session list --format json`
// subprocess SpawnSpec runs (best-effort, on a respawn only) to discover a
// cwd-matching session id. A stuck/misbehaving opencode binary must not hang
// tab startup indefinitely.
const opencodeSessionListTimeout = 5 * time.Second

// opencodeNotifyPluginRelPath is where the generated notify plugin JS is
// written under $HOME (never under ~/.config/opencode — the plugin is loaded
// per-process via OPENCODE_CONFIG_CONTENT, see SpawnSpec).
const opencodeNotifyPluginRelPath = ".local/share/palmux/opencode-plugins/palmux-notify.js"

// OpencodeAdapter is the built-in Adapter for the `opencode` CLI. It was
// spiked live against opencode 1.17.18 (see this Sprint's decisions.json for
// the raw findings, which diverge from the two assumptions the Sprint brief
// started with):
//
//   - Resume is NOT a flat `~/.local/share/opencode/storage/session/*.jsonl`
//     directory the way codex's rollout transcripts are — 1.17.18 stores
//     sessions in a SQLite db (~/.local/share/opencode/opencode.db). Rather
//     than add a SQLite driver dependency (or hand-roll a page-format
//     parser) to read that db directly, SpawnSpec shells out to the
//     officially supported `opencode session list --format json` (run with
//     cmd.Dir set to the worktree, mirroring how a real spawn from that
//     directory would resolve its own project) and filters the returned
//     {id, directory, updated} records for an exact directory match, picking
//     the newest by `updated`. Verified live: this DOES matter for
//     correctness — `opencode --continue` is NOT cwd-scoped (it resumes
//     whichever session across the whole machine has the highest
//     time_updated, observed hijacking an unrelated worktree's session in
//     manual testing), so it is used only as the last-resort fallback when
//     no cwd-matching session is found, exactly mirroring CodexAdapter's
//     `resume --last` fallback.
//   - The dedicated `"permission.ask"` Hooks callback documented in
//     @opencode-ai/plugin's TypeScript types is NEVER actually invoked in
//     opencode 1.17.18 (confirmed by instrumenting a live plugin: the
//     generic `event` hook fires `permission.asked` — a plain event-bus
//     broadcast, not the same-named dedicated hook — while the dedicated
//     hook produces no callback at all, headless or otherwise). The notify
//     plugin therefore drives BOTH signals off the single `event` hook:
//     `event.type === "session.idle"` for turn-end, `event.type ===
//     "permission.asked"` for permission-wait. This matches the wording of
//     this Sprint's own AC-S339021-2-2 ("permission.asked → permission"),
//     which (unlike the Sprint brief's prose) already named the event-bus
//     type correctly.
//
// Like CodexAdapter, OpencodeAdapter does not implement [SessionDiscoverer]:
// resume-by-cwd-match is entirely self-contained inside SpawnSpec (via the
// `session list` shell-out above), so there is no transcript directory to
// fsnotify-watch for a fresh session id.
//
// In-container support (S339021): opencode runs INSIDE a workspace
// container by bind-mounting the HOST opencode install (binary + npm
// package tree, if any, plus its auth/config dirs) into the container at
// the identical path — no image bake required, mirroring claude's
// containerClaudeBin approach. See [OpencodeAdapter.ContainerBinary] /
// [OpencodeAdapter.SharedContainerPaths]. The notify plugin FileDrop (see
// SpawnSpec) is written under a dedicated palmux-managed directory that
// SharedContainerPaths also shares, so the in-container opencode process
// can read the exact file the (always host-side) writeFileDrops call just
// wrote.
type OpencodeAdapter struct {
	mu   sync.RWMutex
	bin  string
	args []string

	// binResolver overrides resolveHostBinary for tests (host-independent:
	// production code must not depend on whether opencode happens to be
	// installed on the machine running `go test`). Defaults to
	// resolveHostBinary in NewOpencodeAdapter.
	binResolver func(string) (string, bool)
}

var (
	_ Adapter             = (*OpencodeAdapter)(nil)
	_ Configurable        = (*OpencodeAdapter)(nil)
	_ InContainerProvider = (*OpencodeAdapter)(nil)
)

// NewOpencodeAdapter creates an OpencodeAdapter. bin defaults to "opencode"
// when empty; args are extra arguments appended to every spawn (after the
// resume flags this adapter injects itself).
func NewOpencodeAdapter(bin string, args []string) *OpencodeAdapter {
	if bin == "" {
		bin = "opencode"
	}
	return &OpencodeAdapter{bin: bin, args: append([]string(nil), args...), binResolver: resolveHostBinary}
}

// Kind returns "opencode".
func (a *OpencodeAdapter) Kind() Kind { return KindOpencode }

// DisplayName returns "opencode" (a fixed built-in label, like claude's
// "Claude"/codex's "Codex" — a custom [AgentConfigEntry.DisplayName] for the
// reserved "opencode" section name is intentionally ignored).
func (a *OpencodeAdapter) DisplayName() string { return "opencode" }

// Capabilities reports resume + FULL notify support (turn-end AND
// permission-wait — see the type doc comment for how both are driven off
// opencode's generic `event` hook). InContainer (S339021) is true exactly
// when the configured opencode binary resolves on this host
// (ContainerBinary()) — like codex, opencode's own host install is bind-
// mounted into the workspace container at the identical path, no
// palmux-ws image bake required. When it does not resolve (opencode not
// installed on this host), the agenttui daemon's D12 guard refuses an
// in-container spawn with an explicit error rather than silently falling
// back to a host exec. PermissionMode is false: opencode has no
// palmux-recognized permission-mode flag equivalent to claude's
// --permission-mode (its own permission policy is session-config-driven,
// not a CLI flag SpawnSpec can set generically).
func (a *OpencodeAdapter) Capabilities() Capabilities {
	_, ok := a.ContainerBinary()
	return Capabilities{
		Resume:         true,
		Notify:         NotifyFull,
		InContainer:    ok,
		PermissionMode: false,
	}
}

// ContainerBinary resolves the configured opencode bin to its real,
// symlink-resolved absolute host path (see resolveHostBinary). This is the
// exact path SpawnSpec uses as Argv[0] when intent.InContainer is true, and
// the exact path SharedContainerPaths bind-mounts into every workspace
// container.
func (a *OpencodeAdapter) ContainerBinary() (string, bool) {
	bin, _, resolver := a.snapshot()
	return resolver(bin)
}

// SharedContainerPaths returns the opencode binary's own share (the whole
// npm package tree when opencode was installed as an npm global package,
// else just the binary file — see binaryShareForContainer), the auth/config
// dirs (~/.local/share/opencode holds opencode.db + auth.json;
// ~/.config/opencode holds opencode.jsonc + any plugin deps) when present,
// and the notify-plugin drop directory (MkdirAll'd here so it exists before
// the very first in-container spawn's OPENCODE_CONFIG_CONTENT references
// it — otherwise the first spawn could race the profile reconcile that
// picks up a directory writeFileDrops only creates at spawn time).
func (a *OpencodeAdapter) SharedContainerPaths() []string {
	_, _, resolver := a.snapshot()
	var out []string
	if resolved, ok := a.ContainerBinary(); ok {
		out = append(out, containerSharesForBinary(resolved, resolver)...)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	for _, rel := range []string{
		filepath.Join(".local", "share", "opencode"),
		filepath.Join(".config", "opencode"),
	} {
		if dir := existingDir(filepath.Join(home, rel)); dir != "" {
			out = append(out, dir)
		}
	}
	if pluginPath, perr := opencodeNotifyPluginPath(); perr == nil {
		pluginDir := filepath.Dir(pluginPath)
		if mkErr := os.MkdirAll(pluginDir, 0o755); mkErr == nil {
			out = append(out, pluginDir)
		}
	}
	return out
}

// SetBin hot-swaps the opencode binary path used for spawns after this call
// (mirrors [ClaudeAdapter.SetBin] / [CodexAdapter.SetBin]). Empty bin is a
// no-op.
func (a *OpencodeAdapter) SetBin(bin string) {
	if bin == "" {
		return
	}
	a.mu.Lock()
	a.bin = bin
	a.mu.Unlock()
}

// SetArgs hot-swaps the extra args passed to opencode on every spawn.
func (a *OpencodeAdapter) SetArgs(args []string) {
	a.mu.Lock()
	a.args = append([]string(nil), args...)
	a.mu.Unlock()
}

func (a *OpencodeAdapter) snapshot() (bin string, args []string, binResolver func(string) (string, bool)) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	resolver := a.binResolver
	if resolver == nil {
		resolver = resolveHostBinary
	}
	return a.bin, append([]string(nil), a.args...), resolver
}

// SpawnSpec builds the opencode argv, hook env, and notify-plugin FileDrop
// for intent:
//
//	fresh:                <bin> <args...>
//	resume by ID:         <bin> --session <id> <args...>
//	respawn (cwd match):  <bin> --session <id-found-by-cwd> <args...>
//	respawn (no match):   <bin> --continue <args...>
//
// When hook wiring is present (intent.Hook.HookBinPath and .NotifyURL both
// set), SpawnSpec additionally requests a PreFiles write of the notify
// plugin JS under $HOME/.local/share/palmux/opencode-plugins/ and sets
// OPENCODE_CONFIG_CONTENT to a minimal inline config that loads only that
// plugin — proven live (AC-S339021-2-1) to load the plugin without ever
// touching the user's ~/.config/opencode.
//
// In-container (intent.InContainer): argv[0] becomes the resolved
// ContainerBinary() path rather than the plain bin. A binary that fails to
// resolve here returns an explicit error (defense-in-depth — the agenttui
// daemon's D12 guard, driven by Capabilities().InContainer, is the primary
// enforcement point and should already prevent SpawnSpec from ever being
// called this way). Resume-by-cwd-match (latestOpencodeSessionForCwd) is
// always shelled out on the HOST using the plain configured bin — it reads
// the SAME ~/.local/share/opencode/opencode.db an in-container opencode
// process reads too (bind-mounted at the identical path by
// SharedContainerPaths), so host-side discovery is correct regardless of
// where the actual spawn will run.
func (a *OpencodeAdapter) SpawnSpec(intent SpawnIntent) (SpawnSpec, error) {
	bin, base, binResolver := a.snapshot()

	var resumeArgs []string
	switch {
	case intent.ResumeSessionID != "":
		resumeArgs = []string{"--session", intent.ResumeSessionID}
	case intent.IsRespawn:
		if id, ok := latestOpencodeSessionForCwd(bin, intent.Worktree); ok {
			resumeArgs = []string{"--session", id}
		} else {
			// Global-scope fallback (opencode's own --continue is NOT
			// cwd-scoped — verified live, see type doc comment): better
			// than nothing, matches CodexAdapter's `resume --last`
			// fallback philosophy, but callers should know this may
			// resume an unrelated worktree's session when no cwd match
			// exists.
			resumeArgs = []string{"--continue"}
		}
	}

	argvBin := bin
	if intent.InContainer {
		resolved, ok := binResolver(bin)
		if !ok {
			return SpawnSpec{}, fmt.Errorf("agent: opencode adapter: cannot resolve binary %q on this host; no in-container support", bin)
		}
		argvBin = resolved
	}

	argv := append([]string{argvBin}, resumeArgs...)
	argv = append(argv, base...)

	var env []string
	var preFiles []FileDrop
	if intent.Hook.HookBinPath != "" && intent.Hook.NotifyURL != "" {
		if pluginPath, perr := opencodeNotifyPluginPath(); perr == nil {
			preFiles = append(preFiles, FileDrop{
				Path:    pluginPath,
				Content: []byte(opencodeNotifyPluginJS),
				Mode:    0o644,
			})
			if configJSON, cerr := opencodeConfigContentValue(pluginPath); cerr == nil {
				env = append(env, "OPENCODE_CONFIG_CONTENT="+configJSON)
			}
		}
		env = append(env, hookEnv(intent.Hook.NotifyURL, intent.Hook.Token, intent.Hook.RepoID, intent.Hook.BranchID, intent.Hook.TabID)...)
		env = append(env, "PALMUX_HOOK_BIN="+intent.Hook.HookBinPath)
		if intent.Hook.TabName != "" {
			env = append(env, "PALMUX_TAB_NAME="+intent.Hook.TabName)
		}
	}

	return SpawnSpec{
		Argv:        argv,
		Env:         env,
		PreFiles:    preFiles,
		KillPattern: argvBin,
	}, nil
}

// opencodeNotifyPluginPath returns the absolute, palmux-managed path the
// notify plugin JS is written to — under $HOME so it survives across
// respawns and is reused (overwritten, not appended) on every spawn.
func opencodeNotifyPluginPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, filepath.FromSlash(opencodeNotifyPluginRelPath)), nil
}

// opencodeConfigContentValue builds the OPENCODE_CONFIG_CONTENT JSON value
// that loads exactly one plugin (pluginPath), per opencode's Config shape
// (`{"$schema":"...","plugin":["<path>"]}`) — proven live in AC-S339021-2-1.
func opencodeConfigContentValue(pluginPath string) (string, error) {
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"plugin":  []string{pluginPath},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// opencodeSessionListEntry is one element of `opencode session list --format
// json`'s output (fields beyond these three are ignored).
type opencodeSessionListEntry struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Updated   int64  `json:"updated"`
}

// latestOpencodeSessionForCwd shells out to `<bin> session list --format
// json` with the subprocess's working directory set to worktree (so
// opencode's own project-scoping logic resolves exactly as it would for a
// real spawn from that directory — verified live to include sessions whose
// `directory` exactly matches, even from a non-git scratch dir), and returns
// the id of the newest (`updated`) session whose `directory` field exactly
// matches worktree (absolute-path compared).
//
// Returns ("", false) for anything that isn't a clean, parseable, matching
// result — an empty/missing bin, a subprocess error or timeout, unparseable
// JSON, or simply no session in that exact directory — all treated as
// best-effort misses (the caller falls back to --continue), never a hard
// error. This mirrors CodexAdapter's latestCodexSessionForCwd fail-open
// contract, adapted to a subprocess call instead of a filesystem walk
// because opencode 1.17.18 stores sessions in a SQLite db rather than a
// scannable transcript directory (see the type doc comment).
func latestOpencodeSessionForCwd(bin, worktree string) (string, bool) {
	if bin == "" || worktree == "" {
		return "", false
	}
	absWorktree, err := filepath.Abs(worktree)
	if err != nil {
		absWorktree = worktree
	}

	ctx, cancel := context.WithTimeout(context.Background(), opencodeSessionListTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "session", "list", "--format", "json")
	cmd.Dir = absWorktree
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}

	var sessions []opencodeSessionListEntry
	if jerr := json.Unmarshal(out, &sessions); jerr != nil {
		return "", false
	}

	var bestID string
	var bestUpdated int64 = -1
	for _, s := range sessions {
		dirAbs, aerr := filepath.Abs(s.Directory)
		if aerr != nil {
			dirAbs = s.Directory
		}
		if dirAbs != absWorktree {
			continue
		}
		if bestID == "" || s.Updated > bestUpdated {
			bestID = s.ID
			bestUpdated = s.Updated
		}
	}
	return bestID, bestID != ""
}

// opencodeNotifyPluginJS is the notify plugin written to
// opencodeNotifyPluginPath() and loaded per-process via
// OPENCODE_CONFIG_CONTENT (never touching ~/.config/opencode). See the
// OpencodeAdapter type doc comment for why both signals are driven off the
// generic `event` hook rather than the (empirically dead) dedicated
// "permission.ask" Hooks callback.
const opencodeNotifyPluginJS = `// palmux-notify.js — injected per-process by palmux via OPENCODE_CONFIG_CONTENT
// (see internal/agent/opencode.go). Lives under a palmux-managed path
// ($HOME/.local/share/palmux/opencode-plugins/) and is never installed into
// ~/.config/opencode — the per-process inline config's "plugin" array loads
// this file for that one opencode process only.
//
// Forwards two opencode events to ` + "`palmux hook --agent=opencode`" + ` over
// that subprocess's stdin. Identity (which palmux tab this is) travels via
// the PALMUX_* env vars this plugin's own opencode process already has
// (injected by internal/agent.OpencodeAdapter.SpawnSpec) — they are
// inherited by the spawned hook subprocess automatically, so this plugin
// never has to read or forward them itself beyond PALMUX_HOOK_BIN (the path
// to the hook binary):
//   - event.type === "session.idle"      -> "your turn" (a turn finished)
//   - event.type === "permission.asked"  -> "permission wanted" (a tool call
//     needs approval)
//
// NOTE: opencode's TypeScript plugin types also declare a dedicated
// "permission.ask" Hooks callback, but it is never actually invoked in
// opencode 1.17.18 (verified live). The generic "event" hook's
// "permission.asked" event fires reliably instead, so that is what this
// plugin uses.
//
// Kept dependency-free (Node/Bun builtins only) — it is written to disk and
// loaded by opencode's own JS runtime, not compiled by palmux's Go toolchain.

import { spawn } from "node:child_process";

function notifyHook(payload) {
  const bin = process.env.PALMUX_HOOK_BIN;
  if (!bin) return;
  try {
    const child = spawn(bin, ["hook", "--agent=opencode"], {
      stdio: ["pipe", "ignore", "ignore"],
      detached: true,
    });
    child.on("error", () => {});
    child.stdin.on("error", () => {});
    child.stdin.write(JSON.stringify(payload));
    child.stdin.end();
    child.unref();
  } catch {
    // best-effort: a plugin hook must never throw into opencode's own event loop.
  }
}

export const PalmuxNotify = async () => {
  return {
    event: async ({ event }) => {
      if (!event) return;
      if (event.type === "session.idle") {
        notifyHook({
          type: "session.idle",
          sessionID: event.properties && event.properties.sessionID,
        });
        return;
      }
      if (event.type === "permission.asked") {
        const p = event.properties || {};
        const command = p.metadata && p.metadata.command;
        const message = command
          ? "opencode wants to run: " + command
          : "opencode needs permission" + (p.permission ? " (" + p.permission + ")" : "");
        notifyHook({
          type: "permission.asked",
          sessionID: p.sessionID,
          message: message,
        });
      }
    },
  };
};
`
