# Sprint Saa8506-3 — Final manual smoke (sprint gate)

Run date: 2026-05-11
Operator: autopilot agent
Dev rig: `make serve INSTANCE=dev` (port 8205)

## Final regression suite

- `docs/sprint-logs/Saa8506/regression-final.json` → all green
  - go test (`./internal/tab/claudeagent/...`): pass
  - go build (`./...`): pass
  - fe build (`npm --prefix frontend run build`): pass
  - fe lint (`npm --prefix frontend run lint`): pass — errors=0 warnings=8
  - Python E2E: 23/23 pass (22 pre-existing + new s006_attach_dnd_wire)

## Hermetic verification (env override deleted)

The regression script `scripts/sprint-regression-Saa8506.sh` no longer
contains the env-override layer that S4b9df4 / S13b16a used to bridge
hardcoded `BRANCH_ID`s to the dev rig's first open branch. Verified:

```text
$ grep -nE '^\s*export\s+S[0-9A-Za-z_]+_BRANCH_ID|TEST_BRANCH_ID\s*=' \
    scripts/sprint-regression-Saa8506.sh
$ echo $?
0
```

Zero matches — the bridge is gone. All 23 E2E tests pass without any
external bootstrap.

## Manual smoke (fixture lifecycle, post-deletion)

Pre-state:
- `git worktree list | wc -l` = 2
- `ls ~/ghq/github.com/palmux2-test/ | wc -l` = 0

Ran 4 representative tests sequentially against the clean dev rig:

| Test | Result | Fixture cleanup OK |
|---|---|---|
| s001_refine_plan.py | PASS | yes |
| s006_add_dir_file.py | PASS | yes |
| s006_attach_dnd_wire.py (NEW Story 2) | PASS | yes |
| s009_multi_tab.py | PASS | yes |

Post-state:
- `git worktree list | wc -l` = 2 (delta 0)
- `ls ~/ghq/github.com/palmux2-test/ | wc -l` = 0 (delta 0)

The hermetic fixture machinery introduced in S025 + extended in
Saa8506-1-1 reliably cleans up its own state: no leftover ghq paths,
no leftover repos.json entries, no leftover worktrees, no leftover
tmux sessions.

all_clean=yes
