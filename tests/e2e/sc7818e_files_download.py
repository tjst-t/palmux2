#!/usr/bin/env python3
"""Sprint Sc7818e — Files タブ ダウンロード機能 E2E.

GET {filesPrefix}/download エンドポイント (単一ファイル attachment / フォルダ・
複数選択 zip) と、その UI 導線 (コンテキストメニュー / プレビューヘッダ / batch /
モバイル幅) を実機 (make serve INSTANCE=dev) に対して検証する。

このプロジェクトの E2E 正典は Python + Playwright (CLAUDE.md / 既存 s033)。
.spec.ts は使わない。

Acceptance criteria verified here:

  [AC-Sc7818e-1-1]  単一テキストを全サイズ download (previewMaxBytes 超でも truncate 無し)、
                    Content-Disposition: attachment
  [AC-Sc7818e-1-2]  バイナリ全バイト一致 + ServeContent の Range (206) 応答
  [AC-Sc7818e-1-3]  非ASCII ファイル名は RFC5987 filename*=UTF-8'' でエンコード
  [AC-Sc7818e-1-4]  ../ / worktree 外 / symlink 経由は 400 で内容を漏らさない

  [AC-Sc7818e-2-1]  ディレクトリ download は階層保持の zip (空ディレクトリ含む)
  [AC-Sc7818e-2-2]  path クエリ複数指定で選択集合を 1 zip にまとめる
  [AC-Sc7818e-2-3]  zip 内エントリ名は worktree 相対のみ (zip-slip 耐性)、不正 path は 400
  [AC-Sc7818e-2-4]  zip はストリーム配信 (Content-Length 無し or chunked)

  [AC-Sc7818e-3-1]  コンテキストメニューに Download / (folder) Download / Download N items…、
                    クリックで <a download> ネイティブ DL が起動
  [AC-Sc7818e-3-2]  プレビューヘッダのダウンロードボタンで開いているファイルが DL される
  [AC-Sc7818e-3-3]  batch/フォルダ DL は ?path= 反復の zip URL、文言が選択数を反映
  [AC-Sc7818e-3-4]  モバイル幅 (<600px) でも導線が崩れず DL が起動する

Runs against: make serve INSTANCE=dev (default port 8215).
Exit 0 = PASS, nonzero = FAIL.
"""
from __future__ import annotations

import asyncio
import io
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path

from playwright.async_api import Page, async_playwright

sys.path.insert(0, str(Path(__file__).parent))
from _fixture import BASE_URL, _http_json, palmux2_test_fixture

TIMEOUT = 12_000  # ms
TIMEOUT_S = 12.0


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(msg: str) -> None:
    print(f"  ok: {msg}")


# ─── HTTP helpers that expose headers (the _fixture ones drop them) ──────────


def raw_get(path: str, *, headers: dict[str, str] | None = None) -> tuple[int, bytes, dict[str, str]]:
    """GET returning (status, body, lowercased-headers)."""
    req = urllib.request.Request(f"{BASE_URL}{path}", method="GET", headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            h = {k.lower(): v for k, v in resp.headers.items()}
            return resp.status, resp.read(), h
    except urllib.error.HTTPError as e:
        h = {k.lower(): v for k, v in e.headers.items()} if e.headers else {}
        return e.code, e.read(), h


def files_base(repo_id: str, branch_id: str) -> str:
    return (
        f"/api/repos/{urllib.parse.quote(repo_id)}"
        f"/branches/{urllib.parse.quote(branch_id)}/files"
    )


def download_url(repo_id: str, branch_id: str, paths: list[str]) -> str:
    qs = "&".join(f"path={urllib.parse.quote(p)}" for p in paths)
    return f"{files_base(repo_id, branch_id)}/download?{qs}"


async def api_create_file(repo_id: str, branch_id: str, rel: str, content: str) -> None:
    code, data = _http_json(
        "POST", f"{files_base(repo_id, branch_id)}/create", body={"path": rel, "content": content}
    )
    if code not in (200, 201):
        fail(f"create {rel}: {code} {data}")


async def api_create_dir(repo_id: str, branch_id: str, rel: str) -> None:
    code, data = _http_json(
        "POST", f"{files_base(repo_id, branch_id)}/create-dir", body={"path": rel}
    )
    if code not in (200, 201):
        fail(f"create-dir {rel}: {code} {data}")


# ══════════════════════════════════════════════════════════════════════════
# Story 1 — single-file download (API)
# ══════════════════════════════════════════════════════════════════════════


def test_single_full_size(repo_id: str, branch_id: str, worktree: Path) -> None:
    """[AC-Sc7818e-1-1] full-size text download with attachment disposition."""
    # 2 MiB known pattern, certainly above any preview/read limit.
    big = ("PALMUX-DL-LINE-%08d\n" % 0).join(str(i) for i in range(120_000))
    big_bytes = big.encode()
    (worktree / "big.txt").write_bytes(big_bytes)

    code, body, h = raw_get(download_url(repo_id, branch_id, ["big.txt"]))
    if code != 200:
        fail(f"[AC-Sc7818e-1-1] download big.txt → {code}")
    cd = h.get("content-disposition", "")
    if not cd.lower().startswith("attachment"):
        fail(f"[AC-Sc7818e-1-1] Content-Disposition not attachment: {cd!r}")
    if "big.txt" not in cd:
        fail(f"[AC-Sc7818e-1-1] filename not in Content-Disposition: {cd!r}")
    if body != big_bytes:
        fail(f"[AC-Sc7818e-1-1] body mismatch (got {len(body)} want {len(big_bytes)} bytes) — truncated?")
    ok(f"AC-Sc7818e-1-1: full {len(body)} bytes, attachment ({cd[:60]!r})")


def test_binary_and_range(repo_id: str, branch_id: str, worktree: Path) -> None:
    """[AC-Sc7818e-1-2] binary byte-exact + Range 206."""
    blob = bytes(range(256)) * 64  # 16 KiB binary
    (worktree / "logo.bin").write_bytes(blob)

    code, body, h = raw_get(download_url(repo_id, branch_id, ["logo.bin"]))
    if code != 200 or body != blob:
        fail(f"[AC-Sc7818e-1-2] binary download mismatch: code={code} len={len(body)}")
    if h.get("accept-ranges", "").lower() != "bytes":
        fail(f"[AC-Sc7818e-1-2] missing Accept-Ranges: bytes (got {h.get('accept-ranges')!r})")

    code2, body2, h2 = raw_get(
        download_url(repo_id, branch_id, ["logo.bin"]), headers={"Range": "bytes=0-9"}
    )
    if code2 != 206:
        fail(f"[AC-Sc7818e-1-2] Range request expected 206, got {code2}")
    if body2 != blob[0:10]:
        fail(f"[AC-Sc7818e-1-2] Range body mismatch: {body2!r}")
    if "0-9/" not in h2.get("content-range", ""):
        fail(f"[AC-Sc7818e-1-2] bad Content-Range: {h2.get('content-range')!r}")
    ok("AC-Sc7818e-1-2: binary byte-exact + 206 Partial Content")


def test_nonascii_filename(repo_id: str, branch_id: str, worktree: Path) -> None:
    """[AC-Sc7818e-1-3] RFC5987 filename* for non-ASCII names."""
    name = "設計メモ.txt"
    (worktree / name).write_text("メモ本文\n", encoding="utf-8")

    code, _body, h = raw_get(download_url(repo_id, branch_id, [name]))
    if code != 200:
        fail(f"[AC-Sc7818e-1-3] download {name} → {code}")
    cd = h.get("content-disposition", "")
    enc = urllib.parse.quote(name)  # %E8%A8%AD...
    if "filename*=UTF-8''" not in cd or enc not in cd.replace("%2E", ".").replace("%2e", "."):
        # tolerate '.' either raw or %2E in the encoded form
        if "filename*=UTF-8''" not in cd or "%E8%A8%AD" not in cd:
            fail(f"[AC-Sc7818e-1-3] RFC5987 filename* missing/wrong: {cd!r}")
    ok(f"AC-Sc7818e-1-3: RFC5987 filename* present ({cd[:70]!r})")


def test_traversal_and_symlink(repo_id: str, branch_id: str, worktree: Path) -> None:
    """[AC-Sc7818e-1-4] traversal / external symlink → 400, no leak."""
    code, body, _h = raw_get(download_url(repo_id, branch_id, ["../../../../etc/passwd"]))
    if code != 400:
        fail(f"[AC-Sc7818e-1-4] traversal expected 400, got {code}")
    if b"root:" in body:
        fail("[AC-Sc7818e-1-4] traversal leaked /etc/passwd content")

    # symlink inside the worktree pointing outside.
    link = worktree / "evil"
    try:
        if link.exists() or link.is_symlink():
            link.unlink()
        os.symlink("/etc/passwd", link)
    except OSError as e:
        fail(f"[AC-Sc7818e-1-4] could not create symlink fixture: {e}")
    code2, body2, _ = raw_get(download_url(repo_id, branch_id, ["evil"]))
    if code2 != 400:
        fail(f"[AC-Sc7818e-1-4] external symlink expected 400, got {code2}")
    if b"root:" in body2:
        fail("[AC-Sc7818e-1-4] symlink leaked external content")
    ok("AC-Sc7818e-1-4: traversal + external symlink → 400, no leak")


# ══════════════════════════════════════════════════════════════════════════
# Story 2 — folder / multi-select zip download (API)
# ══════════════════════════════════════════════════════════════════════════


async def test_folder_zip(repo_id: str, branch_id: str, worktree: Path) -> None:
    """[AC-Sc7818e-2-1] directory → hierarchy-preserving zip incl. empty dir."""
    await api_create_file(repo_id, branch_id, "dl_dir/a.txt", "alpha\n")
    await api_create_file(repo_id, branch_id, "dl_dir/sub/b.txt", "bravo\n")
    await api_create_dir(repo_id, branch_id, "dl_dir/empty")

    code, body, h = raw_get(download_url(repo_id, branch_id, ["dl_dir"]))
    if code != 200:
        fail(f"[AC-Sc7818e-2-1] folder download → {code}")
    cd = h.get("content-disposition", "")
    if "dl_dir.zip" not in cd:
        fail(f"[AC-Sc7818e-2-1] zip filename not dl_dir.zip: {cd!r}")
    try:
        zf = zipfile.ZipFile(io.BytesIO(body))
    except zipfile.BadZipFile:
        fail("[AC-Sc7818e-2-1] response is not a valid zip")
    names = set(zf.namelist())
    # entry names may be prefixed with the dir or not; check by suffix.
    def has(suffix: str) -> bool:
        return any(n.endswith(suffix) for n in names)
    if not has("a.txt") or not has("sub/b.txt"):
        fail(f"[AC-Sc7818e-2-1] zip missing files: {names}")
    a = next(n for n in names if n.endswith("a.txt"))
    if zf.read(a) != b"alpha\n":
        fail("[AC-Sc7818e-2-1] zip content mismatch for a.txt")
    if not any(n.rstrip("/").endswith("empty") for n in names):
        fail(f"[AC-Sc7818e-2-1] empty dir not preserved in zip: {names}")
    ok(f"AC-Sc7818e-2-1: folder zip ok ({sorted(names)})")


async def test_multi_zip(repo_id: str, branch_id: str, worktree: Path) -> None:
    """[AC-Sc7818e-2-2] multiple path= → single zip of that set."""
    await api_create_file(repo_id, branch_id, "m_a.txt", "AA\n")
    await api_create_file(repo_id, branch_id, "m_sub/b.txt", "BB\n")
    await api_create_file(repo_id, branch_id, "m_dir2/c.txt", "CC\n")
    await api_create_file(repo_id, branch_id, "m_excluded.txt", "ZZ\n")

    url = download_url(repo_id, branch_id, ["m_a.txt", "m_sub/b.txt", "m_dir2"])
    code, body, _h = raw_get(url)
    if code != 200:
        fail(f"[AC-Sc7818e-2-2] multi download → {code}")
    zf = zipfile.ZipFile(io.BytesIO(body))
    names = set(zf.namelist())
    if not any(n.endswith("m_a.txt") for n in names):
        fail(f"[AC-Sc7818e-2-2] m_a.txt missing: {names}")
    if not any(n.endswith("b.txt") for n in names):
        fail(f"[AC-Sc7818e-2-2] m_sub/b.txt missing: {names}")
    if not any(n.endswith("c.txt") for n in names):
        fail(f"[AC-Sc7818e-2-2] m_dir2/c.txt missing: {names}")
    if any(n.endswith("m_excluded.txt") for n in names):
        fail(f"[AC-Sc7818e-2-2] unselected file leaked into zip: {names}")
    ok(f"AC-Sc7818e-2-2: multi-path zip ({sorted(names)})")


async def test_zip_slip_and_reject(repo_id: str, branch_id: str, worktree: Path) -> None:
    """[AC-Sc7818e-2-3] zip entries are worktree-relative; bad path → 400, no zip."""
    await api_create_file(repo_id, branch_id, "zs_dir/a.txt", "x\n")
    code, body, _h = raw_get(download_url(repo_id, branch_id, ["zs_dir"]))
    if code != 200:
        fail(f"[AC-Sc7818e-2-3] zip download → {code}")
    zf = zipfile.ZipFile(io.BytesIO(body))
    for n in zf.namelist():
        if n.startswith("/") or ".." in n.split("/"):
            fail(f"[AC-Sc7818e-2-3] zip-slip entry name: {n!r}")

    # one bad path among the set → whole request 400, not a partial zip.
    code2, body2, h2 = raw_get(download_url(repo_id, branch_id, ["zs_dir", "../../etc"]))
    if code2 != 400:
        fail(f"[AC-Sc7818e-2-3] mixed bad path expected 400, got {code2}")
    if h2.get("content-type", "").startswith("application/zip") or body2[:2] == b"PK":
        fail("[AC-Sc7818e-2-3] server returned a (partial) zip despite bad path")
    ok("AC-Sc7818e-2-3: no zip-slip names; bad path → 400 (no partial zip)")


async def test_zip_streamed(repo_id: str, branch_id: str, worktree: Path) -> None:
    """[AC-Sc7818e-2-4] zip is streamed (no Content-Length precomputation)."""
    for i in range(4):
        await api_create_file(repo_id, branch_id, f"bigdir/f{i}.bin", ("D" * (1024 * 1024)))
    code, body, h = raw_get(download_url(repo_id, branch_id, ["bigdir"]))
    if code != 200:
        fail(f"[AC-Sc7818e-2-4] bigdir zip → {code}")
    te = h.get("transfer-encoding", "").lower()
    has_len = "content-length" in h
    # Streaming archive/zip to ResponseWriter cannot know the final size up
    # front, so Go emits chunked transfer (no Content-Length). Either chunked
    # OR absent Content-Length satisfies "streamed, not buffered".
    if "chunked" not in te and has_len:
        fail(f"[AC-Sc7818e-2-4] zip not streamed (Content-Length={h.get('content-length')}, TE={te!r})")
    # sanity: it still unzips.
    zipfile.ZipFile(io.BytesIO(body)).namelist()
    ok(f"AC-Sc7818e-2-4: zip streamed (TE={te!r}, has_content_length={has_len})")


# ══════════════════════════════════════════════════════════════════════════
# Story 3 — download UI affordances (Playwright, real backend)
# ══════════════════════════════════════════════════════════════════════════


async def nav_to_files(page: Page, repo_id: str, branch_id: str, sub: str = "") -> None:
    url = f"{BASE_URL}/{urllib.parse.quote(repo_id)}/{urllib.parse.quote(branch_id)}/files"
    if sub:
        url += "/" + sub
    await page.goto(url)
    await page.wait_for_selector('[data-testid="files-list"]', timeout=TIMEOUT)


async def row_by_name(page: Page, name: str):
    return page.locator(f'[data-testid="files-list"] button:has-text("{name}")').first


async def test_ui_ctx_single_download(page: Page, repo_id: str, branch_id: str) -> None:
    """[AC-Sc7818e-3-1] single-file context menu Download triggers a download."""
    await api_create_file(repo_id, branch_id, "ui_single.txt", "hello-ui\n")
    await nav_to_files(page, repo_id, branch_id)
    await (await row_by_name(page, "ui_single.txt")).click(button="right")
    await page.locator('[data-testid="files-context-menu"]').wait_for(timeout=TIMEOUT)
    dl_item = page.locator('[data-testid="files-ctx-download"]')
    if not await dl_item.is_visible():
        fail("[AC-Sc7818e-3-1] 'Download' item not in single-file context menu")
    async with page.expect_download() as dlinfo:
        await dl_item.click()
    download = await dlinfo.value
    if "/files/download?" not in download.url or "ui_single.txt" not in urllib.parse.unquote(download.url):
        fail(f"[AC-Sc7818e-3-1] wrong download url: {download.url}")
    ok(f"AC-Sc7818e-3-1: single download triggered ({download.suggested_filename})")


async def test_ui_ctx_folder_download(page: Page, repo_id: str, branch_id: str) -> None:
    """[AC-Sc7818e-3-1] folder context menu Download → zip url."""
    await api_create_file(repo_id, branch_id, "ui_folder/x.txt", "fx\n")
    await nav_to_files(page, repo_id, branch_id)
    await (await row_by_name(page, "ui_folder")).click(button="right")
    await page.locator('[data-testid="files-context-menu"]').wait_for(timeout=TIMEOUT)
    dl_item = page.locator('[data-testid="files-ctx-download"]')
    if not await dl_item.is_visible():
        fail("[AC-Sc7818e-3-1] 'Download' item not in folder context menu")
    async with page.expect_download() as dlinfo:
        await dl_item.click()
    download = await dlinfo.value
    if "ui_folder" not in urllib.parse.unquote(download.url):
        fail(f"[AC-Sc7818e-3-1] folder download url wrong: {download.url}")
    ok(f"AC-Sc7818e-3-1: folder download triggered ({download.suggested_filename})")


async def test_ui_preview_download(page: Page, repo_id: str, branch_id: str) -> None:
    """[AC-Sc7818e-3-2] preview header download button."""
    await api_create_file(repo_id, branch_id, "ui_preview.txt", "preview-body\n")
    await nav_to_files(page, repo_id, branch_id)
    await (await row_by_name(page, "ui_preview.txt")).click()
    await page.locator('[data-testid="file-preview"]').wait_for(timeout=TIMEOUT)
    btn = page.locator('[data-testid="file-preview-download"]')
    if not await btn.is_visible():
        fail("[AC-Sc7818e-3-2] download button missing in preview header")
    async with page.expect_download() as dlinfo:
        await btn.click()
    download = await dlinfo.value
    if "ui_preview.txt" not in urllib.parse.unquote(download.url):
        fail(f"[AC-Sc7818e-3-2] preview download url wrong: {download.url}")
    ok("AC-Sc7818e-3-2: preview header download triggered")


async def test_ui_batch_download(page: Page, repo_id: str, branch_id: str) -> None:
    """[AC-Sc7818e-3-3] batch 'Download N items…' → ?path= repeated zip url."""
    await api_create_file(repo_id, branch_id, "ui_b1.txt", "1\n")
    await api_create_file(repo_id, branch_id, "ui_b2.txt", "2\n")
    await api_create_file(repo_id, branch_id, "ui_b3.txt", "3\n")
    await nav_to_files(page, repo_id, branch_id)
    modifier = "Meta" if sys.platform == "darwin" else "Control"
    await (await row_by_name(page, "ui_b1.txt")).click(modifiers=[modifier])
    await (await row_by_name(page, "ui_b2.txt")).click(modifiers=[modifier])
    await (await row_by_name(page, "ui_b3.txt")).click(modifiers=[modifier])
    await (await row_by_name(page, "ui_b3.txt")).click(button="right", modifiers=[modifier])
    await page.locator('[data-testid="files-context-menu"]').wait_for(timeout=TIMEOUT)
    batch_item = page.locator('[data-testid="files-ctx-batch-download"]')
    if not await batch_item.is_visible():
        fail("[AC-Sc7818e-3-3] batch 'Download N items…' missing")
    label = (await batch_item.inner_text()).strip()
    if "3" not in label:
        fail(f"[AC-Sc7818e-3-3] batch label does not reflect count: {label!r}")
    async with page.expect_download() as dlinfo:
        await batch_item.click()
    download = await dlinfo.value
    u = urllib.parse.unquote(download.url)
    if u.count("path=") < 3:
        fail(f"[AC-Sc7818e-3-3] zip url missing repeated path= : {download.url}")
    for f in ("ui_b1.txt", "ui_b2.txt", "ui_b3.txt"):
        if f not in u:
            fail(f"[AC-Sc7818e-3-3] {f} not in batch zip url: {download.url}")
    ok(f"AC-Sc7818e-3-3: batch zip url ok (label={label!r})")

    # Rule 6 coverage: the multi-select ACTION BAR also exposes a ⬇ Download
    # button (data-testid files-batch-download) — exercise it too.
    bar_btn = page.locator('[data-testid="files-batch-download"]')
    if not await bar_btn.is_visible():
        fail("[AC-Sc7818e-3-3] action-bar download button not visible with selection")
    async with page.expect_download() as dlinfo2:
        await bar_btn.click()
    u2 = urllib.parse.unquote((await dlinfo2.value).url)
    if u2.count("path=") < 3:
        fail(f"[AC-Sc7818e-3-3] action-bar download url missing repeated path=: {u2}")
    ok("AC-Sc7818e-3-3: action-bar ⬇ Download triggers zip url too")


async def test_ui_mobile(page: Page, repo_id: str, branch_id: str) -> None:
    """[AC-Sc7818e-3-4] mobile width: context menu + preview download reachable.

    Runs in a FRESH context (clean localStorage) so files-memory does not
    restore a previously-opened file — on mobile a restored selection hides the
    list pane (`.body.previewOpen .listPane { display:none }`), which is correct
    app behavior but not what this AC exercises. We verify both surfaces:
    (1) the list context-menu download, and (2) the preview-header download.
    """
    await api_create_file(repo_id, branch_id, "ui_mobile.txt", "m\n")

    # (1) Context-menu download from the list (list is visible by default on
    #     mobile when no file is restored/selected).
    await nav_to_files(page, repo_id, branch_id)
    await (await row_by_name(page, "ui_mobile.txt")).click(button="right")
    await page.locator('[data-testid="files-context-menu"]').wait_for(timeout=TIMEOUT)
    dl_item = page.locator('[data-testid="files-ctx-download"]')
    if not await dl_item.is_visible():
        fail("[AC-Sc7818e-3-4] download item not visible at mobile width")
    # the menu must be within the viewport (not clipped off-screen).
    box = await dl_item.bounding_box()
    if box is None or box["x"] < 0 or box["x"] + box["width"] > 390 + 1:
        fail(f"[AC-Sc7818e-3-4] download item overflows mobile viewport: {box}")
    async with page.expect_download() as dlinfo:
        await dl_item.click()
    await dlinfo.value
    ok("AC-Sc7818e-3-4: mobile context-menu download reachable + triggers")

    # (2) Preview-header download on mobile: open the file (preview takes over
    #     the pane on mobile) and download from the header button.
    await (await row_by_name(page, "ui_mobile.txt")).click()
    await page.locator('[data-testid="file-preview"]').wait_for(timeout=TIMEOUT)
    pbtn = page.locator('[data-testid="file-preview-download"]')
    if not await pbtn.is_visible():
        fail("[AC-Sc7818e-3-4] preview download button not visible at mobile width")
    pbox = await pbtn.bounding_box()
    if pbox is None or pbox["x"] < 0 or pbox["x"] + pbox["width"] > 390 + 1:
        fail(f"[AC-Sc7818e-3-4] preview download button overflows mobile viewport: {pbox}")
    async with page.expect_download() as dlinfo2:
        await pbtn.click()
    await dlinfo2.value
    ok("AC-Sc7818e-3-4: mobile preview-header download reachable + triggers")


# ─── Runner ──────────────────────────────────────────────────────────────


async def main() -> None:
    with palmux2_test_fixture("sc7818e") as fx:
        repo_id = fx.repo_id
        branch_id = fx.branch_id
        worktree = fx.path  # primary worktree == repo path

        print("--- Story 1: single-file download (API) ---")
        test_single_full_size(repo_id, branch_id, worktree)
        test_binary_and_range(repo_id, branch_id, worktree)
        test_nonascii_filename(repo_id, branch_id, worktree)
        test_traversal_and_symlink(repo_id, branch_id, worktree)

        print("--- Story 2: folder/multi zip download (API) ---")
        await test_folder_zip(repo_id, branch_id, worktree)
        await test_multi_zip(repo_id, branch_id, worktree)
        await test_zip_slip_and_reject(repo_id, branch_id, worktree)
        await test_zip_streamed(repo_id, branch_id, worktree)

        print("--- Story 3: download UI (Playwright, real backend) ---")
        async with async_playwright() as pw:
            browser = await pw.chromium.launch(headless=True)
            ctx = await browser.new_context(viewport={"width": 1280, "height": 800})
            page = await ctx.new_page()
            await test_ui_ctx_single_download(page, repo_id, branch_id)
            await test_ui_ctx_folder_download(page, repo_id, branch_id)
            await test_ui_preview_download(page, repo_id, branch_id)
            await test_ui_batch_download(page, repo_id, branch_id)

            # Mobile test in a FRESH context (clean localStorage so files-memory
            # does not restore a prior selection that would hide the list pane).
            mobile_ctx = await browser.new_context(viewport={"width": 390, "height": 844})
            mpage = await mobile_ctx.new_page()
            await test_ui_mobile(mpage, repo_id, branch_id)
            await mobile_ctx.close()

            await browser.close()

    print("\n=== Sc7818e PASS ===")


if __name__ == "__main__":
    asyncio.run(main())
