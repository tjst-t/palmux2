package runtime

// Registry maps a (repoID, branchID) pair to the live Runtime for that
// workspace.  The store consults it when it needs to know which runtime a
// workspace is running in.
//
// For Story S8478ca-1, every workspace resolves to a host Runtime (Story -3
// adds real per-workspace resolution).  Get returns nil if no entry is found,
// which callers treat as "use host fallback".
type Registry interface {
	// Get returns the Runtime for the given workspace, or nil if no runtime
	// has been registered for it yet (caller should fall back to host).
	Get(repoID, branchID string) Runtime
}
