# Palmux v2.1 UI 要件書

> Claude Design でデザインを作成する際の入力ドキュメント。
> 全画面状態・コンポーネント・インタラクションを網羅する。
> v2 からの変更点: Activity Inbox 追加、⌘K コマンドパレット追加、Toolbar 2モード化

## 概要

Palmux は Web ベースのターミナルクライアント。開発者が複数のリポジトリ・ブランチを横断して、ターミナル（Claude Code / Bash）、ファイルブラウザ、Git 操作を1つの画面で行う。PC とスマートフォン両対応。

**複数の Claude Code を並行運用する** ユースケースを重視し、Activity Inbox で全ブランチの状態を集約的に把握できる。

## デザイントーン

- **Fog パレット v2.1**（独自デザインシステム）
- Accent: `#7c8aff`（ペリウィンクルブルー）、Accent Light: `#9ba6ff`、Accent Dark: `#5c6ae0`
- ダークモード（デフォルト）:
  - bg: `#0f1117`（ベース背景）
  - surface: `#13151c`（Drawer, Header, TabBar）
  - elevated: `#1a1c25`（カード, コードブロック, 入力フィールド）
  - border: `#1e2028`（区切り線）
  - border-hover: `#2a2d38`（ホバー時の区切り線）
  - fg: `#d4d4d8`（本文テキスト）
  - fg-muted: `#8b8fa0`（補助テキスト）
  - fg-dim: `#6b6f7b`（ラベル, プレースホルダ）
  - fg-faint: `#4a4e5c`（非活性, ヒント）
  - fg-ghost: `#3d4150`（最も薄いテキスト）
- ライトモード:
  - bg: `#fafafa`
  - surface: `#f4f4f5`
  - elevated: `#ffffff`
  - border: `#e4e4e7`
  - border-hover: `#d4d4d8`
  - fg: `#18181b`
  - fg-muted: `#52525b`
  - fg-dim: `#71717a`
  - fg-faint: `#a1a1aa`
  - fg-ghost: `#d4d4d8`
- セマンティックカラー:
  - success: `#64d2a0`（ファイルパス, 成功状態）
  - warning: `#f59e0b`（通知, 注意）— パルスアニメーション付き
  - error: `#ef4444`（エラー, urgent 通知）
  - info: `#7c8aff`（リンク, アクティブ状態 = accent と同一）
- ターミナル固有:
  - terminal-bg: `#0c0e14`（ターミナル領域の背景。bg より更に暗い）
  - terminal-yellow: `#e8b45a`（ファイル名ハイライト）
  - terminal-green: `#64d2a0`（パス, 成功出力）
  - terminal-blue: `#7c8aff`（キーワード, プロンプト）
  - terminal-gray: `#6b6f7b`（コメント, 枠線）
- ターミナルフォント: `"Geist Mono", "Cascadia Code", "Fira Code", monospace`
- UI フォント: `"Geist", "Noto Sans JP", -apple-system, BlinkMacSystemFont, sans-serif`
- テイスト: ニュートラルかつシャープ。紫みを排除したダーク基調。アクセント（`#7c8aff`）だけがブランド性を担い、それ以外は邪魔しない。情報密度が高い画面だが、微妙な層の差（bg → surface → elevated）で奥行きを作る

---

## 画面レイアウト（全体構造）

### PC（幅 ≥ 900px）

```
┌────────────┬──────────────────────────────────────┐
│            │ Header                                │
│            ├──────────────────────────────────────┤
│  Drawer    │ TabBar                                │
│  (pinned)  ├──────────────────────────────────────┤
│            │                                      │
│            │  Main Area (Terminal / Files / Git)    │
│            │                                      │
│            ├──────────────────────────────────────┤
│            │ Toolbar (非表示 or 表示)               │
└────────────┴──────────────────────────────────────┘

Activity Inbox: Header の 🔔 ボタンから右側ポップオーバーで開く
⌘K パレット: 画面中央にオーバーレイ表示
```

- Drawer: 左側に常時表示（ピン固定）。幅 200〜600px、ドラッグで調整可能

### PC（Split Mode 有効時）

```
┌────────────┬──────────────────┬───────────────────┐
│            │ Header           │ [repo▾][branch▾]  │
│            ├──────────────────┤ RightTabBar       │
│  Drawer    │ TabBar (left)    ├───────────────────┤
│            ├──────────────────┼───────────────────┤
│            │                  │                    │
│            │  Left Panel      │  Right Panel       │
│            │                  │                    │
│            ├──────────────────┴───────────────────┤
│            │ Toolbar                               │
└────────────┴──────────────────────────────────────┘
```

- 左パネル: Header + TabBar と連動
- 右パネル: ミニセレクタバー（repo ▾ + branch ▾）+ 独立 TabBar
- Divider: ドラッグで幅調整（20%〜80%）
- `Ctrl+Shift+←/→` でフォーカスパネル切り替え

### モバイル（幅 < 600px）

```
┌──────────────────────────────────────┐
│ Header (☰ + ブランチ名 + 🔔 + ...)   │
├──────────────────────────────────────┤
│ TabBar                                │
├──────────────────────────────────────┤
│                                      │
│  Main Area                            │
│                                      │
├──────────────────────────────────────┤
│ Toolbar (常時表示)                     │
├──────────────────────────────────────┤
│ IME Bar (ime モード時のみ)             │
└──────────────────────────────────────┘
```

- Drawer: 非表示。☰ ボタンで左からスライドイン
- Activity Inbox: 🔔 ボタンで右からスライドイン
- Split Mode: 無効
- Toolbar: 常時表示

---

## コンポーネント詳細

### 1. Header

横幅いっぱいのバー。高さ 40〜48px。

**左側:**
- ☰ ハンバーガーボタン — Drawer を開く（モバイル時 or Drawer 未ピン時に表示）
- 現在のブランチ名（テキスト表示。長い場合はトランケート+省略記号）

**右側（アイコンボタン群）:**
- [🔍] — ⌘K コマンドパレットを開く（クリック、または `⌘K` / `Ctrl+K` キーボードショートカット）
- [🔔] — Activity Inbox を開く。未読件数バッジ付き（amber 背景 + 白文字の丸バッジ）
- [Portman] — ポートリースがある場合のみ表示
- [GitHub] — GitHub リポジトリの場合のみ表示
- [⚙] — 設定メニュー（テーマ切り替え等のドロップダウン）
- [⊟] — Toolbar の表示/非表示トグル
- [⧉] — Split Mode のオン/オフトグル（幅 ≥ 900px のみ表示）
- 接続状態インジケータ — connected: 緑ドット / connecting: 黄色パルス / disconnected: 赤ドット

### 2. Drawer

左側のナビゲーションパネル。3セクション構成。ブランチの俯瞰・発見に使う。

```
★ Starred
├── ▼ tjst-t/palmux                  ☆→★ トグル
│   ├── develop ●                     ● = アクティブブランチ
│   ├── feature/new-ui 🤖💬           🤖 = Claude 動作中、💬 = 入力待ち
│   └── fix/reconnect-bug
└── ▼ tjst-t/hydra
    └── main ●

Repositories
├── ▼ tjst-t/ansible-nas
│   └── main ●
└── ▼ tjst-t/design-system
    └── main ●

Other Sessions
├── dev-server
└── scratch
```

**Starred セクション:**
- スター付きリポジトリが最上部に表示
- リポジトリ行の右端にスターアイコン（☆/★）トグル
- Starred に入ったリポジトリは Repositories セクションには重複表示しない

**Repositories セクション:**
- スターなしの Open リポジトリ
- 各リポジトリはクリックで展開/折りたたみ（▼/▶）
- リポジトリ名はホスト名を省略して `owner/repo` 形式

**共通操作:**
- ブランチ行クリック → そのブランチのタブセットを TabBar に展開
- アクティブブランチに ● ドット
- Claude 状態アイコン: 🤖（動作中）/ 💬（入力待ち — amber パルス）/ なし（停止中）
- ブランチ右クリック / 長押し → コンテキストメニュー: Close Branch

**ブランチピッカー:**
- 各リポジトリ展開部の下部に「+ Open Branch...」ボタン
- モーダル: テキストフィルタ + ブランチ一覧（Open / Local / Remote）

**フッター:**
- 「+ Open Repository...」ボタン

**Drawer 操作:**
- ピン固定トグル、幅調整（200〜600px ドラッグ）、ブランチソート

**Other Sessions:**
- 折りたたみ式。選択でフラットなウィンドウ一覧を TabBar に表示

**モバイル:** ☰ で左スライドイン。オーバーレイクリック or ブランチ選択で自動クローズ

### 3. Activity Inbox（新規）

全ブランチのエージェントイベントを集約するフィードパネル。
「通知を探しに行く」のではなく「通知が来る」UI。

**PC:** Header の 🔔 ボタンでポップオーバー表示（幅 360〜400px、右寄せ、高さは画面の 60〜80%）
**モバイル:** 🔔 ボタンで右からスライドインするパネル（幅 100%）

**表示内容:**
```
Activity                              [Mark all read]
─────────────────────────────────────────────────
🔴 palmux / feature/new-ui            2 min ago
   Claude is waiting for confirmation
   "Apply changes to 3 files?"
   [Yes (y)] [No (n)] [Open Branch →]

🔴 hydra / main                       5 min ago
   Claude stopped: permission denied
   [Resume] [Open Branch →]

⚪ ansible-nas / main                 12 min ago
   Claude completed task
   [Open Branch →]
─────────────────────────────────────────────────
```

**イベントタイプ:**

| アイコン | タイプ | 説明 | インラインアクション |
|---|---|---|---|
| 🔴 | urgent | Claude の確認待ち（y/n）、権限要求 | [Yes] [No] ボタン |
| 🟡 | warning | Claude 停止、エラー発生 | [Resume] ボタン |
| ⚪ | info | Claude 完了、タスク終了 | なし |

**インタラクション:**
- **インラインアクション**: ブランチに遷移せずに y/n 返答 or Resume が可能。ボタン押下で WebSocket 経由でターミナルにキー入力を送信
- **[Open Branch →]**: 該当ブランチの Claude タブに遷移
- **Mark all read**: 全通知を既読にする
- 各通知の右端に × ボタンで個別クリア
- 未読は背景色を微妙に明るくして区別
- 通知がない場合: 「No activity — all agents are idle.」メッセージ

**フィルタ:**
- デフォルト: 全イベント表示
- ヘッダーにフィルタチップ: [All] [Urgent] [Warnings]
- 将来: ブランチ単位でのミュート

**バッジ:**
- Header の 🔔 アイコンの右上に未読件数（urgent のみカウント）
- 0件の場合はバッジ非表示

### 4. ⌘K コマンドパレット（新規）

VS Code スタイルのコマンドパレット。画面中央にオーバーレイ表示。

**起動方法:**
- `⌘K` / `Ctrl+K` キーボードショートカット
- Header の 🔍 ボタンクリック
- モバイル: Header の 🔍 ボタンタップ

**レイアウト:**
```
┌──────────────────────────────────────────┐
│ 🔍 [入力フィールド]                       │
├──────────────────────────────────────────┤
│ Workspaces                               │
│   📂 palmux / develop          ● active  │
│   📂 palmux / feature/new-ui   💬 waiting│
│   📂 hydra / main              🤖 running│
│                                          │
│ Files                                    │
│   📄 src/main.go          palmux/develop │
│   📄 README.md            hydra/main     │
│                                          │
│ Commands                                 │
│   > make build                           │
│   > make test                            │
│   > npm run dev                          │
└──────────────────────────────────────────┘
```

**検索カテゴリ:**

| プレフィクス | カテゴリ | 検索対象 |
|---|---|---|
| （なし） | 全て | Workspaces + Files + Commands を混合表示 |
| `@` | Workspaces | Open 中のブランチ一覧 |
| `/` | Files | 全ワークスペース横断のファイル名検索 |
| `>` | Commands | 自動検出コマンド（make, npm 等）+ スラッシュコマンド |
| `:` | Slash | Claude スラッシュコマンド（/compact, /clear 等） |

**インタラクション:**
- テキスト入力でリアルタイムフィルタリング（fuzzy match）
- ↑↓ で選択、Enter で実行
- **Workspace 選択**: そのブランチに切り替え
- **File 選択**: そのブランチに切り替え + Files タブでファイルを開く
- **Command 選択**: アクティブブランチのターミナルにコマンドを送信
- **Slash 選択**: アクティブブランチの Claude タブにコマンドを送信
- `⌘Enter` / `Ctrl+Enter`: Split モードの右パネルで開く（Workspace / File）
- Esc / オーバーレイクリックで閉じる

**デフォルト表示（入力なし）:**
- 最近使ったブランチ（上位 5件）
- 最近開いたファイル（上位 5件）
- 推奨コマンド（自動検出 上位 5件）

### 5. TabBar

Header の直下。高さ 36〜40px。

**タブ構成（ブランチ選択時）:**
```
[🧠 Claude] [📁 Files] [⎇ Git] [$ Bash] [$ Bash 2] [+]
```

- 各タブにアイコン + ラベル
- アクティブタブは下線 or 背景色で強調
- Claude タブに通知がある場合: amber バッジ
- タブ数が多い場合: 水平ドラッグスクロール（5px 以上でドラッグ判定）
- 右端に [+] ボタン（Bash タブ追加）

**タブコンテキストメニュー（右クリック / 長押し）:**
- Claude タブ: Restart（モデル選択） / Resume
- Bash タブ: Rename / Delete（最後の1つは Delete 無効）
- Files / Git: メニューなし

**Orphan Session 選択時:**
- tmux ウィンドウ名をそのまま表示

### 6. Main Area

TabBar の下、Toolbar の上。残りの全スペースを占有。

**ターミナルタブ（Claude / Bash）:**
- xterm.js でレンダリング。背景はターミナル色
- 切断時: 半透明オーバーレイ + "Reconnecting..." + スピナー
- フォントサイズ: 8〜24px（ピンチズーム or Toolbar A-/A+）

**Files タブ:**
```
┌──────────────────────────────────────┐
│ 📁 src / components / header.tsx      │ ← Breadcrumb
├──────────────────────────────────────┤
│ 🔍 [検索フィールド] [Aa] [.*] [Grep] │ ← 検索バー
├──────────────────────────────────────┤
│ 📁 components/          2026-04-03   │
│ 📁 hooks/               2026-04-02   │
│ 📄 main.tsx      1.2KB  2026-04-03   │
│ 📄 App.tsx       3.4KB  2026-04-01   │
└──────────────────────────────────────┘
```
- ディレクトリクリック → 配下の一覧
- ファイルクリック → プレビュー（Markdown, コード, 画像）
- Breadcrumb: 各セグメントがクリッカブル
- 検索: ファイル名（glob）/ grep 切り替え + Aa + .* オプション
- grep 結果の行クリック → ファイル + 該当行スクロール + 黄色ハイライト（3s フェードアウト）

**Git タブ:**
```
┌──────────────────────────────────────┐
│ [Status] [Log] [Diff] [Branches]     │ ← サブタブ
├──────────────────────────────────────┤
│ Staged (2)                           │
│   ✓ src/main.go         [Unstage]    │
│   ✓ src/handler.go      [Unstage]    │
│ Unstaged (3)                         │
│   ● src/store.go        [Stage] [X]  │
│   ● README.md           [Stage] [X]  │
│   ● go.mod              [Stage] [X]  │
└──────────────────────────────────────┘
```
- Status: Staged / Unstaged グルーピング + Stage/Unstage/Discard ボタン
- Diff: ファイル > hunk 単位表示 + hunk アクションボタン + Discard 確認ダイアログ
- Log: コミット一覧
- Branches: ローカル / リモート一覧

### 7. Toolbar

Main Area の下。高さ 44〜52px。**2モード構成**。

**通常モード:**
```
上段: [Ctrl] [Alt] [Esc] [Tab] [↑] [↓] [←] [→] [PgUp] [PgDn] [/|] [-_] [A-] [A+] [あ] [🎤]
下段: [^C] [^Z] [^D] [^L] [^R] [^A] [^E] [^W] [^U] [^K]
```
- 上段: 修飾キー + 特殊キー + 矢印 + フォント + IME + 音声
- 下段: よく使う制御文字。横スクロール可能
- Ctrl / Alt: ワンショット（タップ）/ ロック（長押し 400ms）
- 矢印キー: 長押しで連続送信（400ms → 80ms リピート）
- `/|` と `-_`: タップで主キー、上スワイプで代替キー
- [あ]: キーボードモード切り替え（none → direct → ime → none）
- [🎤]: Web Speech API 対応時のみ表示

**Claude モード:**
```
上段: [y] [n] [↑] [⏎] [^C] [Esc]
下段: [/compact] [/clear] [/help] [/cost] [/status]
```
- Claude タブにフォーカスで自動切替、離れると自動で通常モードに戻る

**コマンド実行:**
- ⌘K パレットの `>` プレフィクスで自動検出コマンドにアクセス（Toolbar にコマンドモードは設けない）

**表示トグル:**
- Header の [⊟] ボタン。PC デフォルト: 非表示 / モバイル デフォルト: 表示

### 8. IME Bar

Toolbar の下（ime モード時のみ表示）。

```
┌──────────────────────────────────────┐
│ [テキスト入力フィールド]        [Send] │
└──────────────────────────────────────┘
```

- Enter: 確定テキスト + \r 送信
- Shift+Enter: テキストのみ送信
- Backspace（空）: \x7f 送信
- Toolbar 非表示時は IME Bar も非表示

### 9. コンテキストメニュー

PC: 右クリック位置。モバイル: 長押し → 画面中央。
ビューポートはみ出し自動調整。React Portal。

### 10. モーダル / ダイアログ

半透明オーバーレイ + 中央カード。× / Esc / オーバーレイクリックで閉じる。
使用箇所: ⌘K パレット、ブランチピッカー、リポジトリピッカー、Claude Restart、Discard 確認、設定

### 11. 通知バッジ / インジケータ

- **Header 🔔 ボタン**: 未読件数バッジ（urgent のみ、amber + 白文字）
- **Drawer ブランチ行**: Claude 状態（🤖 動作中 / 💬 入力待ち amber パルス）
- **TabBar Claude タブ**: amber バッジ
- **Header 接続状態**: green / yellow pulse / red ドット
- Activity Inbox 確認 or Claude タブフォーカスで通知クリア

---

## 画面状態一覧

### 必須画面

1. **PC ダーク（通常）** — Drawer ピン固定、ターミナル表示
2. **PC ダーク（Split）** — 左右ターミナル、Divider
3. **PC ダーク（Files）** — ファイル一覧
4. **PC ダーク（Git Status）** — Staged / Unstaged
5. **PC ダーク（Git Diff）** — hunk + アクションボタン
6. **PC ダーク（Activity Inbox）** — 🔔 ポップオーバー、複数通知
7. **PC ダーク（⌘K パレット）** — 検索結果表示
8. **PC ライト（通常）** — テーマ確認
9. **モバイル ダーク（ターミナル + Toolbar）** — 2段 Toolbar
10. **モバイル ダーク（Drawer）** — 左スライドイン
11. **モバイル ダーク（Activity Inbox）** — 右スライドイン
12. **モバイル ダーク（IME）** — IME Bar + ソフトキーボード

### 補助画面

13. **ブランチピッカー** — フィルタ + カテゴリ
14. **Toolbar Claude モード** — 自動切替時
15. **⌘K（> コマンド検索）**
16. **⌘K（/ ファイル検索）**
17. **再接続オーバーレイ**
18. **コンテキストメニュー**
19. **grep 検索結果**
20. **Activity Inbox（空）**

---

## キーボードショートカット

| ショートカット | 動作 |
|---|---|
| `⌘K` / `Ctrl+K` | コマンドパレットを開く |
| `Ctrl+Shift+←` | 左パネルにフォーカス（Split 時） |
| `Ctrl+Shift+→` | 右パネルにフォーカス（Split 時） |
| `Ctrl+V` / `Cmd+V` | ペースト |
| `Esc` | パレット / Inbox / モーダルを閉じる |

---

## レスポンシブブレークポイント

| 幅 | レイアウト |
|---|---|
| ≥ 900px | PC フル。Drawer ピン、Split 可 |
| 600〜899px | PC コンパクト。Drawer ピン可、Split 無効 |
| < 600px | モバイル。Drawer モーダル、Toolbar 常時、Split 無効 |

---

## アニメーション / トランジション

- Drawer スライドイン: 200〜300ms ease-out
- Activity Inbox スライドイン（モバイル）: 200〜300ms ease-out
- Activity Inbox ポップオーバー（PC）: 150ms fade-in + scale(0.95→1)
- ⌘K パレット: 100ms fade-in + translateY(-8px→0)
- タブ切り替え: 即時（アニメーションなし）
- 通知パルス: amber pulse（1.5s 間隔）
- grep ハイライト: 黄色 → 3s フェードアウト
- Divider ドラッグ: リアルタイム追従
- モーダルオーバーレイ: 150ms fade-in
- 再接続オーバーレイ: 200ms fade-in / fade-out
- 修飾キーワンショット: 200ms ハイライト
- Inbox 通知到着: リスト上部にスライドイン（200ms）
