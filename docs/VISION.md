# プロダクトビジョン

## 一言で言うと

ブラウザから tmux と複数の Claude Code エージェントを並行運用できる
Web ターミナルクライアント。PC とモバイルから同じ環境にアクセスでき、
**シングルユーザ・自前ホスティング前提**。

## 誰のために

複数リポジトリ・複数ブランチで Claude Code を併走させる **個人開発者**。
自分のサーバー / VPS / ホームラボに常駐させ、PC・スマホ・タブレットから
同じ作業環境を使いたい人。

## 何を解決するか

- ターミナル + AI エージェントを切り替えるオーバーヘッド
- 複数 Claude Code を並行運用したときの「いま何が動いてる？」「権限要求来てる？」の見えなさ
- デスクトップアプリがモバイルから使えない（外出先で進捗が見られない／タップで操作できない）
- セッション履歴・添付画像・下書きが画面遷移で散逸する

## 現在の状態 (v0.1.0)

- **Phase 1 完了**: stream-json + MCP 経由で claude CLI を制御、5 種ブロック描画
- **Phase 2 完了**: 入力補完 (slash/@-mention/画像) / ツール出力リッチ化 / Drawer pip / Activity Inbox 統合 / セッション履歴 popup / Permission Edit / Always-allow / Effort・モデル動的選択
- 単一バイナリ配布、Linux amd64/arm64 のリリース自動化済み
- モバイル対応: 画面幅収まり / Files タブ preview-only / scroll-to-latest 等

## 今後の方向性

- **Phase 3** (進行中): Plan モード UI / `.claude/settings.json` editor / Sub-agent ツリー / MCP server 表示 / Hook events / `--add-dir`+`--file`
- **Phase 4**: 磨き込み (virtualization / syntax highlight / 会話内検索 / export / モバイル UX 総点検)
- **Phase 5+**: 需要が明確になってから検討

詳細は [`docs/original-specs/06-claude-tab-roadmap.md`](original-specs/06-claude-tab-roadmap.md) と
スプリント単位の [`docs/ROADMAP.md`](ROADMAP.md) 参照。

## スコープ外（やらないこと）

- **マルチユーザ / マルチテナント / SaaS 化** — シングルユーザ前提を覆さない。OIDC / OAuth ログインも対象外
- **Git ホスティング機能** — GitHub / GitLab の代替にはならない
- **IDE 機能** — LSP / 補完 / デバッガ / リファクタリング
- **音声入力** — spec 8 章で skip 確認済
- **iOS/Android ネイティブアプリ** — PWA は範囲内
- **Claude Code 以外のエージェント単独サポート** — Phase 6 候補としての Provider 抽象化はあるが、需要が出てから

## 技術的な制約・前提

- 言語: Go 1.25 (バックエンド) / TypeScript + React 19 (フロントエンド)
- 配布: 単一バイナリ（フロントエンドは `embed.FS`）
- 必須外部ツール: tmux ≥ 3.2 / git / ghq / gwq
- 任意外部ツール: claude CLI / portman / Caddy
- WebSocket + REST のハイブリッド通信
- セッション管理は tmux に委譲（独自実装しない）

## 参考にするプロダクト・デザイン

- **Claude Code Desktop** — Claude タブの対話 UI / ツール折りたたみ / 権限フロー
- **VS Code** — ⌘K コマンドパレット
- **iTerm2 / WezTerm** — ターミナル分割の感覚
- **Linear** — Activity Inbox の通知集約スタイル
- カラー: Fog palette v2.1 (Accent `#7c8aff`)
- ターミナルフォント: Geist Mono / Cascadia Code / Fira Code
- UI フォント: Geist / Noto Sans JP
