// Package agent defines the seam between the generic PTY runtime
// (internal/tab/agenttui) and the agent-specific knowledge of how to spawn,
// resume, and notify-hook one particular AI coding CLI (claude, and in later
// Sprints codex / opencode / gemini / user-defined agents).
//
// See docs/multi-agent-framework-design.md §4.1 for the design rationale:
// the PTY/ring/emulator/role/WS transport, tab.Provider/Registry, the
// runtime.PTYCommander/ExecCommander in-container exec path, and the
// notify.Hub are already agent-agnostic. This package concentrates the
// remaining agent-literal knowledge (binary path, CLI flags, hook schema,
// transcript layout) behind a single interface so a second agent can be
// added by implementing Adapter rather than copy-pasting the daemon.
package agent

import "os"

// Kind identifies an agent adapter: a built-in one ("claude", "codex", …)
// or a user-defined name from config.toml's [agents.<name>] section.
type Kind string

// NotifyLevel describes how much of the palmux Activity Inbox notification
// vocabulary an agent's hook/notify mechanism can drive.
type NotifyLevel string

const (
	// NotifyNone means the adapter has no notification mechanism at all.
	NotifyNone NotifyLevel = "none"
	// NotifyTurnEnd means the adapter can only signal "your turn" (a turn
	// completed); it cannot signal "waiting for permission".
	NotifyTurnEnd NotifyLevel = "turn_end"
	// NotifyFull means the adapter can signal both turn-completion and
	// permission-wait, matching Claude Code's Notification/Stop hooks.
	NotifyFull NotifyLevel = "full"
)

// Capabilities declares what an Adapter supports so callers (the Manager,
// and eventually the FE via GET /api/agents) can adapt behavior without a
// type switch on Kind.
type Capabilities struct {
	// Resume reports whether the adapter can resume a prior session by ID.
	// When false, SpawnIntent.ResumeSessionID is never populated by the
	// caller and a respawn always starts a fresh session.
	Resume bool
	// Notify is the richest notification level the adapter's hook
	// mechanism can drive.
	Notify NotifyLevel
	// InContainer reports whether the adapter can resolve a binary path
	// that exists inside an incus workspace container.
	InContainer bool
	// PermissionMode reports whether the adapter interprets
	// SpawnIntent.PermissionMode (false means the field is ignored).
	PermissionMode bool
}

// HookEnv carries the notification-hook wiring for one spawn: the palmux
// callback endpoint, optional auth token, the tab's identity (used by the
// hook handler to route a notification back to the originating tab), and
// the absolute path to the palmux binary used as the hook command.
//
// All fields are already resolved for host vs. in-container execution by
// the caller — an Adapter never has to know which (see agenttui.Daemon,
// which picks the container-reachable notify URL / hook binary path before
// building the intent).
type HookEnv struct {
	NotifyURL   string
	Token       string
	RepoID      string
	BranchID    string
	TabID       string
	TabName     string
	HookBinPath string
}

// SpawnIntent describes what should be spawned: a fresh session or a
// resume, in or out of a container, with what permission mode and hook
// wiring.
type SpawnIntent struct {
	// Worktree is the absolute path the subprocess should run in.
	Worktree string
	// ResumeSessionID is empty for a fresh spawn, or the session ID to
	// resume (only meaningful when Capabilities.Resume is true).
	ResumeSessionID string
	// InContainer reports whether this spawn runs inside a workspace
	// container (incus) rather than directly on the palmux host.
	InContainer bool
	// Hook carries the resolved notification-hook wiring. Adapters with
	// Capabilities.Notify == NotifyNone may ignore it entirely.
	Hook HookEnv
	// PermissionMode is the global permission-mode setting value. Adapters
	// that don't understand permission modes (Capabilities.PermissionMode
	// == false) ignore it.
	PermissionMode string
	// IsRespawn is true when this spawn is respawning an existing session
	// after a crash or a container regenerate, and false for the very first
	// spawn of a tab (agenttui.Daemon.EnsureStarted). Most adapters key
	// resume off ResumeSessionID (populated by a SessionDiscoverer) and can
	// ignore this field entirely — it exists for adapters like codex whose
	// CLI resumes "the most recently active session" without palmux ever
	// learning a session ID up front (`codex resume --last`), which needs to
	// distinguish "this is a fresh tab, start clean" from "this is a
	// redo-the-same-session respawn, try to pick back up".
	IsRespawn bool
}

// FileDrop is a file an Adapter needs written before spawn (e.g. a
// generated plugin config for a user-defined agent). Unused by the
// built-in claude adapter today; the generic PTY daemon writes any
// declared FileDrops under $HOME/palmux-managed paths before exec.
type FileDrop struct {
	Path    string
	Content []byte
	Mode    os.FileMode
}

// SpawnSpec is the fully-resolved execution plan for one spawn, built by an
// Adapter from a SpawnIntent.
type SpawnSpec struct {
	// Argv is the full command line; Argv[0] is the binary (may be an
	// absolute in-container path).
	Argv []string
	// Env is additional KEY=VALUE environment entries layered over the
	// inherited environment by the caller.
	Env []string
	// PreFiles are written before spawn.
	PreFiles []FileDrop
	// KillPattern is the pkill-style pattern runtime.ContainerProcessKiller
	// uses to reap a lingering in-container process on shutdown/respawn.
	// Empty means no targeted kill is attempted.
	KillPattern string
}

// Adapter is the seam between the generic PTY daemon (agenttui) and one AI
// coding agent's spawn/resume/hook knowledge.
type Adapter interface {
	// Kind returns the stable adapter identifier (also the tab type for
	// generic agent tabs — see design §4.4).
	Kind() Kind
	// DisplayName is the human-readable label (e.g. "Claude").
	DisplayName() string
	// Capabilities describes what this adapter supports.
	Capabilities() Capabilities
	// SpawnSpec builds the execution plan for intent. Called once per
	// spawn (fresh start, crash respawn, or container-regenerate respawn).
	SpawnSpec(intent SpawnIntent) (SpawnSpec, error)
}

// SessionDiscoverer is an optional capability: an adapter that can locate a
// session ID by watching a transcript directory for new/modified files. A
// Manager only creates a SessionWatcher when its Adapter implements this
// interface. Adapters without it should report Capabilities.Resume == false
// (or, in a later Sprint, derive a resume ID some other way).
type SessionDiscoverer interface {
	// TranscriptDir maps a worktree absolute path to the directory the
	// agent writes per-session transcripts to.
	TranscriptDir(worktree string) (string, error)
	// SessionIDFromPath reports whether the given file (as delivered by an
	// fsnotify event under TranscriptDir) names a valid session transcript,
	// and if so, the session ID.
	SessionIDFromPath(path string) (id string, ok bool)
}

// Configurable is an optional capability: an Adapter whose binary path and
// extra arguments can be hot-swapped after construction (e.g. from a
// settings/config change) without rebuilding the owning Manager.
type Configurable interface {
	SetBin(bin string)
	SetArgs(args []string)
}
