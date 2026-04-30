# Sprint S005 — Hook events 表示 — Autonomous Decisions

Branch: `autopilot/S005`
Base: `main` (commits 4c2ab10 → 9cd350a series)
Date: 2026-04-30
Result: **done** — all 4 tasks completed, all acceptance criteria verified.

## Wire-format discovery (foundational)

Before writing any code I ran the live CLI (`claude 2.1.123`) with
`--include-hook-events --output-format=stream-json --include-partial-messages
--verbose --print` against a temp cwd configured with a benign
PreToolUse hook. The captured envelopes are:

```jsonl
{"type":"system","subtype":"hook_started","hook_id":"<uuid>","hook_name":"PreToolUse:Bash","hook_event":"PreToolUse","uuid":"...","session_id":"..."}
{"type":"system","subtype":"hook_response","hook_id":"<uuid>","hook_name":"PreToolUse:Bash","hook_event":"PreToolUse","output":"...","stdout":"...","stderr":"...","exit_code":0,"outcome":"success","uuid":"...","session_id":"..."}
```

This deviates from the roadmap's hypothetical name `hook_event` — the
actual CLI emits **two** envelopes per hook: `system/hook_started` then
`system/hook_response`. The discovery is captured as a comment on
`streamMsg` in `internal/tab/claudeagent/protocol.go`.

## Planning Decisions

- **Opt-in lives on `BranchPrefs`, not on a global Manager config.** Each
  branch has its own Claude tab; users will want hook visibility on
  branches where they configured hooks and not on the others. Mirrors
  the existing per-branch `Model` / `Effort` / `PermissionMode` shape so
  users don't have to learn a new mental model. Persisted via
  `tmp/sessions.json`.
- **Toggle UI lives in the Settings popup.** The popup already shows
  the `.claude/settings.json` content (S002), so a checkbox labelled
  "Show hook events" beside the file viewer is the natural seam — the
  user is already thinking about hooks when they have that modal open.
  Putting it on the Composer row would crowd a UX surface that's
  already at parity with mobile breakpoints.
- **Two envelopes ⇒ one block.** `hook_started` opens a `kind:"hook"`
  Block in `Done:false` state; `hook_response` flips `Done:true` and
  stamps stdout/stderr/exit_code/outcome on the same block (keyed by
  `hook_id`). This matches Anthropic's `content_block_start` →
  `content_block_stop` lifecycle so the existing renderer chrome
  (chevron, summary line, badge) carries over cleanly.
- **Hook turns are role:`"hook"`.** Hook events are independent of the
  assistant turn — they fire from the CLI's own machinery, often after
  the turn that triggered them already ended (PostToolUse). A new
  `Turn.Role = "hook"` keeps them separate from `assistant`/`tool` for
  rendering, and adjacent hooks are coalesced into a single hook turn
  (so PreToolUse + PostToolUse don't fragment the timeline).
- **Out-of-scope deferred:** real-time toggle without respawn. The
  `--include-hook-events` flag is a CLI startup arg — there's no
  in-band control_request to flip it. The PATCH `/prefs` handler
  triggers a respawn; the user is told this in the toggle hint copy.

## Implementation Decisions

- **`internal/tab/claudeagent/client.go`**: Added
  `ClientOptions.IncludeHookEvents` and an argv append in `NewClient`.
  Includes a wire-format reference comment so a future maintainer
  doesn't have to re-run the CLI to know what fields to expect.
- **`internal/tab/claudeagent/protocol.go`**: Extended `streamMsg` with
  `HookID`, `HookName`, `HookEvent`, `HookOutput`, `HookStdout`,
  `HookStderr`, `HookExitCode`, `HookOutcome`, `HookPayload`. Tags
  match the CLI's snake_case keys. `HookPayload` uses the field name
  `modified_payload` per the wire shape (untested in 2.1.123 since
  echoing hooks don't modify payload, but documented for the
  PreToolUse-with-rewrite case).
- **`internal/tab/claudeagent/normalize.go`**: New
  `processHookStarted` / `processHookResponse` paths off `system/*`.
  `hook_response` re-emits a fresh `BlockStart` on top of `BlockEnd` so
  late-joining clients receive the fully-formed completed block.
  `hookCompletion` struct keeps `Session.CompleteHookBlock`'s signature
  modest.
- **`internal/tab/claudeagent/session.go`**: Added `hookBlocks` map
  (`hookID → (turnID, blockID)` ref) and `hookBlockRef` struct.
  `OpenHookBlock` reuses the most recent role:`"hook"` turn at the
  tail when present so consecutive hooks coalesce. `CompleteHookBlock`
  has a synthetic-creation fallback for the rare case where
  hook_response lands without a prior hook_started (e.g. WS reconnect
  mid-hook).
- **`internal/tab/claudeagent/store.go`**: `BranchPrefs.IncludeHookEvents`
  field with `omitempty` so existing sessions.json files don't bloat.
- **`internal/tab/claudeagent/manager.go`**: Plumbing through
  `agentDeps`, `Agent.includeHookEvents` (mu-guarded), and
  `EnsureClient`'s ClientOptions. New `Agent.SetIncludeHookEvents` is
  the single mutation entry point — persists the change to sessions.json
  via `persistPrefs()` and respawns if a client is running.
- **`internal/tab/claudeagent/handler.go` + `provider.go`**:
  `GET/PATCH /api/repos/{repoId}/branches/{branchId}/tabs/claude/prefs`
  endpoints. `branchPrefsView` is a deliberately minimal projection of
  `BranchPrefs` — model/effort/permissionMode already have their own
  WS frame paths, so we don't expose them via this REST entrypoint to
  avoid two writers fighting.
- **`frontend/src/tabs/claude-agent/types.ts`**: Added `'hook'` to
  `BlockKind`, `'hook'` to `Turn.role`, and a small block of
  `hook*`-prefixed Block fields mirroring the Go shape.
- **`frontend/src/tabs/claude-agent/blocks.tsx`**: New `HookBlock`
  component. Reuses `.toolUse`/`.toolHeader`/`.chevron` chrome with a
  `.hookBlock` modifier that swaps the accent colour to `fg-muted` so
  the row reads as ambient automation rather than agent activity.
  Header includes a tone pip keyed off `hookOutcome` + `hookExitCode`.
  Body has a meta grid (event / exit / outcome) plus optional stdout /
  stderr / modified-payload panels.
- **`frontend/src/tabs/claude-agent/agent-state.ts`**: `block.start`
  reducer now dedupes by block id when an existing block with that id
  is already in the turn — required because the backend's
  `processHookResponse` re-emits a `BlockStart` to update the block in
  place. Default-role for an implicit turn from `block.start` is
  `'hook'` when the block is a hook block, else `'assistant'`.
- **`frontend/src/tabs/claude-agent/settings-popup.tsx`**: Added a
  toggle row above the existing scope cards. Reads `/prefs` on open,
  PATCHes on click. Hidden gracefully when the prefs endpoint is
  missing (forward-compat for older binaries that never shipped this).

## Verification

- **Go unit tests** (`go test ./internal/tab/claudeagent/... -run Hook`):
    - `TestHookEvents_StartedAndResponseProduceHookBlock` — full
      lifecycle (started → response → stamped block).
    - `TestHookEvents_MultipleHooksInSameTurn` — Pre+Post coalesce
      into one role:"hook" turn.
    - `TestHookEvents_OptInClientOption` — argv contract.
- **Synthetic-injection E2E** (`tests/e2e/s005_hook_events.py`):
    - 12 PASS against `make serve INSTANCE=dev` on port 8241. Verifies
      REST `/prefs` round-trip, page mount, hook block render, header
      label, expand → stdout/stderr panels, outcome string,
      Settings popup toggle visibility.
- **Live-CLI wire-format check** (`tests/e2e/s005_hook_cli_wire.py`):
    - 4 PASS against `claude 2.1.123` with no palmux server in the
      loop. Spawns the CLI in a temp cwd containing a probe hook,
      sends one Bash request, asserts the captured `hook_started` /
      `hook_response` envelopes carry the fields we depend on.

## Notes for the next sprint

- The dev instance and the host palmux2 share `tmp/sessions.json`
  because both `make serve` invocations cd into the same worktree.
  We rely on per-branch keying (`{repoId}/{branchId}`) to avoid
  collisions; works fine in practice for S005 but worth keeping in
  mind for any future per-instance state.
- We have not exercised `modified_payload` (a hook that rewrites the
  tool input). The wire field exists in our struct, the UI panel is
  there, but no live test fires it. PostToolUse hooks that block tool
  use would also surface as `outcome:"blocked"` — `hookTone` already
  handles that (renders in error tone). Backlog: add a hook fixture
  that demonstrates payload modification once we have a use case.
- The HookBlock auto-collapses on completion via the same false→true
  effect the TaskTreeBlock uses. If a user has expanded a finished
  hook on reload and it gets re-emitted by a fresh `block.start`
  (e.g. WS reconnect), the auto-collapse would not slam shut because
  `prevDone.current` is already `true`. Worth a regression test if the
  reload path turns out to be common.

## E2E reproduction commands

```bash
# Dev instance must be running:
make serve INSTANCE=dev

# Synthetic-injection E2E:
PALMUX_DEV_PORT=8241 \
S005_REPO_ID=tjst-t--palmux2--2d59 \
S005_BRANCH_ID=autopilot--S005--6987 \
python3 tests/e2e/s005_hook_events.py

# Live-CLI wire test (no palmux server needed):
python3 tests/e2e/s005_hook_cli_wire.py
```
