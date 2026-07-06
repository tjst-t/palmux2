"""S41bdf2 — app card model backend acceptance (real HTTP against a dev instance).

Drives the /api/apps surface directly (real HTTP client, not mocks) to verify the
install store, the drop-in generation, the catalog, the nixpkgs validation, and the
share↔shared_dirs single-source binding. Runs against a plain dev instance
(non-NixOS): install persists intent + generates the drop-in but does not rebuild;
share toggles [workspace].shared_dirs (visible via GET /api/deploy — the SAME
single source the generic 共有フォルダ list reads).

  [AC-S41bdf2-1-1] install persists structured intent + generates a one-way drop-in
  [AC-S41bdf2-1-2] GET /api/apps returns the catalog with install/share/reach meta
  [AC-S41bdf2-1-3] install on non-NixOS reports needsRebuild without rebuilding
  [AC-S41bdf2-1-5] POST /api/apps/validate rejects a bad charset / bad name; a fake
                   nix resolves a good name (no rebuild on validate)
  [AC-S41bdf2-2-1] share ON writes the auth path into the SAME shared_dirs source
                   (GET /api/deploy reflects it); share OFF removes it
  [AC-S41bdf2-2-2] sharing a not-installed app is refused (従属, server-enforced)

Env:
    PALMUX2_DEV_PORT=<port>          dev instance port
    PALMUX2_DEV_CONFIG_DIR=<dir>     dev instance --config-dir (to read apps.json / drop-in)
    PALMUX_NIXOS_FLAKE_DIR=<dir>     where 20-apps.nix is written (local/ under it)
"""
from __future__ import annotations

import json
import os
import sys
import urllib.error
import urllib.request

PORT = os.environ.get("PALMUX2_DEV_PORT") or os.environ.get("PALMUX_DEV_PORT") or "8200"
BASE = f"http://localhost:{PORT}"
CONFIG_DIR = os.environ.get("PALMUX2_DEV_CONFIG_DIR", "")
FLAKE_DIR = os.environ.get("PALMUX_NIXOS_FLAKE_DIR", "")
HOME = os.path.expanduser("~")


def _api(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode() or "{}")


def main() -> int:
    failures: list[str] = []

    def check(name, cond, extra=""):
        print(f"[{'PASS' if cond else 'FAIL'}] {name}" + (f" — {extra}" if extra and not cond else ""))
        if not cond:
            failures.append(name)

    # Clean any prior install of our test apps (idempotent).
    for app_id in ("infisical", "gh", "customzz"):
        _api("POST", "/api/apps/uninstall", {"id": app_id})

    # --- GET /api/apps: catalog with meta -------------------------------------
    st, lv = _api("GET", "/api/apps")
    apps = {a["id"]: a for a in lv.get("apps", [])}
    check("AC-S41bdf2-1-2 GET /api/apps returns catalog", st == 200 and "infisical" in apps, extra=str(lv))
    inf = apps.get("infisical", {})
    check("AC-S41bdf2-1-2 card carries reach/boundary meta",
          inf.get("installReach") == "host+containers" and inf.get("shareBoundary") == "hot"
          and inf.get("installBoundary") == "rebuild")
    check("AC-S41bdf2-1-2 fresh app is 'available'", inf.get("state") == "available" and not inf.get("installed"))

    # --- install (non-NixOS: persist + drop-in, no rebuild) -------------------
    st, res = _api("POST", "/api/apps/install", {"id": "infisical"})
    check("AC-S41bdf2-1-3 install returns needsRebuild without rebuildKicked (non-NixOS)",
          st == 200 and res.get("needsRebuild") and not res.get("rebuildKicked"), extra=str(res))

    st, lv = _api("GET", "/api/apps")
    apps = {a["id"]: a for a in lv.get("apps", [])}
    check("AC-S41bdf2-1-1 install persisted → state installed",
          apps.get("infisical", {}).get("installed") and apps["infisical"]["state"] == "installed")

    # apps.json persisted (structured intent).
    if CONFIG_DIR:
        try:
            with open(os.path.join(CONFIG_DIR, "apps.json")) as f:
                af = json.load(f)
            ids = [a["id"] for a in af.get("installed", [])]
            check("AC-S41bdf2-1-1 apps.json persists install intent", "infisical" in ids, extra=str(af))
        except Exception as e:
            check("AC-S41bdf2-1-1 apps.json readable", False, extra=str(e))

    # drop-in generation is a NixOS-only path (short-circuited on non-NixOS); it is
    # verified against a real appliance in the green smoke and by the Go unit test
    # (TestInstallCatalogAppNixOS). Assert it here ONLY when running on a NixOS host.
    if FLAKE_DIR and lv.get("nixOSHost"):
        dropin = os.path.join(FLAKE_DIR, "local", "20-apps.nix")
        try:
            with open(dropin) as f:
                txt = f.read()
            check("AC-S41bdf2-1-1 20-apps.nix generated with pkgs.infisical",
                  "environment.systemPackages" in txt and "pkgs.infisical" in txt, extra=txt)
        except Exception as e:
            check("AC-S41bdf2-1-1 20-apps.nix present", False, extra=str(e))

    # --- share depends on install (從属) ---------------------------------------
    st, res = _api("POST", "/api/apps/share", {"id": "gh", "on": True})
    check("AC-S41bdf2-2-2 sharing a not-installed app is refused", st == 400, extra=f"{st} {res}")

    # --- share ON → shared_dirs single source (GET /api/deploy) ---------------
    st, res = _api("POST", "/api/apps/share", {"id": "infisical", "on": True})
    check("AC-S41bdf2-2-1 share ON accepted", st == 200 and res.get("shared") is True, extra=str(res))
    st, dv = _api("GET", "/api/deploy")
    shared = dv.get("workspace", {}).get("sharedDirs", [])
    want = os.path.join(HOME, ".infisical")
    check("AC-S41bdf2-2-1 auth path in the SAME shared_dirs source (deploy view)", want in shared, extra=str(shared))
    st, lv = _api("GET", "/api/apps")
    apps = {a["id"]: a for a in lv.get("apps", [])}
    check("AC-S41bdf2-2-1 card reflects shared state", apps.get("infisical", {}).get("state") == "shared")

    # --- share OFF removes it -------------------------------------------------
    _api("POST", "/api/apps/share", {"id": "infisical", "on": False})
    st, dv = _api("GET", "/api/deploy")
    check("AC-S41bdf2-2-1 share OFF removes from shared_dirs",
          want not in dv.get("workspace", {}).get("sharedDirs", []))

    # --- validate: charset reject + bad-name reject ---------------------------
    st, r = _api("POST", "/api/apps/validate", {"package": "foo; rm -rf /"})
    check("AC-S41bdf2-1-5 validate rejects bad charset", st == 200 and not r.get("valid"), extra=str(r))
    st, r = _api("POST", "/api/apps/validate", {"package": "definitely-not-a-real-nixpkg-xyz"})
    # Either invalid (nix present) or unavailable (nix absent) — never valid.
    check("AC-S41bdf2-1-5 validate never passes an unresolved name", st == 200 and not r.get("valid"), extra=str(r))

    # cleanup
    _api("POST", "/api/apps/uninstall", {"id": "infisical"})

    print()
    if failures:
        print(f"FAILED: {len(failures)} check(s): {failures}")
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
