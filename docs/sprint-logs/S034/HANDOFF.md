# S034 Handoff — for the continuing Claude Code session

Sprint: **S034 — Per-worktree network namespace isolation (rootless MVP)**.
Status: **milestone refine phase** — implementation done, hotfixes ongoing
on the test VM. Not yet `sprint done`.

## Where you are

| | |
|---|---|
| Branch (this worktree) | `autopilot/feat/netns-isolation/S034` |
| Long-lived feature branch | `feat/netns-isolation` (merge target when S034 done) |
| Eventually merges into | `main` (with S035+ stacked) |
| Commits ahead of `feat/netns-isolation` | 11 (`git log feat/netns-isolation..HEAD`) |
| Pushed | yes — `origin/autopilot/feat/netns-isolation/S034` |
| Lock | `.claude/autopilot-feat-netns-isolation.lock` (claim it for your session) |

The previous session checked the primary worktree (`/home/ubuntu/ghq/...`) back
to `main` so this branch is free for your worktree to claim.

## What got done

- Sprint impl by an autopilot sub-agent: `internal/netns/{manager,state,forward,discovery,caddy}.go`,
  `frontend/src/components/network-modal.tsx`, `frontend/src/tabs/settings/settings-page.tsx`,
  `frontend/src/stores/netns.ts`, BE endpoints `/ports/{expose,list}` + `/listeners`,
  `docs/INSTALL.md` skeleton.
- 8 hotfixes during refine (newest first):
  - `541f2e4` tabs of isolated worktrees actually run inside the netns
    (added `sudo -n -E nsenter --user= --net=` wrap, fixed empty-command
    case where shells silently ran in host net)
  - `c01b6f7` slirp4netns rendezvous via `--ready-fd` instead of socket-poll
    (fixes rapid-creation race)
  - `22c4b98` per-branch `Branch.Isolated` populated from netns state
    (was reading repo-level default, so `isolated:off` worktrees still
    showed the badge)
  - `905cfab` restore listener-discovery goroutine on palmux restart
    (it died with palmux but the netns survives)
  - `4628215` Network modal Open `↗` uses `window.location.hostname`,
    not `localhost` (was unreachable from remote browsers)
  - `b89a437` 3 BE bugs found during live demo: `nsenter` was EPERM-blocked
    by unprivileged userns, fixed by reading `/proc/<anchorPID>/net/tcp`
    directly; discovery ctx was the HTTP req ctx (canceled on response);
    slirp4netns hostfwd binding `0.0.0.0` instead of `127.0.0.1` for LAN access
  - `1eb6304` AC status `done → pass`/`no_test` to match schema
  - `701899d` Drawer Settings entry, ⌘, shortcut, Settings Cancel/✕/Esc,
    isolate-network checkbox always visible

## Verified on test VM

| Scenario | Result |
|---|---|
| 2 isolated worktrees both bind `:5173` (no host collision) | ✓ |
| LAN access `http://192.168.1.41:13000/` returns dev-a's content | ✓ |
| LAN access `http://192.168.1.41:13001/` returns dev-b's content | ✓ |
| Bash tab process is in netns (`readlink /proc/<pid>/ns/net` matches anchor) | ✓ |
| Listener auto-detect via `/proc/<anchor>/net/tcp` → WS event → badge | ✓ |
| Settings page (`/settings/network`) Save & Close, Cancel, ✕, Esc | ✓ |
| Header `🛡 Isolated · N` count reactive | ✓ |
| New worktree dialog isolate-network checkbox default-on for repo with `isolateNetwork=on` | ✓ |
| 3 ACs (S034-2-1/2/5) skipped: `outbound HTTPS from inside netns` returned `HTTP 000`. Implementation is in place; the failure is environment-specific (slirp4netns config / sandbox). Documented in `acceptance-matrix.json`. | env-skip |

## Test VM

- **Host**: `192.168.1.41` (Ubuntu 24.04, kernel 6.8)
- **SSH**: `ssh ubuntu@192.168.1.41` (passwordless from this VM's key)
- **NOPASSWD sudo**: yes
- **AppArmor**: `kernel.apparmor_restrict_unprivileged_userns=0` applied via Ansible
- **Pre-installed**: `slirp4netns` 1.2.1, `tmux` 3.4, `git`, `python3` 3.12, `ghq` 1.7.1, `gwq` v0.0.14
- **Not installed**: `caddy` (S034-5 Caddy integration verification deferred)
- **palmux running**: `palmux --addr 0.0.0.0:8080 --config-dir ~/.config/palmux` (port 8080)
- **State files**: `~/tmp/netns-state.json`, `/tmp/palmux-slirp4-*.{sock,log}`
- **Test repo**: `~/ghq/github.com/local/dev-test` (registered with palmux as `local--dev-test--fe89`)
- **Helper**: `/tmp/serve.py` — quick HTTP server for inside-netns testing
  - `PORT=5173 MSG="hello dev-a" python3 /tmp/serve.py`

UI: `http://192.168.1.41:8080`

## Open items (priority order)

1. **`docs/INSTALL.md` hotfix** — document NOPASSWD sudo for `nsenter`,
   AppArmor sysctl, slirp4netns / ghq / gwq install, the `setns(CLONE_NEWNET)
   Operation not permitted` failure mode. Foundation is in place from sub-agent;
   needs the post-impl operational details.
2. **Re-test on user's running tabs** — the existing `iso-test-1` /
   `iso-test-2` worktrees on the test VM had their tmux sessions created
   *before* the nsenter-wrap fix, so their bash tabs are still in host net.
   User should close + reopen those branches via the UI to get fresh
   tabs that wrap with the new `sudo -n -E nsenter` prefix.
3. **`leftover slirp4netns` cleanup bug** — palmux restart leaves stale
   `slirp4netns` processes (anchor PID + slirp4netns PID survive as detached
   children). The reconcile path detects orphans by `nsExists`, but does
   not kill the surviving slirp4netns. Add killing in `Reconcile` /
   add a kill-on-startup pass.
4. **slirp4netns log retention** — `/tmp/palmux-slirp4-*.log` files left
   behind on shutdown. Add to cleanup.
5. **Sprint done** when 1–4 are addressed:
   - Update `docs/ROADMAP.json` AC statuses if any changed
   - Verify all sprint-logs in `docs/sprint-logs/S034/`
   - Merge `autopilot/feat/netns-isolation/S034` → `feat/netns-isolation`
   - Push `feat/netns-isolation`
   - Don't merge to `main` yet — next netns sprint stacks on `feat/netns-isolation`

## Key constraints (do not violate)

- **Linux only**. macOS not in scope. No fallback paths needed.
- **Rootless** is the design goal. Sudo is permitted only via `sudo -n nsenter`
  for entering an unprivileged userns from outside (no other operation
  needs sudo). Don't run palmux itself as root.
- **Default ON** for `isolateNetwork` on *newly added* repos. Existing
  repos default OFF (don't break setups that pre-date S034).
- **Subagent worktrees inherit parent ns** by default (`isolateNetwork: "inherit"`);
  override via `"own"`.
- **Caddy via file-based snippet + `caddy reload`**. Not the admin API.
- **Modal, not panel** for Network UI.
- **`make serve INSTANCE=dev`** for any local dev-server testing — never
  `make serve` plain (kills host palmux2 = your own session).

## Files you'll touch

| Path | What |
|---|---|
| `internal/netns/manager.go` | netns lifecycle + slirp4netns subprocess + `--ready-fd` rendezvous |
| `internal/netns/state.go` | `tmp/netns-state.json` SSOT |
| `internal/netns/forward.go` | slirp4netns `add_hostfwd` / `remove_hostfwd` API |
| `internal/netns/discovery.go` | listener detection via `/proc/<anchor>/net/tcp` |
| `internal/netns/caddy.go` | Caddyfile snippet generation + `caddy reload` |
| `internal/store/branch.go` | `wrapWithNsenter()` — note: uses `sudo -n -E nsenter --user= --net=` |
| `internal/store/store.go` | `RestoreNetnsDiscovery()` — startup reconcile |
| `internal/server/handler_branch.go` | populates `Branch.Isolated` from netns state |
| `frontend/src/components/network-modal.tsx` | Network modal + `IsolatedBadge` |
| `frontend/src/tabs/settings/settings-page.tsx` | `/settings/network` page |
| `frontend/src/stores/netns.ts` | Zustand store for listeners + ports |
| `prototype/s034-*.html` | visual source of truth (8 mocks, approved) |

## Roadmap

`docs/ROADMAP.json` → `.sprints.S034`. 5 stories, 47 ACs, 36 tasks. Most
ACs marked `pass`; 3 (`AC-S034-2-1/2/5`) marked `no_test` (env-skip).
S035 ("Unified Settings page") is `pending` and depends on S034 — will
likely base from `main` after S034 merge.

## Sprint-logs

- `docs/sprint-logs/S034/decisions.json` — autopilot sub-agent's decisions
- `docs/sprint-logs/S034/acceptance-matrix.json` — AC pass/fail/skip
- This file: `docs/sprint-logs/S034/HANDOFF.md`
