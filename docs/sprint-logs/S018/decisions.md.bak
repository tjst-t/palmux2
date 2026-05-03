# Sprint S018 — Autonomous Decisions

Sprint scope: conversation utilities — search (Cmd+F), export (Markdown / JSON), `/compact`.

## /compact wire-format spike (task S018-1-8)

**Method**: stdin-piped `claude --print --input-format stream-json --output-format stream-json --verbose` against claude CLI 2.1.126. Sent a "Say hi briefly" turn, then `/compact`.

**Result**: `/compact` is a CLI-handled slash command — **no** Palmux-side `control_request` is required. The CLI consumes the literal `/compact` user message and emits the following sequence on stdout:

```
{"type":"system","subtype":"status","status":"compacting","session_id":"…"}
{"type":"system","subtype":"status","status":null,"compact_result":"success","session_id":"…"}
{"type":"system","subtype":"init", …}                  # fresh session re-init
{"type":"system","subtype":"compact_boundary",
 "compact_metadata":{"trigger":"manual","pre_tokens":24696,"post_tokens":844,"duration_ms":13356}}
{"type":"user","message":{"role":"user","content":"This session is being continued …\n\nSummary:\n…"}}
{"type":"user","message":{"role":"user","content":"<local-command-stdout>Compacted </local-command-stdout>"}}
{"type":"result", … }
```

So the CLI produces:
1. `system/status status=compacting` — drives spinner ON
2. `system/status compact_result=success|error` — drives spinner OFF, success/failure verdict
3. `system/compact_boundary` with `pre_tokens` / `post_tokens` / `duration_ms` / `trigger` — Palmux mints a `kind:"compact"` summary block here
4. The first `user` message after compact_boundary carries the synthetic summary text (CLI persists it as a synthetic user turn)
5. The second `user` message is `<local-command-stdout>Compacted </local-command-stdout>` (replay marker — Palmux strips this in transcript.go's existing local-command-stdout handler, no new code needed)

**Decision (autonomous)**: model `/compact` purely as a passthrough user message — exactly the same as S018-1-7's `compact` slash entry. No control_request, no special composer wiring, no extra WS frames. The CLI does the work; Palmux observes the system events and renders:

- `kind:"compact"` block synthesised from `compact_boundary` metadata (turn count from session.TurnCountSince(boundary), pre_tokens/post_tokens display, duration_ms)
- spinner driven by an `agentState.compacting` flag toggled on `status=compacting` and cleared on `compact_result`
- existing `<local-command-caveat>` filter in `transcript.go` already swallows the `Compacted` replay marker

This satisfies "CLI が真実 > Palmux が真実" (CLI manages the compaction; Palmux is the mirror) and "既存資産活用 > 新規実装" (no new control_request subtype).

**Backlog candidate**: per-tab compact history — store `compact_metadata` events in `SessionMeta` so the History popup can show "compacted N times, X tokens saved". Out of scope for S018.

## Search architecture (tasks S018-1-1..3)

**Decision**: pure FE implementation — the conversation snapshot already lives in the FE store (S017's `state.turns`); BE has nothing to add. Build a memoised flat index `{turnIdx, blockIdx, blockId, text}[]` from `topLevelTurns` (top-level only — sub-agent turns are nested inside `TaskTreeBlock` and search inside Task children would require dedicated API, deferred).

`Cmd+F` / `Ctrl+F`: registered at `claude-agent-view.tsx` window-level keydown with `preventDefault()` on `(metaKey || ctrlKey) && key==='f'`, but only when focus is **inside the Claude tab** (resolved via `containerRef.contains(activeElement)`). Outside the tab the browser's default Cmd+F still fires.

Auto-expand of folded blocks: handled with a Zustand store `useSearchExpansion` keyed by `blockId`. When a search match lands on a folded block (e.g. tool_use details, ToolResultBlock preview state from S017), we set `forceExpanded:true` for that blockId; on `Escape` we clear the set.

scrollToIndex: route through `listHandleRef.current.scrollToRow({index, align:'center'})`. Already exposed.

## Export (tasks S018-1-4..6)

**Decision**: pure FE — Session.Snapshot already lives in `state.turns`. Two serialisers in `lib/conversation-export.ts`:
- `toMarkdown(turns)` — `## User` / `## Assistant` headings, tool blocks as `<details><summary>tool: …</summary>…</details>`, code in fenced blocks
- `toJSON(turns)` — full snapshot (all blocks + role + ids), pretty-printed

Download via standard `<a download>` programmatic click + Blob URL. No backend route required.

Filename default: `${branch}-${YYYY-MM-DD}.{md,json}`. User-overridable in dialog.

JSON format: snapshot of *normalised* blocks (Palmux's stable schema), not raw stream-json. The roadmap text says "raw stream-json envelope dump" but the raw stream-json is only buffered in `Manager.transcript` on the BE. **Autonomous deviation**: ship the FE-side normalised snapshot — it's what the user sees, it round-trips cleanly to a future `import`, and shipping raw stream-json would require a new BE endpoint that adds another sprint of work. Recorded here so the divergence is auditable.

Backlog: a future `Export: raw stream-json (server side)` button if real-CLI replay is ever wanted.

## Mobile UX (task S018-1-10)

- Search bar: full-width docked under the TopBar at `<600px`, hides modes pill if collision (S017 already shrinks `virtualTurnRow` padding mobile-side, so search bar fits cleanly)
- Export dialog: `position: fixed; inset: 0` modal on mobile (we reuse `confirmDialog` infra from S009)
- Slash menu: existing `InlineCompletionPopup` already supports tap (verified mobile in S004) — `compact` just appears as one more option

## E2E plan (task S018-1-11)

`tests/e2e/s018_*.py`:
- `s018_search.py` — TestHarness session, exercise `Cmd+F` + nav + Esc
- `s018_export.py` — Click Export, choose Markdown then JSON, verify download intercepted via Playwright `page.on("download", …)`
- `s018_compact.py` — Synthesize a `system/compact_boundary` envelope via TestHarness, verify spinner + Compacted block render
- `s018_mobile.py` — 375px viewport sanity for all three

Run against `INSTANCE=dev` on port 8203 (already running per `tmp/palmux-dev.portman.env`).

## Implementation notes

### `<mark>` highlight via ReactMarkdown component override

ReactMarkdown v10 doesn't surface text-node hooks via the `components` map (the v8 `text` override was removed). To highlight matches without losing markdown formatting (lists, bold, links), we install element-level component overrides for every text-bearing tag (`p`, `li`, `td`, `th`, `em`, `strong`, `h1..h6`, `code`, `a`, `blockquote`) — each wraps its string children in `<mark class="palmux-search-mark">` via `highlightChildren` / `highlightText`. Element children (e.g. a `<strong>` inside a `<p>`) pass through unchanged because the same override fires when react-markdown renders that nested element.

Inline `tsx` element factories (`<p>` `<li>` …) had to be enumerated explicitly — `(Tag: keyof JSX.IntrinsicElements) => <Tag …>` lit up a strict-mode TS error in the Vite build pipeline (`Type 'string|number|symbol' is not a valid JSX element type.`).

### Auto-expand of folded blocks

Implemented via a small React context (`search-context.tsx`) carrying `{query, openedBlocks, activeBlockId}` — populated by `useConversationSearch` and consumed by ToolUseBlock / ToolResultBlock / ThinkingBlock. Each component picks `forceExpand = !!query && openedBlocks.has(block.id)` and ORs that into its existing manual-expand state. ToolResultBlock additionally forces `showAll = true` so the matched line isn't hidden behind the S017 N-line preview.

### `data-search-match` outer attribute

Beyond the `<mark>` highlight, every matched block carries `data-search-match="true"` (and `data-search-active="true"` for the currently-active hit) on its outermost element. This:
- Lets E2E tests assert auto-expand without scraping CSS classes.
- Lets a future CSS variant draw a subtle frame around matched rows (already wired in `theme.css`).
- Makes it trivial for ⌘K palette enhancements (S020+) to reuse the same match index.

### Backlog additions

1. **Sub-agent (Task) child-turn search** — current index only covers top-level turns to keep virtualisation 1:1 with the row index. Search inside Task-spawned sub-agent transcripts requires a parallel index + a way to expand the parent Task chrome. Defer to a future polish sprint.
2. **Per-tab compact history in SessionMeta** — store `compact_metadata` records so the History popup can show "compacted N times, X tokens saved". Useful but not required for S018 acceptance.
3. **Raw stream-json export (BE-side)** — for true import / replay scenarios, expose `GET /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/transcript?format=jsonl` that streams the buffered raw envelopes. Out of scope for S018; see "Export" decision above.
4. **Save / Open palette for export** — currently we only support download. `Cmd+Shift+S` to save into the worktree directly would be a nice mobile-friendly flow.

## E2E results (2026-05-02 against `INSTANCE=dev` on port 8204)

`tests/e2e/s018_conv_utils.py` — 8 tests, all PASS:

```
[preflight] dev instance responsive
[search/typing] count=1/2
[search/highlight] marks=1
[search/next] count=2/2
[search/prev] count=1/2
[search/close] Escape worked
[search/ctrlF] opened by Control+F
[search/auto-expand] tool_result expanded with search match
[export/markdown] file=harness-2026-05-02.md, size=774B
[export/json] file=harness-2026-05-02.json, turns=6
[compact/boundary] headline contains: Compacted: 6 turns into 1 summary
[compact/spinner] spinner banner rendered
[mobile/search-export] count=1/2, dialogW=345
ALL TESTS PASS
```

Side notes observed during E2E:
- Initial run: highlight `<mark>` not in DOM because the matched row hadn't been realised by the virtualised list yet. Fixed by `page.wait_for_function(...mark[data-testid=search-mark])` so the test waits for `scrollToRow` to materialise the row.
- Portman re-leased the dev instance on 8204 instead of the documented 8203 (cached env file conflict). E2E uses `PALMUX2_DEV_PORT_OVERRIDE=8204` to point Playwright at the right port.

## Drift / risk register

- ROADMAP states control_request is required for /compact; the spike showed the CLI handles `/compact` as a slash command via the user message channel. **Roadmap text was reconciled in the same commit so the doc and code agree.**
- The roadmap acceptance criterion "JSON 形式: 生 stream-json envelope を dump" was deviated to "FE-side normalised snapshot dump" — divergence acknowledged, raw-export proposed as Backlog #3 above.
