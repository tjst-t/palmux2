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

All 5 tests PASS.
