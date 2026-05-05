#!/usr/bin/env python3
"""Sprint S034 — Caddy integration acceptance tests.

Covers AC-S034-5-1 through AC-S034-5-7:
  - settings.json caddy section
  - Caddy snippet written on port expose (when enabled)
  - publicUrl in expose response
  - Caddy snippet removed on unexpose
  - FQDN template placeholder substitution
  - Caddy reload failure: expose still succeeds
  - worktree close removes all Caddy routes

Usage: python3 tests/acceptance/s034_caddy.py

Requires a running palmux2 dev server (make serve INSTANCE=dev).
"""
from __future__ import annotations

import json
import os
import sys
import tempfile
import time
import urllib.parse
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent / "e2e"))
from _fixture import BASE_URL, _http_json, palmux2_test_fixture

PASS = 0
FAIL = 1
_failures: list[str] = []


def ok(tag: str, msg: str) -> None:
    print(f"  ok [{tag}]: {msg}")


def fail(tag: str, msg: str) -> None:
    print(f"FAIL [{tag}]: {msg}", file=sys.stderr)
    _failures.append(f"[{tag}] {msg}")


def get_settings() -> dict:
    code, data = _http_json("GET", "/api/settings")
    if code != 200 or not isinstance(data, dict):
        return {}
    return data  # type: ignore[return-value]


def patch_settings(body: dict) -> bool:
    code, _ = _http_json("PATCH", "/api/settings", body=body)
    return code in (200, 204)


def open_branch(repo_id: str, branch_name: str, isolate: str = "on") -> dict | None:
    code, data = _http_json(
        "POST",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/open",
        body={"branchName": branch_name, "isolateNetwork": isolate},
    )
    if code not in (200, 201):
        return None
    return data  # type: ignore[return-value]


def close_branch(repo_id: str, branch_id: str) -> bool:
    code, _ = _http_json(
        "DELETE",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}",
    )
    return code in (200, 204)


def expose_port(repo_id: str, branch_id: str, internal_port: int) -> tuple[int, dict]:
    return _http_json(
        "POST",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/ports/expose",
        body={"internalPort": internal_port},
    )  # type: ignore[return-value]


def unexpose_port(repo_id: str, branch_id: str, host_port: int) -> int:
    code, _ = _http_json(
        "DELETE",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/ports/{host_port}",
    )
    return code


# ─── AC-S034-5-1 ──────────────────────────────────────────────────────────────

def test_settings_caddy_section() -> None:
    """[AC-S034-5-1] settings.json has networkIsolation.caddy section."""
    settings = get_settings()
    if not settings:
        fail("AC-S034-5-1", "GET /api/settings returned empty")
        return

    net = settings.get("networkIsolation")
    if net is None:
        # Check if it's at a different path.
        fail("AC-S034-5-1", f"settings missing networkIsolation; keys: {list(settings.keys())}")
        return

    caddy = net.get("caddy")
    if caddy is None:
        fail("AC-S034-5-1", "settings.networkIsolation missing caddy section")
        return

    for field in ("enabled", "fqdnTemplate", "configPath", "reloadCmd"):
        if field not in caddy:
            fail("AC-S034-5-1", f"caddy config missing field: {field}")
            return

    ok("AC-S034-5-1", f"settings has networkIsolation.caddy section: available={caddy.get('available')}")


# ─── AC-S034-5-2 ──────────────────────────────────────────────────────────────

def test_caddy_snippet_written_on_expose() -> None:
    """[AC-S034-5-2] Caddy snippet written to configPath on port expose (when enabled)."""
    with tempfile.TemporaryDirectory() as tmpdir:
        snippet_path = str(Path(tmpdir) / "test.caddyfile")

        # Enable Caddy with a no-op reload command and temp snippet path.
        ok_patch = patch_settings({
            "networkIsolation": {
                "caddy": {
                    "enabled": True,
                    "fqdnTemplate": "{{.branch}}-{{.port}}.test.local",
                    "configPath": snippet_path,
                    "reloadCmd": "true",  # no-op
                },
            },
        })
        if not ok_patch:
            fail("AC-S034-5-2", "PATCH /api/settings failed")
            return

        with palmux2_test_fixture("s034-5-2") as fx:
            branch = open_branch(fx.repo_id, "main", isolate="on")
            if branch is None:
                fail("AC-S034-5-2", "openBranch returned null")
                return
            branch_id = branch.get("id", "")
            try:
                time.sleep(2)
                code, data = expose_port(fx.repo_id, branch_id, 5050)
                if code not in (200, 201):
                    fail("AC-S034-5-2", f"expose_port failed: {code} {data}")
                    return

                # Check snippet file.
                if not Path(snippet_path).exists():
                    fail("AC-S034-5-2", f"Caddy snippet not written to {snippet_path}")
                    return

                content = Path(snippet_path).read_text()
                if "reverse_proxy" not in content:
                    fail("AC-S034-5-2", f"Caddy snippet missing reverse_proxy: {content[:200]}")
                    return

                ok("AC-S034-5-2", f"Caddy snippet written: {content.strip()[:80]}")
            finally:
                close_branch(fx.repo_id, branch_id)
                # Restore Caddy to disabled.
                patch_settings({"networkIsolation": {"caddy": {"enabled": False}}})


# ─── AC-S034-5-3 ──────────────────────────────────────────────────────────────

def test_expose_response_has_public_url() -> None:
    """[AC-S034-5-3] expose response includes publicUrl when Caddy enabled."""
    with tempfile.TemporaryDirectory() as tmpdir:
        snippet_path = str(Path(tmpdir) / "test.caddyfile")
        patch_settings({
            "networkIsolation": {
                "caddy": {
                    "enabled": True,
                    "fqdnTemplate": "{{.branch}}-{{.port}}.test.local",
                    "configPath": snippet_path,
                    "reloadCmd": "true",
                },
            },
        })

        with palmux2_test_fixture("s034-5-3") as fx:
            branch = open_branch(fx.repo_id, "main", isolate="on")
            if branch is None:
                fail("AC-S034-5-3", "openBranch returned null")
                return
            branch_id = branch.get("id", "")
            try:
                time.sleep(2)
                code, data = expose_port(fx.repo_id, branch_id, 5051)
                if code not in (200, 201) or not isinstance(data, dict):
                    fail("AC-S034-5-3", f"expose_port failed: {code} {data}")
                    return
                public_url = data.get("publicUrl", "")
                if not public_url:
                    fail("AC-S034-5-3", "expose response missing publicUrl (Caddy enabled)")
                    return
                ok("AC-S034-5-3", f"expose response includes publicUrl: {public_url}")
            finally:
                close_branch(fx.repo_id, branch_id)
                patch_settings({"networkIsolation": {"caddy": {"enabled": False}}})


# ─── AC-S034-5-4 ──────────────────────────────────────────────────────────────

def test_caddy_snippet_removed_on_unexpose() -> None:
    """[AC-S034-5-4] Caddy snippet entry removed on DELETE /ports/{hostPort}."""
    with tempfile.TemporaryDirectory() as tmpdir:
        snippet_path = str(Path(tmpdir) / "test.caddyfile")
        patch_settings({
            "networkIsolation": {
                "caddy": {
                    "enabled": True,
                    "fqdnTemplate": "{{.branch}}-{{.port}}.test.local",
                    "configPath": snippet_path,
                    "reloadCmd": "true",
                },
            },
        })

        with palmux2_test_fixture("s034-5-4") as fx:
            branch = open_branch(fx.repo_id, "main", isolate="on")
            if branch is None:
                fail("AC-S034-5-4", "openBranch returned null")
                return
            branch_id = branch.get("id", "")
            try:
                time.sleep(2)
                code, data = expose_port(fx.repo_id, branch_id, 5052)
                if code not in (200, 201) or not isinstance(data, dict):
                    fail("AC-S034-5-4", f"expose_port failed")
                    return
                host_port = data.get("hostPort", 0)

                del_code = unexpose_port(fx.repo_id, branch_id, host_port)
                if del_code not in (200, 204):
                    fail("AC-S034-5-4", f"unexpose returned {del_code}")
                    return

                # Snippet should be empty or not contain this port.
                if Path(snippet_path).exists():
                    content = Path(snippet_path).read_text()
                    if str(host_port) in content and "reverse_proxy" in content:
                        fail("AC-S034-5-4", f"Caddy snippet still has hostPort {host_port}: {content[:200]}")
                        return

                ok("AC-S034-5-4", f"Caddy snippet updated after unexpose (hostPort={host_port} removed)")
            finally:
                close_branch(fx.repo_id, branch_id)
                patch_settings({"networkIsolation": {"caddy": {"enabled": False}}})


# ─── AC-S034-5-5 ──────────────────────────────────────────────────────────────

def test_fqdn_template_substitution() -> None:
    """[AC-S034-5-5] FQDN template placeholders replaced correctly."""
    with tempfile.TemporaryDirectory() as tmpdir:
        snippet_path = str(Path(tmpdir) / "test.caddyfile")
        template = "{{.branch}}-{{.port}}.example.com"
        patch_settings({
            "networkIsolation": {
                "caddy": {
                    "enabled": True,
                    "fqdnTemplate": template,
                    "configPath": snippet_path,
                    "reloadCmd": "true",
                },
            },
        })

        with palmux2_test_fixture("s034-5-5") as fx:
            branch = open_branch(fx.repo_id, "main", isolate="on")
            if branch is None:
                fail("AC-S034-5-5", "openBranch returned null")
                return
            branch_id = branch.get("id", "")
            try:
                time.sleep(2)
                code, data = expose_port(fx.repo_id, branch_id, 5053)
                if code not in (200, 201) or not isinstance(data, dict):
                    fail("AC-S034-5-5", f"expose_port failed")
                    return
                public_url = data.get("publicUrl", "")
                if not public_url:
                    fail("AC-S034-5-5", "no publicUrl in response")
                    return

                # Should contain "main" (branch name) and "5053" (internal port).
                if "main" not in public_url and "5053" not in public_url:
                    fail("AC-S034-5-5", f"FQDN template not substituted correctly: {public_url}")
                    return

                ok("AC-S034-5-5", f"FQDN template substituted: {public_url}")
            finally:
                close_branch(fx.repo_id, branch_id)
                patch_settings({"networkIsolation": {"caddy": {"enabled": False}}})


# ─── AC-S034-5-6 ──────────────────────────────────────────────────────────────

def test_caddy_reload_failure_expose_still_succeeds() -> None:
    """[AC-S034-5-6] Caddy reload failure doesn't block expose."""
    with tempfile.TemporaryDirectory() as tmpdir:
        snippet_path = str(Path(tmpdir) / "test.caddyfile")
        patch_settings({
            "networkIsolation": {
                "caddy": {
                    "enabled": True,
                    "fqdnTemplate": "{{.branch}}-{{.port}}.example.com",
                    "configPath": snippet_path,
                    "reloadCmd": "false",  # always fails
                },
            },
        })

        with palmux2_test_fixture("s034-5-6") as fx:
            branch = open_branch(fx.repo_id, "main", isolate="on")
            if branch is None:
                fail("AC-S034-5-6", "openBranch returned null")
                return
            branch_id = branch.get("id", "")
            try:
                time.sleep(2)
                code, data = expose_port(fx.repo_id, branch_id, 5054)
                if code not in (200, 201):
                    fail("AC-S034-5-6", f"expose_port failed even with Caddy reload failure: {code} {data}")
                    return
                ok("AC-S034-5-6", f"expose succeeded despite Caddy reload failure (code={code})")
            finally:
                close_branch(fx.repo_id, branch_id)
                patch_settings({"networkIsolation": {"caddy": {"enabled": False}}})


# ─── AC-S034-5-7 ──────────────────────────────────────────────────────────────

def test_close_removes_caddy_snippet() -> None:
    """[AC-S034-5-7] worktree close removes all Caddy snippets for that worktree."""
    with tempfile.TemporaryDirectory() as tmpdir:
        snippet_path = str(Path(tmpdir) / "test.caddyfile")
        patch_settings({
            "networkIsolation": {
                "caddy": {
                    "enabled": True,
                    "fqdnTemplate": "{{.branch}}-{{.port}}.example.com",
                    "configPath": snippet_path,
                    "reloadCmd": "true",
                },
            },
        })

        with palmux2_test_fixture("s034-5-7") as fx:
            branch = open_branch(fx.repo_id, "main", isolate="on")
            if branch is None:
                fail("AC-S034-5-7", "openBranch returned null")
                return
            branch_id = branch.get("id", "")

            time.sleep(2)
            expose_port(fx.repo_id, branch_id, 5055)
            expose_port(fx.repo_id, branch_id, 5056)

            if not close_branch(fx.repo_id, branch_id):
                fail("AC-S034-5-7", "closeBranch failed")
                patch_settings({"networkIsolation": {"caddy": {"enabled": False}}})
                return

            time.sleep(1)
            if Path(snippet_path).exists():
                content = Path(snippet_path).read_text()
                if "reverse_proxy" in content and content.strip():
                    fail("AC-S034-5-7", f"Caddy snippet still has content after close: {content[:200]}")
                    patch_settings({"networkIsolation": {"caddy": {"enabled": False}}})
                    return

            ok("AC-S034-5-7", "Caddy snippet cleared after worktree close")
            patch_settings({"networkIsolation": {"caddy": {"enabled": False}}})


# ─── Main ─────────────────────────────────────────────────────────────────────

def main() -> int:
    print("=== S034 Caddy Integration Acceptance Tests ===\n")

    test_settings_caddy_section()                    # AC-S034-5-1
    test_caddy_snippet_written_on_expose()           # AC-S034-5-2
    test_expose_response_has_public_url()            # AC-S034-5-3
    test_fqdn_template_substitution()                # AC-S034-5-5
    test_caddy_snippet_removed_on_unexpose()         # AC-S034-5-4
    test_caddy_reload_failure_expose_still_succeeds() # AC-S034-5-6
    test_close_removes_caddy_snippet()               # AC-S034-5-7

    print()
    if _failures:
        print(f"FAILED: {len(_failures)} test(s)")
        for f in _failures:
            print(f"  - {f}")
        return FAIL

    print("All tests passed!")
    return PASS


if __name__ == "__main__":
    sys.exit(main())
