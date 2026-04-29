# Sprint S002 — Autonomous Decisions

Sprint: `.claude/settings.json` editor (autopilot/S002)
Started from: `main` @ `c91a3ee` (claude-agent: add Plan mode UI (S001))

## Planning Decisions

- **Stories already in spec**: S002-1 (read settings) と S002-2 (削除) はそのまま採用。Story 粒度は適切 (one user-facing behavior each)。No split needed.
- **GUI spec autonomous mode**: Playwright harness は backlog 扱い (`docs/ROADMAP.md` の S001 Playwright 繰り越し参照)。S002 でも個別 Playwright tests は書かない。代わりに Go ユニットテストと手動ビルドで品質ゲートとする。
- **Scope限定**: Hooks や arbitrary key の削除は今回スコープ外。`permissions.allow` の削除のみ実装し、それ以外のセクション (deny / hooks / 任意キー) は **read-only** で表示する。理由: DESIGN_PRINCIPLES「責務越境最小」— ユーザが意図的に書いた hooks を Palmux が GUI から「気軽に消せる」UI を提供すると逆に責務越境が広がる。permissions.allow は Palmux 自身が `Always allow` で書き込んでいるので、削除も Palmux が責任を持つべき。

## Implementation Decisions

- **Go パッケージ配置**: `internal/tab/claudeagent/settings_view.go` (read) と `settings_io.go` の拡張 (`removeFromProjectAllowList`, `removeFromUserAllowList`) で対応。既存の `addToProjectAllowList` と同じ atomic write 手法を踏襲。理由: DESIGN_PRINCIPLES「既存資産活用 > 新規実装」。
- **Read API 形状**: `GET /api/repos/{repoId}/branches/{branchId}/tabs/claude/settings` は `{ project: SettingsView, user: SettingsView }` を返す。`SettingsView` は `path` (絶対パス、UI 表示用) / `exists` (ファイル有無) / `permissionsAllow` / `permissionsDeny` / `hooks` (生 JSON / 表示用) / `other` (それ以外のキーを raw JSON で返す) の 5 セクション構成。理由: 04-ui-requirements「分類別に列挙」。
- **Delete API 形状**: `DELETE /api/repos/{repoId}/branches/{branchId}/tabs/claude/settings/permissions/allow?scope=project|user&pattern=...`。`pattern` クエリパラメータで完全一致削除。理由: REST 設計の対称性 (write 側が `addToProjectAllowList` で完全一致 idempotent appendなので削除も完全一致削除にする)。
- **User ホーム解決**: `os.UserHomeDir()` を使う (transcript.go と同じ)。テスト時は `t.Setenv("HOME", tmp)` で差し替える。理由: 既存パターン踏襲。
- **UI 実装**: `claude-settings-popup.tsx` を新規追加し、既存 `Modal` コンポーネント上にダイアログを乗せる。trigger は TopBar に `settings` ボタンを追加。confirm は `window.confirm` で簡易実装し、削除直後に再フェッチして UI 同期。理由: DESIGN_PRINCIPLES「具体的エラーメッセージ」+「楽観的更新」。`window.confirm` は既存コードでも使用例あり (composer.tsx 等)。
- **Backlog 追加**: hooks や deny のフル CRUD は S006 以降の課題に回す。本 Sprint は read + allow 削除のみ。

## Review Decisions

- **ESLint pre-existing false positives**: `react-hooks/refs` rule (eslint-plugin-react-hooks v7.1.1) misfires on `props.X` access inside JSX `onClick={...}` handlers. Pre-Sprint baseline: 31 errors / 7 warnings (claude-agent-view.tsx already had 5 of the same kind). Post-Sprint: 32 errors / 7 warnings — the +1 is the same rule misfiring on `onClick={props.onOpenSettings}`. Not a real defect; left as-is to avoid touching unrelated code. Settings popup itself is lint-clean. Go side is fully clean (`go vet ./...` passes, no golangci-lint installed).
- **Smoke test**: `make build` succeeds (frontend bundle + Go binary). `go test ./...` all pass including the 8 new tests in `settings_view_test.go`. Manual server smoke skipped per S002 task constraint (host palmux2 must not be restarted).

## Drift / Risk

- ホスト用 palmux2 (port 8207) は再起動しない。`make build` のコンパイル確認まで。
- `~/.claude/settings.json` の書換は本番ユーザの実環境を変えうる highly sensitive 操作なので、UI 上で「user スコープ」削除前に必ず確認モーダルを出す (DESIGN_PRINCIPLES No.5「責務越境最小」)。
