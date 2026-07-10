#!/usr/bin/env python3
"""Story S4323c8-3 — Files internal-link navigation pushes history.

Regression: clicking an internal link inside a Markdown preview (e.g.
`[link](./b.md)`) that jumps to another file inside the Files tab must
push a new `history` entry (URL updates to `/{repoId}/{branchId}/files/
<path>`) so the browser back/forward buttons can move between the two
files. AC-S4323c8-3-3 additionally guards that the pre-existing
list-click navigation (clicking a file in the file list) still pushes
history the same way (no regression).

Exit code 0 = PASS. Anything else = FAIL. Run:
  python3 tests/e2e/s4323c8_files_history.py
"""
from __future__ import annotations

import os
import signal
import socket
import subprocess
import sys
import time
import urllib.parse
from pathlib import Path

sys.path.insert(0, os.path.dirname(__file__))
REPO = Path(__file__).resolve().parents[2]
BIN = REPO / "bin" / "palmux"
TO = 20_000


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


A_MD = """# File A

This links to [File B](./b.md).
"""

B_MD = """# File B

This links back to [File A](./a.md).
"""


def main() -> None:
    print("s4323c8_files_history — Files internal-link nav pushes history")
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
        [
            str(BIN),
            "--addr",
            f"127.0.0.1:{port}",
            "--config-dir",
            str(cfg),
            "--claude-bin",
            "/bin/cat",
            "--tmux-prefix",
            f"_pmx_s4323c8{port}_",
        ],
        cwd=REPO,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
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
            (fixture.path / "a.md").write_text(A_MD)
            (fixture.path / "b.md").write_text(B_MD)
            subprocess.run(
                ["git", "add", "."], cwd=fixture.path, check=True, capture_output=True
            )
            subprocess.run(
                [
                    "git",
                    "-c",
                    "user.email=t@example.com",
                    "-c",
                    "user.name=t",
                    "commit",
                    "-m",
                    "S4323c8 fixture",
                    "-q",
                ],
                cwd=fixture.path,
                check=True,
                capture_output=True,
            )

            bid = fixture.primary_branch_id(timeout_s=10.0)
            repo_id = fixture.repo_id
            base = (
                f"http://localhost:{port}/{urllib.parse.quote(repo_id, safe='')}"
                f"/{urllib.parse.quote(bid, safe='')}/files"
            )
            a_url = f"{base}/a.md"

            with sync_playwright() as p:
                b = p.chromium.launch(headless=True)
                ctx = b.new_context(viewport={"width": 1280, "height": 800})
                ctx.add_init_script(
                    "window.sessionStorage.setItem('palmux:onboarding-skipped','1')"
                )
                page = ctx.new_page()

                # ---- AC-S4323c8-3-1 + AC-S4323c8-3-2: link nav pushes
                # history and back/forward moves between the two files.
                page.goto(a_url, wait_until="load", timeout=TO)
                page.wait_for_selector('[data-testid="markdown-view"]', timeout=TO)
                page.wait_for_function(
                    "document.querySelector('[data-testid=\"markdown-view\"] h1')"
                    "?.textContent.includes('File A')",
                    timeout=TO,
                )

                link = page.locator(
                    '[data-testid="markdown-view"] a[data-link-kind="relative"]'
                    '[href="./b.md"]'
                )
                if link.count() < 1:
                    print("FAIL: File B link not found in File A's markdown preview")
                    failed += 1
                else:
                    link.click()
                    page.wait_for_function(
                        "document.querySelector('[data-testid=\"markdown-view\"] h1')"
                        "?.textContent.includes('File B')",
                        timeout=TO,
                    )
                    if not page.url.endswith("/files/b.md"):
                        print(f"FAIL: AC-1: URL did not update to b.md: {page.url!r}")
                        failed += 1
                    else:
                        print("PASS: AC-S4323c8-3-1 — link click pushed URL to b.md")

                    page.go_back()
                    page.wait_for_function(
                        "document.querySelector('[data-testid=\"markdown-view\"] h1')"
                        "?.textContent.includes('File A')",
                        timeout=TO,
                    )
                    if not page.url.endswith("/files/a.md"):
                        print(f"FAIL: AC-2: go_back did not restore a.md: {page.url!r}")
                        failed += 1
                    else:
                        print("PASS: AC-S4323c8-3-2 — go_back restored a.md")

                    page.go_forward()
                    page.wait_for_function(
                        "document.querySelector('[data-testid=\"markdown-view\"] h1')"
                        "?.textContent.includes('File B')",
                        timeout=TO,
                    )
                    if not page.url.endswith("/files/b.md"):
                        print(
                            f"FAIL: AC-2: go_forward did not restore b.md: {page.url!r}"
                        )
                        failed += 1
                    else:
                        print("PASS: AC-S4323c8-3-2 — go_forward restored b.md")

                # ---- AC-S4323c8-3-3: list-click navigation still pushes
                # history (no regression). Go to the worktree root, click
                # a.md in the file list, then b.md, then verify back/
                # forward still works via the list-click path too.
                page.goto(base, wait_until="load", timeout=TO)
                page.wait_for_selector('[data-testid="files-list"]', timeout=TO)
                a_row = page.locator('[data-testid="files-list"] button[title="a.md"]')
                if a_row.count() < 1:
                    print("FAIL: a.md not present in file list")
                    failed += 1
                else:
                    a_row.click()
                    page.wait_for_function(
                        "location.pathname.endsWith('/files/a.md')", timeout=TO
                    )
                    b_row = page.locator('[data-testid="files-list"] button[title="b.md"]')
                    b_row.click()
                    page.wait_for_function(
                        "location.pathname.endsWith('/files/b.md')", timeout=TO
                    )
                    page.go_back()
                    page.wait_for_function(
                        "location.pathname.endsWith('/files/a.md')", timeout=TO
                    )
                    print(
                        "PASS: AC-S4323c8-3-3 — list-click navigation still "
                        "pushes history (back restored a.md)"
                    )

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

    print(f"\ns4323c8_files_history: {'ALL PASS' if failed == 0 else 'FAILED'}")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
