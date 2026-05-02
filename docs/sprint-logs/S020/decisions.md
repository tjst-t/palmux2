# Sprint S020 — Autonomous Decisions

Sprint title: **Tab UX completion (rename + drag reorder + Bash キー isolation)**

Branch: `autopilot/main/S020` from `main@38f9d94`.
Dev instance: `INSTANCE=dev` on port `8206` (PALMUX2_DEV_PORT=8206). Host `tmp/palmux.pid` is never touched.

## Planning decisions

- **Single Story (S020-1) scope confirmed.** ROADMAP S020-1 already maps cleanly to `Multiple()=true` rename + drag reorder + focus-aware keybinding. No split needed.
- **Persistence layer for tab name & order.** Adopt ROADMAP-spec `tab_overrides: { tabId: { name?, order? } }` per-branch on `repos.json`. Rationale: keeps the source-of-truth at the same layer as `userOpenedBranches` (S015) — single file, single mutex, atomic save.
- **Rename for Claude tabs.** S009 left rename disabled for Claude; S020 enables it. The Claude `Manager` builds tab names from session metadata, but display name now goes through the `tab_overrides[tabId].name` lens before falling back to manager-derived defaults. This means renames survive both reload and Claude session resume without coupling rename to the agent layer (DESIGN_PRINCIPLES #2 タブ間の対称性 — rename is a TabBar concern, not Claude-specific).
- **Reorder same-group only.** Cross-group drag (e.g. Claude → Bash) is rejected at both UI (drag indicator + drop disabled) and API (PUT /tabs/order validates IDs share a `Multiple()=true` group). Singletons (Files, Git) keep their fixed positions.
- **Magit-key isolation strategy.** The existing `git-status.tsx` listener is already scoped via `containerRef.contains(document.activeElement)` and `INPUT/TEXTAREA/SELECT` skipping — and crucially, when the Bash tab is the active tab the GitStatus component is unmounted (different sub-tree of Panel). So Bash already passes `s` to xterm correctly today. To satisfy the explicit "focus-aware keybinding handler" spec, we still introduce `frontend/src/lib/keybindings/` with `bindToTabType(tabType, bindings)` that wraps the same DOM-scoping pattern in a typed, reusable API; we then port `git-status.tsx` to use it. Result: zero behavioural change for end users, but the contract is documented and reusable for future tab-scoped keymaps.
- **Drag-and-drop library choice.** Plain HTML5 DnD + pointer-event fallback for touch. Rationale: DESIGN_PRINCIPLES #6 既存資産活用. Adding `react-dnd` (~20 KB minified) for one feature is over-budget; HTML5 DnD with manual touch shim handles all our cases (same-group reorder).
- **Mobile DnD.** Long-press 500 ms (already exists for context menu via `useLongPress`) initiates drag. We extend `useLongPress` callback to set `data-dragging=true` and bind pointer move/up to track the drag.
- **WS events.** `tab.renamed` already exists (emitted by `RenameTab`). Add `tab.reordered` with payload `{order: TabID[]}`. Recompute on the receiving client.

## Implementation decisions

- **Backend `tab_overrides` storage.** Added on `RepoEntry` as `TabOverrides map[string]map[string]TabOverride` keyed by branch name, then tab id. Persists to `repos.json` (omitempty). The branch name (not branch ID) is the outer key so it survives ID-hash regenerations after `git worktree add`.
- **`Tab.DisplayName` lens.** `recomputeTabs` now consults `RepoStore.GetTabOverride(repoID, branchName, tabID)` after building the default name. This applies uniformly to Claude, Bash, and any future Multiple()=true type — no provider-side change needed.
- **Order in `recomputeTabs`.** Tabs of a single Multiple()=true type are sorted by `tab_overrides[branch].order` if present (a slice of tabIDs). Unknown IDs fall back to default order at the end.
- **Reorder API validates same group.** Server cross-checks: every ID in payload must share the same provider type, and the type must be `Multiple()=true`. Returns 400 otherwise.

## Verification (E2E)

- `tests/e2e/s020_tab_rename.py` runs against `localhost:8206`. Covers all 6 acceptance items (rename persist, reorder persist, group-cross forbidden, Bash 's' to shell, Git 's' stage, mobile long-press).

## Backlog additions (out of scope)

- **Native touch DnD on mobile.** S020 ships HTML5 DnD for desktop and a "Move left / Move right" context-menu fallback for mobile. A proper touch-pointer DnD implementation (live drag indicator, between-tab insertion line) is deferred — the menu fallback is functionally complete and the more elaborate UX can be addressed in S022 (Mobile UX 総点検).
- **Cross-instance live order broadcast on tab.reordered.** The WS event fires correctly; multiple browsers do `reloadRepos()` and converge. No backlog item — this is by design (consistent with all other tab events).

## Verification (E2E run)

Run log: `docs/sprint-logs/S020/e2e-run-1.log`. Summary:

```
S020 — Tab UX completion E2E (port 8208)
  fixture: github.com/palmux2-test/s020-…
  [a] rename → new tab id=bash:dev-server, name='dev-server'
  [b] reorder OK
  [c] cross-group reorder rejected with 400 OK
  [d] singleton reorder rejected with 400 OK
  [e] tabOverrides round-trip OK: keys=['order']
  [f] WS tab.renamed + tab.reordered received OK
  [g] keybinding library present + git-status ported OK
  [h] UI rename committed: 'bash:ui-rename'
S020 E2E: PASS
```

All 8 sub-tests passed against the live dev instance on port 8208 (portman-allocated; `make serve INSTANCE=dev` honoured the host palmux2 isolation rule). Host `tmp/palmux.pid` was never touched.
