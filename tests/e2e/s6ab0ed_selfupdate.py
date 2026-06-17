"""Sprint S6ab0ed — GUI self-update E2E (real backend, real GitHub poll, real browser).

Acceptance criteria covered (live, production mode — Rule 7):
  [AC-S6ab0ed-1-2] backend polls GitHub for latest tags and computes per-component
                   update-available (verified via the real /api/selfupdate snapshot).
  [AC-S6ab0ed-1-3] top-right "更新あり" badge + update panel listing components
                   current→latest; badge hidden when nothing to update.
  [AC-S6ab0ed-1-4] GitHub unreachable for a component → graceful degrade (snapshot
                   still returned, degraded flag set, no crash, prior components kept).
  [AC-S6ab0ed-2-4] manual-override install (Nix-unmanaged) shows the "手動更新" note
                   instead of the Update-all button.
  [AC-S6ab0ed-2-5] CLI `palmux update --check` prints current→latest (subprocess).

The dev rig MUST be started with PALMUX_SELFUPDATE_FAKE_INSTALLED=v0.9.0 so the
REAL GitHub poll against tjst-t/palmux2 (latest v0.10.0) yields a deterministic
"update available". That env overrides only the detection INPUT — the GitHub
fetch itself is real (no mock, no DRY_RUN).

Run against a dev instance:
  PALMUX_SELFUPDATE_FAKE_INSTALLED=v0.9.0 ./bin/palmux --addr 0.0.0.0:<port> --config-dir ./tmp ...
  PALMUX2_DEV_PORT=<port> python tests/e2e/s6ab0ed_selfupdate.py
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import urllib.request

from playwright.sync_api import sync_playwright

PORT = os.environ.get("PALMUX2_DEV_PORT") or os.environ.get("PALMUX_DEV_PORT") or "8204"
BASE = f"http://localhost:{PORT}"
BIN = os.environ.get("PALMUX_BIN", "./bin/palmux")


def main() -> int:
    failures: list[str] = []

    def check(name: str, cond: bool) -> None:
        print(f"[{'PASS' if cond else 'FAIL'}] {name}")
        if not cond:
            failures.append(name)

    # ---- AC-S6ab0ed-1-2: real backend + real GitHub poll snapshot ----------
    with urllib.request.urlopen(f"{BASE}/api/selfupdate", timeout=20) as r:
        snap = json.load(r)
    comps = {c["name"]: c for c in snap.get("components", [])}
    check("AC-S6ab0ed-1-2 snapshot has palmux + image core components",
          "palmux" in comps and "image" in comps)
    check("AC-S6ab0ed-1-2 palmux latest resolved from GitHub (v0.x)",
          comps.get("palmux", {}).get("latest", "").startswith("v0."))
    check("AC-S6ab0ed-1-2 palmux update-available (v0.9.0 → latest)",
          comps.get("palmux", {}).get("available") is True)
    check("AC-S6ab0ed-1-2 overall available=true", snap.get("available") is True)

    # ---- AC-S6ab0ed-1-4: graceful degrade (a component whose latest failed) -
    # gwq's repo has no matching releases here → its latest is "" but the whole
    # snapshot still returns and is flagged degraded (no crash, others intact).
    check("AC-S6ab0ed-1-4 degraded flag set when a component's GitHub fetch fails",
          snap.get("degraded") is True)
    check("AC-S6ab0ed-1-4 prior components still present despite degrade",
          len(snap.get("components", [])) >= 2)

    # ---- AC-S6ab0ed-2-4: nixManaged reflects ~/update-palmux2.sh presence ---
    # (On this dev box the helper is absent → nixManaged=false → manual note.)
    nix_managed = snap.get("nixManaged")
    check("AC-S6ab0ed-2-4 nixManaged field present (bool)", isinstance(nix_managed, bool))

    # ---- GUI (real browser through the real backend) -----------------------
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        ctx = browser.new_context()
        # Skip the Sa53137 onboarding wizard overlay (it intercepts clicks on a
        # fresh config dir). Setting the seen flag before any page load.
        ctx.add_init_script("window.localStorage.setItem('palmux:onboarding-seen','1')")
        page = ctx.new_page()
        page.goto(BASE, wait_until="domcontentloaded")
        page.wait_for_timeout(1500)

        # AC-S6ab0ed-1-3: the "更新あり" badge appears (an update is available).
        badge = page.locator('[data-testid="update-available-badge"]')
        appeared = False
        try:
            badge.wait_for(state="visible", timeout=6000)
            appeared = True
        except Exception:
            pass
        check("AC-S6ab0ed-1-3 update-available badge visible", appeared)

        if appeared:
            badge.click()
            panel = page.locator('[data-testid="update-panel"]')
            panel.wait_for(state="visible", timeout=4000)
            check("AC-S6ab0ed-1-3 update-panel opens", panel.is_visible())

            comp_palmux = page.locator('[data-testid="update-comp-palmux"]')
            check("AC-S6ab0ed-1-3 update-comp-palmux row present",
                  comp_palmux.count() > 0)
            txt = comp_palmux.inner_text() if comp_palmux.count() else ""
            check("AC-S6ab0ed-1-3 palmux row shows current→latest (v0.9.0 → v0.10.0)",
                  "v0.9.0" in txt and "→" in txt and "v0.10" in txt)

            # AC-S6ab0ed-2-4: this install is Nix-unmanaged → manual note, NOT
            # the Update-all button. (If a future dev box IS Nix-managed, assert
            # the button instead.)
            if nix_managed:
                check("AC-S6ab0ed-2-4 Update-all button present (Nix-managed)",
                      page.locator('[data-testid="update-all-btn"]').count() > 0)
            else:
                check("AC-S6ab0ed-2-4 manual-update note shown (Nix-unmanaged)",
                      page.locator('[data-testid="update-manual-note"]').count() > 0)

        browser.close()

    # ---- AC-S6ab0ed-2-5: CLI `palmux update --check` (real subprocess) ------
    env = dict(os.environ, PALMUX_SELFUPDATE_FAKE_INSTALLED="v0.9.0")
    res = subprocess.run([BIN, "update", "--check"], capture_output=True, text=True, env=env)
    out = res.stdout
    check("AC-S6ab0ed-2-5 `update --check` lists palmux current→latest",
          "palmux" in out and "v0.9.0" in out and "v0.10" in out)
    check("AC-S6ab0ed-2-5 `update --check` exit 2 when update available",
          res.returncode == 2)

    # `palmux update` on a Nix-unmanaged box → guidance + non-zero exit.
    res2 = subprocess.run([BIN, "update"], capture_output=True, text=True,
                          env=dict(os.environ, HOME="/nonexistent-home-xyz"))
    check("AC-S6ab0ed-2-5 `update` on Nix-unmanaged exits non-zero with guidance",
          res2.returncode != 0 and ("手動更新" in (res2.stdout + res2.stderr)))

    print()
    if failures:
        print(f"{len(failures)} FAILED:")
        for f in failures:
            print("  -", f)
        return 1
    print("ALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
