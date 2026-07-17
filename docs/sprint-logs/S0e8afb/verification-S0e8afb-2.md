# Verification — S0e8afb-2 (P2: graft seam)

Branch: `worktree-S0e8afb-2` (off `autopilot/main/S0e8afb`, on top of the
merged S0e8afb-1)

> **Update**: an independent high-effort review confirmed all load-bearing
> claims below (P0 region untouched, golden-argv tests non-vacuous,
> codex/opencode genuinely inert, real-claude smoke plausible) with no
> blocking issues, but caught one real gap not originally in the Concerns
> section: `EnsureDaemon` didn't gate `SessionWatcher` startup on
> `agent.SessionDiscoverer` (design doc + maultiagent both specify this
> gate). Fixed post-commit — see **Deviation 5** and **Concern 0** below for
> the full writeup, impact analysis, and the new regression test that proves
> the fix holds.

This is explicitly called out as **the single riskiest phase** of the whole
agenttui × ptyhost merge (per the design doc's own risk list and the task
brief). This document records not just PASS/FAIL but the reasoning behind
every judgment call, so a future reader can independently assess whether the
graft is trustworthy rather than taking "tests passed" on faith.

## Why the seam is safe (reasoning, not just test output)

The core claim of `docs/agenttui-ptyhost-merge-design.md` is that main's
"who executes the argv" change (ptyhost) and maultiagent's "who builds the
argv" change (Adapter) are orthogonal and meet at exactly one point: the
`(argv, env, cwd)` tuple handed to `launchAndAttach`. If that's true, the
graft is "just" a substitution at that one point, with everything above and
below unchanged. I verified this claim three independent ways, not just one:

1. **Structural**: I read `internal/agent/adapter.go`'s `SpawnSpec` type and
   confirmed it is exactly `{Argv, Env, PreFiles, KillPattern}` — nothing
   ptyhost-specific, nothing claude-specific beyond what's already opaque
   bytes by the time it reaches `launchAndAttach`. The Adapter never sees a
   `net.Conn`, a `ptyhost.Config`, or anything from `internal/ptyhost`.
2. **Ordering**: the P0 reattach-deadlock fix (v0.14.12, Sfeed64-1) lives
   entirely BELOW where I grafted — `launchAndAttach`, the replay-drainer
   goroutine, and the `Feed(replay)` call are byte-for-byte untouched (see
   the diff: only `spawnWithArgs`'s TOP half, up to and including the
   `d.launchAndAttach(argv, spawnEnv, cwd)` call, changed). I diffed this
   region against the pre-graft file specifically to confirm no line in
   `launchAndAttach` itself, `readLoop`, or the drainer goroutine moved.
3. **Empirical**: `reattach_deadlock_test.go`'s
   `TestReattachSurvivorReplayDoesNotDeadlock` — which forces a >64KiB
   query-heavy replay through exactly this path — passes under `-race`,
   completing in 2.37s with the same "no deadlock" assertion the P0 fix
   exists to guard. If the graft had disturbed the ordering, this is the
   test that would have caught it (it did, historically, on the pre-fix
   commit — see that test's own doc comment).

The second-order risk — "does the Adapter's argv construction actually match
what the inline builder used to produce" — is closed by TWO golden tests at
different layers (see below), not one: `internal/agent/claude_golden_test.go`
(brought in from maultiagent, unmodified) pins the Adapter's OWN output
against a hand-transcribed reference formula; `internal/tab/agenttui/
golden_argv_test.go` (new, written for this Story) pins that `spawnWithArgs`'s
WIRING delivers that exact output to a real spawned process (both host and
in-container), closing the gap a bug ONLY in the wiring (not the Adapter
itself) would otherwise slip through.

## What was done

### 1. Brought in `internal/agent/**` verbatim from `maultiagent`

`git show maultiagent:internal/agent/<file>` for all 13 files (`adapter.go`,
`claude.go`, `claude_test.go`, `claude_golden_test.go`, `codex.go`,
`codex_test.go`, `opencode.go`, `opencode_test.go`, `generic.go`,
`generic_test.go`, `incontainer.go`, `registry.go`, `registry_test.go`),
byte-identical, no repo-specific edits needed — this package has zero
dependency on anything else in this repo (stdlib only), confirmed by
`go build ./internal/agent/...` and `go test ./internal/agent/...` passing
standalone before touching anything else.

`codex.go`/`opencode.go` (and their tests) are present but **inert**:
`cmd/palmux/main.go` does not call `agent.BuildRegistry`, `NewCodexAdapter`,
or `NewOpencodeAdapter` anywhere — only `agent.NewClaudeAdapter` is
constructed and wired. `KindCodex`/`KindOpencode` exist as Go symbols (used
internally by `registry.go`, which I also brought in verbatim since
`codex.go`/`opencode.go` reference `Registry`-adjacent types) but no
user-facing path reaches them. This matches the Story's explicit scope note.

### 2. Completed the `claudetui` → `agenttui` package move

S0e8afb-1 moved `ptyclient.go`/`discover.go` only, leaving `daemon.go`,
`manager.go`, and everything else in `claudetui` because `Manager`/`Daemon`
themselves weren't moving yet (see that Story's own doc comment, which
literally says "that is a later Story's daemon.go/manager.go graft" — this
Story). I completed the move:

- `git mv` every remaining `internal/tab/claudetui/*.go` (and `testdata/`)
  to `internal/tab/agenttui/`, with `package claudetui` → `package agenttui`.
- Deleted `hooks.go` and `hooks_test.go` — their logic
  (`buildClaudeSettings`/`hookEntries`/`hookEnv`/`shellQuote`/
  `palmuxSkillDir`) moved into `agent/claude.go` (already brought in
  verbatim above); their tests are superseded by `agent/claude_test.go`
  (also verbatim). `claude_args.go` (the `--claude-arg` pflag.Value type) is
  UNRELATED to hook-building despite the similar filename and was kept
  as-is.
- `ptyhost_discovery.go` (the claudetui-side residual holding
  `DiscoverAndRestore`/`(*Manager).GCOrphans`) moved into `agenttui` too and
  had its `agenttui.` self-references stripped (same package now) — see its
  updated file-level doc comment explaining the P1→P2 collapse.
- Fixed a few tests that referenced the OLD package's symbols by name
  (`fakePTYRuntime`/`hasArg`/`hasArgPair`, previously defined in the now-
  deleted `hooks_test.go` but used by `shutdown_reap_test.go` too) by
  re-homing them into a new shared helper file
  (`incontainer_testutil_test.go`) rather than dropping them — these are
  still-needed test doubles for in-container PTYCommander WIRING tests
  (unrelated to the deleted hook-building tests they happened to live next
  to).
- `cmd/palmux/main.go` and `cmd/palmux/ptyhost_ownership_test.go` (the
  cross-package `claudetui`↔`claudeagent` dual-manager ownership regression
  test) updated to import `agenttui` instead.

### 3. The actual graft: `spawnWithArgs`

Per the design doc's literal instructions:

1. **Inline arg-builder → Adapter call.** `spawnWithArgs`'s old body (lines
   that built `--settings`, `--plugin-dir`, `--permission-mode`, resolved
   `containerClaudeBin`, and called `buildClaudeSettings`/`hookEnv`
   directly) is replaced with: resolve host-vs-container hook/notify
   (UNCHANGED logic, still lives in `daemon.go` — see AC-2 below), build an
   `agent.SpawnIntent`, call `d.adapter.SpawnSpec(intent)`, and use the
   resulting `spec.Argv`/`spec.Env`/`spec.KillPattern`.
2. **Seam-below unchanged.** Host branch: `argv = append([]string{
   resolveClaudeBin(spec.Argv[0])}, spec.Argv[1:]...)` then
   `d.launchAndAttach(argv, spawnEnv, cwd)` — same call, same function,
   same everything below it. Container branch: `pc.PTYCommand(d.daemonCtx,
   spec.Argv, opts)` — `spec.Argv` handed to `PTYCommand` VERBATIM, matching
   the design doc's literal "container: pc.PTYCommand(daemonCtx, spec.Argv,
   opts)".
3. **`spawnWithArgs` signature** is now `(resumeSessionID string, isRespawn
   bool) error`, matching the design doc's instruction — `EnsureStarted`
   passes `(d.initialSessionID, false)`, `respawnLoop` passes `(sid or "",
   true)`.
4. **`respawnLoop`**: kept main's `gateRespawn` (incus-regenerate wait) and
   the "bad transcript" fallback guard UNCHANGED. Added an
   `agent.Capabilities().Resume` + `SessionDiscoverer` gate around the
   session-ID wait, mirroring maultiagent — for claude (`Resume: true`,
   implements `SessionDiscoverer`) this is a no-op, the exact same
   unconditional wait as before. **Did NOT** bring in maultiagent's
   `immediateFailureBackoff`/`StateFatal` — see "Deviations" below.
5. **Reap → KillPattern**: live-Daemon reap call sites (`teardown`,
   `respawnLoop`) now go through a new `d.effectiveKillPattern()` helper
   (`d.killPattern` if a spawn has happened, else the `containerClaudeBin`
   constant as a fallback — see "Deviations" for why the fallback is
   necessary). `GCOrphans` (no live Daemon) still uses the hardcoded
   constant directly — see "Deviations".

### 4. `manager.go`

Added `ManagerConfig.Adapter agent.Adapter` (shared across every Daemon this
Manager creates) and `Manager.Kind() agent.Kind`. `NewManager` defaults
`Adapter` to `agent.NewClaudeAdapter(ClaudeBin, ClaudeArgs)` when unset — see
"Deviations" for why this default exists. `SetClaudeBin`/`SetClaudeArgs`
(Sa53137-3 hot-apply) now ALSO push through `agent.Configurable.SetBin/
SetArgs` on the shared adapter instance, so an already-spawned daemon's
respawn picks up the new binary — a **correctness improvement** over the
pre-graft code (whose `d.claudeBin` field was immutable per-Daemon, so
`SetClaudeBin` never actually reached an existing daemon despite its doc
comment claiming otherwise; not asserted against by any existing test, so
this is not a behavior change relative to any PASSING test).

### 5. `cmd/palmux/main.go`

Explicitly constructs `claudeAdapter := agent.NewClaudeAdapter(claudeBin,
claudeArgs)` and passes it as `ManagerConfig.Adapter` (rather than relying on
the nil-default), so the deploy-hot-apply path
(`deployHotApplier.SetClaudeBin/SetClaudeArgs`) and the Manager share one
instance.

## Deviations from the design doc's literal instructions (and why)

1. **`immediateFailureBackoff`/`StateFatal` NOT integrated into
   respawnLoop.** The design doc's step 4 says to integrate this from
   maultiagent. I deliberately deferred it. Reasoning: (a) none of
   AC-S0e8afb-2-1..4 require it; (b) `daemon_test.go` has no concept of
   `StateFatal` — introducing it would require either weakening or
   substantially rewriting existing State-transition assertions, which the
   task brief explicitly says to stop and flag rather than push through;
   (c) it protects against a crash-LOOPING subprocess, which claude
   essentially never does in practice (this Sprint's own real-claude smoke
   test and years of production dogfooding back this up) — the protection
   matters far more for a future generic/user-defined or codex adapter
   (arbitrary command, can 127 immediately), which is exactly S0e8afb-3's
   territory. Bringing it in now, unexercised by anything claude-shaped,
   would be undertested exactly where a bug would be least visible. I
   recorded this as `partial` (not `done`) on task S0e8afb-2-2 in
   ROADMAP.json with this reasoning, rather than silently marking it
   complete.

2. **`ManagerConfig`/`DaemonConfig` kept `ClaudeBin`/`ClaudeArgs` as a
   fallback that constructs a DEFAULT `ClaudeAdapter`, rather than requiring
   every caller to pass `Adapter` explicitly.** The design doc doesn't
   dictate this either way, but a strict "Adapter is required" reading of
   maultiagent's own `DaemonConfig` doc comment ("Required — spawning
   without an Adapter fails") would have forced a mechanical rewrite of
   every one of the ~25 `DaemonConfig{ClaudeBin: ..., ClaudeArgs: ...}` /
   `ManagerConfig{ClaudeBin: ...}` test call sites across
   `daemon_test.go`, `daemon_gate_test.go`, `manager_test.go`,
   `manager_integration_test.go`, `discover_test.go`,
   `discover_async_test.go`, `instance_isolation_test.go`,
   `ptyhost_integration_test.go`, `provider_test.go`, `server_test.go`,
   `reattach_deadlock_test.go`. I judged the blast radius of touching that
   many test files in the riskiest phase of this whole merge to be worse
   than the small, well-documented, purely-additive default-construction
   path I added instead (zero behavior change for any of those tests,
   confirmed by all of them passing unmodified — the only test file I DID
   touch for a symbol reason was `daemon_test.go`'s literal string assertion
   `"claudetui daemon:"` → `"agenttui daemon:"`, a pure error-prefix rename
   unrelated to the Adapter plumbing). I only touched 6 direct-
   `DaemonConfig`-construction test files where a genuinely NEW test needed
   the Adapter wired for its own assertions (`golden_argv_test.go`,
   `incontainer_testutil_test.go`), and left the rest alone.

3. **`reapContainerClaude`'s orphan-GC call site (`GCOrphans`, no live
   Daemon) still uses the hardcoded `containerClaudeBin` constant, not
   `agent.SpawnSpec.KillPattern`.** The design doc's step 5 literally says
   "GCOrphans (live Daemon 無し) は StatusFile.KillPattern を読む" — but
   `ptyhost.StatusFile` does not yet have a `KillPattern` field.
   **ROADMAP.json itself scopes that addition to AC-S0e8afb-3-1**
   ("internal/ptyhost/server.go の Config/StatusFile に AgentKind/
   KillPattern が追加されている"), not this Story. I followed the
   authoritative ROADMAP.json story split over the more informally-worded
   summary paragraph in my task brief, and left `GCOrphans`'s reap call
   UNCHANGED (still `containerClaudeBin`, zero behavior difference) rather
   than speculatively adding a `ptyhost` protocol/state field this Story's
   own AC list doesn't ask for, in the riskiest phase of the merge. The
   LIVE-Daemon reap paths (`teardown`, `respawnLoop`) — which don't need any
   `ptyhost` changes, just the Daemon's own in-memory last-built spec — DO
   use the adapter's `KillPattern` now, matching the design doc's "live-
   Daemon 経路は maultiagent 済み" half.

4. **`resolveClaudeBin` stayed in `agenttui/daemon.go`, was not absorbed
   into the Adapter.** The design doc's step 1 lists `resolveClaudeBin`
   among the things replaced by "Adapter 呼び出し". I kept the function
   itself in `daemon.go` and apply it to `spec.Argv[0]` AFTER calling the
   Adapter (host branch only), rather than moving the LookPath-resolution
   logic into `ClaudeAdapter.SpawnSpec` itself. Reasoning: the rationale for
   `resolveClaudeBin` (argv[0] must be resolved in PALMUX2's OWN process,
   because a detached `palmux ptyhost` — possibly under a systemd --user
   session with a different PATH — is what ultimately execs it) is a
   property of THIS repo's ptyhost architecture, not of claude specifically.
   maultiagent's `ClaudeAdapter.SpawnSpec` doesn't do this at all (verified
   by reading `claude.go` — no `resolveClaudeBin`-equivalent exists there),
   because maultiagent execs in the same process and doesn't need it. Making
   the generic `agent.Adapter` interface aware of "which process eventually
   execs this" would leak a ptyhost-specific concern into an
   agent-architecture-agnostic interface — the wrong layer for it. Applying
   it generically to `spec.Argv[0]` (any adapter, not just claude) in
   `daemon.go` is architecturally cleaner AND preserves claude's exact
   byte-behavior (proven by `TestGoldenArgv_HostSpawnMatchesAdapterSpec`).

5. **[POST-COMMIT FIX, found by independent review] `EnsureDaemon`'s
   `SessionWatcher` startup was NOT gated on `agent.SessionDiscoverer`.**
   This was a genuine gap, not a deliberate scope call like 1-4 above — an
   independent high-effort review of the first commit caught it. The design
   doc (manager.go section: "SessionWatcher-when-SessionDiscoverer gate")
   and maultiagent's own reference `manager.go` both gate SessionWatcher
   construction behind `if sd, ok := m.cfg.Adapter.(agent.SessionDiscoverer);
   ok { ... }`. My first commit instead called the OLD hardcoded
   claude-specific free functions `TranscriptDir(worktree)` /
   `NewSessionWatcher(td)` (whose internal file-classification logic was
   ALSO hardcoded to claude's UUID-`.jsonl` convention via a private
   `looksLikeSessionID`) unconditionally, with no reference to
   `m.cfg.Adapter` at all.

   **Impact at the time (none today, real for S0e8afb-3)**: byte-identical
   to before for claude (the only live adapter — `ClaudeAdapter.TranscriptDir`
   computes the exact same path the old free function did). But it is a
   real latent cross-kind bug once S0e8afb-3 wires per-kind Managers: a
   workspace with BOTH a claude tab and e.g. a codex tab on the SAME
   worktree would have had both Managers' SessionWatchers point at the same
   `~/.claude/projects/<slug>` directory (the old `TranscriptDir` is a pure
   function of worktree only, ignorant of kind), so the codex daemon could
   receive claude's own session-ID fsnotify events and silently inject a
   claude session UUID as a codex `--resume` argument.

   **Fix** (mirrors maultiagent's `sessions.go`/`manager.go` exactly, adapted
   to preserve a main-only feature maultiagent doesn't have — see below):
   - `sessions.go`: removed the package-level `TranscriptDir`/
     `transcriptExists`/`looksLikeSessionID`/`LatestSessionID` (that logic
     now lives ONLY in `agent.ClaudeAdapter`, already brought in verbatim —
     `internal/agent/claude.go`/`claude_test.go` already cover it, so no
     test coverage was lost, only relocated to where the logic actually
     lives — same pattern as this Story's original `hooks.go` deletion).
     Added a `SessionIDFromPath` func type and changed
     `NewSessionWatcher(transcriptDir string, idFromPath SessionIDFromPath)`
     — `handleFSEvent` now dispatches through the injected callback instead
     of a hardcoded UUID-`.jsonl` check. A `nil` `idFromPath` makes the
     watcher emit no events (defensive; regression-guarded by the new
     `TestSessionWatcher_NilIDFromPath`, mirrored from maultiagent).
   - `manager.go`'s `EnsureDaemon`: resolves
     `sessionDiscoverer, canDiscoverSessions := m.cfg.Adapter.(agent.
     SessionDiscoverer)` ONCE, up front. The SessionWatcher block is now
     `if worktree != "" && canDiscoverSessions { td, _ :=
     sessionDiscoverer.TranscriptDir(worktree); NewSessionWatcher(td,
     sessionDiscoverer.SessionIDFromPath) }` — an Adapter without the
     capability gets no watcher at all, full stop.
   - **Preserved main's `InitialSessionID`/first-spawn-resume feature**
     (maultiagent has no equivalent at all — it doesn't pre-seed
     `DaemonConfig.InitialSessionID`, only `d.SetSessionID` after
     construction, so a plain copy of maultiagent's `EnsureDaemon` would
     have SILENTLY DROPPED main's "palmux restart re-attaches to the prior
     conversation on the very first spawn" behavior — caught by re-reading
     `manager_integration_test.go`'s `TestFirstSpawnResumesPersistedSession`/
     `TestFirstSpawnFreshWhenTranscriptMissing`, which only exist on this
     branch, not in maultiagent). The old `transcriptExists(worktree, id)`
     (hardcoded `<id>.jsonl` existence check) became a new
     `transcriptExistsFor(sd agent.SessionDiscoverer, td, sessionID)` in
     `sessions.go`, gated the same way, and made MORE general than the old
     helper in the process: instead of assuming a `.jsonl` extension, it
     scans `td` and asks `sd.SessionIDFromPath` of each entry — the exact
     same classifier the watcher itself uses, so "does this ID's transcript
     still exist" and "what ID does this file represent" can never disagree
     for any future adapter's naming convention, not just claude's.
   - Test fallout (mechanical, no assertions weakened): `sessions_test.go`
     rewritten to mirror maultiagent's (TranscriptDir/LatestSessionID tests
     dropped — redundant with `internal/agent/claude_test.go`'s equivalents,
     not lost coverage; SessionWatcher tests kept, now parameterized via a
     `testIDFromPath()` helper that returns
     `agent.NewClaudeAdapter("claude", nil).SessionIDFromPath`; added
     `TestSessionWatcher_NilIDFromPath` from maultiagent). Three call sites
     in `manager_integration_test.go` (`TranscriptDir(worktree)` ×3,
     `NewSessionWatcher(transcriptDir)` ×1) updated to a new
     `claudeTranscriptDir(t, worktree)` test helper /
     `claude.SessionIDFromPath` — same computed paths, mechanical rename.
   - **New regression test directly proving the fix**:
     `TestEnsureDaemon_NoSessionWatcherWithoutSessionDiscoverer` in
     `manager_integration_test.go` — constructs a Manager with
     `agent.NewGenericAdapter(...)` (verified NOT to implement
     `SessionDiscoverer`, per its own doc comment), calls `EnsureDaemon` with
     a real worktree, and white-box-asserts `entry.watcher == nil`. This
     test would have FAILED against the pre-fix code (which started a
     watcher unconditionally whenever `worktree != ""`, regardless of
     Adapter) — it is a genuine regression guard, not a vacuous assertion.

   All re-verified: `go build ./...` / `go vet ./...` clean; full
   `internal/tab/agenttui` suite green; `TestFirstSpawnResumesPersistedSession`/
   `TestFirstSpawnFreshWhenTranscriptMissing` (main's feature) still pass
   unmodified; the P0 `TestReattachSurvivorReplayDoesNotDeadlock` and both
   `TestGoldenArgv_*` tests re-run clean under `-race`; full `go test ./...`
   green.

None of these deviations are silent — each is called out in ROADMAP.json
task notes and/or this document, and none weakens what AC-S0e8afb-2-1..4
actually require.

## Files touched

```
new    internal/agent/{adapter,claude,claude_test,claude_golden_test,codex,
                        codex_test,opencode,opencode_test,generic,
                        generic_test,incontainer,registry,registry_test}.go
                                          (verbatim from maultiagent)

R      internal/tab/claudetui/*.go -> internal/tab/agenttui/*.go   (git mv, package rename;
                                                                     see file list below)
D      internal/tab/claudetui/hooks.go, hooks_test.go               (logic moved to internal/agent/claude.go)
M      internal/tab/agenttui/daemon.go                              (THE graft — spawnWithArgs, EnsureStarted,
                                                                       respawnLoop, reapContainerClaude,
                                                                       effectiveKillPattern, writeFileDrops)
M      internal/tab/agenttui/manager.go                             (Adapter field, Kind(), hot-swap via Configurable;
                                                                       POST-COMMIT FIX: SessionDiscoverer gate — see
                                                                       Deviation 5)
M      internal/tab/agenttui/sessions.go                            (POST-COMMIT FIX: TranscriptDir/looksLikeSessionID/
                                                                       LatestSessionID removed — now agent.ClaudeAdapter-
                                                                       only; NewSessionWatcher takes an injectable
                                                                       SessionIDFromPath; new transcriptExistsFor — see
                                                                       Deviation 5)
M      internal/tab/agenttui/sessions_test.go                       (POST-COMMIT FIX: rewritten to mirror maultiagent;
                                                                       TestSessionWatcher_NilIDFromPath added — see
                                                                       Deviation 5)
M      internal/tab/agenttui/manager_integration_test.go            (POST-COMMIT FIX: claudeTranscriptDir test helper,
                                                                       NEW TestEnsureDaemon_NoSessionWatcherWithoutSessionDiscoverer
                                                                       regression guard — see Deviation 5)
M      internal/tab/agenttui/ptyhost_discovery.go                   (same-package now; doc comment + reap pattern)
M      internal/tab/agenttui/discover.go, ptyclient.go, doc.go,
       role.go                                                      (stale claudetui-package doc-comment fixes)
A      internal/tab/agenttui/golden_argv_test.go                    (NEW — AC-S0e8afb-2-3)
A      internal/tab/agenttui/incontainer_testutil_test.go           (re-homed shared test helpers)
M      internal/tab/agenttui/daemon_test.go                         (1-line error-prefix string fix)
M      internal/tab/agenttui/shutdown_reap_test.go                  (reapContainerClaude call sites: + pattern arg)
M      cmd/palmux/main.go                                           (agenttui import, agent.NewClaudeAdapter wiring)
M      cmd/palmux/ptyhost_ownership_test.go                         (agenttui import + testdata path)
M      docs/ROADMAP.json                                            (S0e8afb-2 → done, with honest partial notes)
A      docs/sprint-logs/S0e8afb/verification-S0e8afb-2.md           (this file)
```

Full `internal/tab/claudetui/*` → `internal/tab/agenttui/*` rename list (`git
mv`, mechanical package-name-only unless noted above):
`claude_args.go`, `claude_args_test.go`, `daemon_gate_test.go`,
`daemon_test.go`, `discover_async_test.go`, `discover_test.go`, `emulator.go`,
`emulator_test.go`, `instance_isolation_test.go`, `manager_integration_test.go`,
`manager_test.go`, `multiclient_test.go`, `provider.go`, `provider_test.go`,
`ptyhost_integration_test.go`, `real_incus_survival_test.go`,
`reattach_deadlock_test.go`, `render_snapshot_test.go`, `ring.go`,
`ring_test.go`, `role_test.go`, `server.go`, `server_test.go`, `sessions.go`,
`sessions_test.go`, `store.go`, `store_test.go`, `survival_gate_test.go`,
`testhelper_test.go`, `testdata/{claude_coldstart.bin,claude_long.bin,
fake_claude.go}`.

## The golden-argv equivalence test (AC-S0e8afb-2-3)

Two layers, closing the loop end-to-end:

1. **`internal/agent/claude_golden_test.go`** (brought in verbatim from
   maultiagent — that Sprint's own golden test for the `Sdec0a7-1`
   extraction): `TestClaudeAdapterSpawnSpecMatchesPreRefactorArgv`
   reconstructs the PRE-refactor inline argv/env formula by hand and asserts
   `ClaudeAdapter.SpawnSpec` produces byte-identical output across 7 cases
   (fresh/resume × host/in-container × with/without hooks × permission
   mode). This pins the Adapter's OWN correctness.
2. **`internal/tab/agenttui/golden_argv_test.go`** (new, written for this
   Story): two tests closing the OTHER half of the loop — that
   `spawnWithArgs`'s WIRING (not just the Adapter) delivers that exact argv
   to a real spawned process:
   - `TestGoldenArgv_HostSpawnMatchesAdapterSpec`: spawns a REAL
     (in-process-ptyhost) Daemon with `fake_claude`, has it dump its actual
     received argv/env to a file via `--dump-invocation`, and asserts that
     dump equals `agent.NewClaudeAdapter(...).SpawnSpec(...)`'s
     independently-computed Argv/Env for the same intent (post-
     `resolveClaudeBin`).
   - `TestGoldenArgv_InContainerSpawnMatchesAdapterSpec`: same idea for the
     in-container branch, using `fakePTYRuntime` to record the EXACT argv
     `pc.PTYCommand` was called with, asserted via `reflect.DeepEqual`
     (full equality, not the pre-existing `TestSpawnWithArgs_
     IncusWrapperHandedOpaquelyToPtyhost`'s weaker spot-check via
     `hasArgPair`) against the Adapter's predicted `spec.Argv`.

Both pass. Together they prove: Adapter formula == pre-refactor formula
(test 1), AND wiring delivers Adapter output == what actually got spawned
(test 2) — so pre-refactor formula == what actually got spawned, transitively.

## Commands run

```
$ go build ./...                          # clean
$ go vet ./...                            # clean
$ go test ./internal/agent/...            # ok, 0.04–0.08s
$ go test ./internal/tab/agenttui/...     # ok, ~17-18s (flaked once at 60s+
                                           #   under full-suite parallel load —
                                           #   see below)
$ go test ./cmd/palmux/...                # ok, includes TestPtyOwnership_ModeFilter
$ go test ./... -count=1                  # ALL PASS (28 packages)
$ go test ./internal/tab/agenttui/... \
    -run 'TestDaemon|TestReattach|TestPtyhost|TestSpawnWithArgs|TestManagerReattach' \
    -race -v                              # ALL PASS, including the P0
                                           #   TestReattachSurvivorReplayDoesNotDeadlock
$ go fmt ./...                            # applied to files this Story
                                           #   touched; reverted 2 unrelated
                                           #   pre-existing-drift files
                                           #   (internal/auth/sso_test.go,
                                           #   internal/tab/browser/browser_test.go)
                                           #   that go fmt also wanted to
                                           #   reformat but this Story never
                                           #   touched
$ golangci-lint run ...                   # ENV ISSUE (pre-existing, unrelated):
                                           #   installed v1 binary vs v2-format
                                           #   .golangci.yml on this host — same
                                           #   condition S0e8afb-1's verification
                                           #   log already recorded. go vet is the
                                           #   satisfied proxy per that precedent.
```

### Re-verification after the post-commit SessionDiscoverer-gate fix (Deviation 5)

```
$ go build ./...                                       # clean
$ go vet ./...                                          # clean
$ go test ./internal/tab/agenttui/...                   # ok, 17.7s
$ go test ./internal/tab/agenttui/... \
    -run 'TestSession|TestFirstSpawn|TestManagerEnsureDaemonWithWorktree|TestManagerCloseDaemonWithWatcher' -v
    # ALL PASS, 13 tests — including main-only
    # TestFirstSpawnResumesPersistedSession / TestFirstSpawnFreshWhenTranscriptMissing
    # (confirms the InitialSessionID feature maultiagent doesn't have was preserved)
$ go test ./internal/tab/agenttui/... \
    -run TestEnsureDaemon_NoSessionWatcherWithoutSessionDiscoverer -v
    # PASS — the new regression guard for this fix
$ go test ./internal/tab/agenttui/... \
    -run 'TestDaemon|TestReattach|TestPtyhost|TestSpawnWithArgs|TestGoldenArgv' -race -v
    # ALL PASS — P0 deadlock test + both golden-argv tests re-confirmed clean
    #   after this fix touched sessions.go/manager.go (neither test depends
    #   on SessionWatcher, but re-running costs nothing and this is the
    #   riskiest phase)
$ go test ./... -count=1                                # ALL PASS (28 packages)
```

### One flaky full-suite run, confirmed non-regression

A single `go test ./...` run showed `internal/tab/agenttui`'s
`TestDiscoverAndRestoreAsyncWrapDoesNotBlockServeUnderRealisticReplay` timing
out at its internal 60s budget. Standalone, this same test passes in 0.95s.
Re-running the full suite immediately after (no code changes) was clean
(`agenttui` at 17.9s). This is CPU-contention flakiness from running many
process-spawning packages fully in parallel (`internal/tab/browser` alone
takes 22s; this box was under load from `make serve` + other agent sessions
during this Sprint), not a logic regression — the test's own logic is
untouched by my diff (only the `package claudetui` → `package agenttui`
rename touched this file). CLAUDE.md's own testing conventions document this
exact class of flakiness (env-gated heavy tests, "dev box incus degrades
under session churn" memory note) as a known environment characteristic on
this host, not something to silently paper over — recorded here rather than
omitted.

### Pre-existing (non-regression) hermetic-e2e finding

I additionally ran the existing hermetic e2e regression suite,
`tests/e2e/s7ce250_claude_tui.py` (uses `/bin/cat` as a `--claude-bin`
stand-in, production binary, real HTTP+WS). Most assertions passed
(`AC-S7ce250-5-1`, `AC-S0fd64b-3-1`, `AC-S7ce250-5-2`), but
`AC-S7ce250-5-3` ("input echoed back") failed with `/bin/cat: unrecognized
option '--permission-mode'`.

**I verified this is pre-existing, not a regression**: `settingsStore`'s
`DefaultClaudePermissionMode` is `"auto"` (`internal/config/settings.go`),
and `ClaudePermissionMode()` NEVER returns empty — so `--permission-mode
auto` was ALWAYS unconditionally injected into argv, in BOTH the pre-graft
and post-graft code (identical `if pm != "" { ...prepend... }` logic; the
golden test above proves the Adapter reproduces this exact prepend). `/bin/
cat` chokes on that flag regardless of which code built the argv. I built
the UNMODIFIED base commit (`autopilot/main/S0e8afb`, 182f49a) in a separate
detached worktree and re-ran the identical test against it: **same failure,
same error text, reproduced**. This is a latent gap in the hermetic test's
own environment assumption (that a bare stand-in binary can handle whatever
argv palmux gives it), unrelated to and unaffected by this Story's changes.
I'm recording it here for visibility, not silently working around it — no
test file was modified to hide this.

## Real-claude spawn smoke (mandatory, not skippable per the task brief)

Per CLAUDE.md's dev-instance convention, ran `make serve
INSTANCE=s0e8afb2` from THIS worktree (own portman name/PID/log/tmux-prefix
`_pmx_s0e8afb2_`, fully isolated from the host's own palmux2 and from the
`dev` worktree's instance) — production binary, embedded frontend, REAL
`claude` binary (no override).

1. Created a throwaway git repo under `~/ghq/github.com/palmux2-test/
   s0e8afb2-smoke-<ts>`, opened it via `POST /api/repos/{id}/open`.
2. Switched the `claude:claude` tab to `tui` mode via `PATCH .../tabs/
   claude:claude/settings {"claude_mode":"tui"}`.
3. Playwright (headless chromium) navigated to `/{repoId}/{branchId}/claude`,
   waited for `[data-testid='claude-tui-terminal']`, confirmed the REAL
   claude CLI booted (rendered `Claude Code v2.1.212`, `Welcome back
   takumi!`, `Sonnet 5 · Claude Max ·
   takumi.tsujishita.fb@east.ntt.co.jp's Organization` — i.e. a REAL
   authenticated session, not a stub).
4. Accepted the real first-run "trust this folder" prompt (Enter).
5. Typed `Reply with exactly the single word PALMUX_S0E8AFB2_SMOKE_<ts> and
   nothing else.` into the live PTY (via WS input frames, exactly the
   production path this graft rebuilt) and pressed Enter.
6. Confirmed the composer's live input line actually emptied (proof Enter
   was processed, not just typed).
7. Waited for the rendered terminal to show a completed reply: `●
   PALMUX_S0E8AFB2_SMOKE_<ts>` (claude's own reply-bullet marker) followed by
   `✻ Cooked for Ns` (claude's own turn-timing footer) — the strict
   detection requires BOTH markers together, not just the marker text
   appearing anywhere (an earlier looser version of this check had a
   false-positive risk of matching the marker while it was still sitting
   unsent in the composer — caught and fixed during this Sprint, not
   silently left in).

Ran this smoke 4 times in a row against the SAME still-alive daemon
(confirming — as a side effect — that the Daemon persists correctly across
separate HTTP+WS request cycles, matching production usage). Full transcript
tail from the final clean run:

```
❯ Reply with exactly the single word PALMUX_S0E8AFB2_SMOKE and nothing else.
● PALMUX_S0E8AFB2_SMOKE
✻ Cogitated for 2s
❯ Reply with exactly the single word PALMUX_S0E8AFB2_SMOKE and nothing else.
● PALMUX_S0E8AFB2_SMOKE
✻ Cooked for 2s
❯ Reply with exactly the single word PALMUX_S0E8AFB2_SMOKE_1784254050 and nothing else.
● PALMUX_S0E8AFB2_SMOKE_1784254050
✻ Worked for 1s
❯ Reply with exactly the single word PALMUX_S0E8AFB2_SMOKE_1784254087 and nothing else.
● PALMUX_S0E8AFB2_SMOKE_1784254087
✻ Cooked for 2s
```

Four independent completed real-claude turns, each with a distinct marker,
each showing the full prompt→reply→timing-footer shape. This is
unambiguous, directly-readable proof (not just an automated heuristic) that
the S0e8afb-2 graft spawns a genuine, working claude session end-to-end.

**Cleanup**: closed the branch (`DELETE .../branches/{id}` → 204) and repo
(`POST .../close` → 204), confirmed `GET /api/repos` returns `[]`, `make
serve-stop INSTANCE=s0e8afb2`, removed the throwaway ghq repo directory,
confirmed no lingering `_pmx_s0e8afb2_*` tmux sessions or `ptyhost`
processes.

## Acceptance criteria

- **AC-S0e8afb-2-1**: PASS — `spawnWithArgs` calls `d.adapter.SpawnSpec(intent)`
  and passes the resulting `spec.Argv`/`spec.Env` (host: through
  `resolveClaudeBin`; container: verbatim into `pc.PTYCommand`) into
  `launchAndAttach`. The old inline `creackpty`-adjacent literal arg-building
  is gone (maultiagent's actual `creackpty.Start` call was never adopted in
  the first place — main's ptyhost-based `launchAndAttach` was kept per the
  design doc, so there was no `creackpty` call in this file to discard, only
  the inline arg-BUILDING was replaced — see "core / why the seam is safe").
- **AC-S0e8afb-2-2**: PASS — `notifyURLInContainer`/`containerHookBinPath`
  host-vs-container resolution is UNCHANGED (still lives directly in
  `spawnWithArgs`, computed BEFORE constructing the `SpawnIntent`), and flows
  into the Adapter exclusively via `SpawnIntent.Hook` (`HookEnv{NotifyURL,
  Token, RepoID, BranchID, TabID, HookBinPath}`) — confirmed both by code
  reading and by `TestGoldenArgv_InContainerSpawnMatchesAdapterSpec`, which
  asserts a host-only `NotifyURL`/`HookBinPath` is NEVER used when
  `InContainer: true`.
- **AC-S0e8afb-2-3**: PASS — `internal/agent/claude_golden_test.go`
  (Adapter-level, from maultiagent) + `internal/tab/agenttui/
  golden_argv_test.go` (new, wiring-level, host AND in-container) both green.
- **AC-S0e8afb-2-4**: PASS — `daemon_test.go`, `reattach_deadlock_test.go`,
  `ptyhost_integration_test.go` all green (part of the full
  `internal/tab/agenttui` package test run, plus individually re-run with
  `-race` for the P0-critical subset).

## Concerns (explicit — this is the riskiest phase, not omitting this section)

0. **[Found by independent review, now fixed] SessionWatcher was not
   SessionDiscoverer-gated — see Deviation 5.** This was a real gap in my
   original submission, not a hypothetical: the original `EnsureDaemon`
   would have started a claude-shaped SessionWatcher for ANY adapter,
   including ones with no well-defined transcript layout. It's fixed now
   (capability-gated, new regression test proves the gate holds, all
   existing tests including main's `InitialSessionID` first-spawn-resume
   feature still pass), but I'm leaving this entry in place rather than
   deleting it, as an honest record that my own first-pass review missed
   something an independent reviewer caught. The other deviations below (1-5)
   are deliberate, reasoned scope calls; this one was an oversight that got
   corrected, and I want that distinction visible rather than blended in.

1. **`immediateFailureBackoff`/`StateFatal` deferral (see Deviation 1)**: I
   am confident this is the right scope call for THIS Story, but it means
   S0e8afb-3 (which wires codex/opencode/generic adapters — all far more
   crash-prone than claude) inherits an OBLIGATION to actually bring this in
   before those kinds go live, not just "eventually." I've flagged it
   explicitly in ROADMAP.json's task note so it isn't silently lost.
2. **`GCOrphans`'s hardcoded `containerClaudeBin` fallback (Deviation 3)**
   means an orphaned NON-claude in-container process (once S0e8afb-3 lands)
   would not be correctly reaped by pattern until `ptyhost.StatusFile`
   carries `KillPattern` — today this is a non-issue (only claude exists),
   but it's a real gap S0e8afb-3 must close, not optional polish.
3. **The default-adapter-construction convenience (Deviation 2)** is safe
   and well-tested for THIS Story (single Manager, single Adapter, claude
   only), but it is NOT the shape S0e8afb-3's "one manager per kind" needs
   long-term — `ManagerConfig.ClaudeBin`/`ClaudeArgs` fields will likely need
   to be removed (or renamed to something kind-agnostic) once multiple
   Manager instances exist, each for a different kind. I left them in place
   because removing them now would have forced the exact test-call-site
   churn I was trying to avoid in this Story, but a future Story should
   revisit whether they still make sense.
4. **Judgment call on scope boundary between "this Story" and "S0e8afb-3"**:
   the task brief's own "Also per the design doc" paragraph asked for
   `ptyhost.Config`/`StatusFile` `AgentKind`/`KillPattern` fields in THIS
   Story, while ROADMAP.json's AC list scopes that explicitly to
   S0e8afb-3. I followed ROADMAP.json (the authoritative, git-tracked
   story-tracking artifact) over the task brief's summary paragraph. I
   believe this was the right call given the explicit AC list is more
   specific and authoritative than a summary paragraph, but I want this
   surfaced rather than assumed uncontroversial, since it's exactly the kind
   of ambiguity a reviewer should be able to override if they disagree.
5. **I did not exhaustively audit every remaining cosmetic `claudetui`
   string** (log messages, doc comments) across the whole repo — only fixed
   the ones in files this Story substantively touched (`daemon.go`,
   `manager.go`, `ptyhost_discovery.go`, `discover.go`, `doc.go`, `role.go`,
   `ptyclient.go`) plus anything that would have been factually WRONG after
   the move (e.g. stale file-path references). Grep still finds `claudetui`
   as plain text in `server.go`/`provider.go`/`emulator.go`/`sessions.go`/
   `store.go` log-message prefixes and in `internal/store/*.go` doc
   comments — all harmless (feature-name "claude-tui" or historical
   commentary, not broken code), left as low-priority cleanup for a future
   pass rather than expanding this Story's diff further in its riskiest
   phase.

None of these concerns affect AC-S0e8afb-2-1..4's correctness as verified
above; they are forward-looking risk notes for S0e8afb-3/4.
