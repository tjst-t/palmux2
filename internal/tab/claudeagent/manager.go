package claudeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// BranchResolver yields the on-disk worktree path for a branch. Implemented
// by *store.Store; passed in as an interface so this package can stay free of
// store imports.
type BranchResolver interface {
	WorktreePath(repoID, branchID string) (string, error)
}

// EventPublisher is the subset of *store.EventHub claudeagent needs to
// fan branch-scoped state changes out to Drawer / Activity Inbox / etc.
// Implemented by main.go via a small adapter so we don't import store.
type EventPublisher interface {
	Publish(eventType, repoID, branchID string, payload any)
}

// Config bundles long-lived settings the Manager needs. Values are
// fall-through defaults — when the user has set per-branch prefs in
// sessions.json, those win.
type Config struct {
	Binary                string
	DefaultModel          string
	DefaultEffort         string
	DefaultPermissionMode string
	ExtraArgs             []string
	// AttachmentDirFn returns the absolute filesystem path of the
	// per-branch attachment upload dir
	// (`<attachmentUploadDir>/<repoId>/<branchId>`). The Manager passes
	// this path to the CLI as `--add-dir <path>` on every spawn so
	// uploaded files are inside Claude's tool boundary the moment they
	// land. Returning empty disables the auto-add (used by tests).
	// (S008-1-3.)
	AttachmentDirFn func(repoID, branchID string) string
}

// NotificationSink is the subset of notify.Hub claudeagent uses to surface
// permission requests / errors in the global Activity Inbox. Implemented
// by main.go's adapter; nil-safe.
type NotificationSink interface {
	IngestInternal(repoID, branchID string, n InternalNotification)
	ClearByRequestID(repoID, branchID, requestID string)
}

// InternalNotification is the shape claudeagent publishes to NotificationSink.
// Mirrors notify.IngestRequest + a stable request_id so the sink can
// dedupe/clear when the underlying request resolves.
type InternalNotification struct {
	RequestID string                       `json:"requestId,omitempty"`
	Type      string                       `json:"type"` // "urgent" | "warning" | "info"
	Title     string                       `json:"title,omitempty"`
	Message   string                       `json:"message,omitempty"`
	Detail    string                       `json:"detail,omitempty"`
	Actions   []InternalNotificationAction `json:"actions,omitempty"`
	// TabID / TabName identify which Claude tab fired the notification
	// (S009). Empty when the publish path doesn't have the dimension.
	TabID   string `json:"tabId,omitempty"`
	TabName string `json:"tabName,omitempty"`
}

// InternalNotificationAction is one inline button on a notification.
type InternalNotificationAction struct {
	Label  string `json:"label"`
	Action string `json:"action"`
}

// Manager owns one Agent per (repoID, branchID). It is the single entry
// point used by the WS handler and the REST handlers.
type Manager struct {
	cfg      Config
	store    *Store
	branches BranchResolver
	events   EventPublisher
	notify   NotificationSink
	logger   *slog.Logger

	mu     sync.Mutex
	agents map[string]*Agent
}

// NewManager constructs a Manager. `cfg.Binary == ""` falls back to "claude".
// `events` and `notify` are optional — Manager falls back to per-WS-only
// broadcast when nil (handy for tests).
func NewManager(cfg Config, store *Store, branches BranchResolver, events EventPublisher, notify NotificationSink, logger *slog.Logger) *Manager {
	if cfg.Binary == "" {
		cfg.Binary = "claude"
	}
	// DefaultModel "" lets the CLI pick its own default (Opus 4.7 1M ctx
	// at the time of writing). Pinning a string here would risk drift
	// when the CLI updates.
	if cfg.DefaultPermissionMode == "" {
		cfg.DefaultPermissionMode = "auto"
	}
	if cfg.DefaultEffort == "" {
		cfg.DefaultEffort = "xhigh"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		cfg:      cfg,
		store:    store,
		branches: branches,
		events:   events,
		notify:   notify,
		logger:   logger,
		agents:   map[string]*Agent{},
	}
}

// key returns the per-tab agent map key. Pre-S009 the dimension was
// `(repoID, branchID)`; now it's `(repoID, branchID, tabID)` so each
// Claude tab on the same branch owns a distinct Agent. Empty / legacy
// "claude" tab ids fold to CanonicalTabID for migration.
func (m *Manager) key(repoID, branchID, tabID string) string {
	return tabKey(repoID, branchID, tabID)
}

// Get returns the existing Agent for (repo, branch, tab), or nil. Empty
// tabID resolves to the canonical first tab.
func (m *Manager) Get(repoID, branchID, tabID string) *Agent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.agents[m.key(repoID, branchID, tabID)]
}

// EnsureAgent returns the existing Agent for (repo, branch, tab) or
// creates a fresh one. The Client is not spawned yet — the caller decides
// when (lazy on first message). Empty / legacy tabID folds to canonical.
func (m *Manager) EnsureAgent(repoID, branchID, tabID string) (*Agent, error) {
	tabID = CanonicaliseTabID(tabID)
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(repoID, branchID, tabID)
	if existing, ok := m.agents[k]; ok {
		return existing, nil
	}
	worktree, err := m.branches.WorktreePath(repoID, branchID)
	if err != nil {
		return nil, err
	}
	// S009: only the canonical (first) Claude tab inherits the
	// pre-existing per-branch resume pointer. Secondary tabs start
	// fresh — a brand-new session id will be allocated by the CLI on
	// first message.
	resumeID := m.store.ActiveFor(repoID, branchID, tabID)
	prefs := m.store.BranchPrefs(repoID, branchID, tabID)
	model := prefs.Model
	if model == "" {
		if meta, ok := m.store.Get(resumeID); ok {
			model = meta.Model
		}
	}
	if model == "" {
		model = m.cfg.DefaultModel
	}
	effort := prefs.Effort
	if effort == "" {
		effort = m.cfg.DefaultEffort
	}
	permMode := prefs.PermissionMode
	if permMode == "" {
		permMode = m.cfg.DefaultPermissionMode
	}
	a := newAgent(agentDeps{
		repoID:            repoID,
		branchID:          branchID,
		tabID:             tabID,
		worktree:          worktree,
		model:             model,
		effort:            effort,
		permissionMode:    permMode,
		resumeID:          resumeID,
		includeHookEvents: prefs.IncludeHookEvents,
		manager:           m,
		logger:            m.logger,
	})
	if cached := m.store.LastInit(); len(cached.Commands) > 0 || len(cached.Models) > 0 {
		a.session.SetInitInfo(cached)
	}
	if resumeID != "" {
		if path, err := transcriptPath(worktree, resumeID); err == nil {
			if turns, err := LoadTranscriptTurns(path); err == nil && len(turns) > 0 {
				a.session.SetTurns(turns)
				a.session.SetSessionID(resumeID)
			} else if err != nil {
				m.logger.Warn("claudeagent: LoadTranscriptTurns failed", "err", err, "session", resumeID)
			}
		}
	}
	m.agents[k] = a
	// Make sure the persisted tab list contains this tabID — covers the
	// canonical-tab cold start where OnBranchOpen seeded an in-memory
	// list but the on-disk record was empty. Silent on errors (the
	// in-memory state is already correct; persistence is for restart).
	m.ensurePersistedTab(repoID, branchID, tabID)
	return a, nil
}

// ensurePersistedTab is idempotent: it appends tabID to the per-branch
// list if not already present.
func (m *Manager) ensurePersistedTab(repoID, branchID, tabID string) {
	tabs := m.store.BranchTabs(repoID, branchID)
	for _, t := range tabs {
		if t == tabID {
			return
		}
	}
	tabs = append(tabs, tabID)
	if err := m.store.SetBranchTabs(repoID, branchID, tabs); err != nil {
		m.logger.Warn("claudeagent: SetBranchTabs failed", "err", err)
	}
}

// tabsForBranch returns the ordered set of Claude tab ids for this
// branch. Always non-empty: a fresh branch yields just the canonical
// id. Used by Provider.OnBranchOpen so recomputeTabs sees the persisted
// multi-tab layout. Called under no lock (Store is internally
// synchronised), with a copy returned.
func (m *Manager) tabsForBranch(repoID, branchID string) []string {
	tabs := m.store.BranchTabs(repoID, branchID)
	if len(tabs) == 0 {
		return []string{CanonicalTabID}
	}
	return tabs
}

// AddTabForBranch appends a new tab to the branch's Claude tab list and
// returns it. The id is auto-picked (`claude:claude-2`, `claude:claude-3`, …).
// Used by the store's MultiTabHook implementation.
func (m *Manager) AddTabForBranch(repoID, branchID string) (string, error) {
	tabs := m.store.BranchTabs(repoID, branchID)
	if len(tabs) == 0 {
		tabs = []string{CanonicalTabID}
	}
	existing := map[string]bool{}
	for _, t := range tabs {
		existing[t] = true
	}
	newID := pickNextClaudeTabID(existing)
	tabs = append(tabs, newID)
	if err := m.store.SetBranchTabs(repoID, branchID, tabs); err != nil {
		return "", err
	}
	return newID, nil
}

// RemoveTabForBranch tears down the agent (if any) for the given tab id
// and removes it from the persisted list. Errors only on store failure;
// removing an unknown tab is a no-op.
func (m *Manager) RemoveTabForBranch(ctx context.Context, repoID, branchID, tabID string) error {
	tabID = CanonicaliseTabID(tabID)
	tabs := m.store.BranchTabs(repoID, branchID)
	out := tabs[:0]
	for _, t := range tabs {
		if t != tabID {
			out = append(out, t)
		}
	}
	if err := m.store.SetBranchTabs(repoID, branchID, out); err != nil {
		return err
	}
	// Drop the agent + active pointer. The transcript on disk stays so
	// the closed session id remains resumable from the history popup.
	m.mu.Lock()
	a, ok := m.agents[m.key(repoID, branchID, tabID)]
	delete(m.agents, m.key(repoID, branchID, tabID))
	m.mu.Unlock()
	if ok {
		a.Shutdown()
	}
	_ = m.store.ClearActive(repoID, branchID, tabID)
	_ = ctx // reserved for future cleanup that needs cancellation
	return nil
}

// pickNextClaudeTabID returns the next available `claude:claude-N` id.
// `claude:claude` (the canonical first tab) is reserved for slot 1.
func pickNextClaudeTabID(existing map[string]bool) string {
	for i := 2; i < 1_000_000; i++ {
		candidate := fmt.Sprintf("%s:%s-%d", TabType, TabType, i)
		if !existing[candidate] {
			return candidate
		}
	}
	return CanonicalTabID + "-overflow"
}

// KillBranch terminates every Agent owned by the given branch. Used from
// Provider.OnBranchClose. After the agents are shut down we also remove
// the per-branch attachment upload directory (S008-1-10): once the
// branch is gone the user has no UI surface to access these files from,
// so leaving them around just leaks disk.
//
// S009: each branch may own multiple Claude tabs; we walk the agent map
// and shut down every entry whose key starts with `repoID/branchID/`.
// The persisted BranchTabs entry is also dropped so a re-Open of this
// branch starts from a fresh canonical tab.
func (m *Manager) KillBranch(_ context.Context, repoID, branchID string) error {
	prefix := repoID + "/" + branchID + "/"
	m.mu.Lock()
	var doomed []*Agent
	for k, a := range m.agents {
		if strings.HasPrefix(k, prefix) {
			doomed = append(doomed, a)
			delete(m.agents, k)
		}
	}
	m.mu.Unlock()
	for _, a := range doomed {
		a.Shutdown()
	}
	if fn := m.cfg.AttachmentDirFn; fn != nil {
		if dir := fn(repoID, branchID); dir != "" {
			if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
				m.logger.Warn("claudeagent: remove attachment dir failed",
					"dir", dir, "err", err)
			}
		}
	}
	// Forget the persisted tab list so a re-Open starts clean.
	if err := m.store.SetBranchTabs(repoID, branchID, nil); err != nil {
		m.logger.Warn("claudeagent: SetBranchTabs(nil) on KillBranch failed", "err", err)
	}
	return nil
}

// Shutdown stops every Agent. Called on server shutdown.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	agents := m.agents
	m.agents = map[string]*Agent{}
	m.mu.Unlock()
	for _, a := range agents {
		a.Shutdown()
	}
}

// Store exposes the underlying persistence store (used by REST handlers).
func (m *Manager) Store() *Store { return m.store }

// Config exposes the runtime config (binary path, defaults).
func (m *Manager) Config() Config { return m.cfg }

// Branches exposes the BranchResolver so REST handlers can resolve
// {repoID, branchID} → worktree path (e.g. for transcript lookup).
func (m *Manager) Branches() BranchResolver { return m.branches }

// validateAddDirs verifies that every entry in dirs resolves inside the
// branch's worktree (using the same EvalSymlinks-based check the Files
// tab runs). Returns the cleaned, absolute, deduped paths in input
// order, or an error naming the first offending entry.
//
// The caller (WS handler / future REST handler) is the auth surface;
// auth itself is enforced one layer higher in the HTTP middleware. We
// only check that the path is *within* the worktree — the user already
// has shell access on the host (single-user assumption from VISION),
// so the goal is to prevent accidental traversal, not to lock the user
// out of their own filesystem. See decision D-3 in the S006 log.
func (m *Manager) validateAddDirs(repoID, branchID string, dirs []string) ([]string, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	worktree, err := m.branches.WorktreePath(repoID, branchID)
	if err != nil {
		return nil, err
	}
	rootResolved, err := evalSymlinksOrSelf(worktree)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(dirs))
	seen := map[string]struct{}{}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		// Reject obviously-traversal forms before EvalSymlinks decides.
		if strings.Contains(d, "/../") || strings.HasSuffix(d, "/..") || d == ".." || strings.HasPrefix(d, "../") {
			return nil, fmt.Errorf("claudeagent: parent traversal in %q", d)
		}
		var abs string
		if filepath.IsAbs(d) {
			abs = filepath.Clean(d)
		} else {
			abs = filepath.Clean(filepath.Join(worktree, d))
		}
		// Confirm the path stays inside the worktree after symlink
		// resolution. Non-existent paths are accepted (CLI may create
		// them later); we still apply the prefix check on the
		// pre-resolve absolute path.
		resolved, _ := filepath.EvalSymlinks(abs)
		if resolved == "" {
			resolved = abs
		}
		if !strings.HasPrefix(resolved+string(filepath.Separator), rootResolved+string(filepath.Separator)) && resolved != rootResolved {
			return nil, fmt.Errorf("claudeagent: %q is outside worktree %q", d, worktree)
		}
		if _, dup := seen[abs]; dup {
			continue
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}
	return out, nil
}

// evalSymlinksOrSelf returns the symlink-resolved absolute path or the
// input unchanged if resolution fails (e.g. dir doesn't exist yet).
func evalSymlinksOrSelf(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p, nil
	}
	return resolved, nil
}

// ──────────── Agent ────────────────────────────────────────────────────────

type agentDeps struct {
	repoID, branchID, tabID string
	worktree                string
	model                   string
	effort                  string
	permissionMode          string
	resumeID                string
	// includeHookEvents seeds the agent with the persisted opt-in for
	// --include-hook-events. The agent stores the live value in its own
	// mu-guarded field so toggles take effect on the next respawn without
	// reaching back into the store every time we spawn a CLI.
	includeHookEvents bool
	manager           *Manager
	logger            *slog.Logger
}

// Agent is one branch's stateful pairing of (Session, optional Client). The
// WS handler subscribes to its broadcast channel; the Manager owns its
// lifecycle.
type Agent struct {
	deps agentDeps

	mu       sync.Mutex
	client   *Client
	session  *Session
	starting bool

	// fan-out: each WS connection registers a channel; all events get cloned
	// into every channel using oldest-drop on overflow.
	subsMu sync.RWMutex
	subs   map[chan AgentEvent]struct{}

	// CLI permission requests block on a per-permission channel until the
	// browser answers. The map lives behind a.mu.
	permWaiters map[string]chan canUseToolResponse

	// intentionalRespawn is flipped on by respawnClient before it kills the
	// current Client. watchClient checks it to suppress the "Claude CLI
	// exited" error event that would otherwise alarm the user.
	intentionalRespawn bool

	// pendingFork is set by ForkSession; consumed by the next EnsureClient
	// to spawn the CLI with --fork-session.
	pendingFork bool

	// includeHookEvents is the live opt-in flag for the --include-hook-events
	// CLI argument. Mirrors agentDeps.includeHookEvents at construction;
	// updated at runtime when the user flips the toggle in the Settings
	// popup. The next CLI spawn (respawn or first lazy spawn) reads this
	// under a.mu — see EnsureClient.
	includeHookEvents bool

	// addDirs is the set of absolute filesystem paths the CLI was last
	// spawned with as `--add-dir <path>`. Used by SendUserMessage to
	// decide whether to respawn: if the next user.message ships a wider
	// addDirs set, we respawn so the new dirs become accessible. We
	// only ever grow the set within an Agent's lifetime — shrinking
	// requires a fresh /clear or branch switch (rationale in
	// docs/sprint-logs/S006/decisions.md, decision D-7).
	addDirs []string
}

func newAgent(deps agentDeps) *Agent {
	sess := NewSession(deps.repoID, deps.branchID, deps.resumeID, deps.model, deps.permissionMode)
	if deps.effort != "" {
		sess.SetEffort(deps.effort)
	}
	return &Agent{
		deps:              deps,
		session:           sess,
		subs:              map[chan AgentEvent]struct{}{},
		includeHookEvents: deps.includeHookEvents,
	}
}

// Subscribe registers a new WS connection and returns the receive channel
// plus an unsubscribe func. The buffer is small but we drop oldest on
// overflow so a slow client can't block the broadcaster.
func (a *Agent) Subscribe() (<-chan AgentEvent, func()) {
	ch := make(chan AgentEvent, 64)
	a.subsMu.Lock()
	a.subs[ch] = struct{}{}
	a.subsMu.Unlock()
	return ch, func() {
		a.subsMu.Lock()
		if _, ok := a.subs[ch]; ok {
			delete(a.subs, ch)
			close(ch)
		}
		a.subsMu.Unlock()
	}
}

func (a *Agent) broadcast(ev AgentEvent) {
	a.subsMu.RLock()
	defer a.subsMu.RUnlock()
	for ch := range a.subs {
		select {
		case ch <- ev:
		default:
			// drop oldest, then push
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

func (a *Agent) broadcastMany(evs []AgentEvent) {
	for _, ev := range evs {
		a.broadcast(ev)
	}
}

// Snapshot returns a SessionInitPayload for the current state.
func (a *Agent) Snapshot() SessionInitPayload {
	return a.session.Snapshot()
}

// SetAuthStatus is called by the WS handler before snapshotting so the UI
// knows whether to show the setup hint.
func (a *Agent) SetAuthStatus(s AuthStatus) {
	a.session.SetAuthStatus(s)
}

// EnsureClient lazily spawns the CLI subprocess on first user message. The
// session_id, model, and permission_mode used come from the Session.
func (a *Agent) EnsureClient(ctx context.Context) error {
	a.mu.Lock()
	if a.client != nil {
		a.mu.Unlock()
		return nil
	}
	if a.starting {
		a.mu.Unlock()
		return errors.New("claudeagent: client is already starting")
	}
	a.starting = true
	a.mu.Unlock()

	a.session.SetStatus(StatusStarting)
	a.broadcastStatus(StatusStarting)

	resumeID := a.session.SessionID()
	if resumeID == "" {
		resumeID = a.deps.manager.store.ActiveFor(a.deps.repoID, a.deps.branchID, a.deps.tabID)
	}
	model := a.session.Model()
	if model == "" {
		model = a.deps.manager.cfg.DefaultModel
	}

	a.mu.Lock()
	fork := a.pendingFork
	a.pendingFork = false
	includeHooks := a.includeHookEvents
	// Snapshot a copy of addDirs so we don't hand the live slice to
	// NewClient (which would race with the next SendUserMessage). The
	// CLI startup path is a one-shot read.
	addDirsSnapshot := append([]string(nil), a.addDirs...)
	a.mu.Unlock()
	// S008-1-3: always include the per-branch attachment upload dir
	// so files dropped via /api/upload are inside Claude's allowed
	// tool surface from the very first message — no respawn needed
	// per upload. We mkdir the path if missing; the CLI tolerates a
	// non-existent --add-dir at startup but Read tools won't see new
	// files inside it without a directory entry.
	if fn := a.deps.manager.cfg.AttachmentDirFn; fn != nil {
		if attDir := fn(a.deps.repoID, a.deps.branchID); attDir != "" {
			if err := os.MkdirAll(attDir, 0o755); err != nil {
				a.deps.logger.Warn("claudeagent: mkdir attachment dir failed",
					"dir", attDir, "err", err)
			}
			// Don't push it into a.addDirs — the user-visible respawn
			// machinery only triggers on user-supplied dirs. We just
			// hand the merged list to NewClient.
			merged := append([]string(nil), addDirsSnapshot...)
			seen := map[string]struct{}{}
			for _, d := range merged {
				seen[d] = struct{}{}
			}
			if _, dup := seen[attDir]; !dup {
				merged = append(merged, attDir)
			}
			addDirsSnapshot = merged
		}
	}
	cli, err := NewClient(ctx, ClientOptions{
		Binary:            a.deps.manager.cfg.Binary,
		Cwd:               a.deps.worktree,
		SessionID:         resumeID,
		Model:             model,
		PermissionMode:    a.session.PermissionMode(),
		Effort:            a.session.Effort(),
		Fork:              fork,
		IncludeHookEvents: includeHooks,
		AddDirs:           addDirsSnapshot,
		ExtraArgs:         a.deps.manager.cfg.ExtraArgs,
		Logger:            a.deps.logger,
	}, a.handleStreamMsg, a.handleCanUseTool, a)
	if err != nil {
		a.mu.Lock()
		a.starting = false
		a.mu.Unlock()
		a.session.SetStatus(StatusError)
		a.broadcastStatus(StatusError)
		a.broadcastError("Failed to start Claude CLI", err.Error())
		return err
	}

	a.mu.Lock()
	a.client = cli
	a.starting = false
	a.mu.Unlock()

	go a.watchClient(cli)

	initCtx, cancel := context.WithTimeout(ctx, controlRequestTimeout)
	defer cancel()
	resp, err := cli.Initialize(initCtx)
	if err != nil {
		a.deps.logger.Warn("claudeagent: initialize failed", "err", err)
	} else if len(resp) > 0 {
		info := parseInitInfo(resp)
		a.session.SetInitInfo(info)
		// Push to any connected WS client so the slash popup updates
		// without waiting for a fresh snapshot.
		if ev, e := makeEvent(EvInitInfo, info); e == nil {
			a.broadcast(ev)
		}
		// Persist for the next agent (potentially across restarts) so
		// the slash menu is non-empty before the lazy spawn.
		if err := a.deps.manager.store.SetLastInit(info); err != nil {
			a.deps.logger.Warn("claudeagent: SetLastInit failed", "err", err)
		}
	}
	// Flip out of "starting" — the CLI is now ready for input. (Stream
	// events from a real turn will subsequently set thinking / tool_running
	// / etc.) If the user is mid-turn we don't clobber the status, but in
	// practice EnsureClient is only entered when no client existed so
	// idle is the right resting state.
	if a.session.Status() == StatusStarting {
		a.session.SetStatus(StatusIdle)
		a.broadcastStatus(StatusIdle)
	}
	return nil
}

func (a *Agent) watchClient(cli *Client) {
	<-cli.Done()
	a.mu.Lock()
	if a.client == cli {
		a.client = nil
	}
	intentional := a.intentionalRespawn
	a.intentionalRespawn = false
	a.mu.Unlock()
	a.session.SetStatus(StatusIdle)
	a.broadcastStatus(StatusIdle)

	// Stale --resume session_id: the CLI emitted "No conversation found
	// with session ID" and exited 1. Drop the active pointer so the next
	// EnsureClient starts fresh (no --resume), surface a less-alarming
	// notice, and kick off the respawn ourselves so the user doesn't have
	// to type a message just to recover.
	if cli.InvalidResume() {
		oldID := a.session.SessionID()
		a.session.Reset()
		_ = a.deps.manager.store.ClearActive(a.deps.repoID, a.deps.branchID, a.deps.tabID)
		a.deps.logger.Info("claudeagent: stored session_id is stale, dropping active pointer",
			"branch", a.deps.branchID, "tab", a.deps.tabID, "stale_session", oldID)
		if ev, err := makeEvent(EvSessionReplaced, SessionReplacedPayload{OldSessionID: oldID, NewSessionID: ""}); err == nil {
			a.broadcast(ev)
		}
		a.publishEvent(EventClaudeSessionReplaced, map[string]any{
			"oldSessionId": oldID,
			"newSessionId": "",
		})
		// Kick a fresh spawn. If this also fails (e.g. auth issue) the
		// regular error path takes over.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := a.EnsureClient(ctx); err != nil {
				a.deps.logger.Warn("claudeagent: fresh spawn after stale resume failed", "err", err)
			}
		}()
		return
	}

	if err := cli.ExitErr(); err != nil && !intentional {
		a.broadcastError("Claude CLI exited", err.Error())
	}
}

func (a *Agent) handleStreamMsg(msg streamMsg) {
	if msg.Type == "system" && msg.Subtype == "init" && msg.SessionID != "" {
		// Persist the new session id for resume.
		_ = a.deps.manager.store.SetActive(a.deps.repoID, a.deps.branchID, a.deps.tabID, msg.SessionID, a.session.Model())
	}
	beforeStatus := a.session.Status()
	evs := processStreamMessage(a.session, msg)
	a.broadcastMany(evs)
	// processStreamMessage flips status via session.SetStatus directly
	// (e.g. message_start → thinking, content_block_start tool_use →
	// tool_running, result → idle). Per-WS subscribers see those via the
	// EvStatusChange events returned above, but the global EventHub
	// (Drawer pip / Activity Inbox) needs an explicit publish — without
	// it, branches stay stuck on "thinking" after a turn completes.
	if afterStatus := a.session.Status(); afterStatus != beforeStatus {
		a.publishEvent(string(EventClaudeStatus), map[string]any{
			"status": string(afterStatus),
		})
	}

	if msg.Type == "result" {
		_ = a.deps.manager.store.UpdateMeta(a.session.SessionID(), func(m *SessionMeta) {
			m.LastActivityAt = time.Now().UTC()
			m.TurnCount++
			m.TotalCostUSD += msg.TotalCostUSD
		})
		// Cross-tab notification: turn-end carries cost / duration / error
		// flag so the Drawer / Inbox can react. The full per-block stream
		// stays on the WS only.
		a.publishEvent(EventClaudeTurnEnd, map[string]any{
			"isError":      msg.IsError,
			"totalCostUsd": msg.TotalCostUSD,
			"durationMs":   msg.DurationMs,
		})
		// Activity-Inbox notification — "Claude is ready". Lets the user
		// switch tabs / contexts during a long turn and come back when
		// it's done. The Inbox auto-clears as soon as the Claude tab is
		// focused, so users actively reading the conversation don't see
		// stale entries pile up.
		if !msg.IsError {
			a.publishNotification(InternalNotification{
				Type:    "info",
				Title:   "Claude is ready",
				Message: turnEndPreview(msg.Result),
			})
		}
	}
}

// turnEndPreview compresses the assistant's final reply into a short
// one-line preview for the Activity Inbox / browser notification.
func turnEndPreview(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Collapse whitespace.
	out := make([]rune, 0, len(s))
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		if r == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		out = append(out, r)
	}
	if len(out) > 100 {
		return string(out[:99]) + "…"
	}
	return string(out)
}

func (a *Agent) handleCanUseTool(_ context.Context, req canUseToolRequest, cliRequestID string) (canUseToolResponse, error) {
	if a.session.IsAllowedThisSession(req.ToolName) {
		return canUseToolResponse{Behavior: "allow"}, nil
	}
	permID, _, _ := a.session.AddPermissionRequest(cliRequestID, req.ToolName, req.Input)
	a.session.SetStatus(StatusAwaitingPermission)
	a.broadcastStatus(StatusAwaitingPermission)
	if ev, err := makeEvent(EvPermissionRequest, PermissionRequestPayload{
		PermissionID: permID,
		ToolName:     req.ToolName,
		Input:        req.Input,
	}); err == nil {
		a.broadcast(ev)
	}
	resp, err := a.awaitPermission(permID)
	a.session.SetStatus(StatusThinking)
	a.broadcastStatus(StatusThinking)
	return resp, err
}

// RequestPermission satisfies PermissionRequester. The MCP `tools/call`
// handler calls this when the CLI asks whether a tool may run; the flow
// mirrors handleCanUseTool but returns the MCP-shaped permissionResponse.
// Session-scoped allow-listing is honoured here too.
//
// AskUserQuestion is special-cased: instead of the generic Allow/Deny
// permission UI we route it to the ask flow. The waiter is resolved
// from `AnswerAskQuestion` once the user picks an option; we package
// the chosen labels into the tool's `input.questionAnswers` field so
// the CLI tool implementation can read the answer when it executes.
func (a *Agent) RequestPermission(_ context.Context, toolName string, input json.RawMessage, toolUseID string) (permissionResponse, error) {
	if a.session.IsAllowedThisSession(toolName) {
		return permissionResponse{Behavior: "allow"}, nil
	}
	if isAskQuestionToolName(toolName) {
		return a.requestAskAnswer(toolName, input, toolUseID)
	}
	if isPlanToolName(toolName) {
		return a.requestPlanResponse(toolName, input, toolUseID)
	}
	permID, _, _ := a.session.AddPermissionRequest(toolUseID, toolName, input)
	a.session.SetStatus(StatusAwaitingPermission)
	a.broadcastStatus(StatusAwaitingPermission)
	if ev, err := makeEvent(EvPermissionRequest, PermissionRequestPayload{
		PermissionID: permID,
		ToolName:     toolName,
		Input:        input,
	}); err == nil {
		a.broadcast(ev)
	}
	// Cross-tab fan-out: Drawer / Inbox / etc.
	a.publishEvent(EventClaudePermissionRequest, map[string]any{
		"permissionId": permID,
		"toolName":     toolName,
		"input":        json.RawMessage(input),
	})
	a.publishNotification(InternalNotification{
		RequestID: permID,
		Type:      "urgent",
		Title:     "Tool permission needed",
		Message:   summariseToolForNotification(toolName, input),
		Actions: []InternalNotificationAction{
			{Label: "Allow", Action: "claude.permission.allow:" + permID},
			{Label: "Deny", Action: "claude.permission.deny:" + permID},
		},
	})
	resp, err := a.awaitPermission(permID)
	// The notification (if any) is no longer actionable — clear it so the
	// Inbox doesn't keep nagging.
	a.clearNotification(permID)
	a.publishEvent(EventClaudePermissionResolved, map[string]any{
		"permissionId": permID,
		"decision":     resp.Behavior,
	})
	// Status flips back when the next stream event arrives; if the user
	// chose deny the CLI may immediately produce a tool_result and a fresh
	// thinking pass, so we don't try to predict it here.
	a.broadcastStatus(a.session.Status())
	if err != nil {
		return permissionResponse{Behavior: "deny", Message: err.Error()}, nil
	}
	out := permissionResponse{Behavior: resp.Behavior, Message: resp.Message}
	if len(resp.UpdatedInput) > 0 {
		out.UpdatedInput = resp.UpdatedInput
	}
	return out, nil
}

// requestAskAnswer is the AskUserQuestion-specific branch of
// RequestPermission. We route around the generic permission UI:
//   - the kind:"ask" block is already in the session (added when the
//     content_block_start envelope landed)
//   - we register a permission_id, attach it to that block, and emit
//     ask.question so the frontend enables its action row
//   - the waiter is resolved from AnswerAskQuestion when the user picks
//     an option; the chosen labels are packaged into the tool input
//     under `questionAnswers` so the CLI's AskUserQuestion tool body
//     can read them when it executes.
func (a *Agent) requestAskAnswer(toolName string, input json.RawMessage, toolUseID string) (permissionResponse, error) {
	_ = toolName
	// Bypass-only: no kind:"permission" block is added — the kind:"ask"
	// block carries the UI for this request. RegisterPendingPermission
	// minted permID + records cliRequestID; AttachAskPermission stamps
	// the id onto the existing ask block created by content_block_start.
	permID := a.session.RegisterPendingPermission(toolUseID)
	a.session.RegisterAskPermission(permID, toolUseID)
	turnID, blockID := a.session.AttachAskPermission(toolUseID, permID)

	a.session.SetStatus(StatusAwaitingPermission)
	a.broadcastStatus(StatusAwaitingPermission)

	// Tell the frontend a question is now answerable. We send `questions`
	// inline so a freshly-connected client can render the action row
	// without needing the prior content_block_start.
	if ev, err := makeEvent(EvAskQuestion, AskQuestionPayload{
		PermissionID: permID,
		BlockID:      blockID,
		TurnID:       turnID,
		ToolUseID:    toolUseID,
		Questions:    extractAskQuestionsRaw(input),
	}); err == nil {
		a.broadcast(ev)
	}
	// Activity-Inbox notification — surfacing AskUserQuestion in the same
	// channel as permission prompts so a multi-tab user sees it from
	// anywhere. The Inbox row clears once an answer is recorded.
	a.publishNotification(InternalNotification{
		RequestID: permID,
		Type:      "urgent",
		Title:     "Claude is asking a question",
		Message:   summariseAskQuestionForNotification(input),
	})

	resp, err := a.awaitPermission(permID)
	a.clearNotification(permID)
	a.broadcastStatus(a.session.Status())
	if err != nil {
		return permissionResponse{Behavior: "deny", Message: err.Error()}, nil
	}
	// The handler that resolves the waiter (AnswerAskQuestion) packs the
	// CLI-bound shape into resp.UpdatedInput. If for any reason it's
	// empty, fall back to the original input so the MCP CLI doesn't
	// reject our response.
	out := permissionResponse{Behavior: resp.Behavior, Message: resp.Message}
	if len(resp.UpdatedInput) > 0 {
		out.UpdatedInput = resp.UpdatedInput
	} else if len(input) > 0 {
		out.UpdatedInput = input
	}
	return out, nil
}

// requestPlanResponse is the ExitPlanMode-specific branch of
// RequestPermission. Mirrors requestAskAnswer:
//   - the kind:"plan" block was already added to the session when the
//     content_block_start envelope landed (normalize.go re-tags
//     ExitPlanMode tool_use → kind:"plan");
//   - we register a permission_id (no extra UI block — the plan block IS
//     the UI), attach it to the plan block, and emit `plan.question` so
//     the frontend enables its action row;
//   - the waiter is resolved from `AnswerPlanResponse` when the user
//     clicks Approve / Keep planning.
//
// On approve, the chosen TargetMode (default "auto") is applied via a
// goroutine-spawned SetPermissionMode call and the EditedPlan (when
// non-empty) is shipped as updatedInput.plan so the executing CLI sees
// the user's final markdown — same updatedInput plumbing the ask path
// uses for questionAnswers.
//
// On reject, behavior:"deny" is returned with a "User chose to keep
// planning" message; permission_mode stays at "plan" so the agent
// continues drafting.
func (a *Agent) requestPlanResponse(toolName string, input json.RawMessage, toolUseID string) (permissionResponse, error) {
	_ = toolName
	permID := a.session.RegisterPendingPermission(toolUseID)
	a.session.RegisterPlanPermission(permID, toolUseID)
	turnID, blockID := a.session.AttachPlanPermission(toolUseID, permID)

	a.session.SetStatus(StatusAwaitingPermission)
	a.broadcastStatus(StatusAwaitingPermission)

	// Tell the frontend the plan is now actionable. The plan block was
	// already streamed; this event just hooks up the action row by
	// stamping permission_id on the FE side too.
	if ev, err := makeEvent(EvPlanQuestion, PlanQuestionPayload{
		PermissionID: permID,
		BlockID:      blockID,
		TurnID:       turnID,
		ToolUseID:    toolUseID,
	}); err == nil {
		a.broadcast(ev)
	}
	a.publishNotification(InternalNotification{
		RequestID: permID,
		Type:      "urgent",
		Title:     "Claude is awaiting plan approval",
		Message:   summarisePlanForNotification(input),
		Actions: []InternalNotificationAction{
			{Label: "Approve", Action: "claude.plan.approve:" + permID},
			{Label: "Keep planning", Action: "claude.plan.reject:" + permID},
		},
	})

	resp, err := a.awaitPermission(permID)
	a.clearNotification(permID)
	a.broadcastStatus(a.session.Status())
	if err != nil {
		return permissionResponse{Behavior: "deny", Message: err.Error()}, nil
	}
	out := permissionResponse{Behavior: resp.Behavior, Message: resp.Message}
	if len(resp.UpdatedInput) > 0 {
		out.UpdatedInput = resp.UpdatedInput
	} else if len(input) > 0 {
		out.UpdatedInput = input
	}
	return out, nil
}

// AnswerPlanResponse resolves an outstanding ExitPlanMode permission
// from a `plan.respond` WS frame. Mirrors AnswerAskQuestion:
//   - on approve, wake the waiter with behavior:"allow" and (when
//     EditedPlan is non-empty) updatedInput={"plan": editedPlan}; if
//     TargetMode is set, kick a background SetPermissionMode so the
//     conversation continues in the requested mode;
//   - on reject, wake the waiter with behavior:"deny" and a friendly
//     "User chose to keep planning" message.
//
// Also stamps the decision onto the kind:"plan" block so the action
// row hides on this and any other connected client.
func (a *Agent) AnswerPlanResponse(frame PlanRespondFrame) error {
	if frame.PermissionID == "" {
		return errors.New("plan.respond: missing permission_id")
	}
	switch frame.Decision {
	case "approve", "reject":
	default:
		return fmt.Errorf("plan.respond: invalid decision %q", frame.Decision)
	}
	if _, ok := a.session.ConsumePlanPermission(frame.PermissionID); !ok {
		return errors.New("plan.respond: unknown or already-resolved permission_id")
	}
	cliDecision := "allow"
	if frame.Decision == "reject" {
		cliDecision = "deny"
	}
	cliRequestID, ok := a.session.ResolvePermission(frame.PermissionID, cliDecision)
	if !ok {
		return errors.New("plan.respond: permission already resolved")
	}
	_ = cliRequestID

	// If the user edited the plan, persist that text on the block so
	// re-snapshots see the edited markdown rather than the agent's
	// original draft.
	if frame.Decision == "approve" && frame.EditedPlan != "" {
		a.session.UpdatePlanBlockText(frame.PermissionID, frame.EditedPlan)
	}

	// Stamp the decision onto the plan block (Done flips, label flips).
	turnID, blockID := a.session.MarkPlanBlockDecided(frame.PermissionID, decisionLabelForPlan(frame.Decision), frame.TargetMode)

	if ev, e := makeEvent(EvPlanDecided, PlanDecidedPayload{
		PermissionID: frame.PermissionID,
		BlockID:      blockID,
		TurnID:       turnID,
		Decision:     decisionLabelForPlan(frame.Decision),
		TargetMode:   frame.TargetMode,
	}); e == nil {
		a.broadcast(ev)
	}

	// Build the canUseToolResponse to wake the blocked CLI.
	resp := canUseToolResponse{}
	switch frame.Decision {
	case "approve":
		resp.Behavior = "allow"
		if frame.EditedPlan != "" {
			body := map[string]any{"plan": frame.EditedPlan}
			if raw, err := json.Marshal(body); err == nil {
				resp.UpdatedInput = raw
			}
		}
		// Switch the permission mode for subsequent turns. We CANNOT
		// respawn here — respawning kills the CLI mid tools/call before
		// it gets the allow we're about to send, and the new CLI never
		// learns the ExitPlanMode was approved. So:
		//   1. Record the mode in session + BranchPrefs immediately so the
		//      next CLI start picks it up.
		//   2. Re-broadcast a session snapshot so connected browsers
		//      reflect the new mode in the UI pill (no respawn → no
		//      natural session.init to ride on).
		//   3. After the waiter is woken (allow propagates), fire an
		//      in-band `set_permission_mode` control_request only — no
		//      respawn. The CLI's ExitPlanMode handler treats the chosen
		//      mode itself as the post-plan policy, so the in-band call
		//      is largely belt-and-suspenders.
		if frame.TargetMode != "" {
			a.session.SetPermissionMode(frame.TargetMode)
			a.persistPrefs()
			if ev, e := makeEvent(EvPermissionModeChange, PermissionModeChangePayload{Mode: frame.TargetMode}); e == nil {
				a.broadcast(ev)
			}
		}
	case "reject":
		resp.Behavior = "deny"
		resp.Message = "User chose to keep planning"
	}

	a.mu.Lock()
	ch, ok := a.permWaiters[frame.PermissionID]
	if ok {
		delete(a.permWaiters, frame.PermissionID)
	}
	cli := a.client
	a.mu.Unlock()
	if ok {
		ch <- resp
	}
	a.publishEvent(EventClaudePermissionResolved, map[string]any{
		"permissionId": frame.PermissionID,
		"decision":     resp.Behavior,
	})
	// In-band mode change AFTER the allow has propagated. Best-effort —
	// the CLI's ExitPlanMode handler already implements the post-plan
	// policy itself based on the chosen mode in the user-facing UI, so
	// this is a redundancy that doesn't depend on the CLI cooperating.
	// We deliberately do NOT respawn (would kill the CLI mid-stream).
	if frame.Decision == "approve" && frame.TargetMode != "" && cli != nil {
		go func(mode string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := cli.SetPermissionMode(ctx, mode); err != nil {
				a.deps.logger.Info("claudeagent: in-band set_permission_mode after plan approve failed (benign)",
					"err", err, "mode", mode)
			}
		}(frame.TargetMode)
	}
	return nil
}

// decisionLabelForPlan converts the wire-level "approve"/"reject" into
// the labels we stamp onto the plan block (and ship to the FE on
// plan.decided). Kept as a small helper so any future i18n / drift sits
// in one place.
func decisionLabelForPlan(decision string) string {
	switch decision {
	case "approve":
		return "approved"
	case "reject":
		return "rejected"
	}
	return decision
}

// summarisePlanForNotification builds a short Inbox preview of the plan
// awaiting approval. Best-effort: the plan markdown lives in
// `input.plan` once the CLI finalises the partial JSON.
func summarisePlanForNotification(input json.RawMessage) string {
	var raw struct {
		Plan string `json:"plan"`
	}
	if err := json.Unmarshal(input, &raw); err != nil {
		return "Plan ready for approval"
	}
	plan := strings.TrimSpace(raw.Plan)
	if plan == "" {
		return "Plan ready for approval"
	}
	for _, line := range strings.Split(plan, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		// Strip leading markdown header marks for a tighter preview.
		for len(t) > 0 && t[0] == '#' {
			t = strings.TrimSpace(t[1:])
		}
		if len(t) > 80 {
			t = t[:79] + "…"
		}
		return t
	}
	return "Plan ready for approval"
}

// AnswerAskQuestion resolves an outstanding AskUserQuestion by packing
// the chosen labels into the CLI-bound tool input and waking the
// blocked RequestPermission goroutine. Also stamps the answers onto
// the kind:"ask" block so the UI flips to the "decided" view (in this
// tab and any other connected client).
func (a *Agent) AnswerAskQuestion(frame AskRespondFrame) error {
	if frame.PermissionID == "" {
		return errors.New("ask.respond: missing permission_id")
	}
	if _, ok := a.session.ConsumeAskPermission(frame.PermissionID); !ok {
		return errors.New("ask.respond: unknown or already-resolved permission_id")
	}
	// Resolve the underlying pending permission (this clears it from
	// pendingPermissions but doesn't write a decision string — the ask
	// kind doesn't have allow/deny).
	cliRequestID, ok := a.session.ResolvePermission(frame.PermissionID, "allow")
	if !ok {
		return errors.New("ask.respond: permission already resolved")
	}
	_ = cliRequestID

	// Snapshot answers to JSON and stamp on the block.
	answersRaw, err := json.Marshal(frame.Answers)
	if err != nil {
		return fmt.Errorf("ask.respond: marshal answers: %w", err)
	}
	turnID, blockID := a.session.MarkAskBlockDecided(frame.PermissionID, answersRaw)

	// Fan the decided event to all subscribers so other open tabs flip
	// to the "decided" view too.
	if ev, e := makeEvent(EvAskDecided, AskDecidedPayload{
		PermissionID: frame.PermissionID,
		BlockID:      blockID,
		TurnID:       turnID,
		Answers:      answersRaw,
	}); e == nil {
		a.broadcast(ev)
	}

	// Build the CLI-bound updatedInput. We preserve the original
	// `questions` array verbatim and add `questionAnswers` — the field
	// name AskUserQuestion's TS implementation reads to retrieve the
	// chosen labels at run time. (See the SDK source if a future CLI
	// build renames it.)
	updated := buildAskUpdatedInput(frame.Answers)

	// Resolve the waiter so RequestPermission returns and the MCP layer
	// answers the CLI's tools/call.
	a.mu.Lock()
	ch, ok := a.permWaiters[frame.PermissionID]
	if ok {
		delete(a.permWaiters, frame.PermissionID)
	}
	a.mu.Unlock()
	if ok {
		ch <- canUseToolResponse{
			Behavior:     "allow",
			UpdatedInput: updated,
		}
	}
	a.publishEvent(EventClaudePermissionResolved, map[string]any{
		"permissionId": frame.PermissionID,
		"decision":     "allow",
	})
	return nil
}

// buildAskUpdatedInput packages the chosen labels into the tool input
// shape the AskUserQuestion CLI handler reads. The underlying tool
// expects an updatedInput that contains the chosen answers. Today the
// CLI's permission_prompt path discards the original input and replaces
// it with what we send back, so we ship a self-contained object whose
// shape is stable across CLI versions.
func buildAskUpdatedInput(answers [][]string) json.RawMessage {
	if answers == nil {
		answers = [][]string{}
	}
	body := map[string]any{
		"questionAnswers": answers,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		// Should never happen; ship empty so the CLI surfaces a clean
		// "no answer" tool_result rather than crashing the Agent.
		return json.RawMessage(`{"questionAnswers":[]}`)
	}
	return raw
}

// extractAskQuestionsRaw returns the `questions` field of an
// AskUserQuestion tool input as raw JSON, or nil when missing /
// unparseable. Used to populate ask.question wire payloads so a
// freshly-connected client doesn't have to wait for a stream replay.
func extractAskQuestionsRaw(input json.RawMessage) json.RawMessage {
	if len(input) == 0 {
		return nil
	}
	var raw struct {
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil
	}
	return raw.Questions
}

// summariseAskQuestionForNotification builds a one-line preview of an
// AskUserQuestion's first question for the Activity Inbox.
func summariseAskQuestionForNotification(input json.RawMessage) string {
	var raw struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &raw); err != nil {
		return "Claude is asking a question"
	}
	if len(raw.Questions) == 0 {
		return "Claude is asking a question"
	}
	q := raw.Questions[0].Question
	if len(q) > 80 {
		q = q[:79] + "…"
	}
	if q == "" {
		return "Claude is asking a question"
	}
	return q
}

// summariseToolForNotification compresses a tool name + input into one
// readable line for the Inbox. Mirrors toolSummary on the frontend.
func summariseToolForNotification(toolName string, input json.RawMessage) string {
	var obj map[string]any
	if err := json.Unmarshal(input, &obj); err != nil {
		return toolName
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := obj[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
		return ""
	}
	switch toolName {
	case "Bash":
		s := pick("command")
		if len(s) > 80 {
			s = s[:79] + "…"
		}
		return "Bash: " + s
	case "Edit", "Write", "NotebookEdit":
		return toolName + ": " + pick("file_path")
	case "Read":
		return "Read: " + pick("file_path")
	case "Glob", "Grep":
		return toolName + ": " + pick("pattern", "glob")
	case "WebFetch", "WebSearch":
		return toolName + ": " + pick("url", "query")
	case "Task":
		return "Task: " + pick("description", "subagent_type")
	}
	return toolName
}

func (a *Agent) awaitPermission(permID string) (canUseToolResponse, error) {
	a.mu.Lock()
	if a.permWaiters == nil {
		a.permWaiters = map[string]chan canUseToolResponse{}
	}
	ch, ok := a.permWaiters[permID]
	if !ok {
		ch = make(chan canUseToolResponse, 1)
		a.permWaiters[permID] = ch
	}
	a.mu.Unlock()

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(15 * time.Minute):
		// Reasonable upper bound — the user might be away from the keyboard;
		// erring on the side of patience here costs the CLI a turn.
		a.mu.Lock()
		delete(a.permWaiters, permID)
		a.mu.Unlock()
		return canUseToolResponse{Behavior: "deny", Message: "permission request timed out"}, nil
	}
}

// SendUserMessage relays text to the CLI, after lazily spawning if needed.
func (a *Agent) SendUserMessage(ctx context.Context, content string) error {
	return a.SendUserMessageWithDirs(ctx, content, nil)
}

// SendUserMessageWithDirs relays text to the CLI and also ensures the CLI
// has been started with `--add-dir <path>` for every entry in addDirs.
// When the user attaches a directory chip in the composer the new path
// must reach the CLI as a startup arg (the flag is not addable mid-
// session). We grow the Agent's persistent addDirs set and respawn the
// CLI when the set actually expanded; if the new dirs are already a
// subset of what the running CLI was launched with, no respawn happens.
//
// addDirs entries should be absolute, already-validated paths (the
// composer's REST-side picker uses Files-tab `resolveSafePath` and we
// normalise to abs in the WS frame handler).
func (a *Agent) SendUserMessageWithDirs(ctx context.Context, content string, addDirs []string) error {
	// 1) Decide whether the new dirs require a respawn. The check has to
	// happen before EnsureClient so we don't spawn-then-immediately-kill.
	needRespawn, mergedDirs := a.mergeAddDirs(addDirs)
	if needRespawn {
		a.mu.Lock()
		a.addDirs = mergedDirs
		hasClient := a.client != nil
		a.mu.Unlock()
		if hasClient {
			a.deps.logger.Info("claudeagent: respawning CLI to apply --add-dir",
				"newDirs", addDirs, "merged", mergedDirs)
			if err := a.respawnClient(ctx); err != nil {
				return err
			}
		}
	}
	if err := a.EnsureClient(ctx); err != nil {
		return err
	}
	a.mu.Lock()
	cli := a.client
	a.mu.Unlock()
	if cli == nil {
		return errors.New("claudeagent: client unavailable")
	}
	turnID := a.session.AppendUserTurn(content)
	if ev, err := makeEvent(EvUserMessage, UserMessagePayload{TurnID: turnID, Content: content}); err == nil {
		a.broadcast(ev)
	}
	a.session.SetStatus(StatusThinking)
	a.broadcastStatus(StatusThinking)
	return cli.SendUserMessage(content)
}

// Interrupt aborts the in-flight assistant turn.
func (a *Agent) Interrupt(ctx context.Context) error {
	a.mu.Lock()
	cli := a.client
	a.mu.Unlock()
	if cli == nil {
		return nil
	}
	return cli.Interrupt(ctx)
}

// SetModel sends a control_request to swap models.
func (a *Agent) SetModel(ctx context.Context, model string) error {
	a.mu.Lock()
	cli := a.client
	a.mu.Unlock()
	a.session.SetModel(model)
	a.persistPrefs()
	if cli == nil {
		return nil
	}
	return cli.SetModel(ctx, model)
}

// SetEffort swaps the `--effort` level. There's no documented
// control_request for this, so we always respawn with the new flag. The
// session_id carries the conversation forward.
func (a *Agent) SetEffort(ctx context.Context, effort string) error {
	a.session.SetEffort(effort)
	a.persistPrefs()
	a.mu.Lock()
	cli := a.client
	a.mu.Unlock()
	if cli == nil {
		return nil
	}
	a.deps.logger.Info("claudeagent: respawning CLI to apply effort", "effort", effort)
	return a.respawnClient(ctx)
}

// SetPermissionMode swaps the permission policy. The CLI's
// `set_permission_mode` control_request is best-effort — empirically the
// running CLI doesn't always honour the change for tool execution. To get
// reliable behaviour we kill and re-spawn with `--permission-mode <mode>`
// and `--resume <session_id>` so the conversation survives the swap.
func (a *Agent) SetPermissionMode(ctx context.Context, mode string) error {
	a.session.SetPermissionMode(mode)
	a.persistPrefs()
	a.mu.Lock()
	cli := a.client
	a.mu.Unlock()
	if cli == nil {
		// No running CLI; the next spawn will pick up the new mode.
		return nil
	}
	// Try the in-band control_request first — cheaper if it works.
	if err := cli.SetPermissionMode(ctx, mode); err != nil {
		a.deps.logger.Warn("claudeagent: set_permission_mode control request failed, will respawn",
			"err", err, "mode", mode)
	}
	a.deps.logger.Info("claudeagent: respawning CLI to apply permission mode", "mode", mode)
	return a.respawnClient(ctx)
}

// IncludeHookEvents reports the live opt-in flag.
func (a *Agent) IncludeHookEvents() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.includeHookEvents
}

// RepoID returns the repository ID this agent is bound to.
func (a *Agent) RepoID() string { return a.deps.repoID }

// BranchID returns the branch ID this agent is bound to.
func (a *Agent) BranchID() string { return a.deps.branchID }

// AddDirs returns a copy of the absolute paths the running CLI was last
// spawned with as `--add-dir`. Used by tests / debugging.
func (a *Agent) AddDirs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.addDirs))
	copy(out, a.addDirs)
	return out
}

// mergeAddDirs decides whether new is wider than the live a.addDirs and
// returns (needRespawn, merged). A respawn is needed iff `new` contains
// an entry that's not already in `a.addDirs`. Empty paths in `new` are
// dropped. We compare by exact string match — callers are responsible
// for normalising (filepath.Clean / abs) before passing in.
//
// The function only reads a.addDirs while a.mu is held; the caller is
// expected to apply the returned `merged` slice under the same lock to
// avoid TOCTOU between two concurrent SendUserMessageWithDirs calls.
func (a *Agent) mergeAddDirs(newDirs []string) (bool, []string) {
	if len(newDirs) == 0 {
		return false, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	existing := map[string]struct{}{}
	for _, d := range a.addDirs {
		existing[d] = struct{}{}
	}
	merged := append([]string(nil), a.addDirs...)
	added := false
	for _, d := range newDirs {
		if d == "" {
			continue
		}
		if _, ok := existing[d]; ok {
			continue
		}
		existing[d] = struct{}{}
		merged = append(merged, d)
		added = true
	}
	if !added {
		return false, nil
	}
	return true, merged
}

// SetIncludeHookEvents flips the per-branch opt-in for --include-hook-events
// and persists it. When the flag value actually changes, the CLI is
// respawned so the new --include-hook-events presence/absence takes effect
// (the flag can't be toggled mid-session — it's a CLI startup arg). When
// no client is running yet (lazy spawn pre-first-message), the next spawn
// just picks up the new value automatically.
func (a *Agent) SetIncludeHookEvents(ctx context.Context, enabled bool) error {
	a.mu.Lock()
	if a.includeHookEvents == enabled {
		a.mu.Unlock()
		// Idempotent — still persist in case sessions.json drifted.
		a.persistPrefs()
		return nil
	}
	a.includeHookEvents = enabled
	hasClient := a.client != nil
	a.mu.Unlock()
	a.persistPrefs()
	if !hasClient {
		return nil
	}
	return a.respawnClient(ctx)
}

// respawnClient kills the current claude process (if any) and starts a new
// one. The new process resumes the existing session_id so the conversation
// continues seamlessly. Subscribers stay connected — broadcast goes through
// Agent, not Client.
//
// `intentionalRespawn` is set so watchClient knows the kill was deliberate
// and should not surface a noisy "Claude CLI exited" error event.
func (a *Agent) respawnClient(ctx context.Context) error {
	a.mu.Lock()
	if a.client != nil {
		a.intentionalRespawn = true
		a.client.Close()
		a.client = nil
	}
	a.mu.Unlock()
	return a.EnsureClient(ctx)
}

// AlwaysAllowTool writes a permission rule for `toolName` into the
// worktree's `.claude/settings.json` permissions.allow array, then resolves
// the current pending permission as allow. Future tool calls of the same
// name will skip our prompt because the CLI honours the rule itself.
func (a *Agent) AlwaysAllowTool(frame PermissionRespondFrame) error {
	pattern := a.session.ToolNameForPermission(frame.PermissionID)
	if pattern == "" {
		return errors.New("unknown permission_id")
	}
	if err := addToProjectAllowList(a.deps.worktree, pattern); err != nil {
		return err
	}
	a.session.AddSessionAllow(pattern) // also short-circuit during this session
	return a.AnswerPermission(PermissionRespondFrame{
		PermissionID: frame.PermissionID,
		Decision:     "allow",
		Scope:        "session",
		UpdatedInput: frame.UpdatedInput,
	})
}

// AnswerPermission resolves the pending permission and forwards the response
// to the CLI.
func (a *Agent) AnswerPermission(frame PermissionRespondFrame) error {
	cliRequestID, ok := a.session.ResolvePermission(frame.PermissionID, frame.Decision)
	if !ok {
		return errors.New("unknown permission_id")
	}
	if frame.Decision == "allow" && frame.Scope == "session" {
		if tool := a.session.ToolNameForPermission(frame.PermissionID); tool != "" {
			a.session.AddSessionAllow(tool)
		}
	}
	_ = cliRequestID
	resp := canUseToolResponse{Behavior: frame.Decision}
	if frame.Decision == "deny" && frame.Reason != "" {
		resp.Message = frame.Reason
	}
	if frame.Decision == "allow" && len(frame.UpdatedInput) > 0 {
		resp.UpdatedInput = frame.UpdatedInput
	}
	a.mu.Lock()
	ch, ok := a.permWaiters[frame.PermissionID]
	if ok {
		delete(a.permWaiters, frame.PermissionID)
	}
	a.mu.Unlock()
	if ok {
		ch <- resp
	}
	return nil
}

// markIntentionalKill flips intentionalRespawn so watchClient won't surface
// the "Claude CLI exited" error event for the next CLI death. Used by
// /clear and by respawnClient — anywhere we deliberately tear down.
func (a *Agent) markIntentionalKill() {
	a.mu.Lock()
	a.intentionalRespawn = true
	a.mu.Unlock()
}

// ForkSession spawns a fresh CLI with `--resume <baseId> --fork-session`
// so the new conversation starts from baseId's history but writes to a
// new session_id. UI can call this from the history popup to branch off
// a past session.
func (a *Agent) ForkSession(ctx context.Context, baseSessionID string) error {
	if baseSessionID == "" {
		return errors.New("claudeagent: empty base session id")
	}
	a.session.Reset()
	a.session.SetSessionID(baseSessionID)
	// Mark the next spawn as fork. Since EnsureClient picks Resume from
	// session.SessionID() and Fork from a flag, store the fork bit on the
	// agent and clear it after one successful spawn.
	a.mu.Lock()
	a.pendingFork = true
	a.mu.Unlock()
	if err := a.deps.manager.store.SetActive(a.deps.repoID, a.deps.branchID, a.deps.tabID, baseSessionID, a.session.Model()); err != nil {
		a.deps.logger.Warn("claudeagent: SetActive on fork failed", "err", err)
	}
	if ev, err := makeEvent(EvSessionReplaced, SessionReplacedPayload{OldSessionID: baseSessionID, NewSessionID: ""}); err == nil {
		a.broadcast(ev)
	}
	a.publishEvent(EventClaudeSessionReplaced, map[string]any{
		"oldSessionId": baseSessionID,
		"newSessionId": "",
	})
	return a.respawnClient(ctx)
}

// ResumeSession swaps the active session_id to the given id, replays the
// disk transcript into the in-memory session so the user sees the past
// turns immediately, then kills + respawns the CLI with `--resume <id>`
// so subsequent input goes to that conversation.
func (a *Agent) ResumeSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("claudeagent: empty session id")
	}
	old := a.session.Reset()
	a.session.SetSessionID(sessionID)
	if err := a.deps.manager.store.SetActive(a.deps.repoID, a.deps.branchID, a.deps.tabID, sessionID, a.session.Model()); err != nil {
		a.deps.logger.Warn("claudeagent: SetActive on resume failed", "err", err)
	}
	// Replay the on-disk transcript into the live session before any UI
	// renders, so the snapshot we ship reflects the resumed history.
	if path, err := transcriptPath(a.deps.worktree, sessionID); err == nil {
		if turns, err := LoadTranscriptTurns(path); err == nil && len(turns) > 0 {
			a.session.SetTurns(turns)
		} else if err != nil {
			a.deps.logger.Warn("claudeagent: LoadTranscriptTurns failed", "err", err, "session", sessionID)
		}
	}
	// Push a fresh init snapshot so the active client repaints with the
	// loaded turns. We deliberately skip the per-WS session.replaced
	// frame: its reducer wipes turns (correct for /clear, wrong for
	// resume) and would undo what session.init just established. The
	// global EventHub publish below still keeps Drawer / Inbox in sync.
	snap := a.Snapshot()
	if ev, err := makeEvent(EvSessionInit, snap); err == nil {
		a.broadcast(ev)
	}
	a.publishEvent(EventClaudeSessionReplaced, map[string]any{
		"oldSessionId": old,
		"newSessionId": sessionID,
	})
	return a.respawnClient(ctx)
}

// Clear is /clear: kill the current CLI, drop the active session pointer,
// reset in-memory state, and notify subscribers. The transcript on disk
// remains.
func (a *Agent) Clear() {
	a.mu.Lock()
	if a.client != nil {
		a.intentionalRespawn = true
		a.client.Close()
		a.client = nil
	}
	a.mu.Unlock()
	old := a.session.Reset()
	_ = a.deps.manager.store.ClearActive(a.deps.repoID, a.deps.branchID, a.deps.tabID)
	if ev, err := makeEvent(EvSessionReplaced, SessionReplacedPayload{OldSessionID: old}); err == nil {
		a.broadcast(ev)
	}
	a.publishEvent(EventClaudeSessionReplaced, map[string]any{
		"oldSessionId": old,
		"newSessionId": "",
	})
	a.broadcastStatus(StatusIdle)
}

// Shutdown is the manager-driven teardown.
func (a *Agent) Shutdown() {
	a.mu.Lock()
	if a.client != nil {
		a.client.Close()
		a.client = nil
	}
	a.mu.Unlock()
}

func (a *Agent) broadcastStatus(s AgentStatus) {
	if ev, err := makeEvent(EvStatusChange, StatusChangePayload{Status: s}); err == nil {
		a.broadcast(ev)
	}
	a.publishEvent(string(EventClaudeStatus), map[string]any{
		"status": string(s),
	})
}

func (a *Agent) broadcastError(msg, detail string) {
	if ev, err := makeEvent(EvError, ErrorPayload{Message: msg, Detail: detail}); err == nil {
		a.broadcast(ev)
	}
	a.publishEvent(string(EventClaudeError), map[string]any{
		"message": msg,
		"detail":  detail,
	})
}

// persistPrefs snapshots the current model / effort / permission mode and
// writes them to the per-branch overrides in sessions.json. Called after
// any user-driven change so a fresh tab on the same branch picks them up.
func (a *Agent) persistPrefs() {
	if a.deps.manager == nil || a.deps.manager.store == nil {
		return
	}
	a.mu.Lock()
	hooks := a.includeHookEvents
	a.mu.Unlock()
	prefs := BranchPrefs{
		Model:             a.session.Model(),
		Effort:            a.session.Effort(),
		PermissionMode:    a.session.PermissionMode(),
		IncludeHookEvents: hooks,
	}
	if err := a.deps.manager.store.SetBranchPrefs(a.deps.repoID, a.deps.branchID, a.deps.tabID, prefs); err != nil {
		a.deps.logger.Warn("claudeagent: SetBranchPrefs failed", "err", err)
	}
}

// publishEvent fans a branch-scoped state change out to the global
// EventHub so non-active UI (Drawer pip / Activity Inbox) can react.
func (a *Agent) publishEvent(eventType string, payload any) {
	if a.deps.manager == nil || a.deps.manager.events == nil {
		return
	}
	a.deps.manager.events.Publish(eventType, a.deps.repoID, a.deps.branchID, payload)
}

// publishNotification surfaces an actionable item in the Notify Hub for
// the Activity Inbox. Idempotent on RequestID — sink decides dedupe.
// Stamps the agent's tab id + display name onto the notification so the
// Inbox can render "Claude 2" / "Claude" labels in multi-tab branches.
func (a *Agent) publishNotification(n InternalNotification) {
	if a.deps.manager == nil || a.deps.manager.notify == nil {
		return
	}
	if n.TabID == "" {
		n.TabID = a.deps.tabID
	}
	if n.TabName == "" {
		n.TabName = DisplayNameForTab(a.deps.tabID)
	}
	a.deps.manager.notify.IngestInternal(a.deps.repoID, a.deps.branchID, n)
}

// clearNotification tells the sink to remove (or mark resolved) a
// previously-published notification. Used when a permission gets answered
// from any path so the Inbox stops showing the prompt.
func (a *Agent) clearNotification(requestID string) {
	if a.deps.manager == nil || a.deps.manager.notify == nil {
		return
	}
	a.deps.manager.notify.ClearByRequestID(a.deps.repoID, a.deps.branchID, requestID)
}

// EventType constants from the store package, redeclared here to avoid the
// import cycle. Keep in lockstep with internal/store/events.go.
const (
	EventClaudeStatus             = "claude.status"
	EventClaudePermissionRequest  = "claude.permission_request"
	EventClaudePermissionResolved = "claude.permission_resolved"
	EventClaudeError              = "claude.error"
	EventClaudeTurnEnd            = "claude.turn_end"
	EventClaudeSessionReplaced    = "claude.session_replaced"
)
