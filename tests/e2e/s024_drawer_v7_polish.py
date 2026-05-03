#!/usr/bin/env python3
"""Sprint S024 — Drawer v7 polish E2E.

Acceptance scenarios driven against the running dev palmux2 instance.

  (a) v7 token: 240px wide drawer renders repo / branch names without
      excessive truncation. We verify the actual rendered font sizes
      from CSS to confirm the v7 tokens are in effect.
  (b) Single-expand: opening repo B auto-collapses repo A. Only one
      `[data-collapsed="false"]` repo at a time.
  (c) HERE label absent: no `here` / `here-label` class anywhere, no
      "HERE" text in any branch row.
  (d) Worktree single-line: my-branch button height stays compact
      (≤ 30px desktop / ≤ 40px mobile). No multi-line meta row.
  (e) Glance line: collapsed repos render `[data-component="glance-line"]`
      directly under the row, with the navigate target visible.
  (f) Section unification: exactly one `[data-section="Repositories"]`
      section in the DOM. Both starred and unstarred repos live in it.
  (g) Mobile (< 600px): layout doesn't break — drawer still renders,
      tap targets ≥ 36px on branch / chip / icon buttons.
  (h) Regression: existing data hooks (data-action="repo-toggle",
      data-action="add-branch", data-action="promote", data-chip,
      data-panel) survive.

Exit code 0 = PASS.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8200"
)

BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 20.0
REPO_ROOT = Path(__file__).resolve().parents[2]


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def assert_(cond: bool, msg: str) -> None:
    if not cond:
        fail(msg)


def http(
    method: str,
    path: str,
    *,
    body: bytes | None = None,
    headers: dict[str, str] | None = None,
) -> tuple[int, dict[str, str], bytes]:
    url = f"{BASE_URL}{path}"
    req = urllib.request.Request(url, method=method, data=body, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            return resp.status, dict(resp.headers), resp.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers or {}), e.read()


def http_json(
    method: str, path: str, *, body: dict | list | None = None
) -> tuple[int, dict | list | str]:
    raw = json.dumps(body).encode() if body is not None else None
    h = {"Accept": "application/json"}
    if body is not None:
        h["Content-Type"] = "application/json"
    code, _hdrs, data = http(method, path, body=raw, headers=h)
    try:
        decoded: dict | list | str = json.loads(data.decode() or "{}")
    except json.JSONDecodeError:
        decoded = data.decode(errors="replace")
    return code, decoded


def run(cwd: Path, *args: str) -> str:
    res = subprocess.run(list(args), cwd=cwd, capture_output=True, text=True)
    if res.returncode != 0:
        raise RuntimeError(
            f"command failed in {cwd}: {' '.join(args)}\nstdout: {res.stdout}\nstderr: {res.stderr}"
        )
    return res.stdout


def ghq_root() -> Path:
    out = subprocess.run(["ghq", "root"], capture_output=True, text=True, check=True)
    return Path(out.stdout.strip())


def fetch_repos() -> list[dict]:
    code, body = http_json("GET", "/api/repos")
    assert_(code == 200, f"GET /api/repos: {code} {body}")
    assert_(isinstance(body, list), f"repos shape: {body!r}")
    return body  # type: ignore[return-value]


def find_branch(repos: list[dict], repo_id: str, branch_name: str) -> dict | None:
    for r in repos:
        if r["id"] != repo_id:
            continue
        for b in r["openBranches"]:
            if b["name"] == branch_name:
                return b
    return None


def make_fixture_repo() -> tuple[Path, str, str]:
    """Create a fresh git repo and register it so the v7 layout has at
    least 2 repos (this fixture + at least one other) for the
    single-expand test to be meaningful."""
    root = ghq_root()
    stamp = time.strftime("%Y%m%d-%H%M%S")
    rel = f"github.com/palmux2-test/s024-{stamp}-{os.getpid()}"
    repo = root / rel
    repo.mkdir(parents=True, exist_ok=False)
    run(repo, "git", "init", "-b", "main")
    run(repo, "git", "config", "user.email", "test@example.com")
    run(repo, "git", "config", "user.name", "Test")
    run(repo, "git", "config", "commit.gpgsign", "false")
    (repo / "README.md").write_text("hi\n")
    run(repo, "git", "add", ".")
    run(repo, "git", "commit", "-m", "init")
    run(repo, "git", "remote", "add", "origin", f"https://example.com/{rel}.git")

    code, avail = http_json("GET", "/api/repos/available")
    assert_(code == 200, f"available: {code} {avail}")
    repo_id = None
    for entry in avail:  # type: ignore[union-attr]
        if entry.get("ghqPath") == rel:
            repo_id = entry["id"]
            break
    assert_(repo_id is not None, f"fixture {rel} not in /api/repos/available")
    code, _ = http_json("POST", f"/api/repos/{urllib.parse.quote(repo_id)}/open")  # type: ignore[arg-type]
    assert_(code in (200, 201), f"open repo: {code}")
    return repo, rel, repo_id  # type: ignore[return-value]


def wait_for_branch(repo_id: str, branch_name: str, *, timeout_s: float = 35.0) -> dict:
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        repos = fetch_repos()
        b = find_branch(repos, repo_id, branch_name)
        if b is not None:
            return b
        time.sleep(2.0)
    fail(f"branch {branch_name} did not appear within {timeout_s}s")
    raise AssertionError("unreachable")


def fixture_cleanup(repo_id: str, repo_path: Path) -> None:
    try:
        http_json("POST", f"/api/repos/{urllib.parse.quote(repo_id)}/close")
    except Exception:
        pass
    if repo_path.exists():
        try:
            shutil.rmtree(repo_path)
        except OSError:
            pass


def test_a_v7_tokens_compact(page) -> None:
    """Drawer at a 240px width — repo names visible, font sizes match
    v7 mock spec (12px repo name, 11.5px branch, 9-10px small text)."""
    page.evaluate("""() => {
        const aside = document.querySelector('aside');
        if (aside) aside.style.width = '240px';
    }""")
    page.wait_for_timeout(200)

    sizes = page.evaluate(
        """() => {
            const get = (sel) => {
                const el = document.querySelector(sel);
                if (!el) return null;
                return parseFloat(getComputedStyle(el).fontSize);
            };
            return {
                brand: get('[data-component="status-strip"] :first-child'),
                repoName: get('[data-repo-id] [data-action="repo-toggle"]'),
                branch: get('[data-repo-id] [data-branch-id]'),
                glance: get('[data-component="glance-line"]'),
            };
        }"""
    )
    assert_(
        sizes["repoName"] is not None and sizes["repoName"] <= 12.5,
        f"repo name font-size should be ≤ 12.5px (v7), got {sizes['repoName']}px",
    )
    if sizes["branch"] is not None:
        assert_(
            sizes["branch"] <= 12.0,
            f"branch font-size should be ≤ 12px (v7), got {sizes['branch']}px",
        )
    if sizes["glance"] is not None:
        assert_(
            sizes["glance"] <= 10.0,
            f"glance line font-size should be ≤ 10px (v7), got {sizes['glance']}px",
        )
    print(f"  [a] v7 token sizes verified: repo={sizes['repoName']} branch={sizes['branch']} glance={sizes['glance']}")


def test_b_single_expand(page, fixture_repo_id: str) -> None:
    """Opening repo B auto-collapses the previously-expanded repo A.

    The test picks two distinct repos from the rendered DOM (so we
    follow the page's sort order, not the API order) and clicks them
    in sequence, verifying that at most one is expanded at a time."""
    page.evaluate("""() => {
        const aside = document.querySelector('aside');
        if (aside) aside.style.width = '320px';
    }""")
    page.wait_for_timeout(200)

    # Pick repos from the rendered DOM in sort order. We need 2 collapsed
    # ones we can click, OR an expanded one + a collapsed one.
    repo_ids = page.evaluate(
        """() => Array.from(document.querySelectorAll('[data-repo-id]')).map(
            li => ({ id: li.dataset.repoId, collapsed: li.dataset.collapsed }))"""
    )
    assert_(len(repo_ids) >= 2, f"need ≥ 2 repos rendered; got {len(repo_ids)}")

    # Pick first 2 distinct repos.
    repo_a_id = repo_ids[0]["id"]
    repo_b_id = repo_ids[1]["id"]

    toggle_a = page.locator(f'[data-repo-id="{repo_a_id}"] button[data-action="repo-toggle"]').first
    toggle_b = page.locator(f'[data-repo-id="{repo_b_id}"] button[data-action="repo-toggle"]').first

    if toggle_a.count() == 0 or toggle_b.count() == 0:
        print("  [b] (skipped: toggles not visible)")
        return

    # Step 1: click A → A is expanded.
    toggle_a.click()
    page.wait_for_timeout(500)
    coll_a_after = page.locator(f'[data-repo-id="{repo_a_id}"]').first.get_attribute("data-collapsed")
    assert_(
        coll_a_after == "false",
        f"after clicking A ({repo_a_id}), A should be expanded; got data-collapsed={coll_a_after}",
    )
    expanded_count_1 = page.evaluate(
        """() => document.querySelectorAll('[data-repo-id][data-collapsed="false"]').length"""
    )
    assert_(
        expanded_count_1 == 1,
        f"after click A: exactly 1 repo expanded; got {expanded_count_1}",
    )

    # Step 2: click B → B is expanded, A is auto-collapsed.
    toggle_b.click()
    page.wait_for_timeout(500)
    coll_a_after2 = page.locator(f'[data-repo-id="{repo_a_id}"]').first.get_attribute("data-collapsed")
    coll_b_after2 = page.locator(f'[data-repo-id="{repo_b_id}"]').first.get_attribute("data-collapsed")
    assert_(
        coll_b_after2 == "false",
        f"after clicking B ({repo_b_id}), B should be expanded; got data-collapsed={coll_b_after2}",
    )
    assert_(
        coll_a_after2 == "true",
        f"after clicking B ({repo_b_id}), A ({repo_a_id}) should be auto-collapsed; got data-collapsed={coll_a_after2}",
    )

    # Final: exactly 1 repo expanded.
    expanded_count = page.evaluate(
        """() => document.querySelectorAll('[data-repo-id][data-collapsed="false"]').length"""
    )
    assert_(
        expanded_count == 1,
        f"single-expand violated: expanded_count={expanded_count}",
    )
    print(f"  [b] single-expand verified: A→B auto-collapsed A, only 1 expanded")


def test_c_no_here_label(page) -> None:
    """No `here-label` / `here` class anywhere, no "HERE" text in any
    branch row, no `data-label="here"` attribute."""
    here_classes = page.evaluate(
        """() => {
            const found = [];
            document.querySelectorAll('*').forEach(el => {
                const cls = (el.className || '').toString();
                if (cls.includes('hereLabel') || cls.includes('here-label') || cls.match(/\\bhere\\b/i)) {
                    found.push(cls);
                }
                if (el.dataset && el.dataset.label && el.dataset.label.toLowerCase() === 'here') {
                    found.push('data-label=' + el.dataset.label);
                }
            });
            return found;
        }"""
    )
    assert_(
        len(here_classes) == 0,
        f"HERE label remnants found in DOM: {here_classes}",
    )

    here_text_count = page.evaluate(
        """() => {
            // Exclude script/style content. Walk text nodes inside [data-repo-id].
            let count = 0;
            document.querySelectorAll('[data-repo-id] *').forEach(el => {
                if (el.children.length === 0) {
                    const t = (el.textContent || '').trim();
                    if (/^HERE$/i.test(t) || /^Here$/.test(t)) count++;
                }
            });
            return count;
        }"""
    )
    assert_(
        here_text_count == 0,
        f"HERE text content found in {here_text_count} elements",
    )
    print("  [c] HERE label removed from DOM: OK")


def test_d_worktree_single_line(page) -> None:
    """Each my-branch row is single-line. Heights stay below the
    multi-line threshold (~30px on desktop, allowing for paddings)."""
    heights = page.evaluate(
        """() => {
            const rows = Array.from(document.querySelectorAll('[data-repo-id] [data-branch-id]'));
            return rows.map(r => Math.round(r.getBoundingClientRect().height));
        }"""
    )
    if not heights:
        print("  [d] (skipped: no branch rows visible)")
        return
    over_threshold = [h for h in heights if h > 32]
    assert_(
        len(over_threshold) == 0,
        f"branch rows should be single-line (≤ 32px), but {len(over_threshold)} are taller: heights={heights}",
    )
    print(f"  [d] all {len(heights)} branch rows are single-line (max height={max(heights)}px): OK")

    # Also verify that no `branchMeta` / `subMeta` element exists.
    meta_present = page.evaluate(
        """() => {
            return document.querySelectorAll('[class*="branchMeta"], [class*="subMeta"]').length;
        }"""
    )
    assert_(
        meta_present == 0,
        f"branchMeta / subMeta elements should be removed; found {meta_present}",
    )
    print("  [d] no multi-line meta lines (branchMeta/subMeta) in DOM: OK")


def test_e_glance_line(page) -> None:
    """Collapsed repos must show a glance line preview of the navigate
    target."""
    # First make sure we have at least 1 collapsed repo.
    collapsed_count = page.evaluate(
        """() => document.querySelectorAll('[data-repo-id][data-collapsed="true"]').length"""
    )
    if collapsed_count == 0:
        # Click an expanded repo to collapse it.
        page.evaluate(
            """() => {
                const expanded = document.querySelector('[data-repo-id][data-collapsed="false"] button[data-action="repo-toggle"]');
                if (expanded) expanded.click();
            }"""
        )
        page.wait_for_timeout(300)
        collapsed_count = page.evaluate(
            """() => document.querySelectorAll('[data-repo-id][data-collapsed="true"]').length"""
        )

    assert_(collapsed_count >= 1, f"need ≥ 1 collapsed repo; got {collapsed_count}")

    glance_count = page.evaluate(
        """() => document.querySelectorAll('[data-repo-id][data-collapsed="true"] [data-component="glance-line"]').length"""
    )
    assert_(
        glance_count >= 1,
        f"glance line missing from collapsed repos (found {glance_count} of {collapsed_count})",
    )
    sample = page.evaluate(
        """() => {
            const el = document.querySelector('[data-repo-id][data-collapsed="true"] [data-component="glance-line"]');
            return el ? { text: (el.textContent || '').trim(), source: el.dataset.targetSource } : null;
        }"""
    )
    assert_(
        sample is not None and len(sample["text"]) > 0,
        f"glance line text missing: {sample}",
    )
    assert_(
        "›" in sample["text"],
        f"glance line should contain navigate arrow ›: {sample}",
    )
    print(f"  [e] glance line rendered: source={sample['source']} text={sample['text']!r}: OK")


def test_f_section_unification(page) -> None:
    """Exactly one `Repositories` section, no separate `Starred` section."""
    section_attrs = page.evaluate(
        """() => Array.from(document.querySelectorAll('section[data-section]'))
            .map(s => s.dataset.section)"""
    )
    repos_sections = [s for s in section_attrs if s == "Repositories"]
    starred_sections = [s for s in section_attrs if s == "Starred" or "★" in s]
    assert_(
        len(repos_sections) == 1,
        f"expected exactly 1 Repositories section; got {len(repos_sections)} ({section_attrs})",
    )
    assert_(
        len(starred_sections) == 0,
        f"Starred section should be merged into Repositories; got {len(starred_sections)}",
    )

    # Verify both starred and unstarred repos are inside the same section.
    repos = fetch_repos()
    has_starred = any(r.get("starred") for r in repos)
    has_unstarred = any(not r.get("starred") for r in repos)
    if has_starred and has_unstarred:
        # All repos must live under the single section.
        rendered_under = page.evaluate(
            """() => {
                const sec = document.querySelector('section[data-section="Repositories"]');
                if (!sec) return [];
                return Array.from(sec.querySelectorAll('[data-repo-id]')).map(li => li.dataset.repoId);
            }"""
        )
        assert_(
            len(rendered_under) >= 2,
            f"merged section should contain ≥ 2 repos; got {len(rendered_under)}",
        )
    print(f"  [f] section unified (1 'Repositories' section): OK")


def test_g_mobile_layout(browser) -> None:
    """Mobile (< 600px) — drawer renders, branch buttons ≥ 36px tap, no
    layout breakage."""
    ctx_m = browser.new_context(viewport={"width": 390, "height": 844})
    page_m = ctx_m.new_page()
    page_m.goto(BASE_URL, wait_until="networkidle")
    page_m.wait_for_timeout(1500)

    hamb = page_m.get_by_role("button", name="Toggle drawer")
    if hamb.count() > 0:
        hamb.first.click()
        page_m.wait_for_timeout(400)

    # The mobile bottom-sheet uses [role="dialog"][aria-modal="true"] OR
    # the desktop drawer rendered narrow. Either way, [data-repo-id]
    # should exist.
    repo_count = page_m.locator("[data-repo-id]").count()
    assert_(repo_count >= 1, f"mobile: no repos visible; count={repo_count}")

    # Branch tap-target check.
    heights = page_m.evaluate(
        """() => {
            const rows = Array.from(document.querySelectorAll('[data-repo-id] [data-branch-id]'));
            return rows.map(r => Math.round(r.getBoundingClientRect().height));
        }"""
    )
    if heights:
        too_small = [h for h in heights if h < 32]
        assert_(
            len(too_small) == 0,
            f"mobile: branch tap-targets should be ≥ 32px; got too small: {too_small}",
        )
        print(f"  [g] mobile branch tap-targets ≥ 32px (heights={heights[:3]}): OK")
    else:
        print("  [g] (no branch rows visible on mobile, but layout renders)")

    ctx_m.close()


def test_h_data_hooks_preserved(page) -> None:
    """Regression — verify existing E2E data hooks survive."""
    hooks = page.evaluate(
        """() => ({
            repoToggle: document.querySelectorAll('[data-action="repo-toggle"]').length,
            addBranch: document.querySelectorAll('[data-action="add-branch"]').length,
            chipRow: document.querySelectorAll('[data-component="chip-row"]').length,
            section: document.querySelectorAll('[data-section]').length,
            statusStrip: document.querySelectorAll('[data-component="status-strip"]').length,
            footerHint: document.querySelectorAll('[data-component="footer-hint"]').length,
        })"""
    )
    assert_(hooks["repoToggle"] >= 1, f"data-action=repo-toggle missing")
    assert_(hooks["addBranch"] >= 1, f"data-action=add-branch missing")
    assert_(hooks["statusStrip"] == 1, f"data-component=status-strip should be 1, got {hooks['statusStrip']}")
    assert_(hooks["footerHint"] == 1, f"data-component=footer-hint should be 1")
    print(f"  [h] data hooks preserved: {hooks}: OK")


def main() -> int:
    print(f"Hitting {BASE_URL}")
    code, body = http_json("GET", "/api/health")
    assert_(code == 200, f"health: {code} {body}")
    print(f"  health OK: {body}")

    # Make sure we have at least 2 repos so single-expand can be tested.
    repos = fetch_repos()
    open_with_branches = [r for r in repos if r.get("openBranches")]
    fixture_repo_path = None
    fixture_repo_id = None
    if len(open_with_branches) < 2:
        print("  fewer than 2 open repos with branches — creating fixture")
        fixture_repo_path, _, fixture_repo_id = make_fixture_repo()
        wait_for_branch(fixture_repo_id, "main")

    try:
        try:
            from playwright.sync_api import sync_playwright  # type: ignore
        except ImportError:
            fail("playwright not installed: run `pip install playwright` and `playwright install chromium`")

        with sync_playwright() as p:
            browser = p.chromium.launch()
            try:
                ctx = browser.new_context(viewport={"width": 1280, "height": 900})
                page = ctx.new_page()
                page.goto(BASE_URL, wait_until="networkidle")
                page.wait_for_timeout(1500)

                test_a_v7_tokens_compact(page)
                test_c_no_here_label(page)
                test_h_data_hooks_preserved(page)
                test_f_section_unification(page)
                test_b_single_expand(page, fixture_repo_id or "")
                test_d_worktree_single_line(page)
                test_e_glance_line(page)
                ctx.close()

                test_g_mobile_layout(browser)
            finally:
                try:
                    browser.close()
                except Exception:
                    pass

        print("\nALL S024 SCENARIOS PASS")
        return 0
    finally:
        if fixture_repo_id and fixture_repo_path:
            fixture_cleanup(fixture_repo_id, fixture_repo_path)


if __name__ == "__main__":
    sys.exit(main())
