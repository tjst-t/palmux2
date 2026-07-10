#!/usr/bin/env python3
"""Sprint S4323c8-2 — branch picker: create a new branch from the filter box.

Drives a dedicated, throwaway palmux2 instance + a hermetic fixture repo
(see `_fixture.py`) through the acceptance criteria:

  [AC-S4323c8-2-1] Typing a name into the branch-picker filter that does
      NOT match any existing branch surfaces a `"<name>" を作成`
      affordance (`data-testid="branch-picker-create-btn"`).
  [AC-S4323c8-2-2] Clicking it creates a new worktree (via `gwq add -b`,
      through `Store.OpenBranch` → `ensureWorktree`) and opens it — the
      browser navigates to the new workspace's URL.
  [AC-S4323c8-2-3] Typing an *existing* branch name shows no create
      affordance (open-only, no duplicate creation); an invalid branch
      name surfaces an inline error instead of navigating.

Covers both the REST endpoint directly (`POST
/api/repos/{repoId}/branches/open`, which already performs create-if-
missing + open) and the branch-picker UI wiring added for this story.

Exit 0 = PASS / SKIP. Run: python3 tests/e2e/s4323c8_branch_create.py
"""
from __future__ import annotations
import json
import os
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

sys.path.insert(0, os.path.dirname(__file__))
REPO = Path(__file__).resolve().parents[2]
BIN = REPO / "bin" / "palmux"
TO = 20_000


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def http_json(port: int, method: str, path: str, *, body: dict | None = None):
    url = f"http://localhost:{port}{path}"
    raw = json.dumps(body).encode() if body is not None else None
    headers = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, method=method, data=raw, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            code, data = resp.status, resp.read()
    except urllib.error.HTTPError as e:
        code, data = e.code, e.read()
    try:
        return code, json.loads(data.decode() or "{}")
    except json.JSONDecodeError:
        return code, data.decode(errors="replace")


def main() -> None:
    print("s4323c8_branch_create — branch picker filter-to-create new workspace")
    if not BIN.is_file():
        print("SKIP: no prebuilt binary (make build)")
        sys.exit(0)
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("SKIP: playwright not installed")
        sys.exit(0)

    port = free_port()
    os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
    cfg = Path(f"/tmp/palmux2-s4323c8-{port}")
    cfg.mkdir(parents=True, exist_ok=True)
    proc = subprocess.Popen(
        [str(BIN), "--addr", f"127.0.0.1:{port}", "--config-dir", str(cfg),
         "--claude-bin", "/bin/cat", "--tmux-prefix", f"_pmx_s4323c8{port}_"],
        cwd=REPO, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    dl = time.time() + 30
    while time.time() < dl:
        line = proc.stdout.readline() if proc.stdout else ""
        if f":{port}" in line or "listening" in line:
            break
        if proc.poll() is not None:
            print("FAIL: server died")
            sys.exit(1)

    failed = 0
    try:
        import _fixture as fx
        with fx.palmux2_test_fixture("s4323c8") as fixture:
            repo_id = fixture.repo_id
            bid = fixture.primary_branch_id(timeout_s=10.0)

            # ------------------------------------------------------------
            # Backend: POST /branches/open create-if-missing + open
            # ------------------------------------------------------------
            new_name = "s4323c8-api-branch"
            code, body = http_json(
                port, "POST",
                f"/api/repos/{urllib.parse.quote(repo_id)}/branches/open",
                body={"branchName": new_name},
            )
            if code not in (200, 201) or not isinstance(body, dict) or body.get("name") != new_name:
                print(f"FAIL: [AC-S4323c8-2-2] branches/open create-if-missing: {code} {body!r}")
                failed += 1
            else:
                print("PASS: [AC-S4323c8-2-2] POST branches/open creates + opens a new worktree")

            code, branches = http_json(
                port, "GET", f"/api/repos/{urllib.parse.quote(repo_id)}/branches",
            )
            names = [b.get("name") for b in branches] if isinstance(branches, list) else []
            if code != 200 or new_name not in names:
                print(f"FAIL: [AC-S4323c8-2-2] new branch not listed after create: {code} {names!r}")
                failed += 1
            else:
                print("PASS: [AC-S4323c8-2-2] new workspace appears in branch list")

            # Re-opening the same (now-existing) name must not error and
            # must not attempt to create a duplicate branch.
            code, body2 = http_json(
                port, "POST",
                f"/api/repos/{urllib.parse.quote(repo_id)}/branches/open",
                body={"branchName": new_name},
            )
            if code not in (200, 201):
                print(f"FAIL: [AC-S4323c8-2-3] re-opening an existing branch errored: {code} {body2!r}")
                failed += 1
            else:
                print("PASS: [AC-S4323c8-2-3] opening an existing name just opens (idempotent, no dup create)")

            # Invalid branch name (embedded space -> illegal git ref) must
            # surface an error, not silently succeed.
            code, body3 = http_json(
                port, "POST",
                f"/api/repos/{urllib.parse.quote(repo_id)}/branches/open",
                body={"branchName": "s4323c8 invalid name"},
            )
            if code < 400:
                print(f"FAIL: [AC-S4323c8-2-3] invalid branch name did not error: {code} {body3!r}")
                failed += 1
            else:
                print("PASS: [AC-S4323c8-2-3] invalid branch name rejected with an error")

            # ------------------------------------------------------------
            # UI: branch-picker filter -> create affordance
            # ------------------------------------------------------------
            url = (f"http://localhost:{port}/{urllib.parse.quote(repo_id, safe='')}"
                   f"/{urllib.parse.quote(bid, safe='')}/claude")
            with sync_playwright() as p:
                b = p.chromium.launch(headless=True)
                ctx = b.new_context(viewport={"width": 1280, "height": 800})
                ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                pg = ctx.new_page()
                pg.goto(url, wait_until="load", timeout=TO)
                pg.wait_for_function("document.getElementById('root').innerHTML.length > 100", timeout=TO)

                pg.wait_for_selector('[data-action="add-branch"]', timeout=TO)
                pg.click('[data-action="add-branch"]')
                pg.wait_for_selector('input[placeholder*="Filter"]', timeout=TO)
                # Wait for the branch-picker entries to actually load before
                # asserting on exact-match behaviour below — otherwise the
                # empty-entries transient would spuriously show "create".
                pg.wait_for_selector('ul li', timeout=TO)

                # [AC-S4323c8-2-3] Typing the name of an already-open branch
                # (the fixture's default "main") shows no create affordance.
                filt = pg.locator('input[placeholder*="Filter"]')
                filt.fill("main")
                pg.wait_for_timeout(150)
                if pg.locator('[data-testid="branch-picker-create-btn"]').count() != 0:
                    print("FAIL: [AC-S4323c8-2-3] create affordance shown for an existing exact-match name")
                    failed += 1
                else:
                    print("PASS: [AC-S4323c8-2-3] no create affordance for an existing branch name")

                # [AC-S4323c8-2-1] A name with no match shows the affordance.
                ui_new_name = "s4323c8-ui-branch"
                filt.fill(ui_new_name)
                pg.wait_for_selector('[data-testid="branch-picker-create-btn"]', timeout=TO)
                btn_text = pg.locator('[data-testid="branch-picker-create-btn"]').inner_text()
                if ui_new_name not in btn_text:
                    print(f"FAIL: [AC-S4323c8-2-1] create button text missing branch name: {btn_text!r}")
                    failed += 1
                else:
                    print(f"PASS: [AC-S4323c8-2-1] create affordance shown: {btn_text!r}")

                # [AC-S4323c8-2-2] Clicking it creates + opens (URL navigates
                # to a new branchId; the old bid disappears from the URL).
                pg.click('[data-testid="branch-picker-create-btn"]')
                try:
                    pg.wait_for_function(
                        f"!location.pathname.includes('/{bid}/')", timeout=TO,
                    )
                    print(f"PASS: [AC-S4323c8-2-2] clicking create navigates to the new workspace ({pg.url})")
                except Exception:
                    print(f"FAIL: [AC-S4323c8-2-2] no navigation after clicking create (url={pg.url})")
                    failed += 1

                code, branches2 = http_json(
                    port, "GET", f"/api/repos/{urllib.parse.quote(repo_id)}/branches",
                )
                names2 = [x.get("name") for x in branches2] if isinstance(branches2, list) else []
                if code != 200 or ui_new_name not in names2:
                    print(f"FAIL: [AC-S4323c8-2-2] UI-created branch not present server-side: {names2!r}")
                    failed += 1
                else:
                    print("PASS: [AC-S4323c8-2-2] UI-created workspace persisted server-side")

                # [AC-S4323c8-2-3] Invalid name via the UI surfaces an inline
                # error and does not navigate away.
                pg.click('[data-action="add-branch"]')
                pg.wait_for_selector('input[placeholder*="Filter"]', timeout=TO)
                filt2 = pg.locator('input[placeholder*="Filter"]')
                filt2.fill("s4323c8 invalid name")
                pg.wait_for_selector('[data-testid="branch-picker-create-btn"]', timeout=TO)
                url_before = pg.url
                pg.click('[data-testid="branch-picker-create-btn"]')
                try:
                    pg.wait_for_selector('[data-testid="branch-picker-error"]', timeout=TO)
                    if pg.url != url_before:
                        print(f"FAIL: [AC-S4323c8-2-3] navigated away despite invalid-name error (url={pg.url})")
                        failed += 1
                    else:
                        print("PASS: [AC-S4323c8-2-3] invalid branch name surfaces an inline error, no navigation")
                except Exception:
                    print("FAIL: [AC-S4323c8-2-3] invalid branch name did not surface an inline error")
                    failed += 1

                b.close()
    finally:
        if proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=8)
            except subprocess.TimeoutExpired:
                proc.kill()
        import shutil
        shutil.rmtree(cfg, ignore_errors=True)

    print(f"\ns4323c8_branch_create: {'ALL PASS' if failed == 0 else 'FAILED'}")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
