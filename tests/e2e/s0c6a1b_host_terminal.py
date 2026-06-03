#!/usr/bin/env python3
"""Sprint S0c6a1b — リポジトリ非依存「Host」ターミナル E2E.

予約 Host スコープ (repoId=host--0000 / branchId=host) と、その Drawer 専用
セクション・empty-state CTA を検証する。

Acceptance criteria verified here:

  [AC-S0c6a1b-1-1]  GET host tabs が既定 Bash タブを返す。tmux セッションは lazy
                    (attach 前には存在しない)
  [AC-S0c6a1b-1-2]  WS attach で _..._host--0000_host セッションが lazy 生成され、
                    cwd=$HOME で pty が動く。2 つ目の Bash も作れる
  [AC-S0c6a1b-1-3]  生成済み Host セッションは GET /api/orphan-sessions に出ず、
                    sync 周期を跨いでも kill されず再 attach できる
  [AC-S0c6a1b-1-4]  Host scope は GET /api/repos / /api/repos/available に出ない

  [AC-S0c6a1b-2-1]  Drawer に Host 専用セクション (Repositories/Orphans と別枠) が
                    あり、クリックで /host--0000/host/bash:bash に遷移しターミナルが
                    描画される。Files/Git/Sprint/Claude 行は無い
  [AC-S0c6a1b-2-2]  reload・戻る/進むで Host ターミナル選択が保持される
  [AC-S0c6a1b-2-3]  Host セクションから Bash を追加/削除でき列挙される

  (mobile)          viewport<600px でも Host セクションに到達できる (priority_rule 10)

AC-S0c6a1b-3-* (empty-state CTA の repo-count トグル) は repo 件数を制御する必要が
あるため hermetic な tests/e2e/s0c6a1b_host_terminal_mock.py 側で検証する。

Runs against: make serve INSTANCE=dev (palmux2 dev instance, default port 8215).
dev instance は --tmux-prefix=_pmx_dev_ で起動するため、生 tmux セッション名の
判定は接頭辞非依存に "host--0000_host" 接尾辞でマッチする。

Exit 0 = PASS, else FAIL (prints failing AC to stderr).
"""
from __future__ import annotations

import asyncio
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8215"
)
BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 20.0
PLAYWRIGHT_TIMEOUT = 15_000  # ms

HOST_REPO = "host--0000"
HOST_BRANCH = "host"
HOST_SESSION_SUFFIX = "host--0000_host"  # prefix-agnostic match (_palmux_ or _pmx_dev_)
DEFAULT_BASH_TAB = "bash:bash"

_FAILED: list[str] = []


# ─── Helpers ────────────────────────────────────────────────────────────────

def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def http(method: str, path: str, *, body: bytes | None = None) -> tuple[int, bytes]:
    req = urllib.request.Request(f"{BASE_URL}{path}", method=method, data=body)
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def http_json(method: str, path: str, *, body: dict | list | None = None) -> tuple[int, object]:
    raw = json.dumps(body).encode() if body is not None else None
    headers = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(f"{BASE_URL}{path}", method=method, data=raw, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            data = resp.read()
            try:
                return resp.status, json.loads(data.decode() or "null")
            except json.JSONDecodeError:
                return resp.status, data.decode(errors="replace")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "null")
        except json.JSONDecodeError:
            return e.code, ""


def host_tabs() -> list:
    code, body = http_json(
        "GET", f"/api/repos/{HOST_REPO}/branches/{HOST_BRANCH}/tabs"
    )
    if code != 200:
        return []
    if isinstance(body, dict):
        return body.get("tabs") or []
    return body if isinstance(body, list) else []


def tmux_sessions() -> list[str]:
    """Raw tmux session names visible to the current user (prefix-agnostic)."""
    try:
        out = subprocess.run(
            ["tmux", "ls", "-F", "#{session_name}"],
            capture_output=True, text=True, timeout=10,
        )
    except (subprocess.SubprocessError, FileNotFoundError):
        return []
    return [ln.strip() for ln in out.stdout.splitlines() if ln.strip()]


def host_session_present() -> bool:
    return any(s.endswith(HOST_SESSION_SUFFIX) or HOST_SESSION_SUFFIX in s
               for s in tmux_sessions())


# ─── WS attach (asyncio websockets, same as s0fd64b/s7ce250) ─────────────────

async def _attach_and_echo(marker: str) -> str:
    import websockets
    uri = (
        f"ws://localhost:{PORT}/api/repos/{HOST_REPO}"
        f"/branches/{HOST_BRANCH}/tabs/{urllib.parse.quote(DEFAULT_BASH_TAB)}"
        f"/attach?cols=80&rows=24"
    )
    collected = bytearray()
    async with websockets.connect(uri, max_size=None) as ws:
        await ws.send(json.dumps({"type": "resize", "cols": 80, "rows": 24}))
        await asyncio.sleep(0.3)
        await ws.send(json.dumps({"type": "input", "data": f"echo {marker}=$PWD\n"}))
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            try:
                frame = await asyncio.wait_for(ws.recv(), timeout=1.0)
            except asyncio.TimeoutError:
                continue
            collected += frame if isinstance(frame, (bytes, bytearray)) else frame.encode()
            if marker.encode() in collected and b"=/" in collected:
                break
    return collected.decode(errors="replace")


def attach_and_echo(marker: str) -> str:
    return asyncio.run(_attach_and_echo(marker))


# ─── Playwright helpers ──────────────────────────────────────────────────────

def get_playwright():
    try:
        from playwright.sync_api import sync_playwright
        return sync_playwright
    except ImportError:
        print("SKIP: playwright not installed "
              "(pip install playwright && playwright install chromium)")
        sys.exit(0)


# ─── HTTP / WS / tmux ACs (Story 1) ──────────────────────────────────────────

def story1_api() -> None:
    home = os.path.expanduser("~")

    # Best-effort clean slate so lazy-spawn is observable.
    for s in tmux_sessions():
        if HOST_SESSION_SUFFIX in s:
            subprocess.run(["tmux", "kill-session", "-t", s],
                           capture_output=True, text=True)

    # [AC-S0c6a1b-1-1] default bash tab present; session NOT created yet.
    tabs = host_tabs()
    ids = [t.get("id") for t in tabs]
    types = [t.get("type") for t in tabs]
    if any(t == "bash" for t in types) and DEFAULT_BASH_TAB in ids:
        if not host_session_present():
            ok("AC-S0c6a1b-1-1", f"default bash tab present, lazy (no tmux session) tabs={ids}")
        else:
            fail("AC-S0c6a1b-1-1", "host tmux session exists before any attach (not lazy)")
    else:
        fail("AC-S0c6a1b-1-1", f"no default bash tab in host tabs: {ids} / types={types}")

    # also: no Files/Git/Sprint/Claude in host scope
    forbidden = {"files", "git", "sprint", "claude"}
    if forbidden & set(types):
        fail("AC-S0c6a1b-1-1", f"host scope exposes non-bash tabs: {types}")

    # [AC-S0c6a1b-1-2] lazy spawn on attach + cwd=$HOME
    try:
        out = attach_and_echo("HOST_PWD")
    except Exception as e:  # noqa: BLE001
        out = ""
        fail("AC-S0c6a1b-1-2", f"WS attach raised: {e!r}")
    if f"HOST_PWD={home}" in out or f"HOST_PWD={home}\r" in out:
        if host_session_present():
            ok("AC-S0c6a1b-1-2", f"attach created session; cwd={home}")
        else:
            fail("AC-S0c6a1b-1-2", "echo showed $HOME but no host tmux session found")
    elif out:
        fail("AC-S0c6a1b-1-2", f"cwd not $HOME ({home}); got: {out[-200:]!r}")

    # add a 2nd bash tab (no name → server auto-picks bash-2)
    code, body = http_json(
        "POST", f"/api/repos/{HOST_REPO}/branches/{HOST_BRANCH}/tabs",
        body={"type": "bash"},
    )
    if code in (200, 201):
        time.sleep(0.5)
        ids2 = [t.get("id") for t in host_tabs()]
        bash_ids = [i for i in ids2 if str(i).startswith("bash:")]
        if len(bash_ids) >= 2:
            ok("AC-S0c6a1b-1-2", f"multiple bash tabs: {bash_ids}")
            # cleanup the extra
            extra = [i for i in bash_ids if i != DEFAULT_BASH_TAB]
            for i in extra:
                http("DELETE", f"/api/repos/{HOST_REPO}/branches/{HOST_BRANCH}"
                               f"/tabs/{urllib.parse.quote(i)}")
        else:
            fail("AC-S0c6a1b-1-2", f"second bash tab not listed: {ids2}")
    else:
        fail("AC-S0c6a1b-1-2", f"POST bash tab failed: {code} {body!r}")

    # [AC-S0c6a1b-1-3] orphan exclusion + survival across sync
    code, orphans = http_json("GET", "/api/orphan-sessions")
    names = []
    if isinstance(orphans, list):
        names = [o.get("name", "") for o in orphans]
    if any(HOST_SESSION_SUFFIX in n for n in names):
        fail("AC-S0c6a1b-1-3", f"host session leaked into orphans: {names}")
    else:
        ok("AC-S0c6a1b-1-3", "host session absent from orphan list")
    # survive sync (~5s) then reconnect
    if host_session_present():
        time.sleep(7)
        if host_session_present():
            try:
                out2 = attach_and_echo("HOST_PWD2")
                if "HOST_PWD2=" in out2:
                    ok("AC-S0c6a1b-1-3", "session survived sync window + re-attach OK")
                else:
                    fail("AC-S0c6a1b-1-3", "re-attach produced no echo output")
            except Exception as e:  # noqa: BLE001
                fail("AC-S0c6a1b-1-3", f"re-attach raised: {e!r}")
        else:
            fail("AC-S0c6a1b-1-3", "host session was killed within sync window")

    # [AC-S0c6a1b-1-4] excluded from repo list + available
    code, repos = http_json("GET", "/api/repos")
    repo_ids = [r.get("id") for r in repos] if isinstance(repos, list) else []
    code2, avail = http_json("GET", "/api/repos/available")
    avail_ids = [r.get("id") for r in avail] if isinstance(avail, list) else []
    if HOST_REPO in repo_ids:
        fail("AC-S0c6a1b-1-4", f"host--0000 leaked into GET /api/repos: {repo_ids}")
    elif HOST_REPO in avail_ids:
        fail("AC-S0c6a1b-1-4", f"host--0000 leaked into /api/repos/available")
    else:
        ok("AC-S0c6a1b-1-4", "host scope absent from repo list + available")


# ─── Browser ACs (Story 2) ───────────────────────────────────────────────────

def story2_gui(page) -> None:
    # [AC-S0c6a1b-2-1] dedicated Host section, separate from repos/orphans
    page.goto(BASE_URL + "/")
    page.wait_for_selector("body", timeout=PLAYWRIGHT_TIMEOUT)
    section = page.locator("[data-testid='drawer-host-section']")
    if section.count() == 0:
        # mobile-collapsed drawer: try opening it (best-effort)
        toggle = page.locator("[data-testid='drawer-toggle'], [aria-label*='drawer' i]")
        if toggle.count() > 0:
            toggle.first.click()
    try:
        page.wait_for_selector("[data-testid='drawer-host-section']", timeout=PLAYWRIGHT_TIMEOUT)
        ok("AC-S0c6a1b-2-1", "drawer-host-section present")
    except Exception:  # noqa: BLE001
        fail("AC-S0c6a1b-2-1", "drawer-host-section not found")
        return

    page.locator("[data-testid='drawer-host-terminal']").first.click()
    try:
        page.wait_for_url(lambda u: "host--0000/host/bash" in u, timeout=PLAYWRIGHT_TIMEOUT)
        page.wait_for_selector("[data-testid='terminal-view'], .xterm", timeout=PLAYWRIGHT_TIMEOUT)
        ok("AC-S0c6a1b-2-1", "click navigates to host terminal + xterm renders")
    except Exception:  # noqa: BLE001
        fail("AC-S0c6a1b-2-1", f"host terminal did not open; url={page.url}")

    # Drawer Host section is a SINGLE "Host" entry (refine 2026-06-03):
    # no per-terminal rows and no "+" button — terminals are managed in the
    # top TabBar. And the TabBar must be bash-only (no claude/files/git tab).
    if page.locator("[data-testid='drawer-host-add-btn']").count() != 0:
        fail("AC-S0c6a1b-2-1", "drawer-host-add-btn should be gone (TabBar owns add)")
    elif page.locator("[data-testid^='drawer-host-term-']").count() != 0:
        fail("AC-S0c6a1b-2-1", "Host section should not list per-terminal rows")
    elif page.locator("[data-testid='claude-tab']").count() != 0:
        fail("AC-S0c6a1b-2-1", "host scope TabBar must not contain a Claude tab")
    elif page.locator("[data-testid='tab-bash:bash']").count() == 0:
        fail("AC-S0c6a1b-2-1", "host scope TabBar missing the bash tab")
    else:
        ok("AC-S0c6a1b-2-1", "Host drawer = single entry; TabBar bash-only")

    # [AC-S0c6a1b-2-2] reload + back preserve selection
    page.reload()
    try:
        page.wait_for_url(lambda u: "host--0000/host/bash" in u, timeout=PLAYWRIGHT_TIMEOUT)
        page.wait_for_selector("[data-testid='terminal-view'], .xterm", timeout=PLAYWRIGHT_TIMEOUT)
        ok("AC-S0c6a1b-2-2", "reload preserved host terminal")
    except Exception:  # noqa: BLE001
        fail("AC-S0c6a1b-2-2", f"reload lost host terminal; url={page.url}")
    page.goto(BASE_URL + "/")
    page.wait_for_selector("body", timeout=PLAYWRIGHT_TIMEOUT)
    page.go_back()
    try:
        page.wait_for_url(lambda u: "host--0000/host/bash" in u, timeout=PLAYWRIGHT_TIMEOUT)
        ok("AC-S0c6a1b-2-2", "browser back restored host terminal URL")
    except Exception:  # noqa: BLE001
        fail("AC-S0c6a1b-2-2", f"back did not restore host url; url={page.url}")

    # [AC-S0c6a1b-2-3] multiple bash terminals managed via the top TabBar
    # (refine 2026-06-03: drawer no longer adds/removes). Add via the TabBar
    # per-type "+" (tab-add-bash); verify the new tab appears in the bar and
    # in GET host tabs; then remove via API and confirm it leaves the bar.
    page.goto(BASE_URL + f"/{HOST_REPO}/{HOST_BRANCH}/bash:bash")
    page.wait_for_selector("[data-testid='tab-bash:bash']", timeout=PLAYWRIGHT_TIMEOUT)
    add = page.locator("[data-testid='tab-add-bash']")
    if add.count() == 0:
        fail("AC-S0c6a1b-2-3", "tab-add-bash (TabBar +) not found")
    else:
        add.first.click()
        try:
            page.wait_for_selector("[data-testid='tab-bash:bash-2']", timeout=PLAYWRIGHT_TIMEOUT)
            ids = [t.get("id") for t in host_tabs()]
            if "bash:bash-2" in ids:
                ok("AC-S0c6a1b-2-3", f"TabBar + added host bash-2 (tabs={ids})")
            else:
                fail("AC-S0c6a1b-2-3", f"bash-2 tab in bar but not in host tabs: {ids}")
        except Exception:  # noqa: BLE001
            fail("AC-S0c6a1b-2-3", "second bash tab did not appear in TabBar")
        # Remove via API and confirm the tab leaves the bar.
        http("DELETE", f"/api/repos/{HOST_REPO}/branches/{HOST_BRANCH}"
                       f"/tabs/{urllib.parse.quote('bash:bash-2')}")
        try:
            page.wait_for_selector("[data-testid='tab-bash:bash-2']",
                                   state="detached", timeout=PLAYWRIGHT_TIMEOUT)
            ok("AC-S0c6a1b-2-3", "removed bash-2 reflected in TabBar")
        except Exception:  # noqa: BLE001
            fail("AC-S0c6a1b-2-3", "bash-2 tab did not disappear from TabBar after delete")


def story2_mobile(page) -> None:
    page.set_viewport_size({"width": 390, "height": 780})
    page.goto(BASE_URL + "/")
    page.wait_for_selector("body", timeout=PLAYWRIGHT_TIMEOUT)
    # Open the mobile drawer via the header hamburger (aria-label="Toggle drawer").
    toggle = page.locator("[aria-label='Toggle drawer']")
    try:
        toggle.first.click(timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:  # noqa: BLE001
        fail("AC-S0c6a1b-2-1", "mobile: drawer toggle not found")
        return
    try:
        page.wait_for_selector("[data-testid='drawer-host-section']", timeout=PLAYWRIGHT_TIMEOUT)
        page.locator("[data-testid='drawer-host-terminal']").first.click()
        page.wait_for_url(lambda u: "host--0000/host/bash" in u, timeout=PLAYWRIGHT_TIMEOUT)
        ok("AC-S0c6a1b-2-1", "mobile: host section reachable + terminal opens (priority_rule 10)")
    except Exception:  # noqa: BLE001
        fail("AC-S0c6a1b-2-1", "mobile: host section/terminal not reachable")


# ─── main ────────────────────────────────────────────────────────────────────

def main() -> int:
    # Liveness guard.
    try:
        code, _ = http("GET", "/api/repos")
    except urllib.error.URLError as e:
        print(f"FAIL: dev instance not reachable at {BASE_URL}: {e}", file=sys.stderr)
        print("  start it with: make serve INSTANCE=dev", file=sys.stderr)
        return 1

    story1_api()

    sync_playwright = get_playwright()
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            ctx = browser.new_context(viewport={"width": 1280, "height": 800})
            page = ctx.new_page()
            story2_gui(page)
            ctx.close()
            mctx = browser.new_context()
            mpage = mctx.new_page()
            story2_mobile(mpage)
            mctx.close()
        finally:
            browser.close()

    if _FAILED:
        print(f"\nFAILED ACs: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
