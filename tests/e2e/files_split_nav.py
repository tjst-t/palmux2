#!/usr/bin/env python3
"""Split-view Files link navigation (BL-files-split-nav).

When the split right panel holds a DIFFERENT workspace than the main route,
that panel is "local" (isUrlPanel=false). Clicking a Markdown internal link
inside it must navigate the RIGHT panel's own local selection — NOT hijack
the main route (which would yank the LEFT panel to the linked file and leave
the right panel unchanged; the pre-fix bug).

Setup: repo with two open workspaces A (primary) + B. Main route = A's Files
(left panel). Right panel = B's Files via `?right=B/files`. B ≠ A, so the
right panel is local. We open a.md in the right panel and click its link to
b.md; the main route must stay on A, and the right panel must show B's b.md.

Exit 0 = PASS / SKIP. Run: python3 tests/e2e/files_split_nav.py
"""
from __future__ import annotations
import json, os, signal, socket, subprocess, sys, time, urllib.parse, urllib.request
from pathlib import Path

sys.path.insert(0, os.path.dirname(__file__))
REPO = Path(__file__).resolve().parents[2]
BIN = REPO / "bin" / "palmux"
TO = 20_000


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0)); return s.getsockname()[1]


def main() -> None:
    print("files_split_nav — link in a local (different-workspace) right panel navigates that panel, not the main route")
    if not BIN.is_file():
        print("SKIP: no prebuilt binary (make build)"); sys.exit(0)
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("SKIP: playwright not installed"); sys.exit(0)

    port = free_port(); os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
    base = f"http://127.0.0.1:{port}"
    cfg = Path(f"/tmp/palmux2-splitnav-{port}"); cfg.mkdir(parents=True, exist_ok=True)
    proc = subprocess.Popen(
        [str(BIN), "--addr", f"127.0.0.1:{port}", "--config-dir", str(cfg),
         "--claude-bin", "/bin/cat", "--tmux-prefix", f"_pmx_sn{port}_"],
        cwd=REPO, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    dl = time.time() + 30
    while time.time() < dl:
        line = proc.stdout.readline() if proc.stdout else ""
        if f":{port}" in line or "listening" in line:
            break
        if proc.poll() is not None:
            print("FAIL: server died"); sys.exit(1)

    def api(method: str, path: str, body: dict | None = None) -> tuple[int, object]:
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(base + path, data=data, method=method,
                                     headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=10) as r:
                raw = r.read().decode()
                return r.status, (json.loads(raw) if raw else None)
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()

    failed = 0
    try:
        import _fixture as fx
        with fx.palmux2_test_fixture("splitnav") as fixture:
            wt = Path(fixture.path)
            (wt / "a.md").write_text("# Doc A\n\n[go to B](b.md)\n")
            (wt / "b.md").write_text("# Doc B\n\nThis is B.\n")
            subprocess.run(["git", "add", "."], cwd=wt, check=True, capture_output=True)
            subprocess.run(["git", "-c", "user.email=t@e.st", "-c", "user.name=t",
                            "commit", "-m", "seed"], cwd=wt, check=True, capture_output=True)
            aid = fixture.primary_branch_id(timeout_s=10.0)
            rid_raw = fixture.repo_id

            # Create workspace B (gwq add -b) as a second open worktree of the same repo.
            code, created = api("POST", f"/api/repos/{urllib.parse.quote(rid_raw)}/branches/open",
                                {"branchName": "splitnav-b"})
            if code not in (200, 201) or not isinstance(created, dict):
                print(f"FAIL: could not create workspace B: {code} {created}"); sys.exit(1)
            bid = created["id"]

            rid = urllib.parse.quote(rid_raw, safe="")
            a = urllib.parse.quote(aid, safe="")
            b = urllib.parse.quote(bid, safe="")
            # Left/main route = A's Files (root). Right panel = B's Files (different
            # workspace → local panel). ?right= is repoId/branchId/tabId, '/'-joined.
            main_url = f"{base}/{rid}/{a}/files?right={rid}/{b}/files"
            with sync_playwright() as p:
                br = p.chromium.launch(headless=True)
                ctx = br.new_context(viewport={"width": 1400, "height": 900})
                ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1');"
                                    "window.localStorage.setItem('palmux:splitEnabled','true')")
                pg = ctx.new_page()
                pg.goto(main_url, wait_until="load", timeout=TO)
                pg.wait_for_function("document.getElementById('root').innerHTML.length > 100", timeout=TO)
                right = pg.locator("[aria-label='Right panel']")
                right.wait_for(state="visible", timeout=TO)
                right.locator("[data-testid='files-list']").wait_for(timeout=TO)
                url_before = pg.url  # main route (left panel)

                # In the RIGHT panel Files list, open a.md, then click its link to b.md.
                right.locator("[data-testid='files-list']").get_by_text("a.md").first.click(timeout=TO)
                right.get_by_role("link", name="go to B").first.wait_for(timeout=TO)
                right.get_by_role("link", name="go to B").first.click(timeout=TO)

                # The right panel must now show B; the main route path must be unchanged.
                right.get_by_text("This is B.", exact=False).first.wait_for(timeout=TO)
                path_before = urllib.parse.urlsplit(url_before).path
                path_after = urllib.parse.urlsplit(pg.url).path
                if path_after != path_before:
                    print(f"FAIL: [BL-files-split-nav] main route path changed on right-panel link click:\n"
                          f"  before={path_before}\n  after ={path_after}")
                    failed += 1
                else:
                    print("PASS: [BL-files-split-nav] local right-panel link navigated the right panel; main route path unchanged")
                br.close()
    finally:
        if proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try: proc.wait(timeout=8)
            except subprocess.TimeoutExpired: proc.kill()
        import shutil; shutil.rmtree(cfg, ignore_errors=True)

    print(f"\nfiles_split_nav: {'ALL PASS' if failed == 0 else 'FAILED'}")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
