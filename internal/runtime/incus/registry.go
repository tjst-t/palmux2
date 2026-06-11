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
	"log/slog"
	"sync"

	"github.com/tjst-t/palmux2/internal/config"
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
}

// PublishDefaults is the host-wide configuration for publishing container ports
// as HTTPS subdomains via the Caddy admin API. BaseDomain == "" disables it.
type PublishDefaults struct {
	BaseDomain string // public base domain, e.g. palmux-deploy-test.tjstkm.net
	CaddyAdmin string // Caddy admin API endpoint, e.g. http://localhost:2019
	BasicUser  string // edge basic_auth username
	BasicHash  string // edge basic_auth bcrypt hash
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
		baseDomain: r.publish.BaseDomain,
		caddyAdmin: r.publish.CaddyAdmin,
		basicUser:  r.publish.BasicUser,
		basicHash:  r.publish.BasicHash,
		repoLabel:  repoLabelFromID(repoID),
		wsLabel:    wsLabelFromID(branchID),
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
	return &Registry{
		repoStore:     repoStore,
		settingsStore: settingsStore,
		hostTmux:      hostTmux,
		log:           log,
		cache:         map[cacheKey]runtime.Runtime{},
	}
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
