# Sprint S024 — Autonomous Decisions

Drawer v7 polish: compact tokens + single-expand + single-line + glance line + section unification + ghq folder = MY auto-management. Design source-of-truth: `/tmp/drawer-mock-v7.html`.

## Planning Decisions

- **Mock-first implementation**: All CSS / DOM structure / state logic was transcribed from `/tmp/drawer-mock-v7.html` to keep designer's intent intact. The HTML's exact font-sizes (12 / 11.5 / 9–10px), padding (5 / 8 / 10 / 22px), `expandedRepoId: string | null` state shape, and accordion behavior were ported verbatim. Rationale: VISION emphasizes "狭い幅でも一瞥で示すコマンドサーフェス"; the v7 mock is the ground truth for that goal, and reinterpreting it would risk drift.
- **Polish vs feature**: S024 is a polish sprint that **does not delete S023 features**. Last-active memory, mobile auto-hide, Git subtab dropdown, BottomSheet, subagent cleanup dialog, orphan section all survive untouched. Only the Drawer presentation layer changed.

## Implementation Decisions

- **`IsPrimary` → `user` category override (S024-1-1)**: Implemented inside `applyCategoriesUnlocked` rather than at categorize() level. Rationale: keeps the lower-level `categorize()` pure (still tested by `category_test.go` unchanged), but guarantees the drawer always sees the primary worktree as MY. DESIGN_PRINCIPLES "明示的 > 暗黙的": a single line at the top of the loop documents the override clearly.
- **Single-expand state ownership**: Held at the `Drawer` level (`useState<string | null>`), not pushed into Zustand. Rationale: this is pure UI state — surviving across navigation makes sense via the existing `activeRepo`-driven `useEffect`. Zustand is reserved for cross-surface state (DESIGN_PRINCIPLES "状態管理は Zustand に集約" applies to domain state, not transient UI).
- **HERE label removal — keep `data-active`**: The visual identifier (border-left + bg + glow + bold) replaces the text label, but the `data-active="true"` data attribute is preserved on the branch row. Rationale: existing E2E (S021, S015) relies on `data-active`, and keeping it is free.
- **Glance line click semantics**: Made the glance line itself click-through to the same `handleRepoHeaderClick` so users do not have to thread the click between repo name and glance — the entire row navigates. Mock-equivalent UX.
- **`navigateTarget` precedence**: `last_active` wins, then ghq primary fallback, then first MY branch. Matches the mock's `navigateTarget()` JS function. Rationale: provides a deterministic preview every collapsed repo can show.
- **Mobile auto-hide on collapse-click**: With v7 always navigating on collapsed-repo click (last_active or ghq), mobile drawer auto-closes via the existing nav hook. This is a behavior change from S023 where collapse-only-no-nav was possible. The S023 E2E was updated to assert collapse-only (when expanded → click → collapse, no nav, drawer stays). DESIGN_PRINCIPLES "明示的 > 暗黙的": the user always sees in advance (glance line) that clicking will navigate, so auto-close is no surprise.
- **Section unification**: One `<DrawerSection title="Repositories">` lists every repo, sorted starred-first then alphabetically. Removed the per-section split. The `★` glyph on each row stays as the marker. Rationale: v7 mock has a single section; matches the simplification.

## Review Decisions

- **`.ghqMark` global vs scoped**: Originally written as `.branchName .ghqMark` and `.glanceLine .ghqMark` (compound selectors), but CSS Modules generates a unique hash per top-level rule. Re-promoted `.ghqMark` to a top-level rule and scoped via the active-state selectors. Rationale: keeps `styles.ghqMark` resolvable in JSX.
- **S015 E2E correction**: The promote-button assertion in `s015_worktree_categorization.py` did not click the chip first. Test updated to click `[data-chip="unmanaged"]` before scanning for `data-action="promote"`. The behavior is consistent with v3 (S023): chip-pill panels are closed by default; opening surfaces the action buttons.
- **S021 E2E selector fix**: The test was looking for `button[data-action="promote-subagent"]` but the v3+ Drawer uses `data-action="promote"` on subagent rows (the `subagent` qualifier is on the parent `data-category="subagent"`). Updated the locator to `[data-category="subagent"] button[data-action="promote"]`. This was a pre-existing breakage from the S023 redesign that was masked because S021 wasn't run after S023 merge; S024 surfaced and fixed it.

## Backlog additions

- None (all S024-1-* tasks completed in scope).

## Drift / open issues

- None. Mock parity is exact at the token level; behavioral parity verified via E2E.
