#!/usr/bin/env python3
"""Sprint S4323c8-1 — Files tab sort control (name / modified / size,
asc / desc), folders always grouped above files, preference persisted to
localStorage across reloads.

Exit 0 = PASS / SKIP. Run: python3 tests/e2e/s4323c8_files_sort.py
"""
from __future__ import annotations
import os, signal, socket, subprocess, sys, time, urllib.parse
from pathlib import Path

sys.path.insert(0, os.path.dirname(__file__))
REPO = Path(__file__).resolve().parents[2]
BIN = REPO / "bin" / "palmux"
TO = 20_000


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0)); return s.getsockname()[1]


FOLDER_NAMES = {".git", "zzz-folder"}  # ".git" is a real on-disk dir every
# git worktree has; ListDir doesn't hide it at the root, so the fixture
# always shows two folders. We assert the folder GROUP (order-independent)
# stays above the file group — that's what AC-S4323c8-1-2 requires — rather
# than pinning the relative order of the two folders to each other, which
# ListDir/browser.go doesn't guarantee for size/modTime keys.


def first_entry_names(page):
    """Return the ordered list of top-level entry names currently rendered
    in the Files list. Each row's <button title="{entry.path}"> — at the
    worktree root, path == name, so `title` is a clean, CSS-module-proof
    way to read the rendered order."""
    return page.eval_on_selector_all(
        '[data-testid="files-list"] > li > button',
        "els => els.map(el => el.getAttribute('title'))",
    )


def check_order(names: list[str], want_files: list[str], label: str) -> str | None:
    """Return None on match, else a FAIL message. The first len(FOLDER_NAMES)
    entries must be exactly the folder set (any order among themselves);
    the remainder must equal `want_files` in order."""
    nfold = len(FOLDER_NAMES)
    got_folders = set(names[:nfold])
    got_files = names[nfold:]
    if got_folders != FOLDER_NAMES or got_files != want_files:
        return (
            f"{label} mismatch: got {names} "
            f"(want folders={FOLDER_NAMES} first, then files={want_files})"
        )
    return None


def main() -> None:
    print("s4323c8_files_sort — Files list sort control (name/modified/size, asc/desc, folders-first, persisted)")
    if not BIN.is_file():
        print("SKIP: no prebuilt binary (make build)"); sys.exit(0)
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("SKIP: playwright not installed"); sys.exit(0)

    port = free_port(); os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
    cfg = Path(f"/tmp/palmux2-filessort-{port}"); cfg.mkdir(parents=True, exist_ok=True)
    proc = subprocess.Popen(
        [str(BIN), "--addr", f"127.0.0.1:{port}", "--config-dir", str(cfg),
         "--claude-bin", "/bin/cat", "--tmux-prefix", f"_pmx_filessort{port}_"],
        cwd=REPO, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    dl = time.time() + 30
    while time.time() < dl:
        line = proc.stdout.readline() if proc.stdout else ""
        if f":{port}" in line or "listening" in line:
            break
        if proc.poll() is not None:
            print("FAIL: server died"); sys.exit(1)

    failed = 0
    try:
        import _fixture as fx
        with fx.palmux2_test_fixture("filessort") as fixture:
            bid = fixture.primary_branch_id(timeout_s=10.0)

            # ── Seed files with distinct sizes/mtimes + a folder ──────────
            # name order (asc):    large.txt, mid.txt, small.txt
            # size order (asc):    small.txt(1B), mid.txt(100B), large.txt(5000B)
            # mtime order (asc):   small.txt (oldest), mid.txt, large.txt (newest)
            # A folder named "zzz-folder" is created too — alphabetically it
            # would sort LAST among these entries, so its presence at the
            # TOP of every ordering proves folder-priority survives the
            # chosen sort (AC-S4323c8-1-2).
            # Fixture repos ship a committed README.md — remove it so the
            # only entries in the listing are the ones this test controls.
            (fixture.path / "README.md").unlink(missing_ok=True)

            (fixture.path / "zzz-folder").mkdir()
            (fixture.path / "small.txt").write_text("x")
            time.sleep(1.2)
            (fixture.path / "mid.txt").write_text("x" * 100)
            time.sleep(1.2)
            (fixture.path / "large.txt").write_text("x" * 5000)

            url = (f"http://localhost:{port}/{urllib.parse.quote(fixture.repo_id, safe='')}"
                   f"/{urllib.parse.quote(bid, safe='')}/files")
            with sync_playwright() as p:
                b = p.chromium.launch(headless=True)
                ctx = b.new_context(viewport={"width": 1280, "height": 800})
                ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                pg = ctx.new_page()
                pg.goto(url, wait_until="load", timeout=TO)
                pg.wait_for_selector('[data-testid="files-list"]', timeout=TO)

                # [AC-S4323c8-1-1] sort control (key + direction) is present.
                if pg.locator('[data-testid="files-sort-bar"]').count() < 1:
                    print("FAIL: files-sort-bar not present"); failed += 1
                key_sel = pg.locator('[data-testid="files-sort-key"]')
                dir_btn = pg.locator('[data-testid="files-sort-dir"]')
                if key_sel.count() < 1 or dir_btn.count() < 1:
                    print("FAIL: files-sort-key / files-sort-dir controls missing"); failed += 1
                else:
                    print("PASS: [AC-S4323c8-1-1] sort control (key select + direction toggle) present")

                # Default: name / asc. 5 entries total (2 folders + 3 files).
                pg.wait_for_function(
                    "() => document.querySelectorAll('[data-testid=\"files-list\"] > li > button').length >= 5",
                    timeout=TO,
                )
                names = first_entry_names(pg)
                err = check_order(names, ["large.txt", "mid.txt", "small.txt"], "default order (name/asc)")
                if err:
                    print(f"FAIL: {err}"); failed += 1
                else:
                    print(f"PASS: [AC-S4323c8-1-2] default order is name/asc with folders grouped first: {names}")

                def set_sort(key: str | None, want_last_file: str, want_files: list[str], label: str):
                    """Change the sort key (if given) and/or rely on a prior
                    dir toggle, then poll until the LAST rendered entry is
                    `want_last_file` (a stable signal the reorder finished —
                    the file group's tail changes with every key/dir combo
                    used below) before asserting the full order."""
                    nonlocal failed
                    if key is not None:
                        key_sel.select_option(key)
                    try:
                        pg.wait_for_function(
                            "(want) => { const b = document.querySelectorAll('[data-testid=\"files-list\"] > li > button');"
                            "return b.length > 0 && b[b.length - 1].getAttribute('title') === want; }",
                            arg=want_last_file,
                            timeout=TO,
                        )
                    except Exception:
                        pass
                    got = first_entry_names(pg)
                    err = check_order(got, want_files, label)
                    if err:
                        print(f"FAIL: {err}"); failed += 1
                    else:
                        print(f"PASS: [AC-S4323c8-1-2] {label}: {got}")

                # size / asc — folders still grouped first, files by size ascending.
                set_sort("size", "large.txt", ["small.txt", "mid.txt", "large.txt"], "size/asc")

                # toggle direction → size / desc.
                dir_btn.click()
                set_sort(None, "small.txt", ["large.txt", "mid.txt", "small.txt"], "size/desc")
                if dir_btn.get_attribute("data-dir") != "desc":
                    print("FAIL: sort-dir button data-dir did not flip to desc"); failed += 1

                # modTime / desc (still desc from the toggle above) → newest first.
                set_sort("modTime", "small.txt", ["large.txt", "mid.txt", "small.txt"], "modTime/desc")

                # flip back to asc → modTime / asc → oldest first.
                dir_btn.click()
                set_sort(None, "large.txt", ["small.txt", "mid.txt", "large.txt"], "modTime/asc")
                if dir_btn.get_attribute("data-dir") != "asc":
                    print("FAIL: sort-dir button data-dir did not flip back to asc"); failed += 1

                # ── [AC-S4323c8-1-3] persisted across reload ──────────────
                key_sel.select_option("size")
                dir_btn.click()  # → size/desc
                pg.wait_for_function(
                    "() => { const b = document.querySelectorAll('[data-testid=\"files-list\"] > li > button');"
                    "return b.length > 0 && b[b.length - 1].getAttribute('title') === 'small.txt'; }",
                    timeout=TO,
                )
                pg.reload(wait_until="load", timeout=TO)
                pg.wait_for_selector('[data-testid="files-list"]', timeout=TO)
                pg.wait_for_function(
                    "() => document.querySelectorAll('[data-testid=\"files-list\"] > li > button').length >= 5",
                    timeout=TO,
                )
                restored_key = pg.locator('[data-testid="files-sort-key"]').input_value()
                restored_dir = pg.locator('[data-testid="files-sort-dir"]').get_attribute("data-dir")
                restored_order = first_entry_names(pg)
                err = check_order(restored_order, ["large.txt", "mid.txt", "small.txt"], "restored order (size/desc)")
                if restored_key == "size" and restored_dir == "desc" and err is None:
                    print(f"PASS: [AC-S4323c8-1-3] sort pref (size/desc) restored after reload: {restored_order}")
                else:
                    print(
                        "FAIL: sort pref not restored after reload: "
                        f"key={restored_key!r} dir={restored_dir!r} order={restored_order!r} err={err!r}"
                    )
                    failed += 1

                b.close()
    finally:
        if proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try: proc.wait(timeout=8)
            except subprocess.TimeoutExpired: proc.kill()
        import shutil; shutil.rmtree(cfg, ignore_errors=True)

    print(f"\ns4323c8_files_sort: {'ALL PASS' if failed == 0 else 'FAILED'}")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
