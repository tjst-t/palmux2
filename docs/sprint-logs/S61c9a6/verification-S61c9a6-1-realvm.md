# S61c9a6-1 real-VM verification (AC-2/AC-3 completion)

Completed after `worktree-agent-a1567146fcae95d34`'s original attempt was blocked (no Nix build host).
A dedicated Nix-capable incus VM builder (`palmux-nix-builder`, incus VM on dev host 192.168.1.40,
16GiB RAM / 4 vCPU / 80GiB disk, Determinate Nix 3.21.5) was set up for this purpose.

## Build

```
ssh ubuntu@192.168.1.40
incus exec palmux-nix-builder -- bash -c '
  . /nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh
  git clone --branch autopilot/main/S61c9a6 --depth 1 https://github.com/tjst-t/palmux2.git
  cd palmux2
  nix build .#appliance-qcow2 -o /root/build-s61c9a6-1/result --extra-experimental-features "nix-command flakes"
'
```

First attempt OOM-killed the inner disko-image-build qemu process on the original 8GiB VM
(`dmesg`: `Out of memory: Killed process ... (qemu-system-x86) ... UID:30001`). Bumped
`palmux-nix-builder` to 16GiB RAM, retried serially (not concurrent with another build on the
same VM — a first attempt collided with a concurrently-running S61c9a6-3 build in a shared
clone directory, clobbering the `result` symlink; re-ran in a dedicated directory with an
explicit `-o` output path to avoid that). Build succeeded: `result -> nixos-disko-images`,
converted `main.raw` (18GB sparse) to compressed qcow2 via `qemu-img convert -O qcow2 -c`
(862MB, ~83s).

## Boot + verify

Pulled the qcow2 to the dev host (`incus file pull`), built a COW overlay, booted with the
existing CLAUDE.md-documented recipe (cloud-init seed, user `palmux` not `ubuntu`,
`hostfwd=tcp::12223-:22,tcp::17684-:7683` to avoid colliding with any other running test VM).

```
$ ssh -p 12223 palmux@192.168.1.40 'which palmux2'
/run/current-system/sw/bin/palmux2
```
[AC-S61c9a6-1-2] PASS — `palmux2` (the actual binary name; there is no separate `palmux` alias,
confirmed against `nix/packages/palmux2.nix`'s `installPhase: install -Dm755 $src $out/bin/palmux2`)
resolves on PATH after a real appliance boot.

```
$ ssh -p 12223 palmux@192.168.1.40 'palmux2 runtime doctor'
palmux runtime doctor

  ✓ incus: Client version: 6.0.5
  ✓ incus-admin group: active on this process
  ✗ palmux-ws image: NOT found — run: palmux runtime install
  ✓ /etc/subuid: root:1000:1 present
  ✓ /etc/subgid: root:1000:1 present
  ✓ Docker: not running (no FORWARD conflict)

Some checks failed — see hints above.
EXIT:0
```
[AC-S61c9a6-1-3] PASS — the command is found and executes (exit 0, produces real diagnostic
output). The `palmux-ws image: NOT found` line is expected and out of this Story's scope
(that's S61c9a6-2's job) — AC-1-3 only requires the command itself be reachable, not that every
check passes.

## Cleanup
qemu process for this boot test killed after verification. `palmux-nix-builder` VM left running
(shared resource for the rest of this Sprint — S61c9a6-2, S0e8afb, and later phases will reuse it).
Overlay/seed files left in `~/appliance-test/` on the dev host for reuse (base qcow2:
`palmuxos-s61c9a6.qcow2`).
