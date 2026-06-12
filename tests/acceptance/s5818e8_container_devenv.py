#!/usr/bin/env python3
"""S5818e8 acceptance — incus container shares the host dev environment.

REAL MODE: against a freshly-(re)created incus-container on the deploy VM that
was launched from the new palmux-ws image (shell-UX tools) by the new palmux
binary (extra bind-mounts). Exercises the container via `incus exec`.

Acceptance criteria:
  [AC-S5818e8-1-1] shell dotfiles + gh/git/ssh are bind-mounted at the same
                   paths, owned by ubuntu, with host content.
  [AC-S5818e8-1-2] gh auth status succeeds inside the container; ~/.ssh keys are
                   readable (600, ubuntu).
  [AC-S5818e8-1-3] (no-regression) a host missing a source skips that mount and
                   still starts — covered by the os.Stat guard; here we assert
                   the container started despite optional mounts.
  [AC-S5818e8-2-1] starship/eza/rg/zoxide/fzf/delta/yazi are present in the image.
  [AC-S5818e8-2-2] an interactive login bash sources the host ~/.bashrc and the
                   prompt is driven by starship.

Config: PALMUX2_E2E_VM (default palmux-deploy-test.tjstkm.net), _CONTAINER.
Infra-gated skip only.
"""
from __future__ import annotations

import os
import subprocess
import sys

VM_HOST = os.environ.get("PALMUX2_E2E_VM", "palmux-deploy-test.tjstkm.net")
VM_USER = os.environ.get("PALMUX2_E2E_VM_USER", "ubuntu")
CONTAINER = os.environ.get("PALMUX2_E2E_CONTAINER", "lxc-incus-c18d-incus-5523-9d493c60")

_FAILED: list[str] = []


def fail(n, m):
    print(f"FAIL: [{n}] {m}", file=sys.stderr)
    _FAILED.append(n)


def ok(n, m=""):
    print(f"  [{n}] {m or 'OK'}")


def skip(m):
    print(f"SKIP: {m}")
    sys.exit(0)


def ssh(cmd, timeout=40):
    return subprocess.run(
        ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
         "-o", "StrictHostKeyChecking=no", f"{VM_USER}@{VM_HOST}", cmd],
        capture_output=True, text=True, timeout=timeout)


def cexec(inner, timeout=40):
    # run `inner` inside the container as ubuntu, via the VM.
    return ssh(f"incus exec {CONTAINER} -- su - ubuntu -c {shquote(inner)} </dev/null", timeout)


def shquote(s):
    return "'" + s.replace("'", "'\\''") + "'"


def main() -> int:
    if os.environ.get("SKIP_INCUS_E2E") or ssh("echo ok").returncode != 0:
        skip("VM not reachable / SKIP_INCUS_E2E")

    # The container must be running.
    st = ssh(f"incus list {CONTAINER} -f csv -c s </dev/null").stdout.strip()
    if "RUNNING" not in st.upper():
        fail("AC-S5818e8-1-3", f"container not RUNNING ({st!r}) — start failed?")
        return 1
    ok("AC-S5818e8-1-3", "container started with the optional dev-env mounts (no regression)")

    # AC-1-1: dotfiles + creds present + owned by ubuntu.
    r = cexec("for p in ~/.bashrc ~/.bashrc.d ~/.gitconfig ~/.config/gh ~/.ssh; do "
              "test -e \"$p\" && stat -c '%n %U' \"$p\" || echo \"MISSING $p\"; done")
    out = r.stdout
    missing = [l for l in out.splitlines() if l.startswith("MISSING")]
    not_ubuntu = [l for l in out.splitlines() if l and not l.startswith("MISSING") and not l.endswith(" ubuntu")]
    if missing:
        fail("AC-S5818e8-1-1", f"missing in container: {missing}")
    elif not_ubuntu:
        fail("AC-S5818e8-1-1", f"not owned by ubuntu: {not_ubuntu}")
    else:
        ok("AC-S5818e8-1-1", "shell dotfiles + gh + gitconfig + ssh present, owned by ubuntu")

    # AC-1-2: gh auth + ssh key perms.
    gh = cexec("gh auth status 2>&1 | head -3 || true")
    key = cexec("ls -l ~/.ssh/id_* 2>/dev/null | head -1")
    gh_ok = "github.com" in gh.stdout and ("Logged in" in gh.stdout or "account" in gh.stdout.lower())
    key_line = key.stdout.strip()
    key_ok = key_line.startswith("-rw-------") and " ubuntu " in key_line
    if gh_ok and key_ok:
        ok("AC-S5818e8-1-2", "gh auth status OK in container; ssh key 600/ubuntu")
    else:
        fail("AC-S5818e8-1-2", f"gh_ok={gh_ok} key_ok={key_ok} gh={gh.stdout.strip()[:80]!r} key={key_line!r}")

    # AC-2-1: shell-UX tools baked into the image.
    tools = cexec("for t in starship eza rg zoxide fzf delta yazi; do "
                  "command -v $t >/dev/null 2>&1 && echo \"$t ok\" || echo \"$t MISSING\"; done")
    miss = [l.split()[0] for l in tools.stdout.splitlines() if "MISSING" in l]
    if miss:
        fail("AC-S5818e8-2-1", f"tools missing from image: {miss}")
    else:
        ok("AC-S5818e8-2-1", "starship/eza/rg/zoxide/fzf/delta/yazi all present")

    # AC-2-2: interactive login bash → host ~/.bashrc runs starship.
    # starship sets PROMPT_COMMAND (or a precmd). Detect its hook.
    prompt = cexec("bash -lic 'echo PROMPT_COMMAND=$PROMPT_COMMAND; "
                   "type -t starship_precmd 2>/dev/null; declare -f starship_precmd >/dev/null 2>&1 && echo HAS_STARSHIP_PRECMD' 2>/dev/null")
    p = prompt.stdout
    if "starship" in p.lower() or "HAS_STARSHIP_PRECMD" in p:
        ok("AC-S5818e8-2-2", "interactive bash sources host ~/.bashrc → starship prompt active")
    else:
        fail("AC-S5818e8-2-2", f"starship not active in interactive shell: {p.strip()[:120]!r}")

    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
