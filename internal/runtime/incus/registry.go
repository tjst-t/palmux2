// Package incus — Registry implementation.
//
// Registry maps (repoID, branchID) → Runtime.  For host workspaces it returns
// a host Runtime (no container); for incus-container workspaces it constructs
// (once, then caches) an incusRuntime and returns it.
//
// The lifecycle contract:
//   - Get() lazily creates the incusRuntime but does NOT call Start().  The
//     store calls Start() the first time it tries to use the runtime (inside
//     tmuxFor).
//   - Entries stay in the cache until EvictRuntime is called (which the store
//     does on workspace close).
package incus

import (
	"context"
	"log/slog"
	"sync"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/gwq"
	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/runtime/host"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// Registry is the incus-aware runtime.Registry.  It resolves per-workspace
// runtime config from the config stores and returns either a host or an
// incusRuntime depending on the resolved Kind.
type Registry struct {
	repoStore     *config.RepoStore
	settingsStore *config.SettingsStore
	hostTmux      tmux.Client
	log           *slog.Logger

	mu    sync.RWMutex
	cache map[cacheKey]runtime.Runtime // (repoID, branchID) → live Runtime

	publish PublishDefaults // host-wide port-publishing config (See8bd4-2)

	// shared is the host-wide palmux-shared profile manager (Sd44947). Created
	// lazily on first use; a single instance is shared by every incus runtime and
	// the scan-loop reconcile.
	shared *SharedProfileManager
}

// PublishDefaults is the host-wide configuration for publishing container ports
// as HTTPS subdomains via the Caddy admin API. BaseDomain == "" disables it.
type PublishDefaults struct {
	BaseDomain     string // public base domain, e.g. palmux-deploy-test.tjstkm.net
	CaddyAdmin     string // Caddy admin API endpoint, e.g. http://localhost:2019
	BasicUser      string // edge basic_auth username (legacy; unused with forward_auth)
	BasicHash      string // edge basic_auth bcrypt hash (legacy; unused with forward_auth)
	PalmuxUpstream string // host:port Caddy dials for forward_auth /auth/verify (Sbe4eee)
}

// SetPublishDefaults configures host-wide port-publishing. Call once at startup.
func (r *Registry) SetPublishDefaults(pd PublishDefaults) {
	r.mu.Lock()
	r.publish = pd
	r.mu.Unlock()
}

// buildPublish resolves the per-workspace publish config (DNS labels + host
// defaults). Returns nil when publishing is disabled.
func (r *Registry) buildPublish(repoID, branchID string) *publishConfig {
	if r.publish.BaseDomain == "" {
		return nil
	}
	return &publishConfig{
		baseDomain:     r.publish.BaseDomain,
		caddyAdmin:     r.publish.CaddyAdmin,
		basicUser:      r.publish.BasicUser,
		basicHash:      r.publish.BasicHash,
		palmuxUpstream: r.publish.PalmuxUpstream,
		repoLabel:      repoLabelFromID(repoID),
		wsLabel:        wsLabelFromID(branchID),
	}
}

type cacheKey struct{ repoID, branchID string }

// NewRegistry returns an incus-aware registry.  When a workspace resolves to
// kind=host it returns a host Runtime backed by hostTmux (unchanged behaviour).
// When it resolves to kind=incus-container it constructs and caches an
// incusRuntime.
func NewRegistry(
	repoStore *config.RepoStore,
	settingsStore *config.SettingsStore,
	hostTmux tmux.Client,
	log *slog.Logger,
) *Registry {
	if log == nil {
		log = slog.Default()
	}
	shared := NewSharedProfileManager(nil, log, nil)
	// Mount gwq's worktree base dir (default ~/worktrees, outside ~/ghq) so
	// linked worktrees are visible at the same path inside containers.
	shared.SetWorktreeBasedirFunc(gwq.New().WorktreeBasedir)
	return &Registry{
		repoStore:     repoStore,
		settingsStore: settingsStore,
		hostTmux:      hostTmux,
		log:           log,
		cache:         map[cacheKey]runtime.Runtime{},
		shared:        shared,
	}
}

// SharedProfileManager returns the host-wide palmux-shared profile manager. Used
// by the deploy plane to push new [workspace] shared_dirs (Sd44947 Story 2).
func (r *Registry) SharedProfileManager() *SharedProfileManager { return r.shared }

// ReconcileShared converges the palmux-shared profile to palmux's declaration.
// Implements runtime.SharedProfileReconciler — the store scan loop calls it once
// per cycle so drift self-heals (AC-Sd44947-1-2).
func (r *Registry) ReconcileShared(ctx context.Context) error {
	if r.shared == nil {
		return nil
	}
	return r.shared.Reconcile(ctx)
}

// Get returns the Runtime for the given workspace.  For host kind it always
// returns the same host Runtime; for incus-container it constructs (once) and
// caches an incusRuntime for the workspace.  Start() is NOT called here — the
// caller (store.tmuxFor) is responsible for starting the container.
//
// Implements runtime.Registry.
func (r *Registry) Get(repoID, branchID string) runtime.Runtime {
	globalDefault := r.settingsStore.DefaultRuntime()
	cfg := r.repoStore.ResolveWorkspaceRuntime(repoID, branchID, globalDefault)

	// Normalise: zero Kind defaults to host.
	if cfg.Kind == "" {
		cfg.Kind = runtime.KindHost
	}

	if cfg.Kind == runtime.KindHost {
		// Always return a fresh host Runtime wrapping the shared tmux.Client.
		// No caching needed: host runtime is stateless.
		return host.NewHost(r.hostTmux)
	}

	// incus-container: create once, cache.
	k := cacheKey{repoID, branchID}
	r.mu.RLock()
	rt, ok := r.cache[k]
	r.mu.RUnlock()
	if ok {
		return rt
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Double-check under write lock.
	if rt, ok = r.cache[k]; ok {
		return rt
	}
	inst := InstanceName(repoID, branchID)
	rt = New(cfg, inst, nil /* defaultRunner */, r.log.With("inst", inst))
	if ir, ok := rt.(*incusRuntime); ok {
		ir.pub = r.buildPublish(repoID, branchID)
		ir.SetSharedProfileManager(r.shared) // Sd44947: profile-as-mold
	}
	r.cache[k] = rt
	r.log.Info("incus.Registry: created runtime", "repoID", repoID, "branchID", branchID, "inst", inst)
	return rt
}

// EvictRuntime removes the cached Runtime for the given workspace.  The store
// calls this on workspace close so subsequent Get() calls create a fresh
// Runtime for the same (repoID, branchID) if the workspace is re-opened.
func (r *Registry) EvictRuntime(repoID, branchID string) {
	k := cacheKey{repoID, branchID}
	r.mu.Lock()
	delete(r.cache, k)
	r.mu.Unlock()
}

// RefreshCaddyAdmin hot-applies a new Caddy admin endpoint (Sa53137-3). It
// updates the publish default and drops every cached incus runtime so the next
// Get() rebuilds with the new admin URL; exposed routes self-heal via the 10s
// resync loop. host runtimes are unaffected.
func (r *Registry) RefreshCaddyAdmin(addr string) {
	r.mu.Lock()
	r.publish.CaddyAdmin = addr
	for k := range r.cache {
		delete(r.cache, k)
	}
	r.mu.Unlock()
}

// RefreshBasicAuth hot-applies new per-port basic-auth route defaults
// (Sa53137-3). Same eviction strategy as RefreshCaddyAdmin.
func (r *Registry) RefreshBasicAuth(user, hash string) {
	r.mu.Lock()
	r.publish.BasicUser = user
	if hash != "" {
		r.publish.BasicHash = hash
	}
	for k := range r.cache {
		delete(r.cache, k)
	}
	r.mu.Unlock()
}

// IncusTmuxClientFor returns the incusTmuxClient for an incus-container Runtime
// that was returned by Get(), or nil if the workspace resolves to host.  This
// avoids a second type-assertion chain at the call site.
func (r *Registry) IncusTmuxClientFor(repoID, branchID string) tmux.Client {
	rt := r.Get(repoID, branchID)
	if rt.Kind() != runtime.KindIncusContainer {
		return nil
	}
	ir, ok := rt.(*incusRuntime)
	if !ok {
		return nil
	}
	return NewTmuxClient(ir.inst, ir.run)
}
