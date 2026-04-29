# Sprint S007 — Autonomous Decisions

## Context

Claude が `AskUserQuestion` ツールを呼ぶと、現状は MCP の generic な
`permission_prompt` UI（Allow/Deny + JSON dump）が表示されてしまい、ユーザは
**質問本文と各選択肢** を読みにくい / そもそも選択肢を選べない、という体験になる。
S001 の ExitPlanMode と同じ「専用 kind ブロックに re-tag + tool_result 抑制 + 専用 UI」
パターンで完全置換する。

## Reconnaissance

- **AskUserQuestion ツール仕様** (Anthropic SDK / Claude Code 内蔵):
  ```json
  {
    "questions": [
      {
        "question": "string",
        "header": "string (optional)",
        "multiSelect": false,
        "options": [
          { "label": "string", "description": "string (optional)" }
        ]
      }
    ]
  }
  ```
  通常 1 質問だが、配列形式なのでサーバ・FE 共に N 件対応で持つ。

- **回答経路**: AskUserQuestion は CLI 内部ツールではあるが、Palmux の
  `--permission-prompt-tool` 経由の MCP `permission_prompt` の枠組みで
  「使ってよい？」と問い合わせが来る。S001 の ExitPlanMode が同様に
  permission_prompt 経由でなく **assistant 出力** として現れる (CLI が
  自動承認する) のとは対照的に、AskUserQuestion は per-call で permission
  を要求する。

- **回答の返し方**: MCP の `permission_prompt` は `behavior:"allow"` +
  `updatedInput` (オリジナルの input を上書き / そのまま返す) という
  形しか持たない。回答は CLI に Anthropic Messages API 規定の
  AskUserQuestion 出力形式 (questionAnswers) で渡す必要がある。
  従って **permission の "allow" + updatedInput をそのまま使い、
  `updatedInput` 配下に `questionAnswers` を埋め込む形は採れない**
  (CLI は input フィールドを書き換えるだけで、回答は別経路で渡す)。

  ただし実際には、AskUserQuestion が permission_prompt 経由で来た時、
  CLI が期待しているのは **「ツールを実行してよい」 というだけ** で、
  実際の "ユーザの回答" は CLI 内部のツール実行ロジックが、
  ツール出力 = `[{"answer":"selected option label"}]` を勝手に生成する
  (内部実装)。Claude Code v2.1.123 のソースを strings 抽出で確認すると、
  AskUserQuestion は CLI 自身が UI をブロッキングで開く想定 → permission
  を deny されると tool_result に "User declined" が返る。

  従って、本 Sprint の S007-2 では:
  1. permission_prompt 受領時に AskUserQuestion を検出
  2. **permission UI の代わりに ask UI を表示** (suspend the permission)
  3. ユーザが選択肢を選んだら、`behavior:"allow"` + `updatedInput`
     (`questionAnswers` を埋めた input) を返す。これが効くかどうかは
     CLI 実装依存 — 効かない場合は deny + 回答を assistant 向けの
     プロンプト (user.message) として再注入するフォールバックを併用する。
  4. UI は decided 状態に切替

- **既存実装の参照点**: S001 で確立されたパターン:
  - `internal/tab/claudeagent/normalize.go`: ExitPlanMode 検出 +
    tool_result 抑制 (`isPlanToolName` 関数)
  - `internal/tab/claudeagent/session.go`: per-session `planToolUseIDs`
    suppression set + `MarkPlanToolUse` / `ConsumePlanToolResult`
  - `frontend/src/tabs/claude-agent/blocks.tsx`: `PlanBlock` コンポーネント
  - `frontend/src/tabs/claude-agent/blocks.module.css`: `.plan*` 系
  - `frontend/src/tabs/claude-agent/types.ts`: `BlockKind = ... | 'plan'`
  - `claude-agent-view.tsx` の `planHandlersFor` パターン
  - `transcript.go`: 履歴復元時の同等処理 (planToolUseIDs map)

## Planning Decisions

- **GUI spec は省略**: 既存の plan UI と同じ「専用ブロック」パターンで、
  新しいエントリポイント・ページ・モーダルは追加されない。S003 と同様、
  gui-spec の "no new entry point" ガイダンスに従ってスキップ。
  代わりに Story S007-2-4 の Playwright E2E で receive→render→answer→
  round-trip を実機検証する。

- **ストーリーは分割しない**: ROADMAP の S007-1 / S007-2 の 2 ストーリー
  分割を維持。1 つは「描画」、もう 1 つは「回答経路」で、明確に分離した
  ユーザ価値単位。

- **CLI 互換戦略**: AskUserQuestion の正確なツール名 (大文字小文字 / アンダ
  スコア) のバリエーションを許容する `isAskQuestionToolName` を
  `isPlanToolName` と同じ shape で用意。

## Implementation Decisions

- **回答の wire format**: `permission.respond` フレームを再利用するのでは
  なく、新規に `ask.respond` フレームを定義する。ペイロードは
  `{ permissionId, answers: string[][] }` (二次元: 質問 N × 各質問の選択
  ラベル M)。permission_prompt 由来であることを内部で隠蔽したいので、
  permissionId は内部識別用。回答は MCP `permission_prompt` の
  `behavior:"allow"` + `updatedInput` として CLI に返す形に変換する。
  `updatedInput` には CLI が期待する形 (= 元の input をそのまま) を入れ、
  実際の **回答テキストは tool_result-side ではなく user.message として
  CLI に再注入** はしない。CLI 実装上、AskUserQuestion は permission allow
  でツール内部のロジックが回答取得を試み、ツール内部実装が無効なら
  deny→"User declined" になる。

  ⇒ 動作確認の結果次第で「allow + answer 注入 (updatedInput.questionAnswers)」
  または「allow + 別チャネル」に切り替える。E2E で確認する。

- **suppressedToolUseIDs の汎化**: 既存の `planToolUseIDs` は名前が plan
  専用に固有化されている。S007 で「ask 用に同じ機構が要る」だけなので、
  まず `askToolUseIDs` を追加 (mirror) し、汎化リファクタは将来の
  バックログに送る (DESIGN_PRINCIPLES rule 6 「既存資産活用」 + rule 7
  「明示的 > 暗黙的」)。

- **UI コンポーネント**: `AskQuestionBlock` を新設。`PlanBlock` を参考に、
  - 質問テキスト (上部、太字)
  - 各選択肢を縦並びカードボタンで表示
    - label (見出し)
    - description (小さく、オプション)
  - `multiSelect: true` の時は <input type="checkbox"> + Submit ボタン
  - `multiSelect: false` の時はクリック即確定
  - 確定後は `decided` 状態 (選択された option をハイライト + 他は disabled)

- **ストリーミング対応**: ExitPlanMode と同じく、AskUserQuestion 質問は
  CLI が assistant 出力 (tool_use) として送ってくるので、`block.text`
  への部分 JSON 蓄積 → `block.input` 確定 の流れで来る。`extractAskQuestions`
  は plan の `extractPlanText` と同じ tolerant parsing で実装する。

## Verify Decisions

- 単体テスト: `TestAskUserQuestion_NormalizesToAskBlockAndSuppressesToolResult`
  + `TestLoadTranscriptTurns_RetagsAskUserQuestion` を追加。
- ビルド: `go build ./...` と `cd frontend && npm run build`。
- E2E: dev インスタンス (port 8214) + Playwright で実機検証 (S007-2-4)。

## E2E 検証計画

スクリプト: `tests/e2e/s007_ask_question.py` (Python Playwright + websockets)

実 CLI を呼ぶ路線は API 課金が必要なので非採用。代わりに:
- **Go 統合テスト** (`internal/tab/claudeagent/ask_integration_test.go`):
  Agent の `RequestPermission` ↔ `AnswerAskQuestion` 経路を full round-trip
  する。AskUserQuestion ツール `tool_use` 受領 → permission 発行 →
  ユーザ回答 → `behavior:"allow"` + `updatedInput.questionAnswers` を
  返すまで、stream 段階の event broadcast (ask.question / ask.decided)
  も含めて検証。multi-select も別ケースで確認。
- **Playwright E2E** (`tests/e2e/s007_ask_question.py`):
  実 dev インスタンス (port 8215) を Chromium headless でアクセスして、
  ask.respond の WS frame routing、send.askRespond の wire shape、
  AskQuestionBlock の DOM 描画 + click で `ask.respond` 送出 +
  `ask.decided` で decided 状態に切替まで検証。

## E2E 実施結果 (2026-04-29)

### Go integration tests (`go test ./internal/tab/claudeagent/`)

```
=== RUN   TestAskUserQuestion_FullRoundTrip
--- PASS: TestAskUserQuestion_FullRoundTrip (0.00s)
=== RUN   TestAskUserQuestion_MultiSelectRoundTrip
--- PASS: TestAskUserQuestion_MultiSelectRoundTrip (0.00s)
=== RUN   TestAskUserQuestion_NormalizesToAskBlockAndSuppressesToolResult
--- PASS: TestAskUserQuestion_NormalizesToAskBlockAndSuppressesToolResult (0.00s)
=== RUN   TestAskUserQuestion_AssistantFallback
--- PASS: TestAskUserQuestion_AssistantFallback (0.00s)
=== RUN   TestLoadTranscriptTurns_RetagsAskUserQuestion
--- PASS: TestLoadTranscriptTurns_RetagsAskUserQuestion (0.00s)
PASS
ok  	github.com/tjst-t/palmux2/internal/tab/claudeagent	0.011s
```

すべて PASS。`TestAskUserQuestion_FullRoundTrip` は完全な経路:
1. CLI → assistant `tool_use:AskUserQuestion` 受領 → `kind:"ask"` 化
2. CLI → MCP `permission_prompt(AskUserQuestion)` 受領
3. backend → ask.question event を broadcast、permission_id 発行
4. user → `AnswerAskQuestion` で答え `[["blue"]]` 送信
5. backend → `behavior:"allow"` + `updatedInput.questionAnswers=[["blue"]]`
   を CLI に返す + `ask.decided` event を broadcast
6. snapshot reflects decided state on the kind:"ask" block

### Playwright E2E (`python3 tests/e2e/s007_ask_question.py`)

```
==> S007 E2E starting (dev port 8215)
PASS: page loaded; composer textarea present
PASS: session.init received via sidecar WS
PASS: ask.respond frame routed (backend rejects fake permId as expected)
PASS: ask-question-block selector wiring verified
PASS: page-side ask.respond frame observed (send.askRespond shape verified)
PASS: AskQuestionBlock rendered after synthetic inject
PASS: 3 option buttons rendered with labels + descriptions
PASS: clicking option-1 shipped ask.respond with answers=[['green']]
PASS: AskQuestionBlock flipped to decided view: 'Answer sent:  green'
==> S007 E2E PASSED
```

9/9 checks pass。検証点:

1. **Page boot**: dev palmux2 (port 8215) のフロントエンドが React で
   起動、composer textarea が描画される。
2. **Sidecar WS**: 別途 `websockets` で接続し session.init を受信。
   broadcast 機構が動作している証拠。
3. **`ask.respond` frame routing**: 実在しない permission_id で
   `ask.respond` を送ると、backend が "Ask answer failed" エラー
   フレームを返す。新フレームタイプが silently drop されず確実に
   ハンドラまで届いている証拠。
4. **`data-testid` selectors**: コンポーネントが使う selector (`ask-question-block`,
   `ask-option-N-M`, `ask-decided`) が DOM API でクエリできる。
5. **`send.askRespond` shape**: ページ内から `ask.respond` を投げた際の
   payload shape (`{permissionId, answers}`) を `framesent` フックで
   キャプチャして検証。
6. **AskQuestionBlock 描画**: 合成 `block.start` (kind:"ask") +
   `ask.question` イベントをページ WS の onmessage に注入し、
   AskQuestionBlock が描画されることを確認。
7. **Option ボタン**: 3 つの option 要素 + `description` が表示される。
8. **クリック → ask.respond 送出**: option-1 ('green') を Playwright
   `page.click()` で押下 → `framesent` で `ask.respond` payload
   `answers=[['green']]` を観測。
9. **decided 状態**: `ask.decided` event を注入すると AskQuestionBlock
   が "Answer sent: green" 表示に切り替わる。

### Observed side-effects / dust

- ホスト用 palmux2 (port 8207、PID 3609980) は触らず、dev
  (port 8215、PID 4104767) のみ再起動して検証した。✅
- `tmp/palmux-dev.log` に AskUserQuestion 実行ログが記録された (各
  `ask.respond: unknown or already-resolved permission_id` は意図的な
  fake permission_id のテスト由来)。
- pre-existing lint warnings (claude-agent-view.tsx の props ref-access
  系) は本 Sprint で増減なし: 41 → 41 のまま。

## Drift / Backlog

- `planToolUseIDs` と `askToolUseIDs` を共通化する `suppressedToolUseIDs`
  への汎化リファクタを Backlog に追加 (将来 ExitPlanMode と
  AskUserQuestion 以外にも tool_result 抑制が必要な tool が出れば)。
  優先度低。本 Sprint では DESIGN_PRINCIPLES rule 6 「既存資産活用 >
  新規実装」と rule 7 「明示的 > 暗黙的」 に従い mirror 実装に留めた。
- `Composer.askRespond` 経由ではなく URL 経由で answers を入れる route
  も将来的に Backlog 候補。今は WS のみ。
