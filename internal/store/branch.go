package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tjst-t/palmux2/internal/domain"
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
	return s.openBranchInternal(ctx, repoID, branchName, true, "")
}

// OpenBranchWithIsolate is like OpenBranch but accepts an isolateNetwork override.
// isolateOverride: "on" | "off" | "" (empty = inherit repo-level setting).
func (s *Store) OpenBranchWithIsolate(ctx context.Context, repoID, branchName, isolateOverride string) (*domain.Branch, error) {
	return s.openBranchInternal(ctx, repoID, branchName, true, isolateOverride)
}

// OpenBranchAuto registers a branch the same way as OpenBranch but does
// NOT mark it as user-opened. Call this from the worktree-sync loop so
// CLI-created worktrees stay in the `unmanaged` (or `subagent`) Drawer
// section until the user explicitly promotes them.
func (s *Store) OpenBranchAuto(ctx context.Context, repoID, branchName string) (*domain.Branch, error) {
	return s.openBranchInternal(ctx, repoID, branchName, false, "")
}

func (s *Store) openBranchInternal(ctx context.Context, repoID, branchName string, markUserOpened bool, isolateOverride string) (*domain.Branch, error) {
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

	// 2. Build the Branch entity (does not yet touch tmux).
	branchID := domain.BranchSlugID(repoFullPath, wt.Branch)
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

	// 3. Network namespace lifecycle (S034). If isolation is enabled for this
	//    repo and the netns manager is available, create (or recover) the ns
	//    before spawning tmux windows so the first window's command can be
	//    wrapped with nsenter.
	// isolateOverride ("on"|"off"|"") takes precedence over repo-level flag.
	shouldIsolate := s.deps.RepoStore.IsolatesNetwork(repoID)
	if isolateOverride == "on" {
		shouldIsolate = true
	} else if isolateOverride == "off" {
		shouldIsolate = false
	}
	var nsenterPath string
	if s.deps.Netns != nil && shouldIsolate {
		ws, nsErr := s.deps.Netns.Create(ctx, branchID, "")
		if nsErr != nil {
			s.logger.Warn("openBranch: netns.Create failed, falling back to host network",
				"branch", branchID, "err", nsErr)
		} else if ws != nil && ws.IsolateNetwork {
			nsenterPath = ws.NetnsPath
			// Start port-discovery loop; events are broadcast via the store hub.
			s.startNetnsDiscovery(ctx, repoID, branchID)
		}
	}

	// 3b. Run each Provider's OnBranchOpen to gather windows + tab metadata.
	specs, err := s.collectOpenSpecs(ctx, branch, false)
	if err != nil {
		return nil, err
	}

	// 3c. Inject nsenterPath into terminal-type windows so they launch inside
	//     the netns. REST-only windows (NeedsTmuxWindow=false) are unaffected.
	if nsenterPath != "" {
		for i := range specs {
			if specs[i].NsenterPath == "" {
				specs[i].NsenterPath = nsenterPath
			}
		}
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

	// Kill tmux first so client connections error out cleanly.
	if err := s.deps.Tmux.KillSession(ctx, branch.TabSet.TmuxSession); err != nil {
		s.logger.Warn("CloseBranch: tmux kill", "session", branch.TabSet.TmuxSession, "err", err)
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

	// S034: tear down netns + slirp4netns for this worktree.
	if s.deps.Netns != nil {
		if err := s.deps.Netns.Destroy(ctx, branchID); err != nil {
			s.logger.Warn("CloseBranch: netns.Destroy failed", "branch", branchID, "err", err)
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
	var windows []tab.WindowSpec
	for _, p := range s.registry.Providers() {
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
func (s *Store) ensureSession(ctx context.Context, branch *domain.Branch, windows []tab.WindowSpec) error {
	cwd := branch.WorktreePath
	sessionName := branch.TabSet.TmuxSession
	exists, err := s.deps.Tmux.HasSession(ctx, sessionName)
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
			err = s.deps.Tmux.NewSession(ctx, tmux.NewSessionOpts{
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
		err = s.deps.Tmux.NewSession(ctx, tmux.NewSessionOpts{
			Name:       branch.TabSet.TmuxSession,
			WindowName: first.Name,
			Cwd:        firstNonEmpty(first.Cwd, cwd),
			Command:    wrapWithNsenter(first.Command, first.NsenterPath),
			Env:        first.Env,
		})
		if err != nil {
			return err
		}
		windows = windows[1:]
	}
	// Get current windows once so we don't add duplicates.
	have, err := s.deps.Tmux.ListWindows(ctx, branch.TabSet.TmuxSession)
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
		err := s.deps.Tmux.NewWindow(ctx, branch.TabSet.TmuxSession, tmux.NewWindowOpts{
			Name:    w.Name,
			Cwd:     firstNonEmpty(w.Cwd, cwd),
			Command: wrapWithNsenter(w.Command, w.NsenterPath),
			Env:     w.Env,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// wrapWithNsenter wraps a command so it runs inside an isolated worktree's
// netns. Two cases:
//
//  1. command non-empty → `sudo -n -E -- nsenter --user= --net= -- <cmd>`
//  2. command empty (tmux defaults to $SHELL) → explicit bash inside the ns
//     so the window's shell ends up isolated. The empty-command path was
//     the long-standing bug: prior code returned "" for empty commands,
//     so tmux opened a bare shell IN THE HOST netns. Result: the user
//     was looking at an "isolated" tab whose shell was actually on the
//     host network. Detected when two isolated worktrees couldn't both
//     bind :5173 from their bash tabs (they were both binding host:5173).
//
// Requires passwordless sudo for nsenter — see docs/INSTALL.md. We need
// to enter both the user and net namespaces, and only root in the parent
// userns (or the same userns) can do that for an unprivileged userns.
func wrapWithNsenter(command, nsenterPath string) string {
	if nsenterPath == "" {
		return command
	}
	// Derive userPath from netPath: /proc/<pid>/ns/net → /proc/<pid>/ns/user
	// (they share the anchor PID and only differ in the trailing component).
	userPath := strings.TrimSuffix(nsenterPath, "/net") + "/user"
	prefix := "sudo -n -E -- nsenter --user=" + userPath + " --net=" + nsenterPath + " --"
	if command == "" {
		// `bash -l` so the user's login profile (PATH, prompt, etc.) is
		// sourced inside the netns shell.
		return prefix + " bash -l"
	}
	return prefix + " " + command
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
