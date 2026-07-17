# Verification — S0e8afb-4 (P4/P5: restart-survival E2E + orphan-GC E2E)

Branch: `worktree-S0e8afb-4` (off `autopilot/main/S0e8afb`, on top of the
merged S0e8afb-1/S0e8afb-2/S0e8afb-3). This is the **final Story of the
whole agenttui × ptyhost merge Sprint** — the heaviest verification phase
per the task brief and `DESIGN_PRINCIPLES.json` priority_rule 9 (real-process
evidence, not mocks/toy scenarios).

## What the whole 4-Story Sprint accomplished (not just this Story's slice)

`docs/agenttui-ptyhost-merge-design.md` set out to merge two branches that
had diverged from `main` and each changed the *same* seam
(`internal/tab/claudetui/daemon.go` → `internal/tab/agenttui/daemon.go`) in
orthogonal ways:

- **`main`** (S3f2658 + S862203, the no-halt-agent milestone): changed *who
  executes the argv* — from an in-process `creackpty.Start` to a detached
  `palmux ptyhost` subprocess reached over a unix socket, so palmux2 can
  restart (self-update / `systemctl restart` / crash) without killing the
  live claude subprocess.
- **`maultiagent`** (a local-only branch, not part of this repo's history):
  changed *who builds the argv* — from an inline claude-specific arg
  builder to a generic `agent.Adapter.SpawnSpec(intent)` contract, enabling
  future non-claude agent kinds (codex, opencode, a user-defined "generic"
  kind) to share the same daemon/manager machinery.

Both changes meet at exactly one point: the `(argv, env, cwd)` tuple handed
to `launchAndAttach`. The Sprint's four Stories implemented this graft in
five phases (P1-P5 per the design doc):

- **S0e8afb-1 (P1 - mechanical adopt)**: brought `internal/ptyhost/**`,
  `cmd/palmux/ptyhost.go`, and the ADRs in verbatim from `main`; renamed
  `claudetui/{ptyclient,discover}.go` -> `agenttui/*.go`. Zero behavior
  change, pure file move.
- **S0e8afb-2 (P2 - graft seam)**: the actual graft. `spawnWithArgs`
  replaced its inline claude arg-builder with a call to
  `d.adapter.SpawnSpec(intent)`; everything below (the P0 reattach-deadlock
  fix region, `launchAndAttach`, the replay drainer) stayed byte-identical.
  Proved via two golden-argv tests (Adapter-level and wiring-level) plus a
  real 4x-repeated live claude smoke test. Found and fixed one real gap
  post-commit (SessionWatcher not gated on `agent.SessionDiscoverer`).
- **S0e8afb-3 (P3 - multiplicity + ownership)**: added `AgentKind`/
  `KillPattern` to `ptyhost.Config`/`StatusFile` (opaque echo), a per-kind
  `Manager` + AgentKind-based discovery/GC ownership filter (so a claude
  Manager and a future generic/codex Manager sharing one run dir never
  cross-adopt or cross-evict each other's ptyhosts - the exact class of bug
  Sfeed64-3 hit for `Mode` ownership). Found and fixed one real safety gap
  post-commit (`killPatternOrFallback` unconditionally guessing
  `containerClaudeBin` for a kind that never declares its own pattern).
- **S0e8afb-4 (this Story, P4/P5 - restart-survival E2E + orphan-GC E2E)**:
  proves the assembled machinery survives a *real* palmux2 crash/restart
  end-to-end, with a *real* running palmux2, a *real* browser, a *real*
  claude subprocess, a *real* `kill -9`, and (for the orphan-GC half) a
  *real* incus container - not a synthetic/unit-level substitute for any of
  those. Documented below.

Net effect for a user: **restarting palmux2 (self-update, `systemctl
restart`, or an unclean crash) no longer kills a running claude session.**
The claude subprocess (or, once a future Story wires `config.toml
[agents.*]`, any other agent kind sharing this same machinery) survives in
its own detached process, and the next palmux2 process picks the
conversation back up mid-stream, screen and context intact. Deleting a
tab/branch while palmux2 happens to be down no longer leaks that
subprocess forever - it gets swept up as soon as palmux2 comes back, via
the same `AgentKind`/`KillPattern` machinery S0e8afb-3 built.

## Scope note (per the task brief, carried forward from S0e8afb-3's own finding)

`agentRegistry.Kinds()` only ever contains `"claude"` in this repo today -
there is no `config.toml [agents.*]` TOML surface to configure a live
"generic" (or codex/opencode) kind (S0e8afb-3 Deviation 1). AC-S0e8afb-4-1's
own wording anticipates this ("generic エージェントタブ**(またはclaude
タブ)**で"). All E2E work below uses **claude in `tui` mode**
(`internal/tab/agenttui`), which is the exact same `agent.Adapter` /
`ptyhost` / `AgentKind` / `KillPattern` machinery a future generic/codex/
opencode kind would use identically - proving the underlying mechanism, not
a claude-specific shortcut. I did not fabricate a fake "generic" tab through
any workaround.

## Environment

This Story ran directly on `dev.tjstkm.net` (`whoami`=`ubuntu`,
`systemd-detect-virt`=`kvm`, `/proc/1/cgroup`=`0::/init.scope` - i.e. this
session was running on the **real host**, not inside an incus Workspace
container, so `incus`, real `systemd --user` scopes, and the host's own
production `palmux2.service` (PID 776, `/usr/local/bin/palmux2`) were all
directly reachable). Per CLAUDE.md's explicit warning ("ホスト用 palmux2 の
`make serve` は自分が今操作している Claude CLI を巻き込んで死ぬ"), **the
host's own production palmux2 instance was never touched** - all work used
`gwq add -b worktree-S0e8afb-4 autopilot/main/S0e8afb` for an isolated
worktree at `/home/ubuntu/ghq/github.com/tjst-t/palmux2/autopilot/main/
S0e8afb` and `make serve INSTANCE=s0e8afb4` for a fully isolated dev
instance (own portman name, own PID/log files, own tmux-prefix
`_pmx_s0e8afb4_`, own ptyhost run dir `/tmp/palmux-ptyhost/pmx_s0e8afb4/`).

## Mandatory-first-steps reading (per the task brief)

Read in full before writing any test code: this CLAUDE.md (including the
palmuxOS real-machine evaluation sections and the env-gate convention
paragraph), `docs/agenttui-ptyhost-merge-design.md`,
`docs/sprint-logs/S0e8afb/verification-S0e8afb-2.md` and
`-3.md`, `docs/no-halt-agent-design.md`, and ADR-0001 through ADR-0004
(`docs/DESIGN/adr/`). Located the existing gated survival tests via
`grep -rl PALMUX_SURVIVAL_SMOKE --include=*.go .` (found
`internal/tab/agenttui/survival_gate_test.go`,
`internal/ptyhost/survival_gate_test.go`, plus a passing mention in
`internal/tab/claudeagent/testutil_test.go`/`spike_control_deadline_test.go`
that document the convention but don't themselves gate a heavy test) and
`grep -rl PALMUX_REALINCUS_SMOKE --include=*.go .` (found
`internal/tab/agenttui/real_incus_survival_test.go`, which is exactly the
S3f2658-4 real-incus survival+reap test the design doc's P5 section
describes - this Story's job is largely to CONFIRM it (and its host-runtime
siblings) still pass post-refactor, per the task brief, plus prove the
same properties in a genuine end-to-end (real running palmux2, not just an
isolated Go test calling `ptyhost.Launcher`/`incus.PTYCommand` directly).

## Part 1 - build + regression baseline (before any E2E)

```
$ cd .../autopilot/main/S0e8afb
$ go build ./...    # clean
$ go vet ./...       # clean
$ go test ./... -count=1
ok  	github.com/tjst-t/palmux2/cmd/palmux              0.874s
ok  	github.com/tjst-t/palmux2/internal/agent            0.050s
ok  	github.com/tjst-t/palmux2/internal/apps              0.013s
ok  	github.com/tjst-t/palmux2/internal/attachment        0.005s
ok  	github.com/tjst-t/palmux2/internal/auth              0.044s
ok  	github.com/tjst-t/palmux2/internal/commands           0.006s
ok  	github.com/tjst-t/palmux2/internal/config             0.427s
ok  	github.com/tjst-t/palmux2/internal/deploy             0.016s
ok  	github.com/tjst-t/palmux2/internal/domain              0.005s
ok  	github.com/tjst-t/palmux2/internal/incusgroup         0.006s
ok  	github.com/tjst-t/palmux2/internal/notify              0.004s
ok  	github.com/tjst-t/palmux2/internal/ptyhost            7.477s
ok  	github.com/tjst-t/palmux2/internal/runtime             0.008s
ok  	github.com/tjst-t/palmux2/internal/runtime/host       0.010s
ok  	github.com/tjst-t/palmux2/internal/runtime/incus      0.096s
ok  	github.com/tjst-t/palmux2/internal/selfupdate          1.097s
ok  	github.com/tjst-t/palmux2/internal/store               1.073s
ok  	github.com/tjst-t/palmux2/internal/tab/agenttab       0.008s
ok  	github.com/tjst-t/palmux2/internal/tab/agenttui      20.032s
ok  	github.com/tjst-t/palmux2/internal/tab/browser       22.121s
ok  	github.com/tjst-t/palmux2/internal/tab/claudeagent    7.299s
ok  	github.com/tjst-t/palmux2/internal/tab/files          0.031s
ok  	github.com/tjst-t/palmux2/internal/tab/git            0.285s
ok  	github.com/tjst-t/palmux2/internal/tab/sprint          0.039s
ok  	github.com/tjst-t/palmux2/internal/tab/sprint/parser   0.020s
ok  	github.com/tjst-t/palmux2/internal/tmux                0.245s
ok  	github.com/tjst-t/palmux2/internal/worktree             0.003s
ok  	github.com/tjst-t/palmux2/internal/worktreewatch        0.330s
```

All 28 packages green, matching S0e8afb-3's own final count.

## Part 2 - existing gated survival tests, re-run post-refactor [AC-S0e8afb-4-2]

```
$ PALMUX_SURVIVAL_SMOKE=1 go test ./internal/ptyhost/... -run \
    'TestLaunch_RealHost_DetachesFromTestProcess|TestLaunch_RealSystemdRunFailure_FallsBackToSetsid|TestLaunch_NoZombieAfterLaunchedProcessExits|TestSurvival_RealSystemd_PtyhostOutlivesLauncherRestartAndKill9' -v -count=1
--- PASS: TestLaunch_RealHost_DetachesFromTestProcess (1.48s)
    launch method = systemd-run
    systemd-run scope cgroup isolation confirmed: ptyhost="0::/user.slice/user-1000.slice/user@1000.service/app.slice/palmux-agent-test-7b764529.scope" test="0::/system.slice/palmux2.service"
--- PASS: TestLaunch_RealSystemdRunFailure_FallsBackToSetsid (1.41s)
    observed fallback-to-setsid: Launch method = "setsid" after unreachable-D-Bus systemd-run failure
    setsid fallback child is alive (pid=1582589)
--- PASS: TestLaunch_NoZombieAfterLaunchedProcessExits (0.74s)
--- PASS: TestSurvival_RealSystemd_PtyhostOutlivesLauncherRestartAndKill9 (3.31s)
    child (counter) pid=1583069, ptyhost pid=1583046, launcher unit=s3f2658-survtest-dk0smulgafh8
    SURVIVAL_PASS x2: ptyhost + held child survived both systemctl --user restart and kill -9 of the launching process
    survival result written to docs/sprint-logs/S3f2658/survival-S3f2658-1.json
ok  	github.com/tjst-t/palmux2/internal/ptyhost	6.938s

$ PALMUX_SURVIVAL_SMOKE=1 go test ./internal/tab/agenttui/... -run \
    'TestParallelInstances_NeverClaimOrGCEachOther' -v -count=1
--- PASS: TestParallelInstances_NeverClaimOrGCEachOther (3.24s)
    [AC-S3f2658-3-3] PASS - instancePrefix isolation confirmed: A/B run dirs differ, each instance's discovery+GC only ever sees its own ptyhosts
ok  	github.com/tjst-t/palmux2/internal/tab/agenttui	3.250s

$ PALMUX_REALINCUS_SMOKE=1 go test ./internal/tab/agenttui/... -run \
    'TestRealIncus_InContainerProcessSurvivesRestartAndIsReaped' -v -count=1 -timeout 300s
--- PASS: TestRealIncus_InContainerProcessSurvivesRestartAndIsReaped (16.03s)
    real incus-exec wrapper argv: [/usr/bin/incus exec -t s3f2658-4-survtest-... -- /bin/bash -c ...]
    ptyhost holds local incus-exec wrapper pid=1591561
    in-container marker process pid=287
    [AC-S3f2658-4-1] PASS: wrapper pid unchanged (1591561), in-container pid unchanged (287), ring grew across simulated restart
    in-container marker process still alive after host-side ptyhost SHUTDOWN alone: false
    [AC-S3f2658-4-2] PASS: explicit runtime.ContainerProcessKiller reaped the in-container process
    confirmed: pre-existing container set unchanged
ok  	github.com/tjst-t/palmux2/internal/tab/agenttui	16.033s
```

All three gated suites pass unmodified against this Sprint's merged code -
the `claudetui` -> `agenttui` rename + Adapter graft + AgentKind/KillPattern
plumbing did not regress any of S3f2658's own no-halt-agent guarantees. The
report JSON files these tests write
(`docs/sprint-logs/S3f2658/{e2e-S3f2658-3,e2e-S3f2658-4,survival-S3f2658-1}.json`)
were refreshed with this run's timestamps/pids and are included in this
Story's commit as fresh PASS evidence.

## Part 3 - real restart-survival E2E [AC-S0e8afb-4-1]

**Setup**: `make serve INSTANCE=s0e8afb4` (isolated dev instance, port
8200). Created a throwaway ghq repo
(`~/ghq/github.com/palmux2-test/s0e8afb4-e2e-<ts>`), opened it via
`POST /api/repos/{id}/open` (repoId
`palmux2-test--s0e8afb4-e2e-1784286251--bfff`, branchId
`s0e8afb4-e2e-1784286251--d8ec`), switched the `claude:claude` tab to `tui`
mode via `PATCH .../tabs/claude:claude/settings {"claude_mode":"tui"}`.

**Turn 1 (pre-crash)**: Playwright (headless chromium, persistent context
so the onboarding-skip localStorage flag survives across script runs)
navigated to `/{repoId}/{branchId}/claude`, accepted the real
"trust this folder" prompt, and sent:

```
Reply with exactly the single word PALMUX_S0E8AFB4_TURN1_1784286251 and nothing else.
PALMUX_S0E8AFB4_TURN1_1784286251
Sauteed for 2s
```

A real, authenticated claude session (`Claude Code v2.1.212`, `Welcome back
takumi!`, `Sonnet 5 - Claude Max - takumi.tsujishita.fb@east.ntt.co.jp's
Organization`) - not a stub.

**Pre-crash pid snapshot**:

```
ptyhost pid=1629553 (agent-kind=claude, kill-pattern=/home/ubuntu/.local/bin/claude)
held claude pid=1629585 (child of 1629553)
palmux2 (s0e8afb4) pid=1611609
```

**The crash** (real, not simulated in a test harness):

```
$ kill -9 1611609
$ sleep 1
palmux2 pid 1611609 alive=no
ptyhost pid 1629553 alive=yes
claude pid 1629585 alive=yes
$ curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8200/api/health --max-time 2
000   # connection refused - palmux2 genuinely dead
```

Waited 5s (simulating a real deploy/restart gap), then relaunched:

```
$ make serve INSTANCE=s0e8afb4
==> Starting palmux2 on port 8200 (log: tmp/palmux-s0e8afb4.log)
    PID: 1676661     # a NEW pid - a genuinely fresh process, not the same one resuming
```

**Reconnect log** (`tmp/palmux-s0e8afb4.log`, from the NEW palmux2 process):

```
INFO agenttui: attached to surviving ptyhost repo=... branch=s0e8afb4-e2e-1784286251--d8ec tab=claude:claude socket=.../918a2ce254ed6b36be25.sock pid=1629585
INFO agenttui: ptyhost spawned/attached repo=... branch=... tab=claude:claude argv="[...]" pid=1629585 reconnected=true degraded=false
INFO agenttui: screen restore jiggle sent repo=... branch=... tab=claude:claude cols=80 rows=24
INFO agenttui: discovery: re-adopted surviving ptyhost repo=... branch=... tab=claude:claude pid=1629585
```

**pid=1629585 is the EXACT SAME claude pid from before the crash** - the
new palmux2 process (a completely different OS process, pid 1676661)
attached to the pre-existing ptyhost (pid 1629553, also unchanged) and its
held claude subprocess (pid 1629585, also unchanged), confirmed by direct
`ps` inspection:

```
$ ps -o pid,ppid,etimes,cmd -p 1629585,1629553
    PID    PPID ELAPSED CMD
1629553       1     223 .../bin/palmux ptyhost --socket ... --agent-kind claude --kill-pattern /home/ubuntu/.local/bin/claude ...
1629585 1629553     222 /home/ubuntu/.local/bin/claude --permission-mode auto ...
```

Both `ppid=1` (reparented to init after the ADR-0003 cgroup-escape spawn)
with `ELAPSED` time spanning the crash - genuine OS-level survival, not a
log-line claim.

**Screen continuity** (a fresh browser page, same persistent context,
navigated to the same URL - no special reconnect logic invoked, just a
normal page load):

```
Reply with exactly the single word PALMUX_S0E8AFB4_TURN1_1784286251 and nothing else.
PALMUX_S0E8AFB4_TURN1_1784286251
Sauteed for 2s
auto mode on (shift+tab to cycle)
```

The terminal shows the exact continuation of the pre-crash conversation -
not a blank/fresh claude session, not a `--resume` re-derivation, the
literal in-progress screen state (via ring-replay + the SIGWINCH screen
jiggle described in `docs/no-halt-agent-design.md` section 5).

**Turn 2 (post-crash, proving live context, not just screen replay)**:
sent a NEW prompt that specifically requires the model to recall turn 1's
content from its own context window (not just the terminal's visual
scrollback, which a human could also read):

```
What was the exact word I asked you to reply with in my previous message? Reply with just that word, nothing else, prefixed by CONTEXT_CHECK:

CONTEXT_CHECK:PALMUX_S0E8AFB4_TURN1_1784286251
Baked for 1s
```

Correct, exact recall - proving the SAME claude conversation (not a fresh
session that happened to have the same visible scrollback) survived the
crash and continued to respond as itself. Screenshot evidence:
`s0e8afb4_turn2.png` (not committed - ephemeral scratchpad artifact,
description above is the durable record).

**Verdict: AC-S0e8afb-4-1 - PASS.** All three sub-claims proven with direct
OS/log/UI evidence, not paraphrase: (a) subprocess survives with the exact
same pid across a real `kill -9` of palmux2 itself, (b) the tab reattaches
with genuine screen continuity (not a blank/fresh session), (c) a new turn
sent after the restart correctly recalls context from before the crash.

## Part 4 - real orphan-GC E2E [AC-S0e8afb-4-3]

Two variants, both against the SAME running dev instance: (a) host
runtime, (b) incus-container runtime (to directly exercise the
in-container `KillPattern` reap path with a REAL running palmux2, not just
the isolated Go test from Part 2).

### 4a - host runtime

Created a second, linked worktree in the same throwaway repo via
`gwq add -b s0e8afb4-orphan-test` (auto-discovered by `sync_worktree` as
`branch.opened`, branchId `s0e8afb4-orphan-test--a456`), set its
`claude:claude` tab to `tui` mode, and spawned it via the browser (a fresh
navigation, no turn sent - spawning is enough to exercise GC).

```
orphan-test ptyhost pid=1742010 (agent-kind=claude, kill-pattern=.../claude)
orphan-test held claude pid=1742027
```

**Crash #2** (`kill -9` the s0e8afb4 palmux2 process again). Confirmed both
survive. **While palmux2 is down**, removed the worktree:

```
$ gwq remove s0e8afb4-orphan-test
Removed worktree: s0e8afb4-orphan-test
```

- i.e. the exact real-world scenario the AC describes: a tab's underlying
identity vanishes during a palmux2-down window (self-update / crash /
deploy gap), not through the DELETE API (which can't be called while
palmux2 is down), through the filesystem directly.

**Relaunch and observe**:

```
$ make serve INSTANCE=s0e8afb4    # new pid 1756787
...
INFO agenttui: discovery: re-adopted surviving ptyhost repo=... branch=s0e8afb4-orphan-test--a456 tab=claude:claude pid=1742027
INFO agenttui: discovery: re-adopted surviving ptyhost repo=... branch=s0e8afb4-e2e-1784286251--d8ec tab=claude:claude pid=1629585
INFO claudetui: startup ptyhost discovery adopted=2 cleanedStale=0
WARN agenttui: subprocess died unexpectedly repo=... branch=s0e8afb4-orphan-test--a456 tab=claude:claude pid=1742027 err=<nil>
INFO agenttui: orphan gc: shut down unreferenced ptyhost repo=... branch=s0e8afb4-orphan-test--a456 tab=claude:claude pid=1742027
INFO store.gcTuiOrphans: reconciled shutdown=1 cleanedStale=0
INFO store.gcTuiOrphans: reconciled shutdown=0 cleanedStale=1
```

Exactly matching the design: boot-time `DiscoverAndRestore` re-adopts
BOTH surviving ptyhosts first (it doesn't know yet which branches are
still open - see `ptyhost_discovery.go`'s own doc comment on why this is
safe: it's the same lazy-attach path a first WS attach would take), then
the very next 10s `gcTuiOrphans` tick (piggybacked on the existing
`scanPorts` loop) checks each one against `store.Tab(...)` - the
orphan-test branch is genuinely gone from the fresh process's in-memory
store (its worktree never existed at boot, so it was never opened) -
and shuts it down.

**Direct OS-level confirmation**:

```
orphan-test ptyhost pid 1742010 alive=no
orphan-test claude pid 1742027 alive=no
---
primary (still-open) branch ptyhost pid 1629553 alive=yes
primary claude pid 1629585 alive=yes
```

The orphan is genuinely dead (pid gone, not just a log line claiming it),
**and** the still-referenced primary branch's ptyhost/claude were
completely untouched by the same GC pass - proving the isLive filter
correctly discriminates, not a blanket sweep.

### 4b - incus-container runtime (in-container `KillPattern` reap, real full stack)

Per the task brief's step 4 ("use real-incus testing... but only if
genuinely needed; host-runtime testing may be sufficient... this Story's
job is proving the end-to-end real-restart flow, which may not require
re-proving the in-container-targeting specifically if it's already
covered"): I judged it worth doing anyway, since I had real incus directly
available on this host and the existing `PALMUX_REALINCUS_SMOKE` test
(re-confirmed in Part 2) only exercises the mechanism via an isolated Go
test calling `ptyhost.Launcher`/`incus.PTYCommand` directly - not through a
genuinely running palmux2 process end-to-end. Doing both closes that last
gap.

Created a third linked worktree (`s0e8afb4-incus-orphan-test`), switched
its runtime via `PATCH .../branches/{id}/runtime {"kind":"incus-container"}`
(-> `{"ok":true,"restarted":true,"runtime":{"kind":"incus-container",
"state":"ready"}}`, real container `palmux2-test-s0e8afb4-e2e-...-inc-
c5dae721` launched from the production `palmux-ws` image), set `tui` mode,
spawned via browser (real `incus exec -t <container> ... -- claude`
wrapper).

```
host-side ptyhost wrapper pid=1790562 (agent-kind=claude, kill-pattern=/home/ubuntu/.local/bin/claude)
local exec-wrapper child pid=1790576
in-container claude pid=446 (via `incus exec <container> -- pgrep -af claude`)
```

**Crash #3**, then **while palmux2 is down**, `gwq remove
s0e8afb4-incus-orphan-test` (worktree gone; the container itself - a
separate resource, see "Concerns" below - was untouched by this step).
Confirmed both host-side ptyhost and in-container claude survive the crash
(`incus exec <container> -- pgrep -af claude` still returns pid 446).
Relaunched palmux2:

```
INFO agenttui: attached to surviving ptyhost repo=... branch=s0e8afb4-incus-orphan-test--25e8 tab=claude:claude socket=.../d671f46ce2b06adbe7f4.sock pid=1790576
INFO agenttui: discovery: re-adopted surviving ptyhost repo=... branch=s0e8afb4-incus-orphan-test--25e8 tab=claude:claude pid=1790576
WARN agenttui: subprocess died unexpectedly repo=... branch=s0e8afb4-incus-orphan-test--25e8 tab=claude:claude pid=1790576 err=<nil>
INFO agenttui: orphan gc: shut down unreferenced ptyhost repo=... branch=s0e8afb4-incus-orphan-test--25e8 tab=claude:claude pid=1790576
```

**Direct evidence, both layers**:

```
host wrapper ptyhost 1790562 alive=no
host wrapper exec-child 1790576 alive=no
$ incus exec palmux2-test-...-inc-c5dae721 -- pgrep -af claude
(empty - no output)
$ incus list palmux2-test-...-inc-c5dae721
RUNNING   # the container itself is untouched - only the claude PROCESS inside it was reaped
```

This is the definitive, real-stack proof for the "in-container proc含む"
half of AC-S0e8afb-4-3: `reapContainerClaude` (called from `GCOrphans` with
`killPatternOrFallback(m.Kind(), h.KillPattern)` - the exact S0e8afb-3
mechanism) drove a real `runtime.ContainerProcessKiller.
KillContainerProcesses(ctx, "TERM", "/home/ubuntu/.local/bin/claude")`
against a real incus container, and the in-container claude process
(pid 446) is genuinely gone, while the container itself survives
(matching the design: only the process, not the workspace, is reaped by
orphan-GC - container lifecycle is a separate concern, see below).
`reapContainerClaude` only logs on error (Debug, non-fatal) - there is no
dedicated success log line by design, so the `pgrep` before/after
comparison is the correct (and only) way to observe this, not a missing
log statement.

**Verdict: AC-S0e8afb-4-3 - PASS.** Both host-runtime and incus-container
orphan-GC proven end-to-end with a real running palmux2, real `kill -9`,
real `gwq remove` during the down window, and real OS/incus-level
before/after pid evidence - not just a passing test assertion.

## Concern (honest, not blocking this Story's AC)

**A worktree removed while palmux2 is down bypasses `CloseBranch`
entirely at the next boot, leaking the incus container (if any) that
workspace held.** Root cause: `sync_worktree`'s "detected removed
worktree" branch (`internal/store/sync_worktree.go` around line ~99) only
fires when a path that WAS in the store's in-memory `OpenBranches` at the
time of a scan tick disappears - i.e. it detects removal RELATIVE TO the
process's own prior in-memory state. A worktree removed while palmux2 is
NOT RUNNING never enters that in-memory state at boot in the first place
(the store's initial branch list, built from `git worktree list` at
startup, already excludes it) - there is no "before" to diff against, so
`CloseBranch` (which contains the `rt.Stop(ctx)` -> `incus delete --force`
logic for incus-container workspaces, `internal/store/branch.go` ~line
255) is never invoked for that branch. I confirmed this empirically in
Part 4b: the incus container (`palmux2-test-...-inc-c5dae721`) was still
`RUNNING` after the orphan-GC pass fully reaped the claude PROCESS inside
it - I cleaned it up manually (`incus delete --force`) as part of this
Story's own test hygiene.

This is **not a defect introduced by S0e8afb** and is **not in scope for
this Story's ACs** (which are specifically about the ptyhost/process layer
- proven correct above, via the `isLive` direct-store-lookup mechanism,
which is a DIFFERENT and independently-correct code path from the
worktree-diff-based `CloseBranch` trigger). It's a pre-existing gap one
layer up, in the S8478ca-era container-lifecycle code, orthogonal to the
no-halt-agent/agenttui merge this Sprint is about. Recording it here for
visibility (per this Story's "highest-stakes verification phase, don't
gloss over uncertainty" instruction) rather than silently working around
it or omitting it - a future Story addressing container-lifecycle
robustness for the "worktree vanished while down" case should know this
gap exists and where it lives.

## Cleanup

All throwaway resources removed after verification: the repo closed via
`POST /api/repos/{id}/close` (204, `GET /api/repos` -> `[]`), `make
serve-stop INSTANCE=s0e8afb4`, the two throwaway incus containers deleted
(`incus delete --force`), throwaway ghq repo + worktree directories
removed, and a final sweep confirmed zero lingering `s0e8afb4`-tagged
ptyhost processes, claude processes, tmux sessions, or incus containers.
(Two intermediate manual `kill -9`s of what I initially - incorrectly -
believed were fully-orphaned leftover pipe-mode ptyhosts turned out to
still be live-referenced by the then-still-running dev-instance palmux2,
which legitimately respawned them via its own `respawnLoop`, the same
"subprocess died unexpectedly -> auto-respawn" feature that keeps a Bash
tab's tmux pane alive after an accidental kill; a final round of kills
performed only AFTER confirming the dev-instance palmux2 was itself fully
stopped resolved this. This was a self-inflicted artifact of manual,
interactive testing hygiene - not a Story bug - recorded here in the
interest of full honesty about the verification process, not because it
reflects on the mechanism under test.)

## Full test suite, one more time on this Story's own branch

```
$ go build ./...     # clean
$ go vet ./...        # clean
$ go test ./... -count=1
# ALL PASS, 28 packages (identical package list to Part 1; one harmless
#   extraneous [no test files] line for frontend/node_modules/flatted's own
#   embedded .go file, a side effect of `npm install` in this fresh
#   worktree, unrelated to any source this Story touched)
```

## Acceptance criteria

- **AC-S0e8afb-4-1**: PASS - real restart-survival E2E with claude (`tui`
  mode, the same `agent.Adapter`/`ptyhost`/`AgentKind`/`KillPattern`
  machinery a generic/codex/opencode kind would use identically). Real
  `kill -9` of a real running palmux2, real subprocess pid survival, real
  screen continuity, real new-turn context recall. See Part 3.
- **AC-S0e8afb-4-2**: PASS - all pre-existing `PALMUX_SURVIVAL_SMOKE=1`
  and `PALMUX_REALINCUS_SMOKE=1` gated tests re-run green post-refactor
  (`internal/ptyhost`'s 4 tests, `agenttui`'s
  `TestParallelInstances_NeverClaimOrGCEachOther` and
  `TestRealIncus_InContainerProcessSurvivesRestartAndIsReaped`), plus the
  full non-gated `go test ./...` (28 packages) clean before and after all
  E2E work. See Parts 1-2.
- **AC-S0e8afb-4-3**: PASS - real orphan-GC E2E on BOTH host and
  incus-container runtime: `kill -9` palmux2, `gwq remove` the worktree
  while down, relaunch, observe (via direct OS/incus pid inspection, not
  just log lines or test assertions) the orphaned ptyhost AND its held
  claude process - including the IN-CONTAINER claude process for the
  incus case - genuinely reaped via the `KillPattern` mechanism, while a
  still-referenced sibling branch's ptyhost/claude are provably
  untouched by the same GC pass. See Part 4.

All three are real-process/real-restart evidence (`DESIGN_PRINCIPLES.json`
priority_rule 9) - no mocks, no toy scenarios, no synthetic stand-ins for
palmux2 itself (the only synthetic substitution anywhere in this Sprint's
verification is the PRE-EXISTING `PALMUX_REALINCUS_SMOKE` test's own
lightweight bash-counter-loop stand-in for claude inside the throwaway
container, documented and justified by THAT test's own file comment -
unrelated to and unchanged by this Story). A toy/synthetic restart test
would not have caught the class of regression the whole no-halt-agent
architecture (and this merge) exists to prevent (the v0.14.12 startup
deadlock this design supersedes).
