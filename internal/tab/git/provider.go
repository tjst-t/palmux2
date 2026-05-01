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
	subs map[string]*worktreewatch.Subscription
}

// New returns a Provider with a Store reference for path resolution.
func New(s *store.Store) *Provider {
	return &Provider{
		store: s,
		subs:  map[string]*worktreewatch.Subscription{},
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
		// Replace any stale subscription (e.g. branch closed and reopened
		// without provider teardown).
		if old, ok := p.subs[key]; ok {
			old.Unsubscribe()
			delete(p.subs, key)
		}
		sub, err := p.watcher.Subscribe(worktreewatch.Spec{
			Roots:    []string{root},
			Filter:   gitFilter(root),
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
			p.subs[key] = sub
		}
		p.mu.Unlock()
	}
	return tab.ProviderResult{
		Tabs: []domain.Tab{{
			ID:        TabType,
			Type:      TabType,
			Name:      p.DisplayName(),
			Protected: true,
		}},
	}, nil
}

func (p *Provider) OnBranchClose(_ context.Context, params tab.CloseParams) error {
	if params.Branch == nil {
		return nil
	}
	key := params.Branch.RepoID + "/" + params.Branch.ID
	p.mu.Lock()
	if sub, ok := p.subs[key]; ok {
		sub.Unsubscribe()
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

	// AI commit message.
	mux.HandleFunc("POST "+prefix+"/ai-commit-message", h.aiCommitMessage)

	// === S013: history & common ops =====================================
	// Rich log replaces the plain /log GET when the FE sends extra
	// filter parameters; the unfiltered "limit"-only GET still works
	// because logFiltered handles missing params gracefully.
	mux.HandleFunc("GET "+prefix+"/log/filtered", h.logFiltered)
	mux.HandleFunc("GET "+prefix+"/branch-graph", h.branchGraph)

	// Stash CRUD.
	mux.HandleFunc("GET "+prefix+"/stash", h.stashList)
	mux.HandleFunc("POST "+prefix+"/stash", h.stashPush)
	mux.HandleFunc("POST "+prefix+"/stash/{name}/apply", h.stashApply)
	mux.HandleFunc("POST "+prefix+"/stash/{name}/pop", h.stashPop)
	mux.HandleFunc("DELETE "+prefix+"/stash/{name}", h.stashDrop)
	mux.HandleFunc("GET "+prefix+"/stash/{name}/diff", h.stashDiff)

	// Cherry-pick / Revert / Reset.
	mux.HandleFunc("POST "+prefix+"/cherry-pick", h.cherryPick)
	mux.HandleFunc("POST "+prefix+"/revert", h.revert)
	mux.HandleFunc("POST "+prefix+"/reset", h.reset)

	// Tag CRUD.
	mux.HandleFunc("GET "+prefix+"/tags", h.tagList)
	mux.HandleFunc("POST "+prefix+"/tags", h.tagCreate)
	mux.HandleFunc("DELETE "+prefix+"/tags/{name}", h.tagDelete)
	mux.HandleFunc("POST "+prefix+"/tags/push", h.tagPush)

	// File history & blame.
	mux.HandleFunc("GET "+prefix+"/file-history", h.fileHistory)
	mux.HandleFunc("GET "+prefix+"/blame", h.blame)

	// === S014: conflict / rebase / merge / submodule / reflog / bisect ====
	// Conflicts. We use a `?path=` query parameter rather than {path...}
	// because Go 1.22's mux requires `{...}` wildcards to sit at the very
	// end of a pattern; `/conflict/{path...}/mark-resolved` would
	// otherwise collide with `/conflict/{path...}` PUT.
	mux.HandleFunc("GET "+prefix+"/conflicts", h.conflicts)
	mux.HandleFunc("GET "+prefix+"/conflict-file", h.conflictFile)
	mux.HandleFunc("PUT "+prefix+"/conflict-file", h.conflictFilePut)
	mux.HandleFunc("POST "+prefix+"/conflict-file/mark-resolved", h.conflictMarkResolved)

	// Rebase TODO + ops.
	mux.HandleFunc("GET "+prefix+"/rebase-todo", h.rebaseTodoGet)
	mux.HandleFunc("PUT "+prefix+"/rebase-todo", h.rebaseTodoPut)
	mux.HandleFunc("POST "+prefix+"/rebase", h.rebaseStart)
	mux.HandleFunc("POST "+prefix+"/rebase/abort", h.rebaseAbort)
	mux.HandleFunc("POST "+prefix+"/rebase/continue", h.rebaseContinue)
	mux.HandleFunc("POST "+prefix+"/rebase/skip", h.rebaseSkip)

	// Merge ops.
	mux.HandleFunc("POST "+prefix+"/merge", h.mergeStart)
	mux.HandleFunc("POST "+prefix+"/merge/abort", h.mergeAbort)

	// Submodules. Same `?path=` rationale as conflicts.
	mux.HandleFunc("GET "+prefix+"/submodules", h.submodulesList)
	mux.HandleFunc("POST "+prefix+"/submodules/init", h.submoduleInit)
	mux.HandleFunc("POST "+prefix+"/submodules/update", h.submoduleUpdate)

	// Reflog.
	mux.HandleFunc("GET "+prefix+"/reflog", h.reflog)

	// Bisect.
	mux.HandleFunc("GET "+prefix+"/bisect/status", h.bisectStatus)
	mux.HandleFunc("POST "+prefix+"/bisect/start", h.bisectStart)
	mux.HandleFunc("POST "+prefix+"/bisect/good", h.bisectGood)
	mux.HandleFunc("POST "+prefix+"/bisect/bad", h.bisectBad)
	mux.HandleFunc("POST "+prefix+"/bisect/skip", h.bisectSkip)
	mux.HandleFunc("POST "+prefix+"/bisect/reset", h.bisectReset)
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
		sub.Unsubscribe()
		delete(p.subs, k)
	}
	w := p.watcher
	p.watcher = nil
	p.mu.Unlock()
	if w != nil {
		_ = w.Close()
	}
}
