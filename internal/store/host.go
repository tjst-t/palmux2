package store

import (
	"context"
	"os"

	"github.com/tjst-t/palmux2/internal/domain"
)

// Host scope (S0c6a1b) — a reserved, repository-independent Workspace that
// always exists so the user can open a terminal *before* opening any ghq
// repository (e.g. to run `gh auth login` / `claude` login right after
// installing palmux2).
//
// Design (Option A): instead of inventing a new top-level domain concept, we
// inject ONE synthetic Repository+Branch into the normal `s.repos` map and
// reuse the entire {repoId}/{branchId}/{tabId} tab/attach/reconnect machinery
// unchanged. The reserved IDs are crafted so the derived tmux session name
// `<prefix>host--0000_host` round-trips through [domain.ParseSessionName]
// (its repoID segment `host--0000` contains the mandatory `--`), which means
// the host session is recognised as a Palmux session — so it never shows up
// as an Orphan and is never killed by sync_tmux's zombie pass.
//
// The host scope differs from a real Workspace in four ways, each handled by
// a targeted guard rather than a parallel code path:
//   - it is hidden from GET /api/repos / /api/repos/available and never
//     persisted to repos.json (Repos() + availableRepos skip it; persistence
//     is RepoStore-backed and we never register it there);
//   - it is bash-only — Claude/Files/Git/Sprint tabs are not generated
//     (recomputeHostTabs);
//   - its tmux session is created lazily on first attach, not eagerly by
//     sync_tmux (SyncTmux skips it in the recovery pass but keeps it tracked
//     so it is never zombie-killed) — priority_rule 4 (lazy spawn);
//   - it is skipped by sync_worktree (it has no git worktree to reconcile).
const (
	HostRepoID      = "host--0000"
	HostBranchID    = "host"
	HostDisplayName = "Host"
)

// IsHostRepoID reports whether the repo ID is the reserved host scope.
func IsHostRepoID(id string) bool { return id == HostRepoID }

// hostHomeDir resolves the cwd for host bash terminals: the user's $HOME so
// that `gh auth login`, `claude`, git config, etc. behave as in a normal
// login shell. Falls back to "/" if the home dir cannot be determined.
func hostHomeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return "/"
}

// seedHostScope injects the reserved host Repository+Branch into s.repos.
// Called once from New() AFTER hydrate but BEFORE providers are registered,
// so it only builds the skeleton — tab computation happens later in
// PopulateTabs (which walks s.repos and calls recomputeTabs → recomputeHostTabs).
//
// Idempotent: a second call replaces the skeleton, which is harmless.
func (s *Store) seedHostScope() {
	home := hostHomeDir()
	branch := &domain.Branch{
		ID:           HostBranchID,
		Name:         HostBranchID,
		WorktreePath: home,
		RepoID:       HostRepoID,
		IsPrimary:    false,
		Category:     BranchCategoryUser,
		TabSet: domain.TabSet{
			TmuxSession: domain.SessionName(HostRepoID, HostBranchID),
		},
	}
	repo := &domain.Repository{
		ID:           HostRepoID,
		GHQPath:      "",
		FullPath:     home,
		Starred:      false,
		OpenBranches: []*domain.Branch{branch},
	}
	s.mu.Lock()
	s.repos[HostRepoID] = repo
	s.mu.Unlock()
}

// HostScope returns the reserved host scope descriptor for GET /api/host.
func (s *Store) HostScope() (repoID, branchID, displayName string) {
	return HostRepoID, HostBranchID, HostDisplayName
}

// recomputeHostTabs is the bash-only equivalent of recomputeTabs for the
// reserved host branch. It never generates Claude/Files/Git/Sprint tabs, and
// it always advertises the canonical `bash:bash` tab even before the tmux
// session exists (lazy spawn) so GET /api/host tabs returns a usable default
// the moment the app boots.
//
// Caller holds s.mu (write lock), same contract as recomputeTabs.
func (s *Store) recomputeHostTabs(ctx context.Context, branch *domain.Branch) {
	const bashType = "bash"

	windows, err := s.deps.Tmux.ListWindows(ctx, branch.TabSet.TmuxSession)
	listFailed := err != nil // session not created yet (lazy) → no live windows

	var names []string
	for _, w := range windows {
		typ, name, ok := domain.ParseWindowName(w.Name)
		if ok && typ == bashType {
			names = append(names, name)
		}
	}
	// Always seed the canonical bash window name so the host scope shows at
	// least one Bash tab as metadata — even when the session is still lazy
	// (listFailed) and has no live windows yet.
	if len(names) == 0 {
		names = []string{bashType} // canonical "bash" → tab id bash:bash
	}

	tabs := make([]domain.Tab, 0, len(names))
	for _, n := range names {
		tabs = append(tabs, domain.Tab{
			ID:         domain.TabID(bashType, n),
			Type:       bashType,
			Name:       displayNameForBash(n),
			Protected:  false,
			Multiple:   true,
			WindowName: domain.WindowName(bashType, n),
		})
	}
	tabs = s.applyTabOverrides(branch, tabs)
	branch.TabSet.Tabs = tabs
	_ = listFailed // documented intent; canonical seeding covers both cases
}

// displayNameForBash mirrors the bash provider's display naming for a window
// suffix without needing the provider registered (recomputeHostTabs runs the
// bash branch directly).
func displayNameForBash(windowSuffix string) string {
	if windowSuffix == "bash" {
		return "bash"
	}
	return windowSuffix
}
