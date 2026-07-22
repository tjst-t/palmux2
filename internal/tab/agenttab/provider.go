// Package agenttab implements the generic (non-claude) agent tab (S0e8afb-3,
// design §"P3 — multiplicity + ownership"). One agenttab.Provider is
// instantiated per enabled, non-claude [agent.Kind] and is registered under
// Type() == that kind's string, e.g. "generic" or a future user-defined
// kind — see cmd/palmux/main.go's per-kind manager loop.
//
// Unlike claude — where the VISIBLE tab (claudeagent.Provider) and the PTY
// RUNTIME (agenttui.Provider, a zero-tab service provider) are split across
// two packages/providers because claude also has a stream-json "agent mode"
// — a generic kind has only the raw-PTY "tui" surface, so agenttab.Provider
// merges both roles into one Provider: it is BOTH the visible tab (persisted
// per-branch tab list, mirroring claudeagent.Provider) AND the PTY runtime
// (WS attach / stats / resize routes, mirroring agenttui.Provider),
// delegating all PTY lifecycle to its own per-kind agenttui.Manager.
//
// Brought over from the maultiagent reference branch (its Sdec0a7-2 origin)
// as part of the agenttui/ptyhost merge's P3 phase — see
// docs/agenttui-ptyhost-merge-design.md. One adaptation from that reference:
// Limits() below does not assume tab.SettingsView has a generalized
// MaxTabsPerBranch(kind) method (this repo's SettingsView only has
// MaxClaudeTabsPerBranch/MaxBashTabsPerBranch — the generalized per-kind
// settings surface is a separate, not-yet-landed feature) — see that
// method's own doc comment for the type-assertion fallback this uses
// instead.
package agenttab

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tjst-t/palmux2/internal/agent"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tab/agenttui"
)

// defaultMaxTabsPerBranch is used when no SettingsView is available (nil), or
// the settings-driven limit resolves to 0 or is unavailable (see Limits).
const defaultMaxTabsPerBranch = 3

// Provider is the tab.Provider for one generic agent kind. Multiple()=true,
// Protected()=false — closing the last tab of a generic kind is allowed
// (unlike claude, which always keeps at least one tab pinned open); Limits
// still enforces Min=1 so a freshly-opened branch shows a default tab, but
// nothing stops the user from removing it afterwards.
type Provider struct {
	kind    agent.Kind
	adapter agent.Adapter
	manager *agenttui.Manager
	store   *agenttui.SessionStore

	resolver agenttui.WorktreeResolver // set once at boot via SetWorktreeResolver
}

// New returns a Provider for kind, backed by mgr (the per-kind agenttui.Manager
// that owns the PTY daemons) and store (the shared, kind-namespaced tab-layout
// persistence — see agenttui.SessionStore.BranchTabs).
func New(kind agent.Kind, adapter agent.Adapter, mgr *agenttui.Manager, store *agenttui.SessionStore) *Provider {
	return &Provider{kind: kind, adapter: adapter, manager: mgr, store: store}
}

var _ tab.RuntimeRestartHook = (*Provider)(nil)

// SetWorktreeResolver installs the lookup used by handleAttach / handleStats
// to resolve a branch's worktree path at attach-time (mirrors
// agenttui.Provider.SetWorktreeResolver). Called once at server boot from
// main.go.
func (p *Provider) SetWorktreeResolver(r agenttui.WorktreeResolver) { p.resolver = r }

func (p *Provider) Type() string          { return string(p.kind) }
func (p *Provider) DisplayName() string   { return p.adapter.DisplayName() }
func (p *Provider) Protected() bool       { return false }
func (p *Provider) Multiple() bool        { return true }
func (p *Provider) NeedsTmuxWindow() bool { return false }
func (p *Provider) Conditional() bool     { return false }

// Limits: at least one tab of this kind is seeded on a fresh branch (so the
// TabBar shows a default entry, matching claude/bash); the upper bound is
// settings-driven when available, default defaultMaxTabsPerBranch.
//
// tab.SettingsView in this repo (unlike the maultiagent reference this
// Provider was adapted from) does not have a generalized
// MaxTabsPerBranch(kind) method — only MaxClaudeTabsPerBranch/
// MaxBashTabsPerBranch exist, and adding a kind-generic method to the core
// SettingsView interface (churning every existing implementer) is not
// required by anything this Story wires (no generic kind is actually
// reachable in production yet — see cmd/palmux/main.go's per-kind loop doc
// comment). Instead this type-asserts for the capability defensively: a
// FUTURE settings-view that does implement it is honoured automatically;
// until then every generic-kind Provider just uses defaultMaxTabsPerBranch.
func (p *Provider) Limits(view tab.SettingsView) tab.InstanceLimits {
	max := defaultMaxTabsPerBranch
	if kv, ok := view.(interface{ MaxTabsPerBranch(kind string) int }); ok {
		if n := kv.MaxTabsPerBranch(string(p.kind)); n > 0 {
			max = n
		}
	}
	return tab.InstanceLimits{Min: 1, Max: max}
}

// CanonicalTabID is the id of the first/default tab of this kind on every
// branch: "<kind>:<kind>" (mirrors claudeagent.CanonicalTabID).
func (p *Provider) CanonicalTabID() string {
	return string(p.kind) + ":" + string(p.kind)
}

// canonicaliseTabID maps a legacy/aliased id (empty or bare kind string) to
// the canonical form, mirroring claudeagent.CanonicaliseTabID.
func (p *Provider) canonicaliseTabID(tabID string) string {
	if tabID == "" || tabID == string(p.kind) {
		return p.CanonicalTabID()
	}
	return tabID
}

// DisplayNameForTab maps a tab id to its UI label, e.g. "dummy:dummy-2" →
// "Dummy 2" (mirrors claudeagent.DisplayNameForTab).
func (p *Provider) DisplayNameForTab(tabID string) string {
	kindStr := string(p.kind)
	base := p.adapter.DisplayName()
	suffix := tabID
	if i := strings.IndexByte(tabID, ':'); i >= 0 {
		suffix = tabID[i+1:]
	}
	if suffix == "" || suffix == kindStr {
		return base
	}
	if strings.HasPrefix(suffix, kindStr+"-") {
		return base + " " + strings.TrimPrefix(suffix, kindStr+"-")
	}
	return suffix
}

// tabsForBranch returns the ordered set of tab ids of this kind for a
// branch. Always non-empty on a genuinely fresh branch (yields just the
// canonical id); once the user has added/removed tabs, the persisted list is
// authoritative — including an empty list, so the last agent tab can be
// closed and stays closed (the user re-adds one from the `+` menu on
// demand).
func (p *Provider) tabsForBranch(repoID, branchID string) []string {
	if !p.store.HasBranchTabs(string(p.kind), repoID, branchID) {
		return []string{p.CanonicalTabID()}
	}
	return p.store.BranchTabs(string(p.kind), repoID, branchID)
}

// OnBranchOpen returns the persisted per-branch tab list for this kind,
// auto-seeding the canonical tab on a branch that has never had one.
// Tabs reports this branch's persisted agent tabs. Pure (ADR-0012): it only
// reads the per-branch tab-id list the provider already persisted, which is
// exactly what the Store's tab-set derivation needs and nothing more.
func (p *Provider) Tabs(_ context.Context, params tab.TabsParams) ([]domain.Tab, error) {
	if params.Branch == nil {
		return nil, nil
	}
	tabIDs := p.tabsForBranch(params.Branch.RepoID, params.Branch.ID)
	tabs := make([]domain.Tab, 0, len(tabIDs))
	for _, id := range tabIDs {
		tabs = append(tabs, domain.Tab{
			ID:        id,
			Type:      string(p.kind),
			Name:      p.DisplayNameForTab(id),
			Protected: false,
			Multiple:  true,
		})
	}
	return tabs, nil
}

// OnBranchOpen has no windows to declare — the agent owns its own PTY, and
// daemon spawn is lazy (first WS attach). Tabs are declared by Tabs.
func (p *Provider) OnBranchOpen(_ context.Context, _ tab.OpenParams) (tab.ProviderResult, error) {
	return tab.ProviderResult{}, nil
}

// OnBranchClose tears down every daemon of this kind owned by the branch and
// forgets its persisted tab layout.
func (p *Provider) OnBranchClose(ctx context.Context, params tab.CloseParams) error {
	if params.Branch == nil {
		return nil
	}
	p.manager.CloseBranchDaemons(ctx, params.Branch.RepoID, params.Branch.ID)
	return p.store.SetBranchTabs(string(p.kind), params.Branch.RepoID, params.Branch.ID, nil)
}

// OnBranchRuntimeRestarted tears down daemons of this kind when the workspace
// runtime is recreated (container regenerate / host↔incus switch), so the
// next WS attach respawns against the new runtime (mirrors
// agenttui.Provider.OnBranchRuntimeRestarted).
func (p *Provider) OnBranchRuntimeRestarted(ctx context.Context, params tab.CloseParams) error {
	if params.Branch == nil {
		return nil
	}
	p.manager.CloseBranchDaemons(ctx, params.Branch.RepoID, params.Branch.ID)
	return nil
}

// AddTabForBranch appends a new tab of this kind to the branch's tab list
// and returns its id (auto-picked, "<kind>:<kind>-2", "<kind>:<kind>-3", …).
// Used by the store's generalized MultiTabHook dispatcher.
func (p *Provider) AddTabForBranch(repoID, branchID string) (string, error) {
	tabs := p.tabsForBranch(repoID, branchID)
	existing := make(map[string]bool, len(tabs))
	for _, t := range tabs {
		existing[t] = true
	}
	newID := p.pickNextTabID(existing)
	tabs = append(tabs, newID)
	if err := p.store.SetBranchTabs(string(p.kind), repoID, branchID, tabs); err != nil {
		return "", err
	}
	return newID, nil
}

// pickNextTabID returns the next available "<kind>:<kind>-N" id (mirrors
// claudeagent.pickNextClaudeTabID).
func (p *Provider) pickNextTabID(existing map[string]bool) string {
	// Prefer the canonical id when free (re-adding after the last tab of this
	// kind was closed) so the tab is named "<Kind>", not "<Kind> 2".
	if canonical := p.CanonicalTabID(); !existing[canonical] {
		return canonical
	}
	kindStr := string(p.kind)
	for i := 2; i < 1_000_000; i++ {
		candidate := fmt.Sprintf("%s:%s-%d", kindStr, kindStr, i)
		if !existing[candidate] {
			return candidate
		}
	}
	return p.CanonicalTabID() + "-overflow"
}

// RemoveTabForBranch tears down the daemon (if any) for tabID and removes it
// from the persisted tab list. Used by the store's generalized MultiTabHook
// dispatcher.
func (p *Provider) RemoveTabForBranch(ctx context.Context, repoID, branchID, tabID string) error {
	tabID = p.canonicaliseTabID(tabID)
	tabs := p.tabsForBranch(repoID, branchID)
	out := tabs[:0]
	for _, t := range tabs {
		if t != tabID {
			out = append(out, t)
		}
	}
	if err := p.store.SetBranchTabs(string(p.kind), repoID, branchID, out); err != nil {
		return err
	}
	return p.manager.CloseDaemon(ctx, repoID, branchID, tabID)
}

// RegisterRoutes attaches the PTY WS/stats/resize endpoints under prefix
// (= "/api/repos/{repoId}/branches/{branchId}/<kind>", computed by
// server.go from Type()). Distinct from agenttui.Provider's hardcoded
// ".../tabs/{tabId}/tui/*" (claude's own runtime) because the kind segment
// in prefix keeps the pattern unique per generic kind — net/http's
// ServeMux panics on two handlers registered for an identical pattern, so
// this MUST NOT collide with agenttui.Provider's routes or another kind's.
//
//	WS    GET  {prefix}/tabs/{tabId}/tui/attach
//	JSON  GET  {prefix}/tabs/{tabId}/tui/stats
//	JSON  POST {prefix}/tabs/{tabId}/tui/resize
func (p *Provider) RegisterRoutes(mux *http.ServeMux, prefix string) {
	base := prefix + "/tabs/{tabId}/tui"
	mux.HandleFunc("GET "+base+"/attach", p.handleAttach)
	mux.HandleFunc("GET "+base+"/stats", p.handleStats)
	mux.HandleFunc("POST "+base+"/resize", p.handleResize)
}

func (p *Provider) resolveWorktree(repoID, branchID string) string {
	if p.resolver == nil {
		return ""
	}
	return p.resolver.BranchWorktreePath(repoID, branchID)
}

// handleAttach upgrades to WebSocket and attaches to the per-tab Daemon,
// lazily spawning it on first attach. A spawn failure — including "no
// in-container support" — closes the WS with an error status rather than
// silently falling back (see agenttui.Daemon.spawnWithArgs).
func (p *Provider) handleAttach(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	tabID := r.PathValue("tabId")
	worktree := p.resolveWorktree(repoID, branchID)

	d, err := p.manager.EnsureDaemon(r.Context(), repoID, branchID, tabID, worktree)
	if err != nil {
		http.Error(w, "daemon error", http.StatusInternalServerError)
		slog.Error("agenttab: handleAttach EnsureDaemon", "kind", p.kind, "err", err)
		return
	}
	agenttui.AttachHandler(d).ServeHTTP(w, r)
}

// handleStats serves a JSON Stats snapshot for the per-tab Daemon.
func (p *Provider) handleStats(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	tabID := r.PathValue("tabId")
	worktree := p.resolveWorktree(repoID, branchID)

	d, err := p.manager.EnsureDaemon(r.Context(), repoID, branchID, tabID, worktree)
	if err != nil {
		http.Error(w, "daemon error", http.StatusInternalServerError)
		slog.Error("agenttab: handleStats EnsureDaemon", "kind", p.kind, "err", err)
		return
	}
	agenttui.StatsHandler(d).ServeHTTP(w, r)
}

// resizeBody is the JSON body for POST …/resize.
type resizeBody struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// handleResize decodes {"cols":<uint16>,"rows":<uint16>} and propagates a
// terminal resize to the PTY. Returns 204 No Content on success.
func (p *Provider) handleResize(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	tabID := r.PathValue("tabId")
	d := p.manager.Get(repoID, branchID, tabID)
	if d == nil {
		http.Error(w, "no daemon for tab", http.StatusNotFound)
		return
	}
	var body resizeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := d.Resize(body.Cols, body.Rows); err != nil {
		http.Error(w, "resize error: "+err.Error(), http.StatusInternalServerError)
		slog.Warn("agenttab: resize", "kind", p.kind,
			"repoId", repoID, "branchId", branchID, "tabId", tabID, "err", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
