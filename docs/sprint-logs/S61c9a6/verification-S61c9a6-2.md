# S61c9a6-2 real-VM verification

## Bug found + fixed during self-verification (commit 63920f0)

Initial implementation (`92ce288`) used the same `Type=oneshot` + `wantedBy=multi-user.target`
shape as `palmux-incus-reconcile`, but a real boot measurement showed `multi-user.target` was
gated on this unit's ExecStart (the ~1GB image download) — `systemd-analyze critical-chain`
showed multi-user.target reached only after ~5min, directly on this unit. Root cause: with
systemd's default `DefaultDependencies=yes`, a `wantedBy=multi-user.target` oneshot gets an
*implicit* `Before=multi-user.target`, so the target's own job doesn't resolve until
`ExecStart` exits — fine for `palmux-incus-reconcile` (sub-second, local-only) but not for a
network download of this size. Fixed with `unitConfig.DefaultDependencies = false` (same
mechanism `palmux-grow-persist` already uses elsewhere in `appliance.nix`), which drops the
implicit `Before=` so `multi-user.target` starts this unit via `Wants=` without waiting on it.

## Real-VM verification (post-fix, this dev host, 2026-07-16)

Built on `deploy-test` (192.168.1.43) from `worktree-S61c9a6-2` @ `63920f0`
(`nix build .#appliance-qcow2`), converted to qcow2 (`qemu-img convert -O qcow2 -c`,
852MB), transferred to this dev host (192.168.1.40), booted fresh via the CLAUDE.md
recipe (cloud-init seed user `palmux`), port 12224/17685 to avoid colliding with other
test instances.

### AC-S61c9a6-2-1 (unit exists, resolves the TODO) — PASS
`nixos/modules/palmux.nix` has `systemd.services.palmux-ws-image-install` (the TODO comment
at the old lines 281-282 is gone, replaced with the unit + the dependency-shape explanation
above).

### AC-S61c9a6-2-2 (best-effort, does not block boot) — PASS, measured live
While the image download was still in progress (captured live, not inferred):
```
$ systemctl is-system-running
starting
$ systemctl list-jobs
JOB UNIT                            TYPE  STATE
140 palmux-ws-image-install.service start running
1 jobs listed.
$ uptime
 13:47:19  up   0:00,  0 users, ...
$ echo SSH_OK   # (this command itself succeeded over SSH)
SSH_OK
```
SSH access and general system usability were already available while
`palmux-ws-image-install.service` was still actively `running` (mid-download) — direct proof
boot did not wait for it. After the unit completed:
```
$ systemd-analyze critical-chain multi-user.target
multi-user.target @14.249s
└─palmux-incus-reconcile.service @13.421s +827ms
  └─incus-preseed.service @13.086s +331ms
    ...
$ systemd-analyze
Startup finished in 2.205s (kernel) + 2min 51.817s (userspace) = 2min 54.023s
multi-user.target reached after 14.249s in userspace.
```
`palmux-ws-image-install.service` does not appear anywhere in the critical chain to
`multi-user.target` (14.249s) — it runs fully outside the boot-critical path, confirming the
fix. (The "2min 51.817s (userspace)" total systemd-analyze figure includes this unit's
~1GB download running concurrently in the background; it is not on the path that gates
`multi-user.target`, `sshd`, or general system usability.)

### AC-S61c9a6-2-3 (image present after boot) — PASS
```
$ incus image list
+-----------+--------------+--------+-------------------+--------------+-----------+------------+----------------------+
|   ALIAS   | FINGERPRINT  | PUBLIC |    DESCRIPTION    | ARCHITECTURE |   TYPE    |    SIZE    |     UPLOAD DATE      |
+-----------+--------------+--------+-------------------+--------------+-----------+------------+----------------------+
| palmux-ws | 48fd9211d294 | no     | palmux-ws v0.12.2 | x86_64       | CONTAINER | 1045.69MiB | 2026/07/16 13:49 UTC |
+-----------+--------------+--------+-------------------+--------------+-----------+------------+----------------------+
$ palmux2 runtime doctor
palmux runtime doctor
  ✓ incus: Client version: 6.0.5
  ✓ incus-admin group: active on this process
  ✓ palmux-ws image: imported
  ✓ /etc/subuid: root:1000:1 present
  ✓ /etc/subgid: root:1000:1 present
  ✓ Docker: not running (no FORWARD conflict)
All checks passed.
```

## Cleanup
qemu boot-test process killed after verification. Build artifacts on `deploy-test`
(`~/S61c9a6-2-build`, `~/S61c9a6-2-build-result`, `~/S61c9a6-2-v2.qcow2`,
`~/S61c9a6-2-appliance.qcow2` from the pre-fix build) left in place for now — free disk
on that shared host was ~17G at last check; can be cleaned up if space becomes tight.
