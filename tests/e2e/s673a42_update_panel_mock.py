#!/usr/bin/env python3
"""Sprint S673a42 — GUI update-panel appliance paths (MOCK / GUI state diagram).

The nixOSHost update UI only renders when the backend reports nixOSHost=true, which
a non-NixOS dev box never does. So the FULL state diagram (Idle / UpdateAvailable /
Kicking / Rebuilding / Reconnecting / Done / Failed) + the palmux-ws image row are
covered here by routing /api/selfupdate (and the rebuild/image endpoints) with the
real frontend served by a real dev backend — only the JSON bodies are simulated;
the store actions, the panel component, and (for the reconnect) a REAL WS drop are
exercised. The companion real-backend E2E (s673a42_update_panel.py) covers the real
API contract (409 gating, applianceFlakeTarget field, image-install job lifecycle),
and the green appliance smoke covers AC-2-4 / AC-3-3 for real.

Acceptance criteria:
  [AC-S673a42-1-1] The nixOS note renders the flake target from the BACKEND
                   (snap.applianceFlakeTarget), not a hardcoded string — proven by
                   injecting a sentinel target and asserting it appears (and the old
                   buggy /etc/palmux never does).
  [AC-S673a42-2-2] nixOSHost + palmux available → update button
                   (update-nixos-rebuild-btn); click → progress/reconnecting panel.
                   Up-to-date → no button (up-to-date note).
  [AC-S673a42-2-5] State diagram: UpdateAvailable → Kicking → Rebuilding(failed) →
                   Failed surfaced; and Kicking → Reconnecting(real WS drop) → Done
                   toast. All interactive elements have data-testids.
  [AC-S673a42-3-1] nixOSHost + image available → image fetch button
                   (update-image-fetch-btn); click → job runs → done → badge clears.
"""
from __future__ import annotations

import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
BIN = os.environ.get("PALMUX_BIN", str(REPO / "bin" / "palmux"))
PW_TIMEOUT = 20_000
_FAILED: list[str] = []


def fail(name, msg):
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name, msg=""):
    print(f"  [{name}] {msg or 'OK'}")


def get_playwright():
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("FAIL: playwright not installed", file=sys.stderr)
        sys.exit(1)
    return sync_playwright


def _free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


class Srv:
    def __init__(self, port, cfg):
        self.port = port
        self.cfg = cfg
        self.proc = None

    def start(self):
        self.proc = subprocess.Popen(
            [BIN, "--addr", f"127.0.0.1:{self.port}", "--config-dir", self.cfg,
             "--tmux-prefix", "_pmx_s673a42_mock_"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=dict(os.environ),
        )
        for _ in range(60):
            try:
                urllib.request.urlopen(f"http://127.0.0.1:{self.port}/api/health", timeout=2)
                return
            except Exception:
                time.sleep(0.2)

    def stop(self):
        if self.proc:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=5)
            except Exception:
                self.proc.kill()
            self.proc = None


def _fulfill(route, obj, status=200):
    route.fulfill(status=status, content_type="application/json", body=json.dumps(obj))


def _snap(*, palmux_available, image_available, flake_target):
    """A selfupdate snapshot for a NixOS appliance."""
    return {
        "components": [
            {"name": "palmux", "display": "palmux 本体", "source": "tjst-t/palmux2",
             "kind": "core-binary", "installed": "v0.12.0",
             "latest": "v0.13.0" if palmux_available else "v0.12.0",
             "available": palmux_available, "fetchable": True},
            {"name": "image", "display": "palmux-ws image", "source": "release asset",
             "kind": "core-image", "installed": "v0.12.0",
             "latest": "v0.13.0" if image_available else "v0.12.0",
             "available": image_available, "fetchable": True},
        ],
        "available": palmux_available or image_available,
        "nixManaged": False,
        "nixOSHost": True,
        "applianceFlakeTarget": flake_target,
        "checkedAt": "2026-07-06T00:00:00Z",
        "degraded": False,
    }


def _open_panel(page, base):
    page.goto(f"{base}/", timeout=PW_TIMEOUT, wait_until="load")
    page.wait_for_selector("[data-testid='update-available-badge']", timeout=PW_TIMEOUT)
    page.locator("[data-testid='update-available-badge']").click(force=True)
    page.wait_for_selector("[data-testid='update-panel']", timeout=PW_TIMEOUT)


def test_flake_target_backend_sourced():
    """[AC-S673a42-1-1] The flake path is rendered from the backend value, not hardcoded."""
    name = "AC-S673a42-1-1"
    port = _free_port(); cfg = tempfile.mkdtemp(prefix="s673-flake-")
    srv = Srv(port, cfg); srv.start(); base = f"http://127.0.0.1:{port}"
    sentinel = "/persist/palmux/nixos#appliance"
    try:
        with get_playwright()() as p:
            browser = p.chromium.launch(headless=True)
            try:
                ctx = browser.new_context(viewport={"width": 1280, "height": 900})
                ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                page = ctx.new_page()
                page.route("**/api/selfupdate", lambda r: _fulfill(
                    r, _snap(palmux_available=True, image_available=False, flake_target=sentinel)))
                _open_panel(page, base)
                note = page.locator("[data-testid='update-nixos-note']").inner_text()
                if sentinel not in note:
                    fail(name, f"note missing backend flake target {sentinel!r}: {note!r}")
                    return
                if "/etc/palmux" in note:
                    fail(name, f"note still shows the buggy /etc/palmux path: {note!r}")
                    return
                ok(name, "note renders backend applianceFlakeTarget; no /etc/palmux")
            finally:
                browser.close()
    finally:
        srv.stop()


def test_update_button_states():
    """[AC-S673a42-2-2] Button shown when available; up-to-date note when not."""
    name = "AC-S673a42-2-2"
    port = _free_port(); cfg = tempfile.mkdtemp(prefix="s673-btn-")
    srv = Srv(port, cfg); srv.start(); base = f"http://127.0.0.1:{port}"
    ft = "/persist/palmux/nixos#appliance"
    try:
        with get_playwright()() as p:
            browser = p.chromium.launch(headless=True)
            try:
                # available → button present.
                ctx = browser.new_context(viewport={"width": 1280, "height": 900})
                ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                page = ctx.new_page()
                page.route("**/api/selfupdate", lambda r: _fulfill(
                    r, _snap(palmux_available=True, image_available=False, flake_target=ft)))
                _open_panel(page, base)
                if page.locator("[data-testid='update-nixos-rebuild-btn']").count() < 1:
                    fail(name, "update button not shown when palmux available")
                    return
                ctx.close()

                # up-to-date (image available only, palmux not) → no host-update button.
                ctx2 = browser.new_context(viewport={"width": 1280, "height": 900})
                ctx2.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                page2 = ctx2.new_page()
                page2.route("**/api/selfupdate", lambda r: _fulfill(
                    r, _snap(palmux_available=False, image_available=True, flake_target=ft)))
                _open_panel(page2, base)
                if page2.locator("[data-testid='update-nixos-rebuild-btn']").count() != 0:
                    fail(name, "host-update button shown even though palmux is up to date")
                    return
                if page2.locator("[data-testid='update-nixos-uptodate']").count() < 1:
                    fail(name, "up-to-date note not shown when palmux up to date")
                    return
                ok(name, "button shown iff palmux update available; else up-to-date note")
            finally:
                browser.close()
    finally:
        srv.stop()


def test_kick_failure_state():
    """[AC-S673a42-2-5] Kicking → Rebuilding(failed) → Failed surfaced (no restart)."""
    name = "AC-S673a42-2-5-fail"
    port = _free_port(); cfg = tempfile.mkdtemp(prefix="s673-fail-")
    srv = Srv(port, cfg); srv.start(); base = f"http://127.0.0.1:{port}"
    ft = "/persist/palmux/nixos#appliance"
    try:
        with get_playwright()() as p:
            browser = p.chromium.launch(headless=True)
            try:
                ctx = browser.new_context(viewport={"width": 1280, "height": 900})
                ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                page = ctx.new_page()
                page.route("**/api/selfupdate", lambda r: _fulfill(
                    r, _snap(palmux_available=True, image_available=False, flake_target=ft)))
                page.route("**/api/selfupdate/rebuild", lambda r: (
                    _fulfill(r, {"ok": True, "status": "triggered", "message": "x"}, 202)
                    if r.request.method == "POST"
                    # GET status → failed (a flake/eval error that never restarts palmux2).
                    else _fulfill(r, {"active": "failed", "result": "exit-code", "running": False})))
                _open_panel(page, base)
                page.locator("[data-testid='update-nixos-rebuild-btn']").click(force=True)
                # Kicking → progress badge appears.
                page.wait_for_selector("[data-testid='update-progress-badge']", timeout=PW_TIMEOUT)
                # Failure poll flips to failed → the failed badge + note appear.
                page.wait_for_selector("[data-testid='update-failed-note']", timeout=PW_TIMEOUT)
                ok(name, "pre-restart rebuild failure surfaced as 更新失敗 (old generation kept)")
            finally:
                browser.close()
    finally:
        srv.stop()


def test_kick_reconnect_done():
    """[AC-S673a42-2-5] Kicking → Reconnecting(real WS drop) → Done toast."""
    name = "AC-S673a42-2-5-done"
    port = _free_port(); cfg = tempfile.mkdtemp(prefix="s673-done-")
    srv = Srv(port, cfg); srv.start(); base = f"http://127.0.0.1:{port}"
    ft = "/persist/palmux/nixos#appliance"
    ver = {"v": "v0.12.0"}
    try:
        with get_playwright()() as p:
            browser = p.chromium.launch(headless=True)
            try:
                ctx = browser.new_context(viewport={"width": 1280, "height": 900})
                ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                ctx.add_init_script("window.__PALMUX_UPDATE_TIMEOUT_MS__ = 12000")
                page = ctx.new_page()
                page.route("**/api/selfupdate", lambda r: _fulfill(
                    r, _snap(palmux_available=True, image_available=False, flake_target=ft)))
                # rebuild POST acks; GET stays 'activating' (still running) so the
                # failure poll never fires — success arrives via the WS-drop handshake.
                page.route("**/api/selfupdate/rebuild", lambda r: (
                    _fulfill(r, {"ok": True, "status": "triggered", "message": "x"}, 202)
                    if r.request.method == "POST"
                    else _fulfill(r, {"active": "activating", "result": "", "running": True})))
                # /health reports the version; we bump it at "restart" time so the
                # reconnect handshake sees a version delta → success toast.
                page.route("**/api/health", lambda r: _fulfill(
                    r, {"status": "ok", "version": ver["v"]}))
                _open_panel(page, base)
                page.locator("[data-testid='update-nixos-rebuild-btn']").click(force=True)
                page.wait_for_selector("[data-testid='update-progress-badge']", timeout=PW_TIMEOUT)
                # Produce a REAL WS drop + reconnect (open-after-drop edge triggers the
                # handshake); bump the reported version as the switch would.
                ver["v"] = "v0.13.0"
                srv.stop(); srv.start()
                page.wait_for_selector("[data-testid='update-complete-toast']", timeout=PW_TIMEOUT)
                ok(name, "host update: real WS drop/reconnect → version delta → 更新しました toast")
            finally:
                browser.close()
    finally:
        srv.stop()


def test_image_fetch_flow():
    """[AC-S673a42-3-1] Image row fetch button → job runs → done → badge clears."""
    name = "AC-S673a42-3-1"
    port = _free_port(); cfg = tempfile.mkdtemp(prefix="s673-img-")
    srv = Srv(port, cfg); srv.start(); base = f"http://127.0.0.1:{port}"
    ft = "/persist/palmux/nixos#appliance"
    phase = {"running": True}
    try:
        with get_playwright()() as p:
            browser = p.chromium.launch(headless=True)
            try:
                ctx = browser.new_context(viewport={"width": 1280, "height": 900})
                ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                page = ctx.new_page()

                def route_selfupdate(r):
                    # After the image install "done", the snapshot no longer has an
                    # available image (badge clears).
                    _fulfill(r, _snap(palmux_available=False,
                                      image_available=phase["running"], flake_target=ft))

                page.route("**/api/selfupdate", route_selfupdate)
                page.route("**/api/selfupdate/image-install", lambda r: (
                    _fulfill(r, {"ok": True, "status": "started", "message": "x"}, 202)
                    if r.request.method == "POST"
                    else _fulfill(r, {"running": phase["running"], "done": not phase["running"],
                                      "error": "", "installed": "v0.13.0"})))
                _open_panel(page, base)
                btn = page.locator("[data-testid='update-image-fetch-btn']")
                if btn.count() < 1:
                    fail(name, "image fetch button not shown for available image on appliance")
                    return
                btn.click(force=True)
                # Job flips to done → the running poll ends, snapshot reloads with no
                # image update → the fetch button disappears (badge cleared).
                time.sleep(0.5)
                phase["running"] = False
                page.wait_for_selector("[data-testid='update-image-fetch-btn']",
                                       state="detached", timeout=PW_TIMEOUT)
                ok(name, "image fetch → job done → snapshot reload → button/badge cleared")
            finally:
                browser.close()
    finally:
        srv.stop()


def main():
    test_flake_target_backend_sourced()
    test_update_button_states()
    test_kick_failure_state()
    test_kick_reconnect_done()
    test_image_fetch_flow()
    print()
    if _FAILED:
        print(f"{len(_FAILED)} FAILED:")
        for f in _FAILED:
            print("  -", f)
        return 1
    print("ALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
