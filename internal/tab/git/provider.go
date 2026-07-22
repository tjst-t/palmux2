package git

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/worktreewatch"
)

// TabType is the stable provider identifier.
const TabType = "git"

// watchSub is a live worktree-watch subscription plus the root it was
// created for. The root is what makes subscription REUSE safe: the provider
// resubscribes only when the worktree behind a branch key actually changed
// (S3f3cb2).
type watchSub struct {
	sub  *worktreewatch.Subscription
	root string
}

// Provider implements tab.Provider for the Git tab.
//
// S012: provider now owns a single shared *worktreewatch.Watcher and an
// (repoID,branchID) → *worktreewatch.Subscription map so the Git tab can
// auto-refresh the status view when files in the worktree (or
// `.git/HEAD` / `.git/refs/`) move. Subscriptions are created on
// OnBranchOpen and torn down on OnBranchClose.
type Provider struct {
	store *store.Store

	mu      sync.Mutex
	watcher *worktreewatch.Watcher
	// subs is keyed "{repoID}/{branchID}".
	subs map[string]watchSub
}

// New returns a Provider with a Store reference for path resolution.
func New(s *store.Store) *Provider {
	return &Provider{
		store: s,
		subs:  map[string]watchSub{},
	}
}

func (p *Provider) Type() string          { return TabType }
func (p *Provider) DisplayName() string   { return "Git" }
func (p *Provider) Protected() bool       { return true }
func (p *Provider) Multiple() bool        { return false }
func (p *Provider) NeedsTmuxWindow() bool { return false }

// Limits — Git is a singleton (exactly one tab per branch).
func (p *Provider) Limits(_ tab.SettingsView) tab.InstanceLimits {
	return tab.InstanceLimits{Min: 1, Max: 1}
}

// Tabs reports the single Git tab. Pure (ADR-0012) — note this deliberately
// does NOT start the worktree watcher: that is a side effect and lives in
// OnBranchOpen, which fires only on a genuine branch open. Before ADR-0012
// the two were in the same method.
func (p *Provider) Tabs(_ context.Context, _ tab.TabsParams) ([]domain.Tab, error) {
	return []domain.Tab{{
		ID:        TabType,
		Type:      TabType,
		Name:      p.DisplayName(),
		Protected: true,
	}}, nil
}

// OnBranchOpen subscribes the worktree watcher for this branch so Git status
// changes publish an event. Side effects are legitimate here — this fires on
// a real branch open, not on every tab recompute.
func (p *Provider) OnBranchOpen(_ context.Context, params tab.OpenParams) (tab.ProviderResult, error) {
	// Lazily start the watcher so unit tests that don't need fsnotify
	// (e.g. parseStatus) skip it.
	p.startWatcher()
	if p.watcher != nil && params.Branch != nil {
		repoID := params.Branch.RepoID
		branchID := params.Branch.ID
		root := params.Branch.WorktreePath
		key := repoID + "/" + branchID
		p.mu.Lock()
		if old, ok := p.subs[key]; ok {
			if old.root == root {
				// Already watching exactly this worktree — reuse it.
				//
				// S3f3cb2: re-subscribing here cost ~1.7 s on a large worktree
				// (recursive inotify registration), and OnBranchOpen runs from
				// ensureBranchSession, which is on the path of BOTH tab creation
				// and every WS attach.
				//
				// The unconditional Unsubscribe this replaces was guarding a
				// stale subscription after "branch closed and reopened without
				// provider teardown". That case is still handled: the root
				// comparison resubscribes whenever the worktree behind this key
				// changed. Since S1e8d02 made BranchID path-derived, an
				// unchanged key implies an unchanged root, so the common path
				// now reuses.
				p.mu.Unlock()
				return tab.ProviderResult{}, nil
			}
			old.sub.Unsubscribe()
			delete(p.subs, key)
		}
		sub, err := p.watcher.Subscribe(worktreewatch.Spec{
			Roots:    []string{root},
			Filter:   gitFilter(root),
			SkipDir:  gitSkipDir(root),
			Debounce: 1000 * time.Millisecond,
			OnEvent: func(_ []worktreewatch.Event) {
				p.store.Hub().Publish(store.Event{
					Type:     EventGitStatusChanged,
					RepoID:   repoID,
					BranchID: branchID,
				})
			},
		})
		if err == nil {
			p.subs[key] = watchSub{sub: sub, root: root}
		}
		p.mu.Unlock()
	}
	return tab.ProviderResult{}, nil
}

func (p *Provider) OnBranchClose(_ context.Context, params tab.CloseParams) error {
	if params.Branch == nil {
		return nil
	}
	key := params.Branch.RepoID + "/" + params.Branch.ID
	p.mu.Lock()
	if sub, ok := p.subs[key]; ok {
		sub.sub.Unsubscribe()
		delete(p.subs, key)
	}
	p.mu.Unlock()
	return nil
}

func (p *Provider) RegisterRoutes(mux *http.ServeMux, prefix string) {
	h := &handler{store: p.store}
	mux.HandleFunc("GET "+prefix+"/status", h.status)
	mux.HandleFunc("GET "+prefix+"/log", h.log)
	mux.HandleFunc("GET "+prefix+"/diff", h.diff)
	mux.HandleFunc("GET "+prefix+"/branches", h.branches)
	mux.HandleFunc("GET "+prefix+"/head-message", h.headCommitMessage)
	mux.HandleFunc("GET "+prefix+"/show", h.show)
	mux.HandleFunc("GET "+prefix+"/raw", h.rawShow)

	mux.HandleFunc("POST "+prefix+"/stage", h.stage)
	mux.HandleFunc("POST "+prefix+"/unstage", h.unstage)
	mux.HandleFunc("POST "+prefix+"/discard", h.discard)
	mux.HandleFunc("POST "+prefix+"/stage-hunk", h.stageHunk)
	mux.HandleFunc("POST "+prefix+"/unstage-hunk", h.unstageHunk)
	mux.HandleFunc("POST "+prefix+"/discard-hunk", h.discardHunk)
	mux.HandleFunc("POST "+prefix+"/stage-lines", h.stageLines)

	// S012 write endpoints.
	mux.HandleFunc("POST "+prefix+"/commit", h.commit)
	mux.HandleFunc("POST "+prefix+"/push", h.push)
	mux.HandleFunc("POST "+prefix+"/pull", h.pull)
	mux.HandleFunc("POST "+prefix+"/fetch", h.fetch)

	// Branch CRUD.
	mux.HandleFunc("POST "+prefix+"/branches", h.createBranch)
	mux.HandleFunc("POST "+prefix+"/switch", h.switchBranch)
	mux.HandleFunc("DELETE "+prefix+"/branches/{name}", h.deleteBranch)
	mux.HandleFunc("PATCH "+prefix+"/branches/upstream", h.setUpstream)
}

func (p *Provider) startWatcher() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.watcher != nil {
		return
	}
	w, err := worktreewatch.New(slog.Default())
	if err != nil {
		slog.Warn("git provider: worktree watcher unavailable", "err", err)
		return
	}
	p.watcher = w
}

// gitFilter drops events that don't affect status output:
//
//   - Anything inside `.git/` *except* HEAD, refs/, packed-refs, and
//     ORIG_HEAD (those move when commits / fetches / merges happen).
//   - The .git/index lock file (created and removed continuously while
//     git itself runs; would otherwise spam the status feed).
//   - Files matching common `.gitignore` style noise (`.tmp`, swap files);
//     git status itself ignores them so we should too.
//
// Path comparisons are made relative to root (the worktree path) so the
// filter is portable.
// gitSkipDir prunes watch REGISTRATION inside .git/ down to what gitFilter
// actually accepts: the ref pointers directly in .git/ plus .git/refs/**.
// Everything else under .git/ (objects/, logs/, hooks/, modules/, …) is
// rejected by the filter anyway, so watching it is pure cost.
//
// The working tree itself is NOT pruned — gitFilter accepts any change there
// because that is what moves `git status` (S3f3cb2).
//
// MUST stay consistent with gitFilter: anything the filter accepts has to live
// under a path this function does NOT prune.
func gitSkipDir(root string) func(string) bool {
	root = filepath.Clean(root)
	gitDir := filepath.Join(root, ".git")
	return func(dir string) bool {
		rel, err := filepath.Rel(gitDir, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false // outside .git/ — working tree, keep watching
		}
		if rel == "." {
			return false // .git itself holds HEAD / packed-refs
		}
		top := filepath.ToSlash(rel)
		if i := strings.IndexByte(top, '/'); i >= 0 {
			top = top[:i]
		}
		return top != "refs"
	}
}

func gitFilter(root string) worktreewatch.Filter {
	root = filepath.Clean(root)
	return func(ev worktreewatch.Event) bool {
		rel, err := filepath.Rel(root, ev.Path)
		if err != nil {
			return false
		}
		// Convert to forward-slash for stable matching.
		relSlash := filepath.ToSlash(rel)
		base := filepath.Base(rel)

		if relSlash == ".git" || strings.HasPrefix(relSlash, ".git/") {
			// Allow ref-pointer changes through.
			if relSlash == ".git/HEAD" ||
				relSlash == ".git/ORIG_HEAD" ||
				relSlash == ".git/MERGE_HEAD" ||
				relSlash == ".git/CHERRY_PICK_HEAD" ||
				relSlash == ".git/FETCH_HEAD" ||
				relSlash == ".git/packed-refs" ||
				strings.HasPrefix(relSlash, ".git/refs/") {
				return true
			}
			return false
		}
		// Drop swap / tmp noise.
		if strings.HasSuffix(base, "~") || strings.HasSuffix(base, ".swp") || strings.HasSuffix(base, ".tmp") {
			return false
		}
		return true
	}
}

// Close releases the shared worktree watcher and any active subscriptions.
// Wired from main.go's shutdown sequence so the fsnotify goroutines exit
// cleanly.
func (p *Provider) Close() {
	p.mu.Lock()
	for k, sub := range p.subs {
		sub.sub.Unsubscribe()
		delete(p.subs, k)
	}
	w := p.watcher
	p.watcher = nil
	p.mu.Unlock()
	if w != nil {
		_ = w.Close()
	}
}
