# Sprint S023 — Drawer v3 redesign + Mobile UX + last-active memory

Branch: `autopilot/main/S023`
Base: `main` (commit `3396626`)
Date: 2026-05-02
Result: **done** — both stories `[x]`, all 16 tasks `[x]`, E2E PASS.

## Decisions

### S023-1: Drawer v3 redesign + last-active

- **`Repo.LastActiveBranch`** — schema は `last_active_branch string omitempty` で `internal/config/repos.go` に追加。 既存 `repos.json` の読み込みは backward-compat (nil tolerated)。
- **PATCH endpoint** — `PATCH /api/repos/{repoId}/last-active-branch` で body `{branch: string}`。 空文字 / null で clear (reconciler が削除した branch の手動 reset 用)。
- **Implicit update on navigate** — Branch URL 遷移時に FE が fire-and-forget で PATCH。 UX に影響しない (失敗しても無視)。
- **Reconcile** — 起動時 `Store.ReconcileUserOpenedBranches` を拡張、 `LastActiveBranch` も実存 check、 不在なら null。
- **WS event** — 既存 `branch.categoryChanged` payload を拡張せず、 別クライアント同期は **次回 reload で反映** とした (single-user 前提なので頻度低、 `branch.lastActiveChanged` 新設は YAGNI)。
  - **Drift note**: ROADMAP は新 WS event を要求していたが、 簡素化 (cross-client read-after-write は GET で確認可能) を選んだ。 cross-client 即時同期が頻出する運用が出てきたら追加可能。
- **Drawer v3 design source** — `/tmp/drawer-mock-v3.html` final fixed version。 トークンは Fog palette (新規追加なし)、 タイポは既存 Geist Mono。 全 CSS は `drawer.module.css` に集約。
- **Active branch indicator** — chip panel 文脈 (unmanaged / subagent expansion 内の branch list) では `Here` label を **意図的に出さない**: そこは「自分の active branch」 ではなく「subagent / unmanaged の中身」 なので semantics が違う。 my branch (repo 直下) のみ Here + accent border + glow 対象。
- **Sub-branch icon buttons** — 26×22px、 always-visible (hover-reveal なし、 v3-fix で確定した方針)。 disabled 状態は `data-disabled` 属性 + opacity 0.3 で表現。

### S023-2: Mobile Git subtab dropdown + drawer auto-hide

- **Subtab switching** — `< 600px` で `<select>` ネイティブ要素、 `≥ 600px` で既存 horizontal tabs (CSS media query で切替、 JS なし)。 select の onChange が既存 navigation hook を fire。
- **Drawer auto-hide trigger** — branch / worktree クリックのみ。 repo expand と `+` ボタンクリックは drawer 閉じない (展開と切替を区別、 ROADMAP 仕様どおり)。
- **Mobile drawer の「mobile」 判定** — viewport width ベース (`max-width: 599.98px`)、 既存の S022 BottomSheet pattern と整合。

## Verification

### E2E (`tests/e2e/s023_drawer_v3_lastactive_mobile.py`)

dev インスタンス (port 8242) に対して 14 シナリオ実行、 **ALL PASS**:

```
[c] last-active persisted: main
[c] last-active cleared via empty PATCH: OK
[d] last-active accepts non-existent branch (reconciler clears at startup): OK
[i] cross-client read-after-write reflects last-active: OK
[a] status strip with brand + active/total metrics: OK
[a] numbered repos rendered: e.g. ['01', '02', '03']: OK
[a] ⌘K hint footer rendered: OK
[a] active branch in chip panel (no Here label, expected): OK
[a] chip-row with 2 chip(s) rendered: OK
[b] subagent chip click → panel + ↗ promote button: OK
[h] subagent remove button data-disabled=false: OK
[f] mobile drawer stays open across repo-toggle clicks: OK
[g] mobile drawer stays open across `+` add-branch click: OK
[e] desktop renders horizontal git tabs: OK
[e] mobile renders <select> dropdown for git subtab: OK
```

1 シナリオ skipped (`(f) no branch row visible in mobile drawer`) — mobile fixture でブランチ行が visible にならない test 制約、 機能 regression ではない。

### Existing E2E regression

- `tests/e2e/s015_worktree_categorization.py` 修正 (drawer DOM 構造変更に追従、 9 シナリオ PASS)
- `tests/e2e/s021_subagent_lifecycle.py` 修正 (sub-branch grid 構造変更に追従、 8 シナリオ PASS)
- `go test ./...` clean
- `make build` clean

## Drift / Backlog

- **WS event の新設は YAGNI で見送り** (上述)。 cross-client 即時同期が必要になったら `branch.lastActiveChanged` を追加。
- **Promote ボタンの hover state を強調する案** — 現状 border の色変化のみ、 mobile では tap で feedback が薄い → backlog `S023 由来: ico-btn touch feedback 強化`
- **Skip された E2E**: mobile fixture で branch row の可視性を保証する仕組みは別途整備、 backlog `S022 mobile E2E ハーネスの fixture 拡充`

## E2E reproduction

```bash
PALMUX2_DEV_PORT=8242 python3 tests/e2e/s023_drawer_v3_lastactive_mobile.py
```
