# S52fc2c incus implementation log

## Stories implemented

### S52fc2c-4: In-container claude reap

**Problem**: After killing the host-side `incus exec` process, the in-container claude may survive as an orphan.

**Solution**:
- Added `ContainerProcessKiller` interface to `internal/runtime/runtime.go`
- Added `KillContainerProcesses` method to `incusRuntime` in `internal/runtime/incus/incus.go`
  - Uses `incus exec <inst> -- pkill -<sig> -f <pattern>` inside the container
  - pkill exit code 1 (no match) is NOT an error
- Modified `internal/tab/claudetui/daemon.go` `Shutdown()` to call `KillContainerProcesses` after the host process is dead, before `sessionIDOnce.Do`
- Modified `internal/tab/claudetui/daemon.go` `respawnLoop()` to call `KillContainerProcesses` before each re-spawn
- Modified `internal/tab/claudeagent/client.go` `Close()` to call `KillContainerProcesses` after killing the host-side incus exec

**Files changed**:
- `internal/runtime/runtime.go` — added `ContainerProcessKiller` interface
- `internal/runtime/incus/incus.go` — added `KillContainerProcesses` method, compile-time assertion
- `internal/tab/claudetui/daemon.go` — kill in `Shutdown()` and `respawnLoop()`
- `internal/tab/claudeagent/client.go` — kill in `Close()`

### S52fc2c-5: Hook binary inode staleness

**Problem**: `ensureHookBinMount` binds the palmux binary by path. After a palmux update (e.g. Nix store creates a new inode at a new path), the bind-mount still serves the old inode.

**Solution**:
- Added `hookBinMu sync.Mutex` and `hookBinInode uint64` fields to `incusRuntime` struct
- Added `syscall` import to `incus.go`
- Replaced `ensureHookBinMount` body with an inode-aware implementation:
  - Resolves symlinks to get the real binary path and inode
  - Compares current inode against the last-mounted inode
  - If they differ, removes the stale device before re-adding
  - Records the new inode on successful mount

**Files changed**:
- `internal/runtime/incus/incus.go` — struct fields, `ensureHookBinMount` replacement

### S52fc2c-7: Image drift scan efficiency

**Problem**: `IsImageStale` calls `aliasFingerprint` (= `incus image list <alias>`) once per workspace per 10s scan. With N workspaces, that's N identical calls.

**Solution**:
- Added `AliasFingerprintCache` struct and `CachedImageDriftChecker` interface to `internal/runtime/runtime.go`
- Added `IsImageStaleWithCache` method to `incusRuntime` — uses the cache to share one `incus image list` call per scan cycle
- Added compile-time assertions for `ContainerProcessKiller` and `CachedImageDriftChecker`
- Modified `internal/store/sync_worktree.go` `scanPorts()`:
  - Creates one `AliasFingerprintCache` per cycle (before the workspace loop)
  - Uses `IsImageStaleWithCache` when available, falls back to `IsImageStale` for any future runtime that only implements the plain interface

**Files changed**:
- `internal/runtime/runtime.go` — `AliasFingerprintCache`, `CachedImageDriftChecker`
- `internal/runtime/incus/incus.go` — `IsImageStaleWithCache`, compile-time assertions
- `internal/store/sync_worktree.go` — per-cycle `fpCache`, two-step drift check

## Tests added

All tests in `internal/runtime/incus/incus_test.go`:

- `TestKillContainerProcesses` — verifies pkill arg sequence; pkill exit 1 is not an error [AC-S52fc2c-4-1]
- `TestEnsureHookBinMount_RefreshesOnInodeChange` — verifies remove before re-add when inode changes [AC-S52fc2c-5-1] [AC-S52fc2c-5-2]
- `TestEnsureHookBinMount_Idempotent` — verifies no remove when inode is unchanged [AC-S52fc2c-5-2]
- `TestIsImageStaleWithCache_OneCallPerCycle` — verifies one `incus image list` for 3 workspaces [AC-S52fc2c-7-1] [AC-S52fc2c-7-3]
- `TestIsImageStaleWithCache_StaleDetected` — verifies stale=true when fingerprints differ [AC-S52fc2c-7-2]
- `TestEnsureHookBinMount_RefreshesAfterRestart` — post-restart (lastInode==0) still removes the stale mount [AC-S52fc2c-5-1] [AC-S52fc2c-5-3]

All 6 tests PASS.

## Review fix (APPROVE-WITH-FIXES)

- **BUG (Story 5, blocked AC-5-3)**: the `inodeChanged` condition originally had a
  `lastInode != 0` clause. After a palmux2 update + restart the `incusRuntime`
  struct is fresh so `hookBinInode`(lastInode)=0 while the container's mount still
  serves the OLD inode — that clause made `inodeChanged=false`, so the stale mount
  was never refreshed (the exact AC-5-3 scenario). Fixed by dropping the guard:
  `inodeChanged := currentInode != 0 && currentInode != lastInode`. The first-ever
  mount now harmlessly does a remove ("not found", logged+ignored) then add. Added
  `TestEnsureHookBinMount_RefreshesAfterRestart` to lock this in.
- **Test hardening (Story 4)**: `TestKillContainerProcesses` now also asserts the
  pkill pattern arg (`cmd[3] == "/home/ubuntu/.local/bin/claude"`) so a wrong/empty
  pattern can't silently pass.

## REAL-INCUS VERIFICATION STEPS (orchestrator, serial, on a real incus host)

These confirm AC-S52fc2c-4-3 / -5-3 / -7 against a live incus-container Workspace
(e.g. `palmux-deploy-test.tjstkm.net`). `<C>` = the workspace container name
(`incus list` to find it, e.g. `palmux2-main-xxxxxxxx`).

### AC-S52fc2c-4-3 — no in-container claude orphan after tab close / session restart

1. Open an incus-container Workspace, start the Claude tab (TUI and/or agent) so
   claude runs inside the container.
2. Confirm it is running inside the container:
   `incus exec <C> -- pgrep -af /home/ubuntu/.local/bin/claude`  → at least 1 pid.
3. Close the Claude tab / restart palmux2 (`systemctl --user restart palmux2`).
4. Re-check inside the container:
   `incus exec <C> -- pgrep -af /home/ubuntu/.local/bin/claude`  → MUST be empty
   (exit code 1, no pids). Confirms the pkill reap fired and left no orphan.
5. Respawn check: trigger a claude crash+resume (or send input that respawns),
   then `pgrep -af` again → exactly ONE claude, not two (AC-4-2).

### AC-S52fc2c-5-3 — hook mount points at the NEW binary after a palmux2 update

1. Note the current mounted binary inode:
   `incus exec <C> -- stat -c %i /usr/local/bin/palmux`  → inode A.
   Cross-check the host: `stat -c %i $(readlink -f "$(which palmux2)")` → inode A.
2. Update palmux2 (new release / `nixos-rebuild switch` / home-manager switch) so
   the host binary inode changes; restart palmux2.
3. Open / re-open the Workspace (drives `Start` → `ensureHookBinMount`).
4. Re-check inside the container:
   `incus exec <C> -- stat -c %i /usr/local/bin/palmux`  → inode B ≠ A, equal to
   the new host binary inode. Confirms the stale device was removed + re-added.
5. Functional check: `incus exec <C> -- /usr/local/bin/palmux --version` (or any
   subcommand whose output changed between versions) reports the NEW version.

### AC-S52fc2c-7 — one `incus image list` per scan cycle regardless of workspace count

1. Open 3+ incus-container Workspaces from the same `palmux-ws` alias.
2. Trace incus CLI invocations for ~30s (3 scan cycles). Either:
   - wrap/temporarily shim the `incus` binary to append argv to a log, OR
   - `sudo journalctl -u incus --since "30s ago"` / `incusd` debug, OR
   - run palmux2 with debug logging and count `image list` calls.
3. Expect ~1 `incus image list palmux-ws -f json` per 10s scan cycle (≈3 over
   30s) — NOT 3-per-cycle (≈9). `config get <inst> volatile.base_image` still
   runs once per workspace per cycle (that is per-container and unavoidable).
4. Parity: the stale ⬆ update badge must appear/clear exactly as before (rebuild
   + re-alias the image, confirm all 3 Workspaces show the badge on the next
   cycle; regenerate one, confirm only that one clears).
