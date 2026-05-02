# Sprint S016-fix-1 — Autonomous Decisions

Sprint Title: ROADMAP parser i18n (English headers) + null safety
Branch: `autopilot/main/S016-fix-1`
Started: 2026-05-02

## Background

User opened the Sprint Dashboard against `tjst-t/hydra` (new-webui branch) and the FE crashed with:

```
Uncaught TypeError: Cannot read properties of null (reading 'map')
```

Root cause: hydra's `docs/ROADMAP.md` uses **English** section headings
(`## Progress`, `## Sprint S001: ... [DONE]`, `### Story S001-1: ... [x]`,
`**Task S001-1-1**: ...`). The Go parser only matched the Japanese
variants emitted by the same claude-skills `sprint` skill, so it never
populated `Roadmap.Sprints` and the response carried `"timeline": null`.
The FE blindly does `data.timeline.map(...)`, hence the crash.

## Implementation Decisions

- **Bilingual regex via alternation, not dual-regex**. Each pattern
  uses `(?:スプリント|Sprint)` style alternation so the same line
  produces the same captures regardless of language. Dual-regex would
  have doubled the maintenance surface and made "mixed" roadmaps
  (English heading, Japanese title) need ad-hoc fallback paths.
- **Section-prefix dispatch via shared helper**. Replaced
  `strings.HasPrefix(s.title, "進捗")` etc. with `hasAnyPrefix(s.title,
  progressPrefixes)`. Adding a third language later is just appending
  to the slice.
- **English progress totals**. Added a second
  `(?i)Total: N ... Done: N ... In Progress: N ... Remaining: N`
  regex alongside the existing JP one. Kept them as two separate
  expressions instead of one combined alternation because the JP form
  has fixed character classes (`合計`, `完了`) that don't fit cleanly
  with the English word-boundary handling.
- **Sectionless task detection**. Hydra-style ROADMAPs skip the
  `**Tasks:**` separator and write
  `- [x] **Task S001-1-1**: ...` directly under the Story heading.
  When `section == ""` and a line matches the explicit
  `**Task <ID>**` form (`reTask`), accept it as a task. Plain
  checkboxes still need an active section so we don't accidentally
  promote arbitrary AC bullets to tasks.
- **Null safety as defence in depth, server side**.
  `ParseRoadmap` now normalises every list field to a non-nil empty
  slice before returning — `Sprints`, `Dependencies`, `Backlog`,
  `ParseErrors`, plus per-Sprint `Stories` and per-Story
  `AcceptanceCriteria` / `Tasks`. Even if a future format variant
  evades the regex, the FE will see `[]` instead of `null` and render
  an empty state.
- **Null safety server side, second front: `OverviewResponse.Timeline`
  and `DependencyGraphResponse.Sprints`**. The handler builds these by
  appending in a loop; the prior code left them as `nil` slices when
  `rm.Sprints` was empty. Pre-allocated to `make([]TimelineEntry, 0, ...)`
  so they always serialise as `[]`.
- **Null safety client side**. Every `.map()` / `.length` call on a
  potentially-undefined array in the Sprint screens (Overview,
  Sprint Detail, Dependency Graph, Decision Timeline, Refine History)
  is now guarded with `?? []`. Backend null safety should make this
  unreachable, but the FE should never crash on a bad payload.

## Test Decisions

- **Three-test E2E in `tests/e2e/s016_fix1_i18n.py`**:
  English-only headers, mixed JP/EN, and an empty (no-sprint) ROADMAP.
  The third asserts `timeline == []` and `activeAutopilot == []` at
  the JSON level — this is the exact shape that pre-fix would have
  been `null` and caused the production crash.
- **Playwright harness driving every subtab**. The sub-test mounts a
  real browser, listens for `pageerror`, and clicks every subtab on
  both an English ROADMAP and an empty ROADMAP. Catches the original
  symptom directly.
- **Three Go unit tests** alongside the existing `*_Real` and
  `*_Malformed`: `*_NullSafety`, `*_EnglishHeaders`,
  `*_MixedHeaders`.
- **Manual hydra check kept out of the test suite**. Verified during
  development by opening
  `tjst-t/hydra` / `new-webui` on the dev instance and parsing live
  (36 sprints, 34 done, 0 page errors). Not codified as an E2E because
  the test would couple to the contributor's worktrees.

## Files Touched

- `internal/tab/sprint/parser/roadmap.go` — i18n regexes,
  section-prefix table, sectionless task detection, null-safety pass.
- `internal/tab/sprint/parser/roadmap_test.go` — three new unit tests.
- `internal/tab/sprint/handler.go` — `Timeline` and dependency
  `Sprints` pre-allocated to empty slice.
- `frontend/src/tabs/sprint/screens/overview.tsx`,
  `sprint-detail.tsx`, `dependency-graph.tsx`,
  `decision-timeline.tsx`, `refine-history.tsx` — `?? []`
  guards on every `.map()` / `.length` access.
- `tests/e2e/s016_fix1_i18n.py` — new E2E.
- `docs/ROADMAP.md` — S016 section appended with the fix note.
- `docs/sprint-logs/S016-fix-1/decisions.md` — this file.

## Verification Summary

- `go test ./internal/tab/sprint/parser/...` — 5/5 pass (2 existing + 3 new).
- `go test ./...` — all green.
- `npm run build` — typecheck + bundle clean.
- `tests/e2e/s016_sprint_dashboard.py` (existing JP) — PASS.
- `tests/e2e/s016_fix1_i18n.py` (new) — PASS (4/4 sub-tests:
  english / mixed / empty / playwright).
- Manual: opened `tjst-t/hydra` / `new-webui` → Sprint tab loaded with
  36 timeline dots, no `pageerror`.
