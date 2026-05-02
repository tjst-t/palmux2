# Palmux2 Mobile Gestures

Cross-sprint reference for touch gesture behaviour and **collision avoidance rules**.

This document is the authoritative source of truth for what touch gestures
exist in palmux2, when they fire, and how potential conflicts are resolved. New
sprints that add gesture-based interaction MUST update this file (see
`docs/CLAUDE.md` § "このファイルの更新ポリシー" pattern).

Audience: implementers adding mobile interaction or debugging gesture conflicts
during S022 audit and beyond.

## Gesture Inventory

| ID | Sprint | Trigger | Surface (zone) | Effect |
|----|--------|---------|----------------|--------|
| **G-1** | S009 | Long-press (~500 ms) on a tab in the TabBar | Mobile-only TabBar row | Open ContextMenu (rename / close) |
| **G-2** | S010 | Pinch-zoom inside SVG viewer | Files tab → SVG `<img>` viewer | Browser-native zoom (sandboxed `<iframe>` for SVG so script content is contained) |
| **G-3** | S012 | Swipe right on a Git status row | Git tab → file row in `unstaged` / `untracked` section | Stage the file |
| **G-4** | S012 | Swipe left on a Git status row | Git tab → file row in `staged` section | Unstage the file |
| **G-5** | S012 | Swipe left on a Git status row | Git tab → file row in `untracked` section | Discard the file (with confirm) |
| **G-6** | S016 | Pinch-zoom inside Mermaid diagram | Sprint tab → Mermaid SVG | Zoom and pan diagram |
| **G-7** | S020 | Long-press on a tab in the TabBar | Mobile-only TabBar row | Initiate drag-reorder (after long-press hold, finger drag moves the tab) |
| **G-8** | S017 | Vertical scroll inside virtualized conversation | Claude tab → conversation list | Scroll (handled by react-window) |
| **G-9** | S022 | Drag down on the BottomSheet handle | Any sheet using `<BottomSheet>` (and `<Modal>` on mobile via CSS) | Dismiss sheet when offset > 80 px |
| **G-10** | S022 | Tap backdrop of a BottomSheet / Modal | Sheet overlay | Dismiss sheet |

## Collision Matrix

Each cell describes the resolution when two gestures could fire on overlapping
input.

| ↓ vs → | G-1 | G-3/4/5 | G-7 | G-8 | G-9 |
|---|---|---|---|---|---|
| **G-1** (long-press tab menu) | — | — (different surface) | **G-7 supersedes** if drag begins before menu opens; ContextMenu cancels on `pointermove` past 8 px threshold | — | — |
| **G-3/4/5** (Git swipe) | — | mutually exclusive (direction + section determines which fires) | — (different surface) | swipe is horizontal-only, vertical scroll handled by container | — |
| **G-7** (tab drag-reorder) | see G-1 | — | — | — | — |
| **G-8** (conversation scroll) | — | — | — | — | — |
| **G-9** (sheet drag-down) | — | — | — | — | — |

### Resolution Rules

- **Direction discriminator (G-3/G-4/G-5 vs G-8)**: Git status rows track touch
  start position. If `|dx| > |dy|` after the first `touchmove`, treat as swipe;
  otherwise allow vertical scroll to win. Section determines whether swipe is
  stage / unstage / discard.

- **Long-press vs drag (G-1 vs G-7)**: G-1 ContextMenu opens on `touchend`
  after 500 ms hold. G-7 (drag-reorder) starts the same long-press timer but
  if `pointermove` exceeds 8 px before timer fires, the tab enters drag-reorder
  mode and the ContextMenu is cancelled. This is the only ambiguous pair —
  the rule is "movement wins over menu".

- **Pinch zoom (G-2, G-6)**: Both are inside their own iframe / SVG viewer with
  `touch-action: pinch-zoom` set on the container. They never compete with
  page-level gestures because the parent uses `touch-action: pan-y` (vertical
  scroll only) and the zoom container opts in explicitly.

- **BottomSheet drag-down (G-9)**: Only fires on the sheet **panel**, not the
  body content. If the sheet's `body` is scrolled (scroll position > 0), G-9
  is suppressed until scroll returns to top — this prevents "drag down to
  dismiss while scrolling content" jank. Implementation note: `<BottomSheet>`
  delegates to `onTouchStart/Move/End` only on the panel root. Inside `body`,
  `overflow: auto` handles its own scroll natively.

## touch-action Map

`touch-action` declarations across the codebase (verified during S022 audit):

| Element | `touch-action` | Reason |
|---------|----------------|--------|
| `body` (default) | `manipulation` | Disable double-tap-zoom, allow pinch-zoom |
| `.terminal-view` (xterm.js host) | `none` | xterm handles all touch input itself |
| Git status row | `pan-y` | Vertical scroll yes, horizontal claimed by swipe |
| TabBar tabs | `none` (during drag-reorder) / default | Long-press + drag are claimed |
| BottomSheet panel root | `pan-y` | Allow drag-down + body scroll |
| BottomSheet body | default | Native scrolling |
| Mermaid container | `pinch-zoom` | Diagram viewer |
| SVG iframe | (sandbox controls touch via attr) | Sandboxed |

## Adding a New Gesture

When a future sprint adds a touch gesture:

1. Append a `G-N` entry to the inventory above with sprint ID, trigger, surface, effect.
2. Walk every existing G-X and decide whether the surfaces overlap.
3. If they do, add a resolution row to the collision matrix and write the rule
   under "Resolution Rules" with concrete pixel / time thresholds.
4. Update `touch-action` map if the new gesture introduces a new container.

## Verified by

- S022 mobile audit (this sprint) — all gestures fire correctly at 320 / 375 / 599 px viewports.
- Mobile E2E suite (`tests/e2e/mobile/`) covers:
  - G-3 (swipe stage)
  - G-9 (BottomSheet drag-down)
  - G-1 + G-7 collision (long-press disambiguation)
