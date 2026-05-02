package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// MultiTabHook is implemented by providers that store the per-branch tab
// list outside the tmux window registry (Claude, post-S009). The Store
// delegates AddTab / RemoveTab to this hook so it doesn't need to know
// about each non-tmux provider's persistence layer. Providers that only
// produce tmux-backed multi tabs leave this nil.
type MultiTabHook interface {
	// CreateTab persists a fresh tab of the given provider for this
	// branch and returns it. The hook is responsible for assigning a
	// unique tab ID, deciding the user-visible name, and serialising
	// state to disk.
	CreateTab(ctx context.Context, repoID, branchID, providerType string) (domain.Tab, error)
	// DeleteTab removes a previously-created tab. No-op when the tab is
	// not owned by this hook.
	DeleteTab(ctx context.Context, repoID, branchID, tabID string) error
}

// SetMultiTabHook registers the hook used for non-tmux multi-instance
// providers. Wired from main.go after the claudeagent.Manager is built.
func (s *Store) SetMultiTabHook(h MultiTabHook) {
	s.multiTabHook = h
}

// AddTab creates a new tab of the given provider type. Only providers with
// Multiple()==true accept this — singletons return an error if a tab already
// exists.
//
// For tmux-backed providers (Bash) we ask tmux for a fresh window name.
// For non-tmux multi providers (Claude, S009) we route through the
// MultiTabHook which owns the per-branch tab id list. Both paths
// enforce Provider.Limits() Max so the user can't blow past the cap.
//
// The optional `name` is the user-friendly suffix; for now only the bash
// path honours it (the Claude hook auto-picks).
func (s *Store) AddTab(ctx context.Context, repoID, branchID, providerType, name string) (domain.Tab, error) {
	provider := s.registry.Get(providerType)
	if provider == nil {
		return domain.Tab{}, fmt.Errorf("%w: unknown provider type %q", ErrInvalidArg, providerType)
	}
	if !provider.Multiple() {
		return domain.Tab{}, fmt.Errorf("%w: %q is a singleton", ErrInvalidArg, providerType)
	}

	s.mu.RLock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.RUnlock()
		return domain.Tab{}, ErrRepoNotFound
	}
	var branch *domain.Branch
	for _, b := range repo.OpenBranches {
		if b.ID == branchID {
			branch = b
			break
		}
	}
	if branch == nil {
		s.mu.RUnlock()
		return domain.Tab{}, ErrBranchNotFound
	}
	// Enforce max instances before mutating anything.
	limits := provider.Limits(s.deps.Settings)
	if limits.Max > 0 {
		count := 0
		for _, t := range branch.TabSet.Tabs {
			if t.Type == providerType {
				count++
			}
		}
		if count >= limits.Max {
			s.mu.RUnlock()
			return domain.Tab{}, fmt.Errorf("%w: %q tabs are at the cap of %d for this branch", ErrTabLimit, providerType, limits.Max)
		}
	}
	sessionName := branch.TabSet.TmuxSession
	cwd := branch.WorktreePath
	s.mu.RUnlock()

	// Branch on provider kind. Non-tmux multi providers delegate to the
	// MultiTabHook (Claude); tmux-backed providers create a window.
	if !provider.NeedsTmuxWindow() {
		if s.multiTabHook == nil {
			return domain.Tab{}, fmt.Errorf("%w: no multi-tab hook registered for %q", ErrInvalidArg, providerType)
		}
		added, err := s.multiTabHook.CreateTab(ctx, repoID, branchID, providerType)
		if err != nil {
			return domain.Tab{}, err
		}
		s.mu.Lock()
		s.recomputeTabs(ctx, branch)
		s.mu.Unlock()
		s.hub.Publish(Event{Type: EventTabAdded, RepoID: repoID, BranchID: branchID, TabID: added.ID, Payload: added})
		return added, nil
	}

	// S009-fix-1: ensure the tmux session is alive before we ask it for
	// a fresh window name. Without this, AddTab races sync_tmux: if a
	// recovery cycle is mid-flight (or the user just closed the only
	// attached client and tmux GC'd the session) `pickNextWindowName`
	// fails with "can't find session" and the user's `+` click looks
	// like a no-op. Calling ensureSession is idempotent — if the session
	// already exists this is a single `tmux has-session` round-trip.
	if err := s.ensureBranchSession(ctx, repoID, branchID); err != nil {
		return domain.Tab{}, fmt.Errorf("ensure branch session: %w", err)
	}

	// Decide window name.
	windowName, err := s.pickNextWindowName(ctx, sessionName, providerType, name)
	if err != nil {
		return domain.Tab{}, err
	}
	if err := s.deps.Tmux.NewWindow(ctx, sessionName, tmux.NewWindowOpts{Name: windowName, Cwd: cwd}); err != nil {
		return domain.Tab{}, err
	}

	// Recompute and return the new tab.
	s.mu.Lock()
	s.recomputeTabs(ctx, branch)
	var added domain.Tab
	for _, t := range branch.TabSet.Tabs {
		if t.WindowName == windowName {
			added = t
			break
		}
	}
	s.mu.Unlock()

	s.hub.Publish(Event{Type: EventTabAdded, RepoID: repoID, BranchID: branchID, TabID: added.ID, Payload: added})
	return added, nil
}

// RemoveTab kills the underlying tmux window if any. Protected tabs (Files /
// Git, plus the lone Claude tab) are guarded by their Provider's Limits Min;
// the floor protection blocks removal of the last instance of any
// Multiple()=true type so a branch always has at least one of each.
//
// S009: post-Claude-multi the protected flag is no longer the right signal
// (Claude tabs are protected to lock the type but multiple instances are
// removable). Removal eligibility is now: tab must belong to a Multiple()
// type AND removing it must not put the count below Limits.Min.
func (s *Store) RemoveTab(ctx context.Context, repoID, branchID, tabID string) error {
	s.mu.RLock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.RUnlock()
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
		s.mu.RUnlock()
		return ErrBranchNotFound
	}
	var target domain.Tab
	for _, t := range branch.TabSet.Tabs {
		if t.ID == tabID {
			target = t
			break
		}
	}
	if target.ID == "" {
		s.mu.RUnlock()
		return ErrTabNotFound
	}
	provider := s.registry.Get(target.Type)
	if provider == nil {
		s.mu.RUnlock()
		return fmt.Errorf("%w: provider %q not registered", ErrInvalidArg, target.Type)
	}
	// Singleton: refuse outright (Files, Git).
	if !provider.Multiple() {
		s.mu.RUnlock()
		return ErrTabProtected
	}
	// Floor protection: would removing this drop the count below Min?
	limits := provider.Limits(s.deps.Settings)
	count := 0
	for _, t := range branch.TabSet.Tabs {
		if t.Type == target.Type {
			count++
		}
	}
	if limits.Min > 0 && count <= limits.Min {
		s.mu.RUnlock()
		return fmt.Errorf("%w: at least %d %q tab(s) must remain", ErrTabLimit, limits.Min, target.Type)
	}
	sessionName := branch.TabSet.TmuxSession
	s.mu.RUnlock()

	if target.WindowName != "" {
		if err := s.deps.Tmux.KillWindowByName(ctx, sessionName, target.WindowName); err != nil {
			// S009-fix-2: the user's intent is "this tab should be
			// gone". If tmux says the window is already gone (race
			// against sync_tmux recovery, external `kill-window`, or
			// a previous failed RemoveTab whose recompute hadn't
			// caught up), treat the call as success rather than a
			// 500 — pre-fix this 500 left the FE state stuck because
			// `removeTab` skips its `reloadRepos()` on throw, and
			// the user reported the deleted Bash tab "stays in the
			// TabBar until I add or remove a Claude tab".
			if !isWindowGoneErr(err) {
				return err
			}
			s.logger.Info("RemoveTab: window already gone, treating as success",
				"session", sessionName, "window", target.WindowName)
		}
	} else if s.multiTabHook != nil {
		// Non-tmux multi tab (Claude): hand off to the hook so per-tab
		// state (agent, sessions.json entries) is torn down too.
		if err := s.multiTabHook.DeleteTab(ctx, repoID, branchID, tabID); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.recomputeTabs(ctx, branch)
	s.mu.Unlock()
	s.hub.Publish(Event{Type: EventTabRemoved, RepoID: repoID, BranchID: branchID, TabID: tabID})
	return nil
}

// isWindowGoneErr matches tmux's various "the window/session is already
// gone" error strings so RemoveTab can swallow them and still publish
// `tab.removed`. Conservative — we only swallow the specific strings
// tmux is known to produce; anything else propagates as a 500.
func isWindowGoneErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "can't find window") {
		return true
	}
	if strings.Contains(msg, "can't find session") {
		return true
	}
	if strings.Contains(msg, "window") && strings.Contains(msg, "not found") {
		return true
	}
	return false
}

// RenameTab renames a multi-instance tab. Behaviour depends on the tab's
// underlying provider:
//
//   - tmux-backed providers (Bash): renames the tmux window. The tab ID
//     changes because IDs are derived from window names. We migrate any
//     existing `tab_overrides` rows to the new ID so user-set display
//     names ride along across renames.
//   - non-tmux multi providers (Claude post-S009): tab ID is stable. We
//     just record `newName` in `repos.json` `tabOverrides[branchName].
//     names[tabID]` and recompute. The Claude session metadata is
//     unaffected — rename is a TabBar concern, not an agent concern.
//
// Singletons (Files, Git) refuse rename with InvalidArg.
func (s *Store) RenameTab(ctx context.Context, repoID, branchID, tabID, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("%w: empty name", ErrInvalidArg)
	}
	s.mu.RLock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.RUnlock()
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
		s.mu.RUnlock()
		return ErrBranchNotFound
	}
	var target domain.Tab
	for _, t := range branch.TabSet.Tabs {
		if t.ID == tabID {
			target = t
			break
		}
	}
	if target.ID == "" {
		s.mu.RUnlock()
		return ErrTabNotFound
	}
	if !target.Multiple {
		s.mu.RUnlock()
		return fmt.Errorf("%w: only multi-instance tabs can be renamed", ErrInvalidArg)
	}
	sessionName := branch.TabSet.TmuxSession
	branchName := branch.Name
	s.mu.RUnlock()

	// Non-tmux multi (Claude): record the rename as an override.
	if target.WindowName == "" {
		if err := s.deps.RepoStore.SetTabName(repoID, branchName, tabID, newName); err != nil {
			return fmt.Errorf("save tab name override: %w", err)
		}
		s.mu.Lock()
		s.recomputeTabs(ctx, branch)
		s.mu.Unlock()
		s.hub.Publish(Event{Type: EventTabRenamed, RepoID: repoID, BranchID: branchID, TabID: tabID, Payload: newName})
		return nil
	}

	// tmux-backed (Bash): rename the window AND migrate any existing
	// override rows so the new ID inherits the previous metadata.
	newWindowName := domain.WindowName(target.Type, newName)
	if err := s.deps.Tmux.RenameWindow(ctx, sessionName, target.WindowName, newWindowName); err != nil {
		return err
	}
	newTabID := domain.TabID(target.Type, newName)
	if err := s.deps.RepoStore.RenameTabIDInOverrides(repoID, branchName, tabID, newTabID); err != nil {
		s.logger.Warn("RenameTab: migrate overrides failed", "err", err)
	}
	s.mu.Lock()
	s.recomputeTabs(ctx, branch)
	s.mu.Unlock()
	s.hub.Publish(Event{Type: EventTabRenamed, RepoID: repoID, BranchID: branchID, TabID: newTabID, Payload: newName})
	return nil
}

// ReorderTabs (S020) records a new ordering for the given branch's
// `Multiple()=true` tabs. The payload is a slice of tab IDs from a
// single Multiple()=true group; cross-group IDs are rejected with
// InvalidArg. Singleton tabs (Files, Git) are not orderable.
//
// We allow callers to send the order for ONE group at a time — the FE
// drag-and-drop UI only ever moves tabs within one group, so per-call
// validation enforces that. We merge into the existing per-branch order
// slice so a Bash reorder doesn't clobber a previous Claude reorder.
func (s *Store) ReorderTabs(ctx context.Context, repoID, branchID string, order []string) error {
	if len(order) == 0 {
		return fmt.Errorf("%w: empty order", ErrInvalidArg)
	}
	s.mu.RLock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.RUnlock()
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
		s.mu.RUnlock()
		return ErrBranchNotFound
	}
	// Map known tabs by ID so we can validate every payload entry exists
	// and shares one Multiple()=true type.
	tabsByID := map[string]domain.Tab{}
	for _, t := range branch.TabSet.Tabs {
		tabsByID[t.ID] = t
	}
	branchName := branch.Name
	s.mu.RUnlock()

	var groupType string
	for _, id := range order {
		t, ok := tabsByID[id]
		if !ok {
			return fmt.Errorf("%w: unknown tab id %q", ErrInvalidArg, id)
		}
		if !t.Multiple {
			return fmt.Errorf("%w: tab %q is not orderable (singleton)", ErrInvalidArg, id)
		}
		if groupType == "" {
			groupType = t.Type
		} else if t.Type != groupType {
			return fmt.Errorf("%w: cross-group reorder forbidden (saw %q and %q)", ErrInvalidArg, groupType, t.Type)
		}
	}

	// Merge with any existing order: keep IDs from other groups in their
	// previous relative position, replace this group's slice.
	existing := s.deps.RepoStore.TabOrder(repoID, branchName)
	merged := make([]string, 0, len(existing)+len(order))
	seenInPayload := map[string]struct{}{}
	for _, id := range order {
		seenInPayload[id] = struct{}{}
	}
	// Drop any prior entries that are part of this group; reorder will
	// reassert them in payload position.
	for _, id := range existing {
		t, ok := tabsByID[id]
		if !ok {
			// Unknown id from earlier session — keep it; recompute will skip
			// it but we don't want to lose stale entries silently.
			merged = append(merged, id)
			continue
		}
		if t.Type == groupType {
			continue
		}
		merged = append(merged, id)
	}
	// Append payload at the end of merged so this group's relative
	// ordering is recorded. Group adjacency in the rendered TabBar is
	// preserved by `applyTabOverrides`'s walk (it groups consecutive
	// same-type tabs first, then sorts each group by the saved order).
	merged = append(merged, order...)

	if err := s.deps.RepoStore.SetTabOrder(repoID, branchName, merged); err != nil {
		return fmt.Errorf("save tab order: %w", err)
	}
	s.mu.Lock()
	s.recomputeTabs(ctx, branch)
	s.mu.Unlock()
	s.hub.Publish(Event{Type: EventTabReordered, RepoID: repoID, BranchID: branchID, Payload: map[string]any{"order": order, "type": groupType}})
	return nil
}

// ensureBranchSession is a thin wrapper around ensureSession that resolves
// the branch by id and re-collects window specs from every Provider before
// delegating. Returns nil if the branch is gone (the caller path will fail
// downstream and surface a 404).
//
// Used by AddTab so a freshly-created Bash window doesn't fail because the
// underlying tmux session is between sync_tmux cycles.
func (s *Store) ensureBranchSession(ctx context.Context, repoID, branchID string) error {
	s.mu.RLock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.RUnlock()
		return ErrRepoNotFound
	}
	var b *domain.Branch
	for _, br := range repo.OpenBranches {
		if br.ID == branchID {
			b = cloneBranch(br)
			break
		}
	}
	s.mu.RUnlock()
	if b == nil {
		return ErrBranchNotFound
	}
	specs, err := s.collectOpenSpecs(ctx, b, true)
	if err != nil {
		return err
	}
	return s.ensureSession(ctx, b, specs)
}

// EnsureTabWindow guarantees that the tmux session AND the named window
// for the given tab exist. Used by the terminal WS attach handler — pre-
// fix the session/window could be GC'd between AddTab and the user's WS
// attach (especially in the dual-instance dev/host setup). Without this
// guard the attach handler creates the conn-group session on top of a
// base that's missing the window, then `attach-session -t group:idx`
// errors with "window not found" and the FE flips into "Reconnecting…"
// forever. Idempotent: if the session is already alive and the window
// is already there, this is just a couple of cheap tmux round-trips.
//
// Returns ErrRepoNotFound / ErrBranchNotFound / ErrTabNotFound when the
// caller has stale state. Wraps any tmux-side error verbatim.
func (s *Store) EnsureTabWindow(ctx context.Context, repoID, branchID, tabID string) error {
	s.mu.RLock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.RUnlock()
		return ErrRepoNotFound
	}
	var branchSnap *domain.Branch
	for _, br := range repo.OpenBranches {
		if br.ID == branchID {
			branchSnap = cloneBranch(br)
			break
		}
	}
	s.mu.RUnlock()
	if branchSnap == nil {
		return ErrBranchNotFound
	}
	var target domain.Tab
	for _, t := range branchSnap.TabSet.Tabs {
		if t.ID == tabID {
			target = t
			break
		}
	}
	if target.ID == "" || target.WindowName == "" {
		return ErrTabNotFound
	}
	// 1. Make sure the base session exists (and was rebuilt with all
	//    user-added bash windows where applicable — see
	//    enrichRecoverySpecs in sync_tmux.go).
	if err := s.ensureBranchSession(ctx, repoID, branchID); err != nil {
		return fmt.Errorf("ensure branch session: %w", err)
	}
	// 2. Make sure the specific window we're about to attach to is in
	//    the session. If it isn't (host-instance recovery recreated the
	//    base without enrichment, external tmux kill, etc.) recreate it.
	have, err := s.deps.Tmux.ListWindows(ctx, branchSnap.TabSet.TmuxSession)
	if err != nil {
		return fmt.Errorf("list windows: %w", err)
	}
	for _, w := range have {
		if w.Name == target.WindowName {
			return nil
		}
	}
	// Missing — recreate. For Bash the cwd is the worktree path; the
	// default shell starts automatically.
	cwd := branchSnap.WorktreePath
	if err := s.deps.Tmux.NewWindow(ctx, branchSnap.TabSet.TmuxSession, tmux.NewWindowOpts{
		Name: target.WindowName,
		Cwd:  cwd,
	}); err != nil {
		return fmt.Errorf("recreate window %q: %w", target.WindowName, err)
	}
	s.logger.Info("EnsureTabWindow: recreated missing window",
		"session", branchSnap.TabSet.TmuxSession, "window", target.WindowName)
	return nil
}

// pickNextWindowName chooses an available `palmux:{type}:{name}` for the
// given session.
func (s *Store) pickNextWindowName(ctx context.Context, session, providerType, requested string) (string, error) {
	have, err := s.deps.Tmux.ListWindows(ctx, session)
	if err != nil {
		return "", err
	}
	existing := map[string]bool{}
	for _, w := range have {
		existing[w.Name] = true
	}
	if requested != "" {
		candidate := domain.WindowName(providerType, requested)
		if existing[candidate] {
			return "", fmt.Errorf("%w: window %q already exists", ErrInvalidArg, candidate)
		}
		return candidate, nil
	}
	if providerType == "bash" {
		return domain.NextBashWindowName(existing), nil
	}
	// Generic fallback: try {type}, {type}-2, {type}-3, ...
	if !existing[domain.WindowName(providerType, providerType)] {
		return domain.WindowName(providerType, providerType), nil
	}
	for i := 2; i < 10000; i++ {
		w := domain.WindowName(providerType, fmt.Sprintf("%s-%d", providerType, i))
		if !existing[w] {
			return w, nil
		}
	}
	return "", fmt.Errorf("could not pick free window name for %s", providerType)
}
