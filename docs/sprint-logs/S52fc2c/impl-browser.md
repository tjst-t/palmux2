# S52fc2c-6 — Browser tab Manager reset on container regenerate

## Summary

When an incus-container Workspace is regenerated (`store.RegenerateBranchContainer`,
S7364e3), the OLD container — and with it the Workspace's chromium, x11vnc, and
CDP relay — is destroyed and replaced. The per-Workspace Browser **Manager**
(`internal/tab/browser`) still held the stale in-container PIDs and the old
container's bridge IP (`cdpAddr`), so any later Browser attach would dial the
dead container.

This change makes the Browser `Provider` implement the **existing**
`tab.RuntimeRestartHook` capability so the store resets the Manager during
regeneration. No new store hook was added — the regenerate path already invokes
`RuntimeRestartHook.OnBranchRuntimeRestarted` on every provider via
`restartBranchTabDaemons` (the seam introduced for the in-container Claude
daemons, S4d8b1c-fix). The Browser provider simply opts into it.

## Mechanism used

Existing optional capability `tab.RuntimeRestartHook` (defined in
`internal/tab/provider.go:74-85`):

```go
type RuntimeRestartHook interface {
    OnBranchRuntimeRestarted(ctx context.Context, params CloseParams) error
}
```

Call site (unchanged): `internal/store/branch.go`
- `restartBranchTabDaemons` (`branch.go:683-694`) iterates all providers and
  type-asserts `tab.RuntimeRestartHook`.
- It is invoked by `RegenerateBranchContainer` (`branch.go:802`) **after** the
  new container is live, and also by `RestartBranchRuntime` (host↔incus switch).

The Browser provider now implements that hook to reset its Manager. Because the
existing wiring already covers both the container regenerate AND the host↔incus
switch paths, the Browser tab is now correctly reset in both cases.

**Why reset (in-memory) and not Stop (exec pkill):** by the time the hook fires
the OLD container is already destroyed, so there is nothing to kill. `Reset()`
only clears the Manager's in-memory state (PIDs + `cdpAddr`); the next
`Start()` re-resolves the NEW container's bridge IP and spawns a fresh daemon
stack. The Manager is kept in the provider map (the Workspace is still open —
only its container changed), unlike `OnBranchClose` which removes it.

## Files changed

| File | Lines | Change |
|---|---|---|
| `internal/tab/browser/browser.go` | +24 (after `Stop`, ~L496) | New `Manager.Reset()` — clears `state`/`xvfbPID`/`dbusPID`/`pid`/`fcitxPID`/`vncPID`/`relayPID`/`cdpAddr` under the mutex, no runtime exec. Idempotent. |
| `internal/tab/browser/provider.go` | +27 | New `Provider.OnBranchRuntimeRestarted` (implements `tab.RuntimeRestartHook`): looks up the Manager via `managerFor`; if present calls `Reset()`; no-op when nil-branch / no-manager (host-safe). Plus a `var _ tab.RuntimeRestartHook = (*Provider)(nil)` compile-time assertion. |
| `internal/tab/browser/provider_reset_test.go` | new, 150 lines | Unit tests for the provider-level reset + Manager.Reset. |
| `internal/store/regenerate_browser_reset_test.go` | new, 183 lines | Store-level wiring test: `RegenerateBranchContainer` fires `RuntimeRestartHook` for the regenerated branch; host runtime is a no-op. |

No edits to `incus.go` or nix (owned by other agents). The store edit is **test
only** — the production wiring in `RegenerateBranchContainer` was already
present and needed no change.

## How the ACs are met

- **AC-S52fc2c-6-1** (Manager reset/disposed on regenerate): `OnBranchRuntimeRestarted`
  → `Manager.Reset()` clears all PIDs + `cdpAddr` and sets state=stopped. A
  later `AttachVNC` with `cdpAddr==""` / state!=running is refused cleanly
  (`cdp.go:41`), so no stale connection survives. Idempotent + host-safe
  (no-op when no Manager).
- **AC-S52fc2c-6-2** (reconnect to NEW container): after reset, the next
  `Start()` calls `m.rtNow()` → `reg.Get(repoID, branchID)` which now resolves
  the NEW container and re-reads its bridge IP into `cdpAddr` (the Manager's
  `getRT` closure is keyed by repo/branch, not a cached runtime), then respawns
  the full daemon stack. The frontend receives `branch.restarted` and re-issues
  Start/attach.
- **AC-S52fc2c-6-3**: see real-incus steps below (orchestrator).

## Unit test output

```
=== RUN   TestOnBranchRuntimeRestarted_ResetsManager
--- PASS: TestOnBranchRuntimeRestarted_ResetsManager (0.00s)
=== RUN   TestOnBranchRuntimeRestarted_OnlyAffectsTargetBranch
--- PASS: TestOnBranchRuntimeRestarted_OnlyAffectsTargetBranch (0.00s)
=== RUN   TestOnBranchRuntimeRestarted_NoManager
--- PASS: TestOnBranchRuntimeRestarted_NoManager (0.00s)
=== RUN   TestOnBranchRuntimeRestarted_NilBranch
--- PASS: TestOnBranchRuntimeRestarted_NilBranch (0.00s)
=== RUN   TestManagerReset_Idempotent
--- PASS: TestManagerReset_Idempotent (0.00s)
ok  	github.com/tjst-t/palmux2/internal/tab/browser

=== RUN   TestRegenerateBranchContainer_InvokesResetHook
--- PASS: TestRegenerateBranchContainer_InvokesResetHook (0.00s)
=== RUN   TestRegenerateBranchContainer_HostNoOp
--- PASS: TestRegenerateBranchContainer_HostNoOp (0.00s)
ok  	github.com/tjst-t/palmux2/internal/store
```

`go build ./...` and `go vet ./internal/...` both pass clean.

## REAL-INCUS VERIFICATION STEPS (AC-S52fc2c-6-3)

Run on `palmux-deploy-test.tjstkm.net` (real incus backend) with a built
palmux2 carrying this change, against an incus-container Workspace.

1. Open an incus-container Workspace; open the **Browser** tab; click **Start**.
   Confirm the noVNC canvas renders chromium (state → `running`). Note the
   container bridge IP (`incus list <inst>` → IPv4) and the running PIDs
   (`incus exec <inst> -- pgrep -a chromium`).
2. Trigger **Update container** (runtime chip menu → "Update container", i.e.
   `POST /api/repos/{repoId}/branches/{branchId}/runtime/regenerate`). Wait for
   the runtime to return to `ready`. The container is destroyed + recreated, so
   the bridge IP and all PIDs differ.
3. Confirm the Browser tab's old VNC WS dropped (server log: `browser: manager
   reset (container recreated)` for that `inst`), and the Browser tab state went
   to `stopped` (the canvas clears / shows "browser not running").
4. Click **Start** again. Confirm:
   - state → `running` and the noVNC canvas re-renders against the NEW
     container (`incus exec <NEW inst> -- pgrep -a chromium` shows fresh PIDs);
   - `GET .../tabs/browser/state` returns the new running state with
     `cdpReachable:true`;
   - typing/navigating in the canvas works (live operation on the new chromium).
5. Negative/host check: on a **host-runtime** Workspace, "Update container" is
   absent / no-op; confirm no Browser reset log and no regression.

Expected: after Update the Browser tab cleanly reconnects to the new container
(no stale-IP dial failures), satisfying AC-6-2/6-3.
