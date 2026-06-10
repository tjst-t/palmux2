package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tmux"
	"github.com/tjst-t/palmux2/internal/worktree"
)

// OpenBranch creates the worktree (via gwq if necessary), the tmux session,
// runs every Provider's OnBranchOpen, and registers the branch in the Store.
//
// This is the "user explicitly opened this branch through the Drawer"
// path: in addition to creating tmux state it appends the branch name
// to repos.json#userOpenedBranches so the Drawer puts the row in `my`.
// Auto-registration of CLI-created worktrees goes through OpenBranchAuto
// instead, which does NOT touch userOpenedBranches.
func (s *Store) OpenBranch(ctx context.Context, repoID, branchName string) (*domain.Branch, error) {
	return s.openBranchInternal(ctx, repoID, branchName, true)
}

// OpenBranchAuto registers a branch the same way as OpenBranch but does
// NOT mark it as user-opened. Call this from the worktree-sync loop so
// CLI-created worktrees stay in the `unmanaged` (or `subagent`) Drawer
// section until the user explicitly promotes them.
func (s *Store) OpenBranchAuto(ctx context.Context, repoID, branchName string) (*domain.Branch, error) {
	return s.openBranchInternal(ctx, repoID, branchName, false)
}

func (s *Store) openBranchInternal(ctx context.Context, repoID, branchName string, markUserOpened bool) (*domain.Branch, error) {
	branchName = strings.TrimSpace(branchName)
	if branchName == "" {
		return nil, fmt.Errorf("%w: branchName empty", ErrInvalidArg)
	}
	repoSnapshot, err := s.Repo(repoID)
	if err != nil {
		return nil, err
	}
	repoFullPath := repoSnapshot.FullPath

	// 1. Ensure a worktree exists for this branch.
	wt, err := s.ensureWorktree(ctx, repoFullPath, branchName)
	if err != nil {
		return nil, err
	}

	// 2. Build the Branch entity (does not yet touch tmux). S1e8d02:
	//    BranchID is path-derived so a subsequent in-place `git checkout`
	//    on the same worktree leaves the ID unchanged.
	branchID := domain.WorkspaceSlugIDFromPath(wt.Path, wt.IsPrimary, repoFullPath)
	sessionName := domain.SessionName(repoID, branchID)

	branch := &domain.Branch{
		ID:           branchID,
		Name:         wt.Branch,
		WorktreePath: wt.Path,
		RepoID:       repoID,
		IsPrimary:    wt.IsPrimary,
		LastActivity: time.Now(),
		TabSet:       domain.TabSet{TmuxSession: sessionName},
	}

	// 3. Run each Provider's OnBranchOpen to gather windows + tab metadata.
	specs, err := s.collectOpenSpecs(ctx, branch, false)
	if err != nil {
		return nil, err
	}

	// 4. Bring up the tmux session with these windows. Idempotent: if the
	//    session already exists (Palmux restart, manual `tmux attach`),
	//    leave it alone.
	if err := s.ensureSession(ctx, branch, specs); err != nil {
		return nil, fmt.Errorf("ensureSession: %w", err)
	}

	// 5. Register in Store + publish event.
	s.mu.Lock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.Unlock()
		return nil, ErrRepoNotFound
	}
	for _, existing := range repo.OpenBranches {
		if existing.ID == branchID {
			s.recomputeTabs(ctx, existing)
			s.applyCategoriesUnlocked(repo)
			snap := cloneBranch(existing)
			s.mu.Unlock()
			// S015-1-6: only the *explicit* drawer path records this as
			// user-opened. The auto path (sync_worktree) leaves it
			// alone so CLI-created worktrees stay `unmanaged`.
			if markUserOpened {
				if _, err := s.deps.RepoStore.AddUserOpenedBranch(repoID, wt.Branch); err != nil {
					s.logger.Warn("OpenBranch: AddUserOpenedBranch failed", "repo", repoID, "branch", wt.Branch, "err", err)
				}
			}
			return snap, nil
		}
	}
	repo.OpenBranches = append(repo.OpenBranches, branch)
	sortBranches(repo.OpenBranches)
	s.recomputeTabs(ctx, branch)
	s.applyCategoriesUnlocked(repo)
	snap := cloneBranch(branch)
	s.mu.Unlock()

	// Sadf90e (review fix): seed TabClaudeModes for every Claude tab on
	// this branch. AddTab covers `+`-added tabs, but the canonical first
	// Claude tab is auto-seeded by claudeagent.Manager.tabsForBranch and
	// bypasses AddTab. Shared helper initClaudeTabModes is also called from
	// OpenRepo for branches discovered at startup.
	s.initClaudeTabModes(repoID, []*domain.Branch{snap})

	if markUserOpened {
		// S015-1-6: persist that the user opened this branch through Palmux
		// so the Drawer puts it in `my`. Idempotent; failure here is non-
		// fatal — branch is still open, just lands in `unmanaged` until the
		// user clicks `+ Add to my worktrees`.
		if _, err := s.deps.RepoStore.AddUserOpenedBranch(repoID, wt.Branch); err != nil {
			s.logger.Warn("OpenBranch: AddUserOpenedBranch failed", "repo", repoID, "branch", wt.Branch, "err", err)
		}
		s.mu.Lock()
		if r, ok := s.repos[repoID]; ok {
			s.applyCategoriesUnlocked(r)
		}
		s.mu.Unlock()
	}

	s.hub.Publish(Event{Type: EventBranchOpened, RepoID: repoID, BranchID: branchID, Payload: snap})
	return snap, nil
}

// RenameBranch updates only the display name of an existing branch in-place
// (S1e8d02). The branch's ID, tmux session, Claude agent process, tab list
// and Drawer position are all preserved; this is the path-identity-stable
// outcome of `git checkout other-branch` inside the same worktree.
//
// Caller passes the branchID (path-derived, so invariant under checkout)
// and the new branch name observed by `git worktree list`. The store
// publishes [EventBranchHeadChanged] so connected browsers can re-label
// the Drawer entry without removing-and-re-adding it.
//
// `OnBranchClose` and `OnBranchOpen` Provider hooks are NOT invoked
// (that's the entire point — tabs remain alive). Providers that want
// to react to head changes implement [tab.Provider.OnBranchHeadChanged]
// (S1e8d02-4, default no-op).
//
// Returns ErrRepoNotFound / ErrBranchNotFound if the IDs don't match.
// A no-op rename (oldName == newName) is silently dropped.
func (s *Store) RenameBranch(ctx context.Context, repoID, branchID, newName string) error {
	s.mu.Lock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.Unlock()
		return ErrRepoNotFound
	}
	var branch *domain.Branch
	for _, b := range repo.OpenBranches {
		if b.ID == branchID {
			branch = b
			break
		}
	}
	if branch == nil {
		s.mu.Unlock()
		return ErrBranchNotFound
	}
	oldName := branch.Name
	if oldName == newName {
		s.mu.Unlock()
		return nil
	}
	branch.Name = newName
	branch.LastActivity = time.Now()
	worktreePath := branch.WorktreePath
	// Categorisation (S015) is name-keyed via repos.json.userOpenedBranches.
	// Recompute so a renamed branch lands in the right Drawer section.
	s.applyCategoriesUnlocked(repo)
	snap := cloneBranch(branch)
	s.mu.Unlock()

	// Notify Providers (default no-op for everyone except those that opt in).
	for _, p := range s.registry.Providers() {
		if hc, ok := p.(tab.HeadChangedHook); ok {
			if err := hc.OnBranchHeadChanged(ctx, tab.HeadChangedParams{
				Branch: snap, OldBranch: oldName, NewBranch: newName,
			}); err != nil {
				s.logger.Warn("OnBranchHeadChanged error", "provider", p.Type(), "err", err)
			}
		}
	}

	s.hub.Publish(Event{
		Type:     EventBranchHeadChanged,
		RepoID:   repoID,
		BranchID: branchID,
		Payload: map[string]any{
			"oldBranch":    oldName,
			"newBranch":    newName,
			"worktreePath": worktreePath,
			"branch":       snap,
		},
	})
	return nil
}

// CloseBranch tears down a branch: kill tmux, gwq remove (unless primary),
// drop from Store.
func (s *Store) CloseBranch(ctx context.Context, repoID, branchID string) error {
	s.mu.Lock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.Unlock()
		return ErrRepoNotFound
	}
	idx := -1
	for i, b := range repo.OpenBranches {
		if b.ID == branchID {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return ErrBranchNotFound
	}
	branch := repo.OpenBranches[idx]
	repo.OpenBranches = append(repo.OpenBranches[:idx], repo.OpenBranches[idx+1:]...)
	repoFullPath := repo.FullPath
	// S009-fix-4: drop ownership of this base session — sync_tmux will
	// no longer treat the next observation of this name as ours, so a
	// reborn-by-peer session of the same name won't be killed.
	if s.knownBaseSessions != nil {
		delete(s.knownBaseSessions, branch.TabSet.TmuxSession)
	}
	s.mu.Unlock()

	// Kill tmux first so client connections error out cleanly.  Route through
	// tmuxFor so incus-container workspaces kill the in-container session.
	// S8478ca-2: after killing the session, stop the container if this was
	// an incus-container workspace (Stop is `incus delete --force`).
	tc := s.tmuxFor(ctx, repoID, branchID)
	if err := tc.KillSession(ctx, branch.TabSet.TmuxSession); err != nil {
		s.logger.Warn("CloseBranch: tmux kill", "session", branch.TabSet.TmuxSession, "err", err)
	}
	// Stop the runtime (no-op for host; deletes container for incus).
	// Evict the cached entry so re-opening the workspace gets a fresh runtime.
	if s.deps.RuntimeRegistry != nil {
		rt := s.deps.RuntimeRegistry.Get(repoID, branchID)
		if rt != nil && rt.Kind() == runtime.KindIncusContainer {
			if stopErr := rt.Stop(ctx); stopErr != nil {
				s.logger.Warn("CloseBranch: runtime Stop", "repoID", repoID, "branchID", branchID, "err", stopErr)
			}
		}
		// Evict via optional interface — incus.Registry implements this.
		type evicterRegistry interface {
			EvictRuntime(repoID, branchID string)
		}
		if er, ok := s.deps.RuntimeRegistry.(evicterRegistry); ok {
			er.EvictRuntime(repoID, branchID)
		}
	}
	if !branch.IsPrimary && s.deps.Gwq != nil {
		if err := s.deps.Gwq.Remove(ctx, repoFullPath, branch.Name); err != nil {
			s.logger.Warn("CloseBranch: gwq remove", "branch", branch.Name, "err", err)
		}
	}
	// Run OnBranchClose hooks.
	for _, p := range s.registry.Providers() {
		if err := p.OnBranchClose(ctx, tab.CloseParams{Branch: branch}); err != nil {
			s.logger.Warn("OnBranchClose error", "provider", p.Type(), "err", err)
		}
	}
	// Drop the branch from `userOpenedBranches` so the Drawer's "my"
	// section stops listing it. Without this the row stays pinned, the
	// FE keeps polling/auto-attaching, and OpenBranch → ensureWorktree
	// re-creates the worktree via gwq.Add — the branch silently
	// resurrects after Close. Best-effort: a save failure here doesn't
	// undo the close.
	if !branch.IsPrimary && s.deps.RepoStore != nil && branch.Name != "" {
		if _, err := s.deps.RepoStore.RemoveUserOpenedBranch(repoID, branch.Name); err != nil {
			s.logger.Warn("CloseBranch: RemoveUserOpenedBranch",
				"repo", repoID, "branch", branch.Name, "err", err)
		}
	}
	s.hub.Publish(Event{Type: EventBranchClosed, RepoID: repoID, BranchID: branchID})
	return nil
}

// ensureWorktree returns the worktree for branchName, creating one via gwq if
// necessary. The Worktree's Branch field reflects what git records (which may
// differ slightly from the requested branchName e.g. for renames).
func (s *Store) ensureWorktree(ctx context.Context, repoFullPath, branchName string) (worktree.Worktree, error) {
	wts, err := worktree.List(ctx, repoFullPath)
	if err != nil {
		return worktree.Worktree{}, fmt.Errorf("worktree.List: %w", err)
	}
	for _, wt := range wts {
		if wt.Branch == branchName {
			return wt, nil
		}
	}
	// Need to create one via gwq. Determine whether the branch already
	// exists (use -b only for new branches).
	if s.deps.Gwq == nil {
		return worktree.Worktree{}, errors.New("gwq client not configured")
	}
	branches, err := worktree.ListAllBranches(ctx, repoFullPath)
	if err != nil {
		return worktree.Worktree{}, fmt.Errorf("ListAllBranches: %w", err)
	}
	exists := false
	for _, b := range branches {
		if b.Name == branchName {
			exists = true
			break
		}
	}
	newBranch := !exists
	if err := s.deps.Gwq.Add(ctx, repoFullPath, branchName, newBranch); err != nil {
		return worktree.Worktree{}, err
	}
	wts, err = worktree.List(ctx, repoFullPath)
	if err != nil {
		return worktree.Worktree{}, err
	}
	for _, wt := range wts {
		if wt.Branch == branchName {
			return wt, nil
		}
	}
	return worktree.Worktree{}, fmt.Errorf("worktree for %q not found after gwq add", branchName)
}

// collectOpenSpecs queries every Provider and merges their declared windows
// + verifies they didn't try to register duplicate tabs.
func (s *Store) collectOpenSpecs(ctx context.Context, branch *domain.Branch, resume bool) ([]tab.WindowSpec, error) {
	hostScope := IsHostRepoID(branch.RepoID)
	var windows []tab.WindowSpec
	for _, p := range s.registry.Providers() {
		if hostScope && p.Type() != "bash" {
			// S0c6a1b: the reserved host scope is bash-only. Skip Claude (would
			// spawn a claude window/agent), Files/Git/Sprint (no windows) so a
			// lazy attach only ever materialises the bash session.
			continue
		}
		res, err := p.OnBranchOpen(ctx, tab.OpenParams{Branch: branch, Resume: resume})
		if err != nil {
			return nil, fmt.Errorf("OnBranchOpen %s: %w", p.Type(), err)
		}
		windows = append(windows, res.Windows...)
	}
	return windows, nil
}

// ensureSession creates the tmux session (with the first window inline) and
// then adds the rest. Idempotent — if the session already exists, only
// missing windows are created.
//
// S009-fix-4: every base-session name we touch (whether we create it or
// just observe it as already-live) is recorded in knownBaseSessions. The
// zombie kill pass in SyncTmux uses that map to *only* kill sessions
// this process previously owned, so a peer palmux instance with a stale
// or empty repos.json can't make us tear down its base sessions every
// 5 s. Symmetric to the knownConnIDs filter introduced in fix-2 for
// __grp_xxx group sessions.
//
// S8478ca-2: tmux operations are routed through tmuxFor(branch) so that
// incus-container workspaces target the in-container tmux server instead of
// the host tmux server.  For host workspaces tmuxFor returns s.deps.Tmux
// unchanged — behaviour is byte-identical.
func (s *Store) ensureSession(ctx context.Context, branch *domain.Branch, windows []tab.WindowSpec) error {
	tc := s.tmuxFor(ctx, branch.RepoID, branch.ID)
	cwd := branch.WorktreePath
	sessionName := branch.TabSet.TmuxSession
	exists, err := tc.HasSession(ctx, sessionName)
	if err != nil {
		return err
	}
	// Record ownership unconditionally. Reaching ensureSession for this
	// branch means the process intends to manage this session — whether
	// we created it just now (block below) or it survived from a
	// previous run with the same name.
	s.mu.Lock()
	if s.knownBaseSessions != nil {
		s.knownBaseSessions[sessionName] = struct{}{}
	}
	s.mu.Unlock()
	if !exists {
		if len(windows) == 0 {
			// No terminal-backed providers (e.g. only Files/Git). Create a
			// minimal placeholder window so the session is valid.
			err = tc.NewSession(ctx, tmux.NewSessionOpts{
				Name:       branch.TabSet.TmuxSession,
				WindowName: domain.WindowName("placeholder", "placeholder"),
				Cwd:        cwd,
			})
			if err != nil {
				return err
			}
			return nil
		}
		first := windows[0]
		err = tc.NewSession(ctx, tmux.NewSessionOpts{
			Name:       branch.TabSet.TmuxSession,
			WindowName: first.Name,
			Cwd:        firstNonEmpty(first.Cwd, cwd),
			Command:    first.Command,
			Env:        first.Env,
		})
		if err != nil {
			return err
		}
		windows = windows[1:]
	}
	// Get current windows once so we don't add duplicates.
	have, err := tc.ListWindows(ctx, branch.TabSet.TmuxSession)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, w := range have {
		existing[w.Name] = true
	}
	for _, w := range windows {
		if existing[w.Name] {
			continue
		}
		err := tc.NewWindow(ctx, branch.TabSet.TmuxSession, tmux.NewWindowOpts{
			Name:    w.Name,
			Cwd:     firstNonEmpty(w.Cwd, cwd),
			Command: w.Command,
			Env:     w.Env,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// RestartBranchRuntime migrates an OPEN workspace to its newly-persisted
// runtime (typically called just after SetWorkspaceRuntime changed the kind).
// Returns (true, nil) when the restart was performed, (false, nil) for no-ops.
//
//   - If the branch is not currently open, this is a no-op (the change
//     applies on the next open).
//   - If the old and new kinds are the same, this is a no-op.
//   - Otherwise: kill the session in the OLD runtime, stop+evict the OLD
//     runtime, then call ensureSession in the NEW runtime.
//
// Locking: we hold s.mu only briefly for the in-memory lookups and for the
// final tab recompute. The slow operations (container Start, tmux session
// create) run outside the lock to avoid blocking other store reads for
// ~10 s during an incus launch.
// RestartBranchRuntime migrates an OPEN workspace to its newly-persisted
// runtime. oldRT MUST be the runtime the workspace is CURRENTLY running on,
// captured by the caller BEFORE persisting the new kind — the registry's Get
// re-resolves the persisted config on every call (host is never cached, incus
// is created-on-demand), so calling Get here after the persist would return the
// NEW runtime and make this a silent no-op. See handler patchWorkspaceRuntime.
func (s *Store) RestartBranchRuntime(ctx context.Context, repoID, branchID string, oldRT runtime.Runtime) (restarted bool, err error) {
	if s.deps.RuntimeRegistry == nil || oldRT == nil {
		return false, nil // nothing to restart without a registry / captured old runtime
	}

	// ── 1. Look up the branch under a read lock ──────────────────────────────
	s.mu.RLock()
	repo, ok := s.repos[repoID]
	var branch *domain.Branch
	if ok {
		for _, b := range repo.OpenBranches {
			if b.ID == branchID {
				branch = b
				break
			}
		}
	}
	s.mu.RUnlock()

	if branch == nil {
		// Branch not open — the persisted config will take effect on next open.
		return false, nil
	}

	// ── 2. Determine old and new kinds ───────────────────────────────────────
	// oldRT was captured by the caller BEFORE the persist (the registry would
	// otherwise re-resolve to the new config — see the doc comment above).
	oldKind := oldRT.Kind()

	// Read the new config directly from repos.json (already updated by
	// SetWorkspaceRuntime). We must NOT evict the cache until after killing the
	// old session, so we derive the new kind from the persisted config directly.
	newCfg := s.deps.RepoStore.ResolveWorkspaceRuntime(
		repoID, branchID,
		s.deps.Settings.DefaultRuntime(),
	)
	newKind := newCfg.Kind
	if newKind == "" {
		newKind = "host" // normalise zero value
	}

	if oldKind == runtime.Kind(newKind) {
		// Same kind — nothing to do.
		return false, nil
	}

	type evicterRegistry interface {
		EvictRuntime(repoID, branchID string)
	}

	sessionName := branch.TabSet.TmuxSession
	s.logger.Info("store.RestartBranchRuntime: migrating workspace runtime",
		"repoID", repoID, "branchID", branchID,
		"oldKind", oldKind, "newKind", newKind,
		"session", sessionName,
	)

	// ── 3. TRANSACTIONAL pre-flight: bring up the NEW runtime BEFORE tearing
	// down the old one. If the new runtime can't start (e.g. the incus image is
	// missing or subuid isn't configured on this host), ROLL BACK: revert the
	// persisted kind, drop the half-created runtime, and leave the old session
	// running untouched. A failed switch must be a clean no-op, never a broken
	// workspace.
	newRT := s.deps.RuntimeRegistry.Get(repoID, branchID) // resolves the new (post-persist) config
	if newRT != nil && newRT.Kind() == runtime.KindIncusContainer {
		if startErr := newRT.Start(ctx); startErr != nil {
			_ = s.deps.RepoStore.SetWorkspaceRuntime(repoID, branchID, &runtime.Config{Kind: oldKind})
			if er, ok2 := s.deps.RuntimeRegistry.(evicterRegistry); ok2 {
				er.EvictRuntime(repoID, branchID)
			}
			s.logger.Error("RestartBranchRuntime: new runtime failed to start — rolled back, old session kept",
				"repoID", repoID, "branchID", branchID,
				"oldKind", oldKind, "newKind", newKind, "err", startErr)
			return false, fmt.Errorf("start %s runtime: %w", newKind, startErr)
		}
	}

	// ── 4. New runtime is up (or host). Now kill the OLD tmux session using the
	// OLD runtime's TmuxClient.
	oldTC := oldRT.TmuxClient()
	if killErr := oldTC.KillSession(ctx, sessionName); killErr != nil {
		s.logger.Warn("RestartBranchRuntime: KillSession (old runtime) failed",
			"session", sessionName, "err", killErr)
		// Non-fatal: the session may already be gone. Continue.
	}

	// ── 5. Stop + evict the OLD runtime. Only evict when the OLD kind was incus:
	// after a host→incus switch the registry cache already holds the NEW incus
	// runtime (which we just started) and must NOT be evicted.
	if oldKind == runtime.KindIncusContainer {
		if stopErr := oldRT.Stop(ctx); stopErr != nil {
			s.logger.Warn("RestartBranchRuntime: Stop (old incus) failed",
				"repoID", repoID, "branchID", branchID, "err", stopErr)
		}
		if er, ok2 := s.deps.RuntimeRegistry.(evicterRegistry); ok2 {
			er.EvictRuntime(repoID, branchID)
		}
	}

	// ── 6. Collect window specs (same as the normal open path) ───────────────
	specs, specErr := s.collectOpenSpecs(ctx, branch, true) // resume=true: claude --resume
	if specErr != nil {
		return false, fmt.Errorf("RestartBranchRuntime collectOpenSpecs: %w", specErr)
	}

	// ── 7. Bring up the session in the NEW runtime (already started above; for
	// host this is the normal tmux path).
	if sessErr := s.ensureSession(ctx, branch, specs); sessErr != nil {
		return false, fmt.Errorf("RestartBranchRuntime ensureSession: %w", sessErr)
	}

	// ── 7. Recompute tabs + publish events ───────────────────────────────────
	s.mu.Lock()
	r2, ok3 := s.repos[repoID]
	if ok3 {
		for _, b := range r2.OpenBranches {
			if b.ID == branchID {
				s.recomputeTabs(ctx, b)
				break
			}
		}
	}
	s.mu.Unlock()

	rtView := s.RuntimeViewFor(repoID, branchID)
	s.hub.Publish(Event{
		Type:     EventBranchRuntimeChanged,
		RepoID:   repoID,
		BranchID: branchID,
		Payload:  rtView,
	})
	// Also publish branch.restarted so FE terminal-views can force reconnect.
	s.hub.Publish(Event{
		Type:     "branch.restarted",
		RepoID:   repoID,
		BranchID: branchID,
		Payload:  map[string]any{"runtime": rtView},
	})

	s.logger.Info("store.RestartBranchRuntime: done",
		"repoID", repoID, "branchID", branchID, "newKind", newKind)
	return true, nil
}
