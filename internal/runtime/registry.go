package runtime

import "context"

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

// SharedProfileReconciler is an OPTIONAL capability a Registry may implement to
// self-heal a host-wide shared configuration on each scan cycle (Sd44947). The
// incus Registry implements it by reconciling the `palmux-shared` incus profile
// so a hand-stripped or drifted device set converges back to palmux's
// declaration (profile-as-mold). The store's 10s scan loop type-asserts for it
// and calls ReconcileShared once per cycle when incus containers are running.
type SharedProfileReconciler interface {
	// ReconcileShared converges host-wide shared config to the declaration.
	// Idempotent; safe to call every scan tick.
	ReconcileShared(ctx context.Context) error
}
