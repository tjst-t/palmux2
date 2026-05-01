// Package sprint implements the "Sprint Dashboard" tab module (S016).
//
// The Sprint tab is **conditional**: it appears in a branch's TabBar only
// when `docs/ROADMAP.md` exists in the branch's worktree. The provider
// hooks the shared `internal/worktreewatch` so that creation / deletion of
// ROADMAP.md (or any file under `docs/sprint-logs/` and
// `.claude/autopilot-*.lock`) re-runs `recomputeTabs` and emits
// `tab.added` / `tab.removed` plus the dashboard refresh event
// `sprint.changed` to all connected browsers.
//
// The module is read-only — it serves five GET endpoints under
// `/api/repos/{repoId}/branches/{branchId}/sprint/`:
//
//   - overview       : project + progress + active autopilot
//   - sprints/{id}   : per-sprint detail (stories, AC matrix, decisions)
//   - dependencies   : Mermaid graph payload
//   - decisions      : timeline (filterable)
//   - refine         : refine.md cross-sprint feed
//
// Markdown parsing lives in internal/tab/sprint/parser/ — section-level
// fail-safe ensures a malformed section degrades to an empty payload + an
// error annotation rather than crashing the whole response.
package sprint

import (
	"context"
	"log/slog"
	"net/http"
	"os"
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
const TabType = "sprint"

// EventSprintChanged is the WS event type emitted when ROADMAP.md /
// sprint-logs / autopilot lock files mutate. Frontend listens and
// invalidates the affected dashboard view.
const EventSprintChanged store.EventType = "sprint.changed"

// Provider implements tab.Provider for the Sprint Dashboard tab.
//
// Like the Git provider it owns a single shared *worktreewatch.Watcher and
// keeps one Subscription per (repoID,branchID). Subscription lifetimes are
// tied to OnBranchOpen / OnBranchClose. Each subscription watches:
//
//   - <worktree>/docs/ROADMAP.md         (presence / mutation)
//   - <worktree>/docs/sprint-logs        (decisions / refine / acceptance)
//   - <worktree>/.claude                 (autopilot-*.lock create/remove)
//
// On any qualifying event the provider:
//
//  1. asks the Store to recompute the branch's tab list — this picks up
//     ROADMAP.md appearing or disappearing and synthesises tab.added /
//     tab.removed events automatically;
//  2. publishes a `sprint.changed` event so live dashboard browsers
//     trigger a partial refetch.
type Provider struct {
	st *store.Store

	mu      sync.Mutex
	watcher *worktreewatch.Watcher
	// subs is keyed "{repoID}/{branchID}".
	subs map[string]*worktreewatch.Subscription
}

// New returns a Provider with a Store reference for path resolution and
// event publishing.
func New(s *store.Store) *Provider {
	return &Provider{st: s, subs: map[string]*worktreewatch.Subscription{}}
}

func (p *Provider) Type() string          { return TabType }
func (p *Provider) DisplayName() string   { return "Sprint" }
func (p *Provider) Protected() bool       { return false }
func (p *Provider) Multiple() bool        { return false }
func (p *Provider) NeedsTmuxWindow() bool { return false }

// Conditional — Sprint is the first conditional tab. recomputeTabs honours
// the empty-result branch of OnBranchOpen for non-Multiple non-tmux providers
// when this returns true.
func (p *Provider) Conditional() bool { return true }

// Limits — singleton when present.
func (p *Provider) Limits(_ tab.SettingsView) tab.InstanceLimits {
	return tab.InstanceLimits{Min: 1, Max: 1}
}

// OnBranchOpen returns a single Sprint tab iff ROADMAP.md exists in the
// branch's worktree; otherwise it returns no tabs (the branch's TabBar
// simply skips Sprint).
//
// It also (re)installs a worktreewatch subscription for the branch so future
// ROADMAP.md / sprint-logs / autopilot-*.lock changes drive recompute +
// `sprint.changed` events.
func (p *Provider) OnBranchOpen(_ context.Context, params tab.OpenParams) (tab.ProviderResult, error) {
	if params.Branch == nil || params.Branch.WorktreePath == "" {
		return tab.ProviderResult{}, nil
	}
	root := params.Branch.WorktreePath

	// Lazy watcher start — unit tests that only exercise OnBranchOpen
	// (e.g. via a fake Store) skip fsnotify entirely.
	p.startWatcher()
	if p.watcher != nil {
		repoID := params.Branch.RepoID
		branchID := params.Branch.ID
		key := repoID + "/" + branchID
		p.mu.Lock()
		// Replace any stale subscription so re-Open after Close stays clean.
		if old, ok := p.subs[key]; ok {
			old.Unsubscribe()
			delete(p.subs, key)
		}
		// Subscribe to docs/ + .claude/. We watch the parent directories
		// (rather than just the file) because fsnotify cannot follow files
		// across create / unlink: a ROADMAP.md that gets deleted and
		// re-created has to be picked up by the directory watch.
		roots := []string{root}
		sub, err := p.watcher.Subscribe(worktreewatch.Spec{
			Roots:    roots,
			Filter:   sprintFilter(root),
			Debounce: 1000 * time.Millisecond,
			OnEvent: func(events []worktreewatch.Event) {
				p.handleEvents(repoID, branchID, events)
			},
		})
		if err == nil {
			p.subs[key] = sub
		} else {
			slog.Warn("sprint provider: subscribe failed", "err", err)
		}
		p.mu.Unlock()
	}

	// Conditional: only emit a tab when ROADMAP.md is present.
	if !roadmapExists(root) {
		return tab.ProviderResult{}, nil
	}
	return tab.ProviderResult{
		Tabs: []domain.Tab{{
			ID:        TabType,
			Type:      TabType,
			Name:      p.DisplayName(),
			Protected: false,
			Multiple:  false,
		}},
	}, nil
}

// OnBranchClose tears down this branch's filewatch subscription so the OS
// watch refcount drops cleanly.
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

// RegisterRoutes wires the five read-only GET endpoints. All handlers are
// implemented in handler.go.
func (p *Provider) RegisterRoutes(mux *http.ServeMux, prefix string) {
	h := newHandler(p.st)
	mux.HandleFunc("GET "+prefix+"/overview", h.overview)
	mux.HandleFunc("GET "+prefix+"/sprints/{sprintId}", h.sprintDetail)
	mux.HandleFunc("GET "+prefix+"/dependencies", h.dependencies)
	mux.HandleFunc("GET "+prefix+"/decisions", h.decisions)
	mux.HandleFunc("GET "+prefix+"/refine", h.refine)
}

// Close releases the shared watcher. Called from main.go shutdown.
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

func (p *Provider) startWatcher() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.watcher != nil {
		return
	}
	w, err := worktreewatch.New(slog.Default())
	if err != nil {
		slog.Warn("sprint provider: worktree watcher unavailable", "err", err)
		return
	}
	p.watcher = w
}

// handleEvents is invoked from the worktreewatch goroutine after the 1s
// debounce window settles. It triggers a tab recompute (so tab.added /
// tab.removed propagate when ROADMAP.md appears or disappears) and then
// publishes a `sprint.changed` event with the affected scopes so live
// dashboard browsers can refetch only the views that need it.
func (p *Provider) handleEvents(repoID, branchID string, events []worktreewatch.Event) {
	if p.st == nil {
		return
	}
	// Recompute tabs first so that a ROADMAP.md create / delete is observed.
	if err := p.st.RecomputeBranchTabs(repoID, branchID); err != nil {
		slog.Debug("sprint provider: recompute failed", "err", err)
	}

	scopes := scopesFromEvents(events)
	files := make([]string, 0, len(events))
	for _, ev := range events {
		files = append(files, ev.Path)
	}
	p.st.Hub().Publish(store.Event{
		Type:     EventSprintChanged,
		RepoID:   repoID,
		BranchID: branchID,
		Payload: map[string]any{
			"files":  files,
			"scopes": scopes,
		},
	})
}

// scopesFromEvents maps raw paths to dashboard view names so the FE can
// invalidate only the affected screens.
func scopesFromEvents(events []worktreewatch.Event) []string {
	seen := map[string]struct{}{}
	for _, ev := range events {
		switch {
		case strings.HasSuffix(ev.Path, "/docs/ROADMAP.md") || strings.HasSuffix(ev.Path, "ROADMAP.md"):
			seen["overview"] = struct{}{}
			seen["dependencies"] = struct{}{}
			seen["sprintDetail"] = struct{}{}
		case strings.Contains(ev.Path, "/docs/sprint-logs/"):
			base := filepath.Base(ev.Path)
			switch {
			case base == "decisions.md":
				seen["decisions"] = struct{}{}
				seen["sprintDetail"] = struct{}{}
			case base == "refine.md":
				seen["refine"] = struct{}{}
			case strings.HasPrefix(base, "acceptance"), strings.HasPrefix(base, "e2e"):
				seen["sprintDetail"] = struct{}{}
			default:
				seen["sprintDetail"] = struct{}{}
			}
		case strings.Contains(ev.Path, "/.claude/") && strings.HasPrefix(filepath.Base(ev.Path), "autopilot-") && strings.HasSuffix(ev.Path, ".lock"):
			seen["overview"] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	return out
}

// roadmapExists reports whether docs/ROADMAP.md exists as a regular file
// inside `root`. Symlink chases are intentionally avoided — Files-tab
// security gates already block escape, and Sprint is read-only so a
// rogue symlink only loses you a tab.
func roadmapExists(root string) bool {
	if root == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(root, "docs", "ROADMAP.md"))
	if err != nil {
		return false
	}
	return st.Mode().IsRegular()
}

// sprintFilter narrows raw fsnotify events down to the paths we actually
// care about so unrelated edits inside the worktree don't reset the
// debounce timer:
//
//   - docs/ROADMAP.md   (presence + mutation)
//   - docs/             (parent dir create — needed because fsnotify
//                        cannot watch a path that doesn't exist yet,
//                        and ROADMAP.md may be written into a freshly-
//                        created docs/ before the recursive subscriber
//                        registers it)
//   - docs/sprint-logs/... (decisions / refine / acceptance / e2e)
//   - .claude/autopilot-*.lock  (active autopilot run)
//
// Anything else returns false so it's dropped before the debounce buffer.
func sprintFilter(root string) worktreewatch.Filter {
	root = filepath.Clean(root)
	return func(ev worktreewatch.Event) bool {
		rel, err := filepath.Rel(root, ev.Path)
		if err != nil {
			return false
		}
		relSlash := filepath.ToSlash(rel)
		base := filepath.Base(rel)

		// Accept the docs/ parent dir itself (create / remove) so the
		// "freshly-created docs/ROADMAP.md" race is covered — when docs
		// is created, recompute anyway, and on the *next* watcher event
		// (the ROADMAP.md write) we'll have docs/ subscribed.
		if relSlash == "docs" || relSlash == "docs/" {
			return true
		}
		if relSlash == "docs/ROADMAP.md" {
			return true
		}
		if strings.HasPrefix(relSlash, "docs/sprint-logs/") || relSlash == "docs/sprint-logs" {
			return true
		}
		if strings.HasPrefix(relSlash, ".claude/") &&
			strings.HasPrefix(base, "autopilot-") &&
			strings.HasSuffix(base, ".lock") {
			return true
		}
		return false
	}
}
