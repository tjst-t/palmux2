#!/usr/bin/env python3
"""
tests/acceptance/s18d013_portman_removal.py — Sprint S18d013 acceptance.

portman 連携の削除 (Medium スコープ).

Driven through the user's real entry points:
  - Go toolchain   : `go build ./...` / `go test ./...` (subprocess of the real toolchain)
  - Built binary   : `palmux2 --help` / `--version` (subprocess of the produced artifact)
  - Running server : real HTTP client against a live palmux2 dev rig
                     (start with `make serve INSTANCE=dev`)
  - install.sh     : `bash -n` + grep over the shipped script (CLI entry point)

Coverage:
  [AC-S18d013-1-1]  internal/portman gone; go build / go test pass; /portman route returns
                    ServeMux 404 (unregistered) while a sibling route still works
  [AC-S18d013-1-2]  --portman-url flag absent from --help; /api/health has no portmanURL
  [AC-S18d013-1-4]  Makefile dev/serve + portman binary install untouched (KEEP)
  [AC-S18d013-2-1]  PORTMAN_ROUTING / caddy.json / /etc/portman / portman-{serve,sync,gc} absent
  [AC-S18d013-2-2]  Caddyfile is the sole Caddy path; caddy.service uses Caddyfile; bash -n OK
  [AC-S18d013-2-3]  portman binary install + Makefile dev/serve preserved (KEEP)
  [AC-S18d013-2-4]  README / docs PORTMAN_ROUTING / --portman-url references removed

Run:
  make serve INSTANCE=dev                          # start the rig first (for HTTP ACs)
  python3 tests/acceptance/s18d013_portman_removal.py [--port 8202]
  python3 tests/acceptance/s18d013_portman_removal.py --no-http   # skip live-server ACs
"""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import subprocess
import sys
import urllib.error
import urllib.request


REPO = pathlib.Path(__file__).resolve().parents[2]
INSTALL_SH = REPO / "scripts" / "install.sh"
README = REPO / "README.md"
MAKEFILE = REPO / "Makefile"

RESULTS: list[tuple[str, bool, str]] = []


def record(ac: str, ok: bool, detail: str = "") -> None:
    RESULTS.append((ac, ok, detail))
    print(f"  [{'PASS' if ok else 'FAIL'}] {ac}  {detail}")


def run(cmd: list[str], *, cwd: pathlib.Path = REPO, timeout: int = 600) -> subprocess.CompletedProcess[str]:
    return subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=timeout)


def http_get(url: str) -> tuple[int, str]:
    try:
        with urllib.request.urlopen(url, timeout=15) as resp:
            return resp.status, resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")


# ---------------------------------------------------------------------------
# Story 1 — palmux 内 portman read-only 連携の削除
# ---------------------------------------------------------------------------


def test_go_build_and_test() -> None:
    """[AC-S18d013-1-1] internal/portman gone; build + test pass."""
    pkg_dir = REPO / "internal" / "portman"
    record(
        "AC-S18d013-1-1/pkg-gone",
        not pkg_dir.exists(),
        f"internal/portman exists={pkg_dir.exists()} (want False)",
    )
    b = run(["go", "build", "./..."])
    record("AC-S18d013-1-1/go-build", b.returncode == 0, b.stderr.strip()[:200])
    t = run(["go", "test", "./..."], timeout=900)
    record("AC-S18d013-1-1/go-test", t.returncode == 0, (t.stderr or t.stdout).strip()[:200])
    # no remaining references to the deleted package anywhere in Go source
    grep = run(["grep", "-rn", "tjst-t/palmux2/internal/portman", "internal", "cmd"])
    record(
        "AC-S18d013-1-1/no-imports",
        grep.returncode != 0,
        f"residual imports: {grep.stdout.strip()[:200]}",
    )


def test_portman_route_removed(port: int) -> None:
    """[AC-S18d013-1-1] /portman route unregistered (ServeMux 404)."""
    base = f"http://127.0.0.1:{port}"
    repos_status, repos_body = http_get(f"{base}/api/repos")
    if repos_status != 200:
        record("AC-S18d013-1-1/route-404", False, f"could not list repos (status {repos_status}); is the rig up?")
        return
    repos = json.loads(repos_body)
    if not repos:
        record("AC-S18d013-1-1/route-404", False, "no repos open in dev rig; cannot drive a real repo route")
        return
    repo_id = repos[0]["id"]
    br_status, br_body = http_get(f"{base}/api/repos/{repo_id}/branches")
    branches = json.loads(br_body) if br_status == 200 else []
    if not branches:
        record("AC-S18d013-1-1/route-404", False, f"repo {repo_id} has no branches")
        return
    br_id = branches[0]["id"]
    # sibling route on the same repo/branch must still resolve (proves routing works)
    cmd_status, _ = http_get(f"{base}/api/repos/{repo_id}/branches/{br_id}/commands")
    # the removed route must 404 with the ServeMux "404 page not found" body
    pm_status, pm_body = http_get(f"{base}/api/repos/{repo_id}/branches/{br_id}/portman")
    ok = cmd_status == 200 and pm_status == 404 and "404 page not found" in pm_body
    record(
        "AC-S18d013-1-1/route-404",
        ok,
        f"commands={cmd_status} (want 200), portman={pm_status} body={pm_body.strip()[:40]!r} (want 404 unregistered)",
    )


def test_flag_and_health(port: int) -> None:
    """[AC-S18d013-1-2] --portman-url flag + health.portmanURL removed."""
    binp = REPO / "bin" / "palmux"
    if binp.exists():
        h = run([str(binp), "--help"])
        help_text = (h.stdout + h.stderr).lower()
        record(
            "AC-S18d013-1-2/help-flag",
            "portman-url" not in help_text,
            "found --portman-url in --help" if "portman-url" in help_text else "absent",
        )
    else:
        record("AC-S18d013-1-2/help-flag", False, "bin/palmux not built (run make build)")
    status, body = http_get(f"http://127.0.0.1:{port}/api/health")
    if status == 200:
        health = json.loads(body)
        record(
            "AC-S18d013-1-2/health-no-portmanurl",
            "portmanURL" not in health,
            f"health keys={sorted(health)}",
        )
    else:
        record("AC-S18d013-1-2/health-no-portmanurl", False, f"health status {status}; rig down?")


def test_keep_dev_workflow() -> None:
    """[AC-S18d013-1-4 / AC-S18d013-2-3] dev workflow + portman binary KEEP."""
    mk = MAKEFILE.read_text()
    keeps = ["portman env", "portman sync"]
    have = all(k in mk for k in keeps)
    record(
        "AC-S18d013-1-4/makefile-portman-dev",
        have,
        f"Makefile retains {[k for k in keeps if k in mk]}",
    )
    sh = INSTALL_SH.read_text()
    binary_install = "install -m 0755" in sh and "/portman" in sh and "port-manager" in sh
    record(
        "AC-S18d013-2-3/portman-binary-install",
        binary_install,
        "portman binary release install retained" if binary_install else "MISSING binary install",
    )


# ---------------------------------------------------------------------------
# Story 2 — install.sh PORTMAN_ROUTING (model-B) 削除
# ---------------------------------------------------------------------------


def test_install_sh_clean() -> None:
    """[AC-S18d013-2-1] routing tokens gone."""
    sh = INSTALL_SH.read_text()
    forbidden = [
        "PORTMAN_ROUTING",
        "caddy.json",
        "/etc/portman",
        "portman-serve",
        "portman-sync",
        "portman-gc",
        "model B",
        "model-B",
    ]
    hits = [tok for tok in forbidden if tok in sh]
    record("AC-S18d013-2-1/tokens-gone", not hits, f"residual tokens: {hits}")


def test_install_sh_caddy_sole_path() -> None:
    """[AC-S18d013-2-2] Caddyfile is the only Caddy path; syntax OK."""
    syn = run(["bash", "-n", str(INSTALL_SH)])
    record("AC-S18d013-2-2/bash-n", syn.returncode == 0, syn.stderr.strip()[:200])
    sh = INSTALL_SH.read_text()
    uses_caddyfile = "/etc/caddy/Caddyfile" in sh and "ExecStart=${CADDY_BIN} run --environ --config /etc/caddy/Caddyfile" in sh
    no_jsonfile = "/etc/caddy/caddy.json" not in sh
    has_forward_auth = "forward_auth 127.0.0.1:8080" in sh and "*.${DOMAIN} {" in sh
    record(
        "AC-S18d013-2-2/caddyfile-sole",
        uses_caddyfile and no_jsonfile and has_forward_auth,
        f"caddyfile={uses_caddyfile} no-json={no_jsonfile} apex-sso+wildcard={has_forward_auth}",
    )


def test_docs_clean() -> None:
    """[AC-S18d013-2-4] README / docs scrubbed (excluding sprint-logs/ROADMAP history)."""
    readme = README.read_text()
    bad = [tok for tok in ("PORTMAN_ROUTING", "--portman-url", "model B", "モデルB") if tok in readme]
    record("AC-S18d013-2-4/readme", not bad, f"README residual: {bad}")
    # docs/*.md (live design docs) must not present PORTMAN_ROUTING as a current option.
    # ROADMAP.json + sprint-logs are historical records and are intentionally excluded.
    g = run(
        ["bash", "-c",
         "grep -rln 'PORTMAN_ROUTING' docs --include='*.md' | grep -v sprint-logs || true"],
    )
    offenders = [
        ln for ln in g.stdout.strip().splitlines()
        if ln and "unified-config-design.md" not in ln  # caveat note updated to strikethrough
    ]
    record("AC-S18d013-2-4/docs-md", not offenders, f"docs offenders: {offenders}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=8202, help="dev rig port (make serve INSTANCE=dev)")
    ap.add_argument("--no-http", action="store_true", help="skip live-server ACs")
    args = ap.parse_args()

    print("== Story 1: palmux 内 portman 連携の削除 ==")
    test_go_build_and_test()
    test_keep_dev_workflow()
    if not args.no_http:
        test_portman_route_removed(args.port)
        test_flag_and_health(args.port)
    else:
        print("  [SKIP-HTTP] route/health ACs skipped (--no-http)")

    print("== Story 2: install.sh PORTMAN_ROUTING 削除 ==")
    test_install_sh_clean()
    test_install_sh_caddy_sole_path()
    test_docs_clean()

    failed = [r for r in RESULTS if not r[1]]
    print(f"\n{'='*60}\n{len(RESULTS) - len(failed)}/{len(RESULTS)} checks passed")
    if failed:
        print("FAILURES:")
        for ac, _, detail in failed:
            print(f"  - {ac}: {detail}")
        return 1
    print("ALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
