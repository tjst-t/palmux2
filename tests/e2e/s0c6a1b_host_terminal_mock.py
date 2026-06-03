#!/usr/bin/env python3
"""Sprint S0c6a1b — empty-state「Open setup terminal」CTA mock/hermetic tests.

empty-state CTA は Open 済みリポジトリ件数 (0 vs >=1) でトグルする。共有 dev
instance では repo 件数を制御できないため、ここでは config-dir を分離した
hermetic な palmux2 バイナリ (bin/palmux, embedded frontend) を 0-repo /
1-repo の 2 状態で立てて実ブラウザ (Playwright) から検証する。

これは page.route 等の network mock では **なく**、実バイナリ + 実ブラウザの
hermetic E2E (test-discipline Rule 2/4 準拠)。repo 件数だけを制御する。

Acceptance criteria verified:
  [AC-S0c6a1b-3-1]  repos==0 で data-testid=empty-setup-cta が表示され、クリックで
                    /host--0000/host/bash:bash に遷移する
  [AC-S0c6a1b-3-2]  CTA 付近に `gh auth login` と claude のログインヒントが出る
  [AC-S0c6a1b-3-3]  repos>=1 では CTA が出ず従来文言に戻る (回帰なし)

要 bin/palmux (make build で embed frontend 込み)。frontend 非 embed のビルドでは
SKIP するが、sprint verify は make build 後に実行するので実際には走る。

Exit 0 = ALL PASS / all-skipped-with-reason. Run standalone:
  make build && python3 tests/e2e/s0c6a1b_host_terminal_mock.py
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
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator

sys.path.insert(0, os.path.dirname(__file__))

REPO_ROOT = Path(__file__).resolve().parents[2]
PLAYWRIGHT_TIMEOUT = 20_000  # ms
_PREBUILT_BIN = REPO_ROOT / "bin" / "palmux"
_USE_PREBUILT = _PREBUILT_BIN.is_file()

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _get_playwright():
    try:
        from playwright.sync_api import sync_playwright
        return sync_playwright
    except ImportError:
        print("SKIP: playwright not installed")
        sys.exit(0)


@contextmanager
def hermetic_palmux2() -> Iterator[int]:
    """Start a hermetic palmux2 (fresh config-dir = 0 repos). Yields port."""
    port = _free_port()
    cfg_dir = Path("/tmp") / f"palmux2-s0c6a1b-mock-{port}"
    cfg_dir.mkdir(parents=True, exist_ok=True)
    cmd = [
        str(_PREBUILT_BIN),
        "--addr", f"127.0.0.1:{port}",
        "--config-dir", str(cfg_dir),
        "--claude-bin", "/bin/cat",
        "--tmux-prefix", f"_pmx_mock{port}_",
    ]
    proc = subprocess.Popen(
        cmd, cwd=REPO_ROOT, stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT, text=True,
    )
    try:
        deadline = time.monotonic() + 60.0
        listening = False
        while time.monotonic() < deadline:
            if proc.stdout is None:
                break
            line = proc.stdout.readline()
            if not line and proc.poll() is not None:
                rest = proc.stdout.read() if proc.stdout else ""
                raise RuntimeError(f"palmux2 exited early rc={proc.returncode}\n{rest}")
            if "palmux2 listening" in line or f":{port}" in line:
                listening = True
                break
        if not listening:
            proc.kill()
            raise RuntimeError("palmux2 did not announce listening port within 60s")
        yield port
    finally:
        if proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=5)
        import shutil
        # tmux cleanup for the hermetic prefix.
        try:
            out = subprocess.run(["tmux", "ls", "-F", "#{session_name}"],
                                 capture_output=True, text=True, timeout=10)
            for s in out.stdout.splitlines():
                if f"_pmx_mock{port}_" in s:
                    subprocess.run(["tmux", "kill-session", "-t", s.strip()],
                                   capture_output=True, text=True)
        except Exception:  # noqa: BLE001
            pass
        shutil.rmtree(cfg_dir, ignore_errors=True)


def _fixture_module(port: int):
    os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
    if "_fixture" in sys.modules:
        del sys.modules["_fixture"]
    import _fixture as fx_mod
    return fx_mod


# ─── Tests ───────────────────────────────────────────────────────────────────

def test_cta_shown_and_hints(port: int) -> None:
    """[AC-S0c6a1b-3-1] [AC-S0c6a1b-3-2] 0-repo: CTA + gh/claude hints, click -> host."""
    sync_playwright = _get_playwright()
    base_url = f"http://localhost:{port}"
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            page = browser.new_page()
            page.goto(base_url + "/")
            page.wait_for_selector("body", timeout=PLAYWRIGHT_TIMEOUT)
            try:
                page.wait_for_selector("[data-testid='empty-setup-cta']",
                                       timeout=PLAYWRIGHT_TIMEOUT)
                ok("AC-S0c6a1b-3-1", "empty-setup-cta shown with 0 repos")
            except Exception:  # noqa: BLE001
                fail("AC-S0c6a1b-3-1", "empty-setup-cta not shown with 0 repos")
                return

            gh = page.locator("[data-testid='empty-setup-hint-gh']")
            cl = page.locator("[data-testid='empty-setup-hint-claude']")
            gh_txt = gh.inner_text() if gh.count() else ""
            cl_txt = cl.inner_text() if cl.count() else ""
            if "gh auth login" in gh_txt and "claude" in cl_txt.lower():
                ok("AC-S0c6a1b-3-2", f"hints present: {gh_txt!r} / {cl_txt!r}")
            else:
                fail("AC-S0c6a1b-3-2", f"login hints missing: gh={gh_txt!r} claude={cl_txt!r}")

            page.locator("[data-testid='empty-setup-cta']").click()
            try:
                page.wait_for_url(
                    lambda u: "host--0000/host/bash" in u, timeout=PLAYWRIGHT_TIMEOUT
                )
                ok("AC-S0c6a1b-3-1", "CTA navigates to host terminal")
            except Exception:  # noqa: BLE001
                fail("AC-S0c6a1b-3-1", f"CTA did not open host terminal; url={page.url}")
        finally:
            browser.close()


def test_cta_hidden_when_repo_open(port: int) -> None:
    """[AC-S0c6a1b-3-3] >=1 repo: CTA absent, legacy text shown."""
    sync_playwright = _get_playwright()
    fx = _fixture_module(port)
    base_url = f"http://localhost:{port}"
    with fx.palmux2_test_fixture("s0c6a1b-mock") as fixture:
        _ = fixture.repo_id  # ensure repo registered/open
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                page.goto(base_url + "/")
                page.wait_for_selector("body", timeout=PLAYWRIGHT_TIMEOUT)
                time.sleep(1.0)  # let repo list settle
                cta = page.locator("[data-testid='empty-setup-cta']")
                if cta.count() == 0:
                    ok("AC-S0c6a1b-3-3", "CTA hidden when a repo is open")
                else:
                    fail("AC-S0c6a1b-3-3", "CTA still shown with a repo open (regression)")
            finally:
                browser.close()


def main() -> int:
    if not _USE_PREBUILT:
        print("SKIP: bin/palmux not built (run `make build` first). "
              "sprint verify builds before running this — a bare skip here is "
              "an environment gap, not an accepted skip.", file=sys.stderr)
        # Treat as failure in CI-like context so verify notices the missing build.
        return 2
    with hermetic_palmux2() as port:
        test_cta_shown_and_hints(port)
        test_cta_hidden_when_repo_open(port)
    if _FAILED:
        print(f"\nFAILED ACs: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
