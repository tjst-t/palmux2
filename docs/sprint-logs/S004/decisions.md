# Sprint S004 — Autonomous Decisions

## Context

`system/init` already returns the MCP server connection statuses
(`MCPServerInfo{Name, Status}`), and `internal/tab/claudeagent/normalize.go`
calls `Session.SetMCPServers(...)` so they are kept on the live `Session`.
`Session.Snapshot()` exposes them as `SessionInitPayload.MCPServers` (JSON
key `mcpServers`). The frontend already declares the right TopBar prop type
(`{ name: string; status: string }[]`) but **passes a literal `[]`** at
`claude-agent-view.tsx:210`. The whole sprint is the missing FE plumbing
plus a popup UI.

## Reconnaissance findings

- **Wire shape (BE → FE)**:
  - Backend struct (`protocol.go`):
    `MCPServerInfo { Name string `json:"name"`, Status string `json:"status"` }`.
  - Statuses observed in CLI emit (per Anthropic SDK doc + recent CLI):
    `"connected"`, `"connecting"`, `"failed"`, `"needs-auth"`, `"disconnected"`,
    `"error"`. UI must accept any string and degrade gracefully.
- **No mid-session refresh**: There is no `mcp.update` event today — MCP
  status only lands once, in `system/init`. If the user reconnects the
  page, the snapshot replays the most-recent statuses. The sprint
  acceptance criterion is "表示のみ" so a single source of truth is fine.
- **Empty-state path**: When CLI starts with no MCP servers (the common
  dev / unauthed case), `mcpServers` is empty / nil. The TopBar pip and
  popup must render gracefully (zero servers → muted "No MCP servers"
  text, no badge).
- **TopBar is razor-thin (32 px)** — adding a button needs to fit visually.
  Existing `iconBtn` class is reusable.

## Planning Decisions

- **No new entry point page / no new route**: per S003 / S007 precedent
  ("no new entry point" → skip gui-spec full pass). UI lives inside
  the Claude tab TopBar as a popup, exactly as ROADMAP S004-1 task list
  spells out.
- **Single Story, three tasks** (mirror ROADMAP). No story split needed —
  it is genuinely one user-facing capability ("see MCP server health").
- **Reducer wiring**: extend `AgentState` with `mcpServers: { name; status }[]`
  and pull the value from `session.init`. Re-export through `claude-agent-view`
  to the existing TopBar prop.
- **Popup pattern**: re-use the shape of `SettingsPopup` /
  `HistoryPopup` (open/close state in the parent, anchored near the
  triggering icon button). The popup is read-only — no controls beyond
  close. This keeps with VISION "Phase 3 ではスコープ外 (再起動など)".
- **Status semantics**:
  - `connected` → green (`--color-success`)
  - `connecting` → amber pulse (`--color-warning`, animated)
  - everything else (`failed`, `needs-auth`, `disconnected`, `error`,
    unknown) → red (`--color-error`)
  - The TopBar pill collapses these into a one-glance summary:
    `OK` (all connected), `N/M` (partial connected), `✕` (any failure),
    `—` (no servers configured).

## Test strategy

1. **Go unit test** in `internal/tab/claudeagent/` confirming
   `Session.SetMCPServers` round-trips through `Snapshot().MCPServers`.
   This locks the wire contract so future refactors don't silently drop it.
2. **Frontend reducer test** in `agent-state.test.ts` that feeds a
   `session.init` with a populated `mcpServers` and asserts the reduced
   state surfaces them.
3. **Playwright E2E** (`tests/e2e/s004_mcp_indicator.py`):
   - dev instance via `make serve INSTANCE=dev`
   - synthetic `session.init` with three servers (one connected, one
     failed, one connecting)
   - assert TopBar pill renders, click opens popup, popup lists servers
     with correct status badges
   - assert empty-state (no servers) renders "No MCP servers"
4. **Empty state test**: ensure the host environment (where claude CLI
   may not be authed) does not crash.

## Implementation Decisions

- **`agent-state.ts` reducer**: extended `AgentState` with
  `mcpServers: MCPServerInfo[]` (initial = `[]`). The `init` reducer
  case takes `p.mcpServers ?? []` from the incoming `SessionInit` —
  the snapshot ships `mcpServers: undefined` when the CLI hasn't
  emitted `system/init` yet, and empty when no MCP servers are
  configured, so the `?? []` defends both. The dedicated
  `session.replaced` case re-uses the regular `init` path
  (reduce(initialState, ...)), so MCP state is reset alongside the
  rest of the session — verified on real reconnect via Playwright.
- **`mcp-popup.tsx` + `mcp-status.ts` split**: original draft had the
  popup component file export both the `MCPPopup` React component and
  pure helper functions (`statusTone`, `rollupTone`, `MCPStatusTone`).
  Vite's `react-refresh/only-export-components` rule flagged this
  (HMR cannot fast-refresh a file that mixes components with non-
  components). Split helpers into `mcp-status.ts`. Net lint count
  improved by 2 (45 → 43; remaining 43 are pre-existing in unrelated
  files — same baseline noted in S007 decisions log).
- **No `mcp.update` event added**: Phase 3 spec is "display only" and
  the CLI only emits MCP state once per session via `system/init`. If
  / when CLI starts streaming reconnect events we'll add a dedicated
  reducer case. Not in scope for S004.
- **TopBar summary format**: chose `mcp N/M` (count of connected over
  total) with a status pip on the leading edge — at-a-glance health
  in 32 px, opens a full read-only popup on click. `—` for the
  empty-state case so the visual element is preserved (avoids the
  TopBar visually shifting on first session.init).
- **Last-connection timestamp is omitted**: the original ROADMAP
  S004-1-2 task description mentioned "最終接続時刻も見られる". The
  CLI's `system/init` payload (verified in protocol.go and the actual
  wire format from CLI 2.1.123) only includes `name + status` —
  there's no last-connect timestamp to show. Surfacing a synthetic
  "ever since the page loaded" timestamp would be misleading. Logged
  here because the ROADMAP wording was aspirational; the acceptance
  criteria themselves only require name + status, which we satisfy.
- **CLI vocabulary tolerance**: `statusTone` accepts `connected`,
  `connecting`, `failed`, `error`, `needs-auth`, `auth-required`,
  `disconnected`, `closed`, `pending`, `starting`, `ready`, `ok`. Any
  other string falls through to a neutral `unknown` tone — the
  status text is still shown verbatim in the badge, so users see
  whatever the CLI emitted even if the colour is muted.

## Test strategy executed

1. **Go unit tests** (`internal/tab/claudeagent/mcp_servers_test.go`):
   - `TestSystemInit_PopulatesMCPServersOnSnapshot` — covers normalize
     + Snapshot round-trip with three servers.
   - `TestSnapshot_MCPServers_IsADefensiveCopy` — guards against
     mutations of the snapshot leaking into the live session.
   - `TestSnapshot_NoMCPServers_EmptyByDefault` — empty-case sanity.
2. **No frontend unit-test framework in repo** (`vitest` not
   installed). Reducer + UI behaviour covered by Playwright E2E
   below.
3. **Playwright E2E**: `tests/e2e/s004_mcp_indicator.py`. Headless
   Chromium against dev palmux2 (port 8215). 11 checks across two
   states (empty + populated).

## Verify Decisions

- **Frontend lint baseline**: 43 problems remain (down from 45). All
  43 are pre-existing in unrelated files (files-view.tsx, git-diff.tsx,
  git-status.tsx — `react-hooks/set-state-in-effect`). The two
  pre-existing `react-refresh/only-export-components` errors that
  would have flagged my `mcp-popup.tsx` are avoided by the
  `mcp-status.ts` split. Verified with grep: zero new lint errors
  attributable to S004 files (mcp-popup.tsx, mcp-status.ts,
  agent-state.ts, types.ts). The `claude-agent-view.tsx`
  `props.X-access` warnings are the same legacy 41 documented in
  S007 decisions log.
- **Go test**: `go test ./...` all PASS (cached for unrelated
  packages, fresh for `internal/tab/claudeagent` with the new
  mcp_servers_test.go).

## E2E 検証計画

スクリプト: `tests/e2e/s004_mcp_indicator.py` (Python Playwright)

ホスト用 palmux2 (PID 2576959, port 8207) は **絶対に触らない**。dev
instance (`make serve INSTANCE=dev`, PID 3157312, port 8215) のみ再起動
してテスト。

実 CLI で MCP サーバを叩く路線は CI 不可なので非採用。代わりに合成
`session.init` を WS proxy 経由で `dispatchEvent` で投入し、
- 空状態 (CLI が未だ system/init を出していない or MCP サーバ ゼロ)
- 3 サーバ (connected, failed, connecting) の rollup-worst-tone
の双方を実機 (本物の prod build バイナリ + 本物の React + 本物の WS
ハンドラ) で検証する。

## E2E 実施結果 (2026-04-30)

### Go integration tests

```
=== RUN   TestSystemInit_PopulatesMCPServersOnSnapshot
--- PASS: TestSystemInit_PopulatesMCPServersOnSnapshot (0.00s)
=== RUN   TestSnapshot_MCPServers_IsADefensiveCopy
--- PASS: TestSnapshot_MCPServers_IsADefensiveCopy (0.00s)
=== RUN   TestSnapshot_NoMCPServers_EmptyByDefault
--- PASS: TestSnapshot_NoMCPServers_EmptyByDefault (0.00s)
PASS
ok  	github.com/tjst-t/palmux2/internal/tab/claudeagent	0.009s
```

3/3 PASS。

### Playwright E2E (`python3 tests/e2e/s004_mcp_indicator.py`)

```
==> S004 E2E starting (dev port 8215)
PASS: page loaded; TopBar mcp indicator present
PASS: empty-state summary correct: 'mcp —'
PASS: empty-state pip tone = unknown (no MCP servers)
PASS: popup empty state renders 'No MCP servers configured'
PASS: popup closes on Escape
PASS: populated-state summary correct: 'mcp 1/3'
PASS: populated-state rollup tone = err (one failed)
PASS: row 'palmux': dot tone=ok, badge='connected'
PASS: row 'github': dot tone=err, badge='failed'
PASS: row 'linear': dot tone=warn, badge='connecting'
PASS: popup closes on click outside
==> S004 E2E PASSED
```

11/11 PASS. 検証点:

1. **Page boot**: dev palmux2 (port 8215) の prod build がブラウザに
   ロードされ React がマウント、TopBar に `mcp-topbar-btn` が描画される。
2. **Empty-state summary**: 通常の dev 環境 (MCP サーバ未接続) で
   TopBar が `mcp —` を出し、pip tone が `unknown` (gray)。**UI が
   壊れず空状態を出す** — VISION の "MCP サーバが繋がっていない環境
   でも UI が壊れず空状態を表示する" 要件を満たす。
3. **Empty popup**: クリックで popup 開く → "No MCP servers
   configured." を表示。
4. **Escape closes popup**: `keyboard.press('Escape')` で popup の
   DOM が unmount される。
5. **Populated state injection**: `__palmuxAllWS` proxy 経由で WS の
   onmessage に合成 `session.init` を `dispatchEvent`。reducer が
   3 サーバを取り込む。
6. **Populated summary**: TopBar が `mcp 1/3` を出す (1 connected of 3)。
7. **Rollup tone**: 1 failed が混じっているので worst-status-wins で
   `err` (red pip)。
8-10. **Per-row tones**: 各サーバの dot (ok/err/warn) と badge
   (`connected`/`failed`/`connecting`) が CLI 値そのままで表示される。
11. **Click-outside closes popup**: body の (5,5) を click → popup
   unmount.

### Observed side-effects / dust

- ホスト用 palmux2 (port 8207, PID 2576959) は触らず、dev
  (port 8215, PID 3157312) のみ再起動。✅
- 既存 dev インスタンス (PID 1907757) は `make serve INSTANCE=dev`
  の自動 kill フローで graceful 終了。
- `tmp/palmux-dev.log` に S004 由来のエラーログ無し。
- 新規 lint 警告: ゼロ (43 → 43、内 mcp-popup の
  `react-refresh/only-export-components` × 2 は `mcp-status.ts`
  split で消滅 → 結果として 45 → 43 で改善)。

## Drift / Backlog

- なし (既存スコープ内)。
- S004 の "最終接続時刻" 記述は ROADMAP のタスク説明側 (S004-1-2) のみ
  に存在し、受け入れ条件には含まれていない。CLI が今のところ提供
  していないので Phase 3 では省略。Phase 4 以降に CLI が timestamp を
  返すようになったら追加 — backlog 対象として明示的なエントリは
  起こさず、CLI の進化を契機に再検討する。
