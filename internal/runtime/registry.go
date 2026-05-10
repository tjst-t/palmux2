package runtime

import (
	"sync"
)

// Registry holds the live Runtime instance for each open Workspace, keyed
// by the (repoID, branchID) tuple.
//
// Phase A wires this in main.go: the store calls Get/Lookup to surface
// runtime state in API responses (Branch.Runtime view) and lifecycle hooks
// call Set/Delete.
//
// Implementations must be safe for concurrent use.
type Registry interface {
	// Get returns the live Runtime for the workspace, or nil if absent.
	Get(repoID, branchID string) Runtime
	// Set installs a Runtime for the workspace. Replaces any existing.
	Set(repoID, branchID string, rt Runtime)
	// Delete removes a Runtime entry (typically called from
	// store.CloseBranch after Stop has succeeded).
	Delete(repoID, branchID string)
	// Each iterates over every (repoID, branchID, Runtime) entry. The
	// callback is invoked while the registry lock is held — keep it
	// short and avoid calling back into the registry. Return false to
	// stop iteration early.
	Each(fn func(repoID, branchID string, rt Runtime) bool)
}

// MemoryRegistry is the default in-memory Registry implementation.
type MemoryRegistry struct {
	mu sync.RWMutex
	// keyed by repoID|branchID
	m map[string]Runtime
}

// NewMemoryRegistry returns an empty MemoryRegistry.
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{m: map[string]Runtime{}}
}

func (r *MemoryRegistry) key(repoID, branchID string) string { return repoID + "|" + branchID }

// Get returns the live Runtime or nil.
func (r *MemoryRegistry) Get(repoID, branchID string) Runtime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[r.key(repoID, branchID)]
}

// Set installs a Runtime.
func (r *MemoryRegistry) Set(repoID, branchID string, rt Runtime) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[r.key(repoID, branchID)] = rt
}

// Delete removes a Runtime entry.
func (r *MemoryRegistry) Delete(repoID, branchID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, r.key(repoID, branchID))
}

// Each iterates over every entry.
func (r *MemoryRegistry) Each(fn func(repoID, branchID string, rt Runtime) bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for k, v := range r.m {
		// key is "repoID|branchID"
		var repoID, branchID string
		for i := 0; i < len(k); i++ {
			if k[i] == '|' {
				repoID = k[:i]
				branchID = k[i+1:]
				break
			}
		}
		if !fn(repoID, branchID, v) {
			return
		}
	}
}
