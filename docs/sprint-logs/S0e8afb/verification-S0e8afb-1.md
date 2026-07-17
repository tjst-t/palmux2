# Verification — S0e8afb-1 (P1: mechanical adopt)

Branch: `worktree-S0e8afb-1` (off `autopilot/main/S0e8afb`)

Per the design doc (`docs/agenttui-ptyhost-merge-design.md`), P1 changes NO
runtime behavior — `daemon.go` stays on `creackpty`/host-invocation, and the
Adapter graft (replacing the exec path) is a later Story's job. Verification
for this phase is `go build` + `go vet` + the ptyhost package tests + the
ptyhost ownership test, not a real-VM smoke test.

## What was done

1. Confirmed `internal/ptyhost/**`, `cmd/palmux/ptyhost.go` (+ its ownership
   test), `docs/DESIGN/adr/ADR-0001..0003`, and `docs/no-halt-agent-design.md`
   are already byte-identical between `origin/main` and this branch (this
   branch already contains S3f2658/S862203, which is where these files came
   from) — `git diff HEAD origin/main -- internal/ptyhost/ cmd/palmux/ptyhost.go
   docs/DESIGN/adr/ docs/no-halt-agent-design.md` is empty. AC-S0e8afb-1-1 is
   satisfied by construction (verbatim adopt, no diff needed).

2. `git mv internal/tab/claudetui/ptyclient.go internal/tab/agenttui/ptyclient.go`
   `git mv internal/tab/claudetui/discover.go internal/tab/agenttui/discover.go`
   with `package claudetui` → `package agenttui` in both.

   `ptyclient.go` has zero coupling to claudetui's `Manager`/`Daemon` types
   (only `internal/ptyhost` + stdlib), so it moved as a clean, self-contained
   unit. Because `daemon.go`/`manager.go` (NOT moving in this phase — see
   below) call several of its previously-unexported helpers
   (`defaultLaunchPtyHost`, `inProcessLaunchPtyHost`, `autoTestRunDir`,
   `waitForSocket`, `probeExisting`, `dialFresh`, `sendHello`, `sendAttach`,
   `ptyHostDialTimeout`), those became exported
   (`agenttui.DefaultLaunchPtyHost`, etc.) — a purely mechanical rename
   required to cross the new package boundary, no logic changed.

   `discover.go` is NOT fully self-contained: `GCOrphans` is
   `func (m *Manager) GCOrphans(...)` — a method on claudetui's `Manager`
   type. Since `daemon.go`/`manager.go` (and therefore `Manager`/`Daemon`
   themselves) are explicitly out of scope for this Story (per the design
   doc, they move in a later Story's daemon.go/manager.go graft), Go's type
   system does not allow defining a new method on `claudetui.Manager` from
   package `agenttui` — a byte-identical whole-file move is not possible
   without either moving `Manager` too (out of scope) or introducing an
   import cycle (agenttui needing `claudetui.Manager` for `DiscoverAndRestore`
   while claudetui needs agenttui's ptyclient helpers for `daemon.go` — Go
   forbids the cycle either way `DiscoverAndRestore`/`GCOrphans` are shaped).

   Resolution: `discover.go` split along its actual coupling boundary —
   - **Moved to `agenttui/discover.go`** (Manager-agnostic, exported where a
     claudetui caller needs them): `DiscoveredHost`, `PidAlive` (was
     `pidAlive`), `ScanRunDir` (was `scanRunDir`), `SendOrphanShutdown` (was
     `sendOrphanShutdown` — signature gained an explicit `grace
     time.Duration` parameter since it can no longer read claudetui's
     unexported `gracefulShutdownTimeout` constant directly). `removeStaleFiles`
     and `scanRunDirDialTimeout` stayed unexported (only ever used inside
     this same file).
   - **Stayed behind in a new `internal/tab/claudetui/ptyhost_discovery.go`**
     (irreducibly `*Manager`-coupled): `DiscoverAndRestore` (package-level
     function, unchanged external call shape —
     `claudetui.DiscoverAndRestore(...)` in `cmd/palmux/main.go` is
     untouched) and `(*Manager).GCOrphans` (unchanged external call shape —
     `mgr.GCOrphans(...)` via `internal/store`'s `TuiOrphanGC` interface,
     still structurally satisfied by `*claudetui.Manager`). Both now call
     `agenttui.ScanRunDir`/`agenttui.SendOrphanShutdown` instead of the
     former same-package unexported functions — same logic, crossing a
     package boundary.

   This split is noted in both new files' doc comments as expected to
   collapse back into one file once `Manager`/`Daemon` themselves move to
   `agenttui` in a later Story (S0e8afb-2/3 per the design doc).

3. Updated `daemon.go`/`manager.go` call sites (all in `internal/tab/claudetui`)
   to reference the now-agenttui symbols via `agenttui.` — no reordering, no
   logic changes; `launchAndAttach`'s survivor-probe-then-launch-then-attach
   sequence (the P0 reattach-deadlock risk area) is untouched line-for-line
   except for the symbol qualification.

4. Updated the claudetui test files that called the moved ptyclient.go/
   discover.go helpers directly for their own test scaffolding
   (`discover_test.go`, `instance_isolation_test.go`,
   `real_incus_survival_test.go`, `manager_test.go`,
   `ptyhost_integration_test.go`) to use the `agenttui.` qualifier. Calls to
   `DiscoverAndRestore`/`GCOrphans` themselves needed NO changes in any test
   file — both stayed in claudetui with unchanged signatures/call shape.
   `discover_async_test.go` and `shutdown_reap_test.go` needed no changes at
   all (they only call `DiscoverAndRestore`/`GCOrphans`, never the moved
   primitives directly).

## Files touched

```
R  internal/tab/claudetui/discover.go  -> internal/tab/agenttui/discover.go   (git mv, package rename, then split — see above)
R  internal/tab/claudetui/ptyclient.go -> internal/tab/agenttui/ptyclient.go  (git mv, package rename + selective export)
A  internal/tab/claudetui/ptyhost_discovery.go                                (new: residual DiscoverAndRestore + GCOrphans)
M  internal/tab/claudetui/daemon.go                                           (call sites qualified with agenttui.)
M  internal/tab/claudetui/manager.go                                          (PtyHostLaunch field type qualified)
M  internal/tab/claudetui/discover_test.go                                    (call sites qualified)
M  internal/tab/claudetui/instance_isolation_test.go                          (call sites qualified)
M  internal/tab/claudetui/real_incus_survival_test.go                         (call sites qualified)
M  internal/tab/claudetui/manager_test.go                                     (call sites qualified)
M  internal/tab/claudetui/ptyhost_integration_test.go                         (call sites qualified)
```

No changes to `internal/ptyhost/**`, `cmd/palmux/ptyhost.go`,
`docs/DESIGN/adr/**`, `docs/no-halt-agent-design.md` (already verbatim-equal
to origin/main on this branch).

## Commands run

```
$ go build ./...
(no output — success)

$ go vet ./...
(no output — success)

$ go test ./internal/ptyhost/...
ok  	github.com/tjst-t/palmux2/internal/ptyhost	5.602s
(TestSurvival_RealSystemd_PtyhostOutlivesLauncherRestartAndKill9 SKIPed —
 gated behind PALMUX_SURVIVAL_SMOKE=1 per project convention, unrelated to
 this Story's file-move scope)

$ go test ./cmd/palmux/... -run TestPtyOwnership_ModeFilter -v
--- PASS: TestPtyOwnership_ModeFilter (0.43s)
ok  	github.com/tjst-t/palmux2/cmd/palmux	0.435s

$ go test ./internal/tab/claudetui/... ./internal/tab/agenttui/...
ok  	github.com/tjst-t/palmux2/internal/tab/claudetui	18.128s
?   	github.com/tjst-t/palmux2/internal/tab/agenttui	[no test files]
(agenttui has no test files yet — this phase moved production code only,
 not tests; scanRunDir/DiscoverAndRestore/GCOrphans behavior is still fully
 exercised end-to-end via claudetui's own discover_test.go, which stayed put
 since it primarily tests DiscoverAndRestore/GCOrphans via Manager/Daemon)

$ go test ./...
ALL PASS (33 packages with tests, 0 failures; see full output archived in
this Story's session log)
```

`golangci-lint` could not be run in this environment (installed v1.64.8
binary vs. a v2-format `.golangci.yml` — pre-existing environment/tooling
mismatch unrelated to this change; `make lint` itself gracefully no-ops in
this situation). `go vet ./...` is the mandated, satisfied proxy.

## Acceptance criteria

- **AC-S0e8afb-1-1**: PASS — `internal/ptyhost/**`, `cmd/palmux/ptyhost.go`,
  `docs/DESIGN/adr/ADR-0001..0003`, `docs/no-halt-agent-design.md` are
  byte-identical to `origin/main` (verified via `git diff`, zero output).
- **AC-S0e8afb-1-2**: PASS with a documented, necessary deviation —
  `ptyclient.go` moved via `git mv` + package rename with call sites
  mechanically re-qualified (no logic change). `discover.go` moved via
  `git mv` + package rename, then had its two `*Manager`-coupled functions
  (`DiscoverAndRestore`, `GCOrphans`) split into a new
  `claudetui/ptyhost_discovery.go` residual file, because Go does not permit
  defining new methods on a foreign package's type and `Manager` is
  explicitly not moving in this phase. External call sites/behavior for both
  are unchanged.
- **AC-S0e8afb-1-3**: PASS — `go build ./...` succeeds, `go vet ./...` is
  clean, `internal/ptyhost` package tests pass, and (for completeness beyond
  the mandated scope) the full `go test ./...` suite is green.
