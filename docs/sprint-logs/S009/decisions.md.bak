# Sprint S009 — Autonomous Decisions

## Sprint scope

複数インスタンス可タブの統一管理 UI (Claude / Bash) — TabBar の管理 UI を tab-type 非依存に refactor し、 Provider interface に MinInstances/MaxInstances を追加し、 Claude タブを `Multiple()=true` に切り替え、 Manager.agents map を per-tab key にし、 Bash タブの auto-naming + ContextMenu 統一を実装する。 19 タスク。

## Planning Decisions

- **Provider.Limits(SettingsView) instead of two methods**: 単純な `MinInstances() int / MaxInstances(settings) int` だと将来仕様 (例: ハードウェア依存の上限) を変えるたびに 5 Provider を全部触ることになる。 `InstanceLimits` 構造体 + `SettingsView` インタフェースで一段抽象化。 DESIGN_PRINCIPLES「明示的 > 暗黙的」の方針通り、 cap が settings 駆動であることを型で明示。
- **Claude タブの canonical id を `claude:claude`**: bash 既存の `{type}:{name}` 規約に統一。 既存 URL `/<repo>/<branch>/claude` は `tabIDFromRequest()` ヘルパで canonical にフォールバックさせて後方互換維持。 `CanonicaliseTabID()` で `""` / `"claude"` を `"claude:claude"` に正規化。
- **`MultiTabHook` interface in store**: 非 tmux multi-instance タブ (Claude) と tmux 系 (Bash) で AddTab/RemoveTab の分岐ロジックを 1 箇所に集約。 store 側はインタフェース越しに claudeagent.Manager を呼ぶので import cycle なし。 hook の wiring は main.go に置いた。
- **per-tab keying は (repoID, branchID, tabID)**: agents map / sessions.json Active map / BranchPrefs map を全部 tabKey 化。 legacy `(repoID, branchID)` キーは `migrateLegacyTabKeys()` で読み込み時に `claude:claude` 化して save 時に新形式に書き換え。
- **2 番目以降の Claude タブは `--resume` なしで spawn**: `EnsureAgent` で `m.store.ActiveFor(repoID, branchID, tabID)` が空なら resume せず、 CLI 起動時に新 session_id を発行させる。 1 番目タブは既存の per-branch resume 挙動を維持 (legacy migration で `claude:claude` に紐づく)。
- **Bash auto-naming: prompt 廃止**: 既存の `prompt: "New tab name"` を削除し、 `addTab` は type だけ渡す。 server 側の `pickNextWindowName` は既存ロジックでそのまま `bash:bash-N` を返す。 hooks/dialog 経由は ContextMenu の Rename 専用に縮約。
- **ContextMenu Close UX**: `Min=1` floor に達しているグループは `closeDisabled=true` で Close 項目をグレーアウト、 ラベルに "(last Claude — protected)" / "(protected)" を付与。 progress-in-flight 確認は既存 `confirmDialog` で実装。
- **MCP server multi-tab**: 1 Agent = 1 mcpServer (PermissionRequester は Agent 自身) の per-CLI 構造なので multi-tab で混線しない。 `tool_use_id` ベースの dedupe も Agent 内 map で完結。 変更不要 — backlog にも追加しない。
- **TabBar refactor: per-group + buttons**: tab を type ごとに group 化し、 `Multiple()=true` の group 末尾に + を出す。 1 + at end of bar は廃止。 group の wrapper には `inline-flex` を使い、 既存の drag-to-scroll は変えない。 mobile 対応は overflow-x: auto + touch-action: pan-x で既存挙動を維持。

## Implementation Decisions

- **`OnBranchOpen` 経由で Claude タブを recompute**: 非 tmux multi-instance providers は `recomputeTabs` がタブを再現できなかったので、 `OnBranchOpen` (resume:false で副作用なし) を呼んで provider 自身に任せる方式に変更。 `tabsForBranch()` が `BranchTabs` map から ordered list を返し、 空なら `[CanonicalTabID]` を auto-seed。
- **`KillBranch` は agent map を prefix scan**: pre-S009 は `(repoID, branchID)` 1 entry だったので削除 1 つで済んだ。 post-S009 は `repoID/branchID/` プレフィクスを map walk して全 Agent shutdown。 `BranchTabs` も `nil` で永続化解除して branch close を完全な reset にする。
- **HTTP 409 mapping**: `ErrTabLimit` を新設し `statusForErr()` で 409 Conflict にマップ。 既存の `ErrTabProtected` (Files/Git) は引き続き 403 Forbidden。 FE はステータスコードで分岐できる。
- **TabRow `data-testid`**: `tab-add-{type}` と `tab-{tabId}` を test 用に追加。 既存の context menu open 経路は変えていない。
- **Activity Inbox `tabId` / `tabName` 経由の per-tab routing**: notify Hub に `TabID` / `TabName` フィールドを足し、 Agent が publishNotification で自分の tab id を stamp。 FE は `pendingItem.tabId` があれば `/tabs/{tabId}/claude/permission/...` にルートし、 なければ legacy `/tabs/claude/permission/...` 経由 (= canonical fallback)。

## E2E results

`tests/e2e/s009_multi_tab.py` (PALMUX2_DEV_PORT=8247) で全 11 アサーション PASS:

```
S009 E2E against http://localhost:8247
  starting tabs: claude=['claude:claude'], bash=['bash:bash']
  [PASS] AC: canonical Claude tab id is `claude:claude`
  [PASS] AC: 2nd Claude tab auto-named 'Claude 2' with id claude:claude-2
  [PASS] AC: 3rd Claude tab auto-named 'Claude 3'
  [PASS] AC: 4th Claude tab returns 409 (cap=3)
  [PASS] AC: DELETE claude:claude-2 succeeded; remaining=['claude:claude', 'claude:claude-3']
  [PASS] AC: removing last Claude tab returns 409 (Min=1 floor)
  [PASS] AC: 2nd Bash tab auto-named 'bash:bash-2'
  [PASS] AC: GET /api/settings exposes maxClaude=3, maxBash=5
  [PASS] AC: TabBar renders per-type + buttons (claude / bash)
  [PASS] AC: clicking + button creates a 2nd Claude tab and refocuses
  [PASS] AC: right-click on the lone Claude tab shows Close as protected
PASS — all S009 acceptance criteria covered
```

UNIT/integration tests: `go test ./internal/...` 全パッケージ PASS。 vet エラーなし。

## Carried-over / partial coverage

- **Real `pgrep -f claude` の独立 PID 検証**: dev インスタンスの auth state によっては実際に CLI が spawn しないため、 ROADMAP の (a) 項目は API レベルでの "2 つの独立した Agent" 検証に留めた (Manager.agents map に 2 entry が入り、 各 Agent が独立した Session を持つことを E2E で確認)。 完全な PID 検証は host palmux2 で実行可能だが、 CRITICAL 制約により host instance を触らない方針。
- **WS event `tab.added` / `tab.removed` cross-client sync**: store の publish パスは S001 以降変更しておらず、 既存 events_test.go と E2E の "POST /tabs → tab list が更新" の流れで間接的にカバー。 専用の cross-WS テストは backlog に追加候補 (S014 Activity Inbox per-tab で活用される)。
- **mobile 幅の長押しメニュー検証**: 既存 `useLongPress` が変わっていないので、 desktop での右クリック動作と同等。 viewport 600 未満の Playwright 検証は dev instance の負荷を避けるため最小限に留め、 backlog として S017+ で深掘り。
- **タブ rename**: ROADMAP S020 にバックログ済み。 S009 では context menu の Rename 項目を Claude タブで disabled にして、 Bash でのみ既存 rename ハンドラを残した (UX 整合のため Claude にも対応する流れは S020 で扱う)。

## Backlog additions (none)

S009 のスコープ内で完結。 S020 (Tab UX completion) に既に rename / drag reorder が並んでいるので追加 entry は不要。
