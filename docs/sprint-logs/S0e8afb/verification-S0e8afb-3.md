# Verification — S0e8afb-3 (P3: multiplicity + ownership)

Branch: `worktree-S0e8afb-3` (off `autopilot/main/S0e8afb`, on top of the
merged S0e8afb-1/S0e8afb-2).

Scope per the task brief and the design doc
(`docs/agenttui-ptyhost-merge-design.md`, "P3 — multiplicity + ownership"):
per-kind `agenttui.Manager` + `AgentKind`-based discovery/GC ownership
separation, wired for **claude and generic only** — codex/opencode kind
*registration* stays deferred to the next Story (S2b5691), even though their
adapter files already exist (brought in verbatim by S0e8afb-2).

## The Sfeed64-3 bug I read, and how the AgentKind filter avoids repeating it

Per the task brief's explicit instruction, before writing any filter code I
read `git log --oneline --all | grep -i Sfeed64` and then
`git show 43cc2da` (`fix(no-halt-agent): Sfeed64-3 owner-mode ptyhost
adoption/GC filter (tui=pty, agent=pipe)`) in full, plus the current
`docs/no-halt-agent-design.md`-adjacent doc comments it left behind in
`internal/tab/agenttui/discover.go` and `internal/tab/claudeagent/discover.go`.

**The bug**: before Sfeed64-3, `claudetui`'s and `claudeagent`'s Managers
shared one on-disk ptyhost run directory (both derive their socket path from
`(repoId, branchId, tabId)` alone, independent of which package spawned it).
Neither manager's discovery/GC scan had any ownership check at all — every
live `(*.sock, *.json)` pair found was treated as "mine". A real dogfood
restart showed both managers **dialing and adopting the SAME two ptyhosts**.
Because `ptyhost.Server.replaceConn` tolerates only ONE active connection at
a time (a new dial silently evicts whatever connection was previously
active), the two managers evicted each other's live connection in a loop —
observed as a broken-pipe → SHUTDOWN+respawn storm, with the surviving
claude session losing its screen/conversation continuity even though it
eventually self-healed.

**The fix, and the exact ordering discipline that matters**: `ptyhost.
StatusFile.Mode` is the explicit, authoritative ownership marker (written
directly from the spawning `Config`, never inferred). `scanRunDir`/
`scanAgentRunDir` check it **immediately after resolving the status file's
identity fields, and BEFORE any dial** (pid-alive check, socket dial, HELLO
probe) — the fix's own doc comment is explicit about this: "dialing it here
(even just for the HELLO liveness probe below) would evict the other
manager's own live connection ... the exact dual-manager eviction loop this
story fixes." The critical lesson is **not** "check X before doing Y" in the
abstract — it is specifically that *any* dial, even a read-only liveness
probe, is itself destructive against another manager's live connection, so
the ownership check must gate the dial, not just gate the "is this stale"
classification that happens after a successful dial.

**How my AgentKind filter avoids the same class of mistake**: in
`internal/tab/agenttui/discover.go`'s `ScanRunDir`, I placed the new
AgentKind check **directly after the existing Sfeed64-3 Mode check**, both
still strictly before `skipLive`, `PidAlive`, and the `net.DialTimeout` call:

```go
if sf.Mode == ptyhost.ModePipe {
    continue
}
// S0e8afb-3: AgentKind ownership filter, BEFORE any dial — same
// ordering discipline as the Sfeed64-3 Mode filter immediately above...
effectiveKind := sf.AgentKind
if effectiveKind == "" {
    effectiveKind = string(agent.KindClaude)
}
if effectiveKind != string(thisKind) {
    continue
}
if skipLive != nil && skipLive(repoID, branchID, tabID) {
    ...
}
if !PidAlive(sf.Pid) { ... }
conn, derr := net.DialTimeout("unix", sockPath, scanRunDirDialTimeout)
```

Every branch that `continue`s before the dial line is untouched — no pid
check, no dial, no HELLO, no counting as cleaned. This is verified not just
by code reading but by the new `TestAgentKindOwnership_
DiscoveryDoesNotCrossAdopt` test below, whose core assertion (the *pid* of
the adopted daemon exactly matches the pre-existing ptyhost's pid) is
specifically designed to catch a dial-before-check regression: if the
ownership check ran AFTER a liveness dial instead of before, the OTHER
kind's manager would have evicted the connection and this test's pid
assertion would fail (a fresh-spawn pid, not the original), even if the
final "which manager adopted which identity" bookkeeping happened to look
right by coincidence.

One additional subtlety **specific to this Story** (not present in the
Sfeed64-3 Mode case): back-compat for a status file written before
`AgentKind` existed. An empty `Mode` already defaults to `ModePTY` inside
`ptyhost.NewServer` (the field's own zero value = the historical only mode),
so Sfeed64-3 didn't need a separate "empty means X" rule — `sf.Mode == ""`
just naturally falls through the `== ModePipe` check as "not pipe". AgentKind
has no such built-in default (it is written by the *caller*, not defaulted
inside `ptyhost.NewServer`), so I added the explicit `effectiveKind == "" →
agent.KindClaude` back-compat rule and covered it with
`TestAgentKindOwnership_EmptyAgentKindTreatedAsClaude` — an in-place binary
upgrade's pre-existing claude ptyhosts (spawned by the pre-S0e8afb-3 binary,
so `AgentKind` is unset) must still be re-adopted by the claude Manager, not
silently orphaned or claimed by a different kind.

## What was done

### 1. `internal/ptyhost/server.go` — additive `AgentKind`/`KillPattern`

Added `AgentKind string` and `KillPattern string` to both `Config` and
`StatusFile` (opaque echo, same discipline as `RepoID`/`BranchID`/`TabID` —
ptyhost stores and returns them verbatim, never interprets them per
ADR-0002). `writeStatusFile` now copies both fields through. [AC-S0e8afb-3-1]

### 2. `cmd/palmux/ptyhost.go` — `--agent-kind`/`--kill-pattern` flags

Mirrors the existing `--repo-id`/`--branch-id`/`--tab-id` flag pattern
exactly — opaque, stored/echoed verbatim, never acted on by the `palmux
ptyhost` subcommand itself (ADR-0002's "ptyhost has zero claude-specific
knowledge" discipline extends unchanged to kind-specific knowledge too).

### 3. `internal/tab/agenttui/ptyclient.go` — thread through the launch request

`PtyHostLaunchRequest` gained `AgentKind`/`KillPattern`. `DefaultLaunchPtyHost`
appends `--agent-kind`/`--kill-pattern` to the real subprocess launch when
non-empty; `InProcessLaunchPtyHost` (the test fallback) sets the same fields
directly on `ptyhost.Config`.

### 4. `internal/tab/agenttui/daemon.go` — `launchAndAttach` populates them

```go
req := PtyHostLaunchRequest{
    ...
    AgentKind:   string(d.adapter.Kind()),
    KillPattern: d.killPattern,
    ...
}
```

`d.killPattern` was already set (under `stateMu`, by `spawnWithArgs`,
synchronously immediately before calling `launchAndAttach` — the only call
site) so reading it here without re-taking the lock is safe: no other
goroutine writes it concurrently with this read, and program order within
the same goroutine guarantees this read observes that write.

### 5. `internal/tab/agenttui/discover.go` — the AgentKind ownership filter

`ScanRunDir` gained a `thisKind agent.Kind` parameter. The filter is
described in detail above ("How my AgentKind filter avoids..."). Also added
`DiscoveredHost.KillPattern`, populated from `sf.KillPattern` for every live
entry returned, so callers with no live in-memory Daemon (GCOrphans) can
still recover the ORIGINAL spawn's kill pattern from disk. [AC-S0e8afb-3-2]

### 6. `internal/tab/agenttui/ptyhost_discovery.go` — wire the kind through + finish the KillPattern deferral

`DiscoverAndRestore` and `(*Manager).GCOrphans` both now call
`ScanRunDir(runDir, mgr.Kind(), ...)` — each Manager scopes discovery/GC to
its OWN kind, using the `Kind()` accessor S0e8afb-2 already added.

**Completing S0e8afb-2's Deviation 3**: that Story's verification doc
explicitly recorded that `GCOrphans`'s orphan reap (no live Daemon to ask)
was left on the hardcoded `containerClaudeBin` fallback because
`ptyhost.StatusFile` didn't carry `KillPattern` yet, and scoped closing that
gap to `AC-S0e8afb-3-1`. This Story closes it:

```go
func killPatternOrFallback(pattern string) string {
    if pattern != "" {
        return pattern
    }
    return containerClaudeBin
}
```

`GCOrphans` now calls `reapContainerClaude(..., killPatternOrFallback(h.
KillPattern), ...)` instead of the hardcoded constant directly — an orphan
whose status file carries a real `KillPattern` (any spawn made by this
Story's binary or later) gets reaped with the ADAPTER's own declared
pattern; only a truly ancient (pre-S0e8afb-3) orphan status file falls back
to the constant. Live-Daemon reap paths (`teardown`, `respawnLoop`) were
already using `d.effectiveKillPattern()` since S0e8afb-2 and are unchanged.

### 7. `internal/tab/agenttui/store.go` — additive `BranchTabs` persistence

Brought over the `BranchTabs`/`SetBranchTabs`/`HasBranchTabs` kind-namespaced
tab-layout persistence from the maultiagent reference branch's `agenttui/
store.go`, adapted to NOT rename the file (maultiagent's Sdec0a7-1 also
renamed `claudetui-sessions.json` → `agent-sessions.json` with a legacy
read-back fallback — that rename is an unrelated, separate-Sprint decision
not required by anything in this Story, so I kept the existing filename and
brought over only the new `BranchTabs` map + accessor methods, purely
additive to the JSON schema (`omitempty`), verified round-trip-compatible
with a pre-existing `claudetui-sessions.json` that has no `branchTabs` key).

### 8. `internal/tab/agenttab/` (new package) — the generic agent tab Provider

Brought over `provider.go` + both test files (`provider_test.go`,
`delete_freely_test.go`) from the maultiagent reference branch, byte-for-byte
except one deliberate adaptation: `Limits()` in maultiagent assumes `tab.
SettingsView` has a generalized `MaxTabsPerBranch(kind string) int` method
(a capability from maultiagent's own Sdec0a7-2 Sprint). This repo's `tab.
SettingsView` only has `MaxClaudeTabsPerBranch()`/`MaxBashTabsPerBranch()` —
adding a kind-generic method to that CORE interface would force churn on
every existing implementer for a capability nothing in this Story's AC list
requires (no generic kind is reachable in production — see below). I
type-assert for the wider interface defensively instead:

```go
if kv, ok := view.(interface{ MaxTabsPerBranch(kind string) int }); ok {
    if n := kv.MaxTabsPerBranch(string(p.kind)); n > 0 {
        max = n
    }
}
```

A future settings-view that DOES implement it is honoured automatically
(and `provider_test.go`'s own `fakeSettingsView`, brought over unmodified
from maultiagent, already exercises exactly this shape — it implements both
the core `tab.SettingsView` interface and the wider `MaxTabsPerBranch`
method, so the type assertion succeeds in that test as designed). Until such
a settings-view exists, every generic-kind Provider just uses
`defaultMaxTabsPerBranch` (3), matching maultiagent's own fallback default.

### 9. `cmd/palmux/main.go` — `agent.BuildRegistry` + per-kind loop + `agenttab` registration

Replaced the bare `agent.NewClaudeAdapter(claudeBin, claudeArgs)` construction
with `agentRegistry, err := agent.BuildRegistry(claudeBin, claudeArgs, nil)`
(the `nil` agents map is deliberate — see "Deviation" below), resolved
`claudeAdapter` via `agentRegistry.Get(agent.KindClaude)`, and added a loop:

```go
genericAgentMgrs := map[agent.Kind]*agenttui.Manager{}
genericAgentProviders := map[string]*agenttab.Provider{}
for _, kind := range agentRegistry.Kinds() {
    if kind == agent.KindClaude {
        continue
    }
    adapter, _ := agentRegistry.Get(kind)
    mgr := agenttui.NewManager(agenttui.ManagerConfig{
        Adapter: adapter, ...
    })
    provider := agenttab.New(kind, adapter, mgr, tuiStore)
    provider.SetWorktreeResolver(storeWorktreeResolver{store: st})
    registry.Register(provider)
    genericAgentMgrs[kind] = mgr
    genericAgentProviders[string(kind)] = provider
}
```

Also added `multiAgentTabHook` (generalizes `claudeMultiTabHook` into a
dispatcher keyed by provider type, mirroring the maultiagent reference's
identically-named/shaped type) and wired `st.SetMultiTabHook(
multiAgentTabHook{claude: ..., generics: genericAgentProviders})`. Startup
discovery (`runDiscoveryAsync`'s goroutine) and shutdown (`DetachAll`) both
loop over `genericAgentMgrs` after the existing claude/agent calls.
[AC-S0e8afb-3-3]

**`codex`/`opencode` remain unregistered — verified by grep, same method as
S0e8afb-2's own check**:

```
$ grep -n "Codex\|Opencode\|KindCodex\|KindOpencode" cmd/palmux/main.go
662:    // file: no agent.NewCodexAdapter/agent.NewOpencodeAdapter/KindCodex/
663:    // KindOpencode call sites exist here) — agentRegistry.Kinds() only ever
```

The only hits are inside my OWN doc comment stating that no such call sites
exist — no actual `agent.NewCodexAdapter`/`agent.NewOpencodeAdapter`/
`agent.KindCodex`/`agent.KindOpencode` symbol reference exists in
`cmd/palmux/main.go`.

### 10. `internal/tab/agenttui/agentkind_ownership_test.go` (new) — the AC-S0e8afb-3-4 test

Detailed in its own section below.

## Deviations from a literal reading of the design doc (documented, not silent)

1. **`agent.BuildRegistry` is called with a `nil` agents map — no
   config.toml `[agents.*]` TOML surface exists in this repo.** The design
   doc's P3 bullet says "maultiagent の agent.BuildRegistry + per-kind
   manager loop + agenttab 登録を維持" — I read this as "bring over the
   STRUCTURAL wiring shape" (which I did: `BuildRegistry`, the per-kind
   loop, `agenttab` registration, the multi-tab-hook dispatcher generalization)
   rather than "also bring over the config.toml `[agents.<name>]` TOML
   parsing layer" — that layer (`internal/config.AgentSection`, `MasterConfig.
   Agents`, `translateAgentConfig`) is maultiagent's OWN separate Sdec0a7-2
   Sprint, not part of the `docs/agenttui-ptyhost-merge-design.md` epic's own
   P1-P5 phasing at all (that design doc's P3 section only lists AgentKind/
   KillPattern/per-kind-manager/discovery-GC-filter — no mention of a config
   surface). Bringing in a full TOML-parsing config feature as a side effect
   of a "ptyhost restart-survival ownership" Story would be real, undiscussed
   scope creep. Because of this, `agentRegistry.Kinds()` only ever contains
   `"claude"` in production today — the per-kind loop, `agenttab` package,
   and `multiAgentTabHook` generalization are all structurally wired and
   exercised directly by tests, but not yet reachable by any real user
   through a running palmux2 instance. This is intentional and, I believe,
   the correct reading of "claude/generic のみを配線する" — the WIRING
   MECHANISM supports a generic kind (proven by the dedicated 2-kind test),
   even though no live path to CONFIGURE one exists yet.
2. **`store.TuiOrphanGC`/`SetTuiOrphanGC` were NOT generalized to a
   multi-slot registration.** The design doc's own P3 bullet says "main の
   boot-time discovery + GC wiring を各 kind-manager ごとに". I implemented
   the discovery half literally (a loop calling `DiscoverAndRestore` for
   every entry in `genericAgentMgrs`) but left the GC half unwired for
   non-claude kinds. Reasoning: `store.SetTuiOrphanGC(gc)` is a single-field
   assignment (`s.tuiGC = gc`) — calling it more than once OVERWRITES, it
   does not accumulate. Generalizing it to fan out across N Managers is real,
   non-trivial scope: a slice/map field, a re-review of the
   `ArmDiscoveryBarrier`/`discoveryGateOpen` single-signal-for-all-managers
   reasoning (`Store.ArmDiscoveryBarrier`'s own doc comment explicitly notes
   it gates "not one-per-manager" because claude-tui/claude-agent share a run
   dir — the SAME reasoning would need re-verifying for N kind-managers), and
   new store-level tests. Nothing in AC-S0e8afb-3-1..4 requires it, and — as
   in Deviation 1 — `genericAgentMgrs` is ALWAYS EMPTY in production today
   (no config surface exists to populate it), so there is currently nothing
   for a generalized GC to reap. I judged forcing that architecture change
   through now, unexercised by any live Manager, to be worse than the
   well-scoped, documented deferral: a future Story that actually lands the
   config.toml `[agents.*]` surface (making `genericAgentMgrs` non-empty in
   production for the first time) inherits the OBLIGATION to generalize GC
   wiring alongside it — flagged explicitly in ROADMAP.json's task note, not
   silently dropped. This mirrors S0e8afb-2's own Deviation-recording
   precedent (e.g. its Deviation 1 on `immediateFailureBackoff`).
3. **`agenttab`'s `Limits()` type-assertion adaptation** — already detailed
   in "What was done" §8 above; recorded here too since it is a genuine
   (small) behavioral difference from the maultiagent reference it was
   copied from, not a pure mechanical port.

None of these deviations weaken AC-S0e8afb-3-1..4 — all four are genuinely,
non-vacuously met (see below). They are forward-looking scope boundaries for
whichever Story next lands `[agents.*]` config (S2b5691 or a successor),
recorded exactly so that Story doesn't have to rediscover them.

## The 2-kind discovery test (AC-S0e8afb-3-4) — scenario and assertions

`internal/tab/agenttui/agentkind_ownership_test.go`, two tests:

### `TestAgentKindOwnership_DiscoveryDoesNotCrossAdopt`

**Scenario**: two REAL `ptyhost.Server` processes (in-process goroutines,
same technique as the existing `discover_test.go`'s `startRawPtyHost`, and
as `cmd/palmux/ptyhost_ownership_test.go`'s Sfeed64-3 regression test) —
both `ptyhost.ModePTY` (deliberately — see below), one with
`AgentKind: "claude"`, one with `AgentKind: "generic"`, holding a real
`fake_claude` child process each, listening at the deterministic socket
paths for two distinct `(repoId, branchId, tabId)` tuples, in the SAME
on-disk run directory. Two REAL `*agenttui.Manager` instances are
constructed: one with `agent.NewClaudeAdapter(...)` (`Kind() == "claude"`),
one with `agent.NewGenericAdapter("generic", ...)` (`Kind() == "generic"`),
both `RunDirOverride`d to the SAME shared directory. Both Managers'
`DiscoverAndRestore` are run against that shared directory — the exact seam
`cmd/palmux/main.go`'s per-kind loop + discovery goroutine would drive once
more than one kind is live in production.

**Why both ptyhosts are `ModePTY`** (not one pty + one pipe, like the
Sfeed64-3 test): the pre-existing Mode filter alone would trivially
distinguish two different-mode entries regardless of whether the NEW
AgentKind filter works at all — that would not prove anything new. Using
two `ModePTY` entries that differ ONLY by `AgentKind` isolates exactly the
mechanism this Story adds from the mechanism S0e8afb-1/pre-existing code
already had, so a bug in the NEW filter can't hide behind the OLD one still
working.

**Assertions** (mirroring the Sfeed64-3 regression test's assertion shape
exactly, one-for-one):
- `claudeAdopted == 1` and `genericAdopted == 1` (not 2 — a value of 2 on
  either side means that manager's filter let it dial/adopt the OTHER
  kind's entry too).
- `claudeCleaned == 0` and `genericCleaned == 0` (the other kind's live
  entry must be left COMPLETELY untouched, not even counted as debris).
- `claudeMgr.Get(claudeIdentity) != nil` AND `claudeMgr.Get(genericIdentity)
  == nil` (positive AND negative adoption check on both sides — the generic
  identity symmetrically).
- The adopted Daemon's `CurrentStats().PID` equals the ORIGINAL raw
  ptyhost's pid (captured via a HELLO probe before either Manager's
  discovery ran) — this is the assertion that specifically catches a
  "dial-before-check" ordering bug (see the Sfeed64-3-lesson section above):
  if the OTHER kind's discovery pass had dialed this ptyhost even once
  (regardless of what it then decided to do with it), `replaceConn` would
  have evicted the connection this Manager's own `DiscoverAndRestore` was
  relying on being the survivor, and the pid observed after re-attach would
  either mismatch or the daemon would show a fresh-spawn state instead of
  `reconnected=true`.

### `TestAgentKindOwnership_EmptyAgentKindTreatedAsClaude`

**Scenario**: one raw ptyhost with NO `AgentKind` set at all (simulating a
ptyhost spawned by a pre-S0e8afb-3 binary, mid-upgrade). Both a claude
Manager and a generic Manager run `DiscoverAndRestore` against it.

**Assertions**: `claudeAdopted == 1` (the back-compat default claims it) and
`genericAdopted == 0` (a non-claude kind must NEVER claim a legacy,
kind-less entry — only the specific back-compat target, claude, may).

## Commands run

```
$ go build ./...                                    # clean
$ go vet ./...                                       # clean
$ go test ./internal/ptyhost/... ./internal/agent/... ./internal/tab/agenttab/...
    # ok  internal/ptyhost      5.8s
    # ok  internal/agent        0.04s
    # ok  internal/tab/agenttab 0.01s
$ go test ./internal/tab/agenttui/... -count=1        # ok, 18.4s
$ go test ./cmd/palmux/... -count=1                   # ok, 0.4s (includes
    #   TestPtyOwnership_ModeFilter, re-confirmed green)
$ go test ./... -count=1                              # ALL PASS (28 packages)
$ go test ./internal/tab/agenttui/... -run \
    'TestDaemon|TestReattach|TestPtyhost|TestSpawnWithArgs|TestGoldenArgv|TestManagerReattach|TestAgentKindOwnership' \
    -race -v
    # ALL PASS — including the P0 TestReattachSurvivorReplayDoesNotDeadlock
    # and both new TestAgentKindOwnership_* tests, clean under -race
$ go test ./internal/tab/agenttui/... -run TestAgentKindOwnership -v
    # PASS x2 (both new tests, non-race run for readable logs — see below)
$ go fmt ./...                                        # applied to files this
    #   Story touched only
```

Sample log excerpt from `TestAgentKindOwnership_DiscoveryDoesNotCrossAdopt`
(non-race run, full output), showing both managers genuinely attaching to
DISTINCT pre-existing pids and neither one touching the other's entry:

```
INFO agenttui: attached to surviving ptyhost repo=owner-claude-repo branch=owner-claude-branch tab=claude:claude socket=.../7e2ec586....sock pid=599485
INFO agenttui: discovery: re-adopted surviving ptyhost repo=owner-claude-repo branch=owner-claude-branch tab=claude:claude pid=599485
INFO agenttui: attached to surviving ptyhost repo=owner-generic-repo branch=owner-generic-branch tab=generic:generic socket=.../a59fffa0....sock pid=599493
INFO agenttui: discovery: re-adopted surviving ptyhost repo=owner-generic-repo branch=owner-generic-branch tab=generic:generic pid=599493
--- PASS: TestAgentKindOwnership_DiscoveryDoesNotCrossAdopt (0.31s)
```

### Full suite, 28 packages, all green

```
ok  github.com/tjst-t/palmux2/cmd/palmux              0.574s
ok  github.com/tjst-t/palmux2/internal/agent           0.052s
ok  github.com/tjst-t/palmux2/internal/apps             0.012s
ok  github.com/tjst-t/palmux2/internal/attachment      0.006s
ok  github.com/tjst-t/palmux2/internal/auth            0.021s
ok  github.com/tjst-t/palmux2/internal/commands         0.009s
ok  github.com/tjst-t/palmux2/internal/config           0.439s
ok  github.com/tjst-t/palmux2/internal/deploy           0.020s
ok  github.com/tjst-t/palmux2/internal/domain            0.005s
ok  github.com/tjst-t/palmux2/internal/incusgroup       0.007s
ok  github.com/tjst-t/palmux2/internal/notify            0.009s
ok  github.com/tjst-t/palmux2/internal/ptyhost          6.777s
ok  github.com/tjst-t/palmux2/internal/runtime           0.003s
ok  github.com/tjst-t/palmux2/internal/runtime/host     0.011s
ok  github.com/tjst-t/palmux2/internal/runtime/incus    0.122s
ok  github.com/tjst-t/palmux2/internal/selfupdate        1.105s
ok  github.com/tjst-t/palmux2/internal/store             1.075s
ok  github.com/tjst-t/palmux2/internal/tab/agenttab      0.007s
ok  github.com/tjst-t/palmux2/internal/tab/agenttui     19.291s
ok  github.com/tjst-t/palmux2/internal/tab/browser      22.118s
ok  github.com/tjst-t/palmux2/internal/tab/claudeagent   5.331s
ok  github.com/tjst-t/palmux2/internal/tab/files         0.027s
ok  github.com/tjst-t/palmux2/internal/tab/git           0.222s
ok  github.com/tjst-t/palmux2/internal/tab/sprint        0.035s
ok  github.com/tjst-t/palmux2/internal/tab/sprint/parser 0.018s
ok  github.com/tjst-t/palmux2/internal/tmux              0.236s
ok  github.com/tjst-t/palmux2/internal/worktree          0.011s
ok  github.com/tjst-t/palmux2/internal/worktreewatch     0.328s
```

No flakes observed across the several full-suite runs during this Story
(unlike S0e8afb-2's verification doc, which recorded one CPU-contention
flake in `agenttui`'s async-discovery test — did not reproduce here).

## Real local smoke (claude tab, both modes exercised)

Per CLAUDE.md's dev-instance convention, ran `make serve INSTANCE=s0e8afb3`
from this worktree (own portman name/PID/log/tmux-prefix `_pmx_s0e8afb3_`,
fully isolated from any other running instance) — production binary,
embedded frontend, REAL `claude` binary.

1. `npm install` in `frontend/` (fresh worktree, no `node_modules` yet),
   then `make serve INSTANCE=s0e8afb3` — built clean, started on port 8200.
2. Created a throwaway git repo
   `~/ghq/github.com/palmux2-test/s0e8afb3-smoke-<ts>`, opened it via
   `POST /api/repos/{id}/open`.
3. Switched the `claude:claude` tab to `tui` mode via `PATCH .../tabs/
   claude:claude/settings {"claude_mode":"tui"}`.
4. Playwright (headless chromium) navigated to `/{repoId}/{branchId}/claude`,
   dismissed the first-run onboarding wizard overlay (`data-testid=
   "onboarding-skip"` — a fresh `--config-dir ./tmp` instance shows it and it
   intercepts pointer events over the terminal, unrelated to this Story),
   confirmed the REAL claude CLI booted (`Claude Code v2.1.212`, `Welcome
   back takumi!`, real workspace-trust flow), typed `Reply with exactly the
   single word PALMUX_S0E8AFB3_SMOKE_<ts> and nothing else.`, and confirmed a
   completed reply:

```
❯ Reply with exactly the single word PALMUX_S0E8AFB3_SMOKE_1784282033 and nothing else.
● PALMUX_S0E8AFB3_SMOKE_1784282033
✻ Worked for 3s
```

5. Confirmed no errors/panics in `tmp/palmux-s0e8afb3.log`.
6. Cleanup: closed the branch (`DELETE .../branches/{id}` → 204) and repo
   (`POST .../close` → 204), confirmed `GET /api/repos` returns `[]`, `make
   serve-stop INSTANCE=s0e8afb3`, removed the throwaway ghq repo directory,
   confirmed no lingering `_pmx_s0e8afb3_*` tmux sessions or `ptyhost`
   processes (`pgrep -af ptyhost` → empty).

This proves the `agent.BuildRegistry`-based rewiring of `claudeAdapter`
construction (this Story's change to how `claudeAdapter` is obtained — via
`agentRegistry.Get(agent.KindClaude)` instead of a bare
`agent.NewClaudeAdapter` call) produces a byte-identical, fully working
claude spawn: same golden-argv-tested Adapter instance either way, only the
CONSTRUCTION path (registry lookup vs direct call) changed.

## Real-VM verification — `palmux-nixos-test.tjstkm.net` (192.168.1.44)

Per the task's UPDATED instructions (quick binary-swap on the existing
persistent NixOS appliance test host, not a qcow2 rebuild):

1. `make build-linux` from this worktree → `bin/palmux-linux-amd64`
   (`v0.15.0-43-g860ca0c-dirty`).
2. Confirmed `systemctl show palmux2 -p ExecStart` on the host:
   `ExecStart={ path=/var/lib/palmux/palmux2-test ; argv[]=/var/lib/palmux/
   palmux2-test serve --addr=127.0.0.1:7683 ; ... }` — matches the task
   brief's description.
3. Backed up the running binary: `cp /var/lib/palmux/palmux2-test /var/lib/
   palmux/palmux2-test.bak-$(date +%s)` (this host already had 3 prior
   `.bak-*` files from previous Stories' same convention — confirmed my new
   backup did not collide with or overwrite any of them).
4. `systemctl stop palmux2` (a running binary can't be overwritten —
   `cp` failed with "Text file busy" on the first attempt, corrected by
   stopping first), `scp`'d the new binary in, `chmod +x`, `systemctl start
   palmux2`.
5. Verified: `--version` reports `v0.15.0-43-g860ca0c-dirty` (matches the
   build), `systemctl is-active` → `active`, `GET /api/health` → `200` with
   `"version":"v0.15.0-43-g860ca0c-dirty"`, `GET /api/repos` still lists the
   pre-existing `tjst-t--palmux2--2d59` repo with all its tabs intact
   (claude/files/git/sprint/bash), `incus list` shows the pre-existing
   workspace container (`lxc-incus-c18d-incus-0a5e-2658c6b1`) still
   `RUNNING`, undisturbed by the restart.
6. Checked `journalctl -u palmux2` for the fresh process's own log lines
   (filtered by the new PID) — clean startup, no panics, no NEW errors.
   `sync_tmux: recovering session` lines for the repo's branches (normal
   startup reconciliation, unrelated to this Story).
7. **Real-claude-turn check on this host was not feasible**: this host's
   `palmux` systemd service user has a `PATH` (visible via `systemctl show
   palmux2 -p Environment`) with no `claude` binary on it at all — and the
   journal shows this is a **pre-existing** condition, not something my
   binary swap introduced: `claudetui: ensure started" err="...exec:
   \"claude\": executable file not found in $PATH"` and (later) `"...ptyhost
   launch failed via both systemd-run and setsid fallback..."` errors appear
   in the journal dated **2026-07-11 and 2026-07-14** — days before this
   binary swap. Confirmed no live ptyhost process exists for the repo's
   `claude:claude` tab at all (`pgrep -af ptyhost` → empty) both before and
   after the swap — there was nothing for the new binary's discovery pass to
   even attempt to reconnect to, and attempting a fresh spawn would just
   reproduce this host's pre-existing, unrelated PATH misconfiguration
   either way. This does NOT indicate a regression from this Story: the
   REAL, working, end-to-end claude-turn proof is the local smoke above
   (real authenticated claude, on an instance where `claude` genuinely IS on
   `PATH`), which is unaffected by whatever host-level PATH configuration
   this particular shared VM happens to have.
8. **Generic-kind wiring check**: since `agentRegistry.Kinds()` only ever
   contains `"claude"` in production (Deviation 1 above — no config.toml
   `[agents.*]` surface exists), there is no live generic-kind tab reachable
   on this host either — this is inherent to the Story's scope, not a defect
   in the swap. The structural correctness of the per-kind wiring (that it
   compiles, that the loop correctly produces zero entries when the registry
   has only claude, that nothing panics) is confirmed by the clean startup
   log above (no panic, no error attributable to the new `agentRegistry`/
   `genericAgentMgrs`/`multiAgentTabHook` code paths) plus the dedicated
   `TestAgentKindOwnership_*` tests, which exercise the actual mechanism
   directly with two REAL, distinct kinds (the only way to meaningfully
   exercise it, absent a live config surface on any host).
9. **Restore**: `systemctl stop palmux2`, `cp /var/lib/palmux/
   palmux2-test.bak-1784282105 /var/lib/palmux/palmux2-test`, `chmod +x`,
   `systemctl start palmux2`. Verified: `--version` → `v0.15.0-1-gc411b5e-
   dirty` (the ORIGINAL pre-swap version, confirmed), `systemctl is-active`
   → `active`, health → `200`, incus container still `RUNNING`. **Checksum
   verification**: `md5sum /var/lib/palmux/palmux2-test /var/lib/palmux/
   palmux2-test.bak-1784282105` — identical hashes, confirming the restore
   is byte-for-byte the original binary, not just "a build that reports the
   same version string." The backup file itself was left in place (matching
   this host's existing convention of retaining prior `.bak-*` files as an
   audit trail — not deleted).

## Acceptance criteria

- **AC-S0e8afb-3-1**: PASS — `internal/ptyhost/server.go`'s `Config`/
  `StatusFile` both carry `AgentKind`/`KillPattern`, opaque echo, verified by
  `internal/ptyhost`'s own test suite (unchanged, still green) plus the new
  `agentkind_ownership_test.go` exercising them end-to-end through a real
  `ptyhost.Server`.
- **AC-S0e8afb-3-2**: PASS — `discover.go`'s `ScanRunDir` gates on
  `AgentKind` mismatch in addition to the pre-existing `Mode==ModePipe`
  skip, BEFORE any dial (verified both by code reading against the Sfeed64-3
  precedent's exact ordering discipline, and by the pid-stability assertion
  in `TestAgentKindOwnership_DiscoveryDoesNotCrossAdopt`, which would fail
  under a dial-before-check regression).
- **AC-S0e8afb-3-3**: PASS — `cmd/palmux/main.go` has `agent.BuildRegistry`
  + a per-kind manager loop + `agenttab` Provider registration; grep confirms
  no `agent.NewCodexAdapter`/`NewOpencodeAdapter`/`KindCodex`/`KindOpencode`
  call sites exist in that file (same verification method S0e8afb-2 used for
  its own analogous check).
- **AC-S0e8afb-3-4**: PASS — `TestAgentKindOwnership_DiscoveryDoesNotCrossAdopt`
  (real 2-kind, real ptyhost processes, real Managers, proves no
  cross-adoption end-to-end) + `TestAgentKindOwnership_
  EmptyAgentKindTreatedAsClaude` (back-compat regression guard) both green,
  plus real-VM smoke on `palmux-nixos-test.tjstkm.net` performed as
  described above.

## Concerns (explicit, not omitted)

1. **`genericAgentMgrs`/`agentRegistry.Kinds()` beyond "claude" are
   currently UNREACHABLE in production** (Deviation 1) — this Story delivers
   the ownership-separation MECHANISM (proven correct by direct tests) and
   the STRUCTURAL wiring shape in `main.go`, but there is no live way for an
   operator to actually enable a second kind yet. The next Story that adds
   config.toml `[agents.*]` parsing inherits both (a) wiring `rc.agents`
   through to `agent.BuildRegistry` (currently hardcoded `nil`) and (b)
   generalizing `store.TuiOrphanGC` to fan out across multiple Managers
   (Deviation 2) — neither is optional at that point, both are flagged here
   and in ROADMAP.json so they aren't rediscovered from scratch.
2. **GCOrphans for non-claude kinds is unwired** (Deviation 2, same root
   cause as #1) — today this is provably a non-issue (nothing to GC, the map
   is always empty), but it is a real gap the Story enabling live generic
   kinds must close, not optional polish. This mirrors S0e8afb-2's own
   Concern 2 (`GCOrphans`'s hardcoded fallback) in shape, one layer further
   up the stack.
3. **The real-VM smoke could not exercise a genuine claude spawn** on
   `palmux-nixos-test.tjstkm.net` due to a pre-existing (confirmed
   pre-dating this Story by 3-6 days), unrelated host PATH misconfiguration.
   I verified this is pre-existing rather than papering over a real gap (see
   §7 above — exact matching error text and timestamps predating the binary
   swap, confirmed via `journalctl`), but flagging it here for visibility:
   this host's claude spawn path is currently broken independent of any
   agenttui/ptyhost merge work, and whoever next uses this host for a
   claude-spawn-dependent verification will hit the same wall until its
   `palmux` service user's `PATH` (or `claude_bin` config) is fixed.
