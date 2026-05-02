# S022 — Mobile Audit Checklist

Cross-tab / cross-component audit at three reference viewports.

| Reference | Width | Why |
|---|---|---|
| Small phone | **320 px** | Min iPhone SE 1st-gen, low end of Android |
| Default mobile | **375 px** | iPhone SE 2nd/3rd gen, mid Android |
| Large mobile | **599 px** | Just under the 600 px desktop breakpoint |

Resolutions of issues found here are recorded inline. If a fix would require
re-architecture beyond this sprint's scope, the issue is moved to the Backlog
section of `docs/ROADMAP.md`.

## Surface Inventory

| Surface | Type | Owner sprint | 320 | 375 | 599 |
|---------|------|--------------|-----|-----|-----|
| Header (top bar) | inline | core | OK (since v1) | OK | OK |
| Drawer (Workspaces) | overlay | S015 | OK (mobile mode active) | OK | OK |
| TabBar | inline | S009/S020 | scrolls horizontally | OK | OK |
| Toolbar (mobile) | inline | core | OK (always shown on mobile) | OK | OK |
| Activity Inbox | popup | core/v2.1 | OK | OK | OK |
| ⌘K palette | popup | core/v2.1 | OK (full-width) | OK | OK |
| Right Panel Selector | inline (split) | core | hidden on mobile (split disabled <600) | hidden | hidden |
| Modal (BranchPicker, OrphanModal, …) | popup | core | **fixed S022** — now bottom sheet on mobile | bottom sheet | bottom sheet |
| Settings popup (Claude tab) | popup | S002 | OK | OK | OK |
| MCP popup (Claude tab) | popup | S004 | OK | OK | OK |
| History popup (Claude tab) | popup | core | OK | OK | OK |
| Subagent Cleanup dialog | popup | S021 | OK | OK | OK |
| Conversation Search bar | inline | S018 | OK (already mobile-tweaked) | OK | OK |
| Export dialog | popup | S018 | OK (already mobile-tweaked) | OK | OK |
| /compact menu | popup | S018 | OK | OK | OK |
| Composer | inline | core | OK | OK | OK |
| Composer attach menu | popup | S008 | OK | OK | OK |
| Files tree | inline | S010 | OK (slim left rail) | OK | OK |
| Files preview (Monaco) | inline | S010/S011 | OK (lazy-loaded) | OK | OK |
| Files preview (drawio) | iframe | S010/S011 | OK (full-width, scroll-only) | OK | OK |
| Git status | inline | S012 | OK (swipe stage) | OK | OK |
| Git history graph | inline | S013 | scrolls horizontally | OK | OK |
| Conflict view | inline | S014 | 3-way diff stacks vertically (already designed) | OK | OK |
| Sprint Dashboard | inline | S016 | OK (5 screens) | OK | OK |
| Mermaid diagram | inline (SVG) | S016 | pinch-zoom OK | OK | OK |

## Tap Target Pass

The new `--tap-min-size: 36px` token + `[data-tap-mobile]` opt-in attribute
were applied to the close button in `<Modal>` and the close in `<BottomSheet>`.
Existing surfaces that already honor 36px+ heights:

- `<TabBar>` tabs: 36px row height (declared `--tabbar-height`)
- `<Toolbar>` buttons: 48px row (declared `--toolbar-height`)
- Drawer `branchItem` rows: padding 8px + line-height makes 36px effective
- Composer send button: 32px height (acceptable per D-2 rule); padding gives
  ~40px touch target
- Activity Inbox bell: 36px icon button

Body-level rule (`button:not([data-tap-mobile-ignore]) { min-height: 32px }`)
catches everything else. The 32px floor is intentional — anything below that
gets visually hidden on mobile, and anything above 32px is fine.

No surface required additional CSS changes beyond Modal + BottomSheet.

## BottomSheet Migration

Per decision D-1, the migration target is incremental. Changed in this sprint:

- `<Modal>` (CSS-only) — used by BranchPicker, OrphanModal, ConflictDialog
  (S011), ImageView fullscreen (S010), ConfirmDialog patterns. **All Modal
  call sites automatically get bottom-sheet styling on mobile** without
  per-call-site changes.
- `<BottomSheet>` (new component) — available for future popups. Not yet
  retrofitted to existing custom popups (Settings/MCP/History) — those have
  bespoke positioning and the audit confirmed they work at all 3 widths
  already. Migration is optional / backlog.

## Bundle Splitting Pass

Files / Git / Sprint tab modules now use `React.lazy` (S022-1-5). Initial
bundle (sum of `<head>`-preloaded chunks): **311 KB gzip** (target < 500 KB).

Mermaid was already split (S016). drawio was already iframe-loaded (S010).
Monaco editor lives in its own webworker chunks (`editor.api2`,
`ts.worker`, …) loaded only when Files renders a non-image file.

## Touch Gesture Pass

See `docs/mobile-gestures.md` for the full collision matrix. Key checks:

- G-1 (long-press menu) vs G-7 (tab drag-reorder) — disambiguated by 8px
  movement threshold; verified at 320 px width.
- G-3 (swipe stage) vs G-8 (vertical scroll) — direction discriminator
  resolves first move in `git-status-row`.
- G-9 (BottomSheet drag-down) — implemented in `<BottomSheet>` with
  80 px dismiss threshold.

No new collisions found.

## Open Items / Backlog

- Migrate Settings / MCP / History popups (Claude tab) to `<BottomSheet>`
  for consistent gesture model. Currently they use bespoke positioning that
  works fine but doesn't share the drag-down dismiss UX. Low priority.
- Tablet-size (600–899 px) selectively pulls in mobile sheet styling only
  on `< 600 px`. Tablet uses desktop modal. This is intentional per
  04-ui-requirements.md but flagged for future review.
