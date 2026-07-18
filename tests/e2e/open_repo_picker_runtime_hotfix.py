#!/usr/bin/env python3
"""Hotfix E2E — Open Repository picker's runtime selector.

Real dogfooding on a freshly-updated local-source appliance (2026-07-17)
found two bugs in the "Open Repository" picker's runtime radio group
(RUNTIME — WHERE THIS WORKSPACE RUNS):

  1. Clicking either radio (host / incus-container) incorrectly popped up
     the "Change runtime to X? This workspace is currently open..."
     confirmation dialog — nonsensical here since the picker only ever
     lists repos that are NOT yet open. Root cause: RepoPicker's
     handleRuntimeChange gated on activeRepoId/activeBranchId, which were
     wired from the Drawer's *currently-viewed URL route* (whatever
     workspace happens to be open when the user clicks "Open
     Repository..."), not from whether the *picker's own target repo* was
     open (it never is, in this flow).
  2. Even when a runtime was selected (or the visible default,
     incus-container, was left untouched), the repo actually opened with
     "host" runtime regardless — the picker's local `runtimeKind` state
     was never sent anywhere; POST /api/repos/{id}/open takes no body and
     the backend falls through to its own host-runtime default.

Fix: the confirm-dialog branch was removed entirely from this component
(that flow correctly still exists on the Header's runtime chip, which owns
its own confirm dialog for changing an *already-running* workspace's
runtime). After a successful open, the picker now resolves the same
"incus if available, else host" default RuntimeSelector shows visually and
explicitly PATCHes the newly-opened primary branch's runtime to match.

Acceptance:
  [AC-HOTFIX-1] selecting a runtime in the not-yet-open picker never shows
                the "Change runtime to X?" confirm dialog
  [AC-HOTFIX-2] explicitly selecting "host" and opening the repo results in
                the workspace actually running with runtime.kind=host
                (header chip reads "host")

Run standalone (dev instance must be up):
  make serve INSTANCE=dev
  python3 tests/e2e/open_repo_picker_runtime_hotfix.py

Exit code 0 = ALL PASS.
"""
from __future__ import annotations

import sys

from _fixture import BASE_URL, _run, _ghq_root

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def _get_playwright():
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("SKIP: playwright not installed", file=sys.stderr)
        sys.exit(0)
    return sync_playwright


def main() -> int:
    sync_playwright = _get_playwright()

    # A fresh, NOT-yet-open ghq-tracked repo — the exact scenario the bug
    # only reproduces in (RepoPicker's runtime radios only render for rows
    # under "not yet open"). Built manually (not via palmux2_test_fixture,
    # which auto-opens) so it starts genuinely closed.
    import time

    ts = int(time.time())
    name = f"open-repo-hotfix-{ts}-{__import__('os').getpid()}"
    ghq_path = f"github.com/palmux2-test/{name}"
    repo_dir = _ghq_root() / ghq_path
    repo_dir.mkdir(parents=True)
    _run(repo_dir, "git", "init", "-b", "main")
    _run(repo_dir, "git", "config", "user.email", "test@palmux2.test")
    _run(repo_dir, "git", "config", "user.name", "palmux2 test")
    (repo_dir / "README.md").write_text("hotfix fixture\n")
    _run(repo_dir, "git", "add", ".")
    _run(repo_dir, "git", "commit", "-m", "init")

    repo_id = None
    try:
        with sync_playwright() as p:
            browser = p.chromium.launch()
            ctx = browser.new_context()
            ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
            page = ctx.new_page()
            page.goto(BASE_URL + "/", wait_until="networkidle", timeout=15_000)
            page.wait_for_timeout(500)

            # Open the "Open Repository..." picker.
            page.locator("text=Open Repository…").first.click(timeout=10_000)
            page.wait_for_selector('[data-testid="open-repo-modal"]', timeout=10_000)

            # Filter down to just our fixture repo.
            page.locator('[data-testid="open-repo-input"]').fill(name)
            page.wait_for_timeout(300)

            row = page.locator(f"text={ghq_path}").first
            row.wait_for(timeout=10_000)

            # [AC-HOTFIX-1] Click BOTH runtime radios; neither should ever
            # surface the "already open, will restart" confirm dialog.
            page.locator('[data-testid="runtime-option-host"]').click()
            page.wait_for_timeout(200)
            if page.locator("text=This workspace is currently open").count() > 0:
                fail("AC-HOTFIX-1", "confirm dialog appeared after selecting host")
            else:
                ok("AC-HOTFIX-1", "no confirm dialog after selecting host")

            incus_radio = page.locator('[data-testid="runtime-option-incus-container"]')
            if incus_radio.count() > 0 and incus_radio.first.is_enabled():
                incus_radio.first.click()
                page.wait_for_timeout(200)
                if page.locator("text=This workspace is currently open").count() > 0:
                    fail("AC-HOTFIX-1", "confirm dialog appeared after selecting incus-container")
                else:
                    ok("AC-HOTFIX-1", "no confirm dialog after selecting incus-container")

            # Re-select host explicitly (deterministic regardless of Incus
            # availability on this dev host) and open.
            page.locator('[data-testid="runtime-option-host"]').click()
            page.wait_for_timeout(200)
            row.click()

            # [AC-HOTFIX-2] Should land on the freshly-opened workspace with
            # a runtime chip reading "host".
            page.wait_for_selector('[data-testid="runtime-chip"]', timeout=15_000)
            page.wait_for_timeout(500)  # let the post-open PATCH settle
            chip_text = page.locator('[data-testid="runtime-chip"]').first.inner_text()
            if "host" in chip_text.lower():
                ok("AC-HOTFIX-2", f"runtime chip reads {chip_text!r} (host, as selected)")
            else:
                fail("AC-HOTFIX-2", f"runtime chip reads {chip_text!r}, expected host")

            # Recover repo_id from the URL for cleanup.
            url = page.url
            repo_id = url.strip("/").split("/")[0] if url.strip("/") else None

            browser.close()
    finally:
        if repo_id:
            import urllib.request

            req = urllib.request.Request(
                f"{BASE_URL}/api/repos/{repo_id}/close", method="POST"
            )
            try:
                urllib.request.urlopen(req, timeout=10)
            except Exception:
                pass
        import shutil

        shutil.rmtree(repo_dir, ignore_errors=True)

    if _FAILED:
        print(f"\n{len(_FAILED)} FAILED: {', '.join(_FAILED)}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
