# docs/sprint-logs/ — Sprint artifact contract

Each `docs/sprint-logs/{SprintID}/` directory holds the machine- and
model-authored artifacts the `sprint` / `autopilot` skills emit while running
a Sprint. The **Palmux Sprint tab** (`internal/tab/sprint`) reads these files
and renders them read-only (priority_rule 1 — Palmux mirrors the CLI's truth,
it never owns Sprint state).

## The tab ⇄ skill artifact contract (single source of truth)

The canonical list of artifacts and their schemas lives **outside this repo**
in the skill:

```
~/.claude/skills/sprint/references/SPRINT_LOGS_SCHEMA.json   ← artifact list + shapes
~/.claude/skills/sprint/references/VERIFY_RUN_SCHEMA.json
~/.claude/skills/sprint/references/VERIFICATION_REPORT_SCHEMA.json
~/.claude/skills/sprint/references/REOPEN_SCHEMA.json
~/.claude/skills/sprint/references/ROADMAP_SCHEMA.json
~/.claude/skills/autopilot/references/COMPROMISES_SCHEMA.json
```

`SPRINT_LOGS_SCHEMA.json` carries an `$artifact_index` block that is the
authoritative list of what the tab must read. Keep it and the tab's readers in
lockstep.

### Current artifacts (read by the tab as of Se173ef)

| File | Trust role | Tab reader |
|---|---|---|
| `decisions.json` | autonomous decisions | `parser/decisions.go` |
| `verify-run.json` | **machine verdict** (exit codes) | `parser/artifacts.go` `ParseVerifyRun` |
| `verify-run-{name}.log` | per-run log (expandable) | `handler_review.go` `sprintLog` |
| `verification-report.json` | **independent verifier** verdict + AC findings | `ParseVerificationReport` |
| `done-judgment.json` | 6(-8) guard done judgment | `ParseDoneJudgment` |
| `compromises.json` | notify-after concessions | `ParseCompromises` |
| `comprehension-report.md` | milestone narrative (Markdown) | `ParseComprehension` |
| `prototype-review.json` | approved prototype screens | `ParsePrototypeReview` |
| `reopen.json` | Sprint re-open history | `ParseReopen` |
| `gui-spec-{StoryID}.json` | state diagram + endpoint contracts | `ParseGUISpec` |
| `scenario-{StoryID}.json` | non-GUI user scenarios | `ParseScenario` |
| `verification-results.json` | model-authored AC↔test traceability (status copied from verify-run.json) | (companion; tab uses verify-run/verification-report) |
| `failures.json` | needs_human escalations | `ParseFailures` (legacy) |
| `<anything-else>.json` | generic additional smoke/verification log | `ClassifySmokeLog` (heuristic pass/fail) |

`acceptance-matrix.json` and `e2e-results.json` are **removed** — AC status now
comes from `verification-report.json` ac_findings + `ROADMAP.json`
acceptance_criteria status, and per-run test results come from
`verify-run.json`. The tab still reads the two legacy files if a very old
Sprint carries them.

## When a NEW artifact is introduced (to prevent drift)

The Se173ef Sprint existed because the tab silently fell behind the artifacts
the skills wrote. To stop that recurring, whenever a new artifact is added:

1. **Add it to `SPRINT_LOGS_SCHEMA.json` `$artifact_index.current`** (skill side).
2. **Add a parser + reader** in `internal/tab/sprint/parser/artifacts.go` and
   wire it into `internal/tab/sprint/handler.go` (or a cross-sprint endpoint in
   `handler_review.go`). New readers must be **fail-open** — a missing/corrupt
   file omits the section, never crashes the response.
3. **Surface it in the tab UI** (`frontend/src/tabs/sprint/`), add a
   `data-testid`, and cover it with mock + E2E tests.
4. The generic "additional smoke log" collector (`ClassifySmokeLog`) is the
   safety net: any unrecognized `*.json` still appears in Sprint Detail's
   "追加検証ログ" list, so a forgotten artifact is visible rather than silent.

Recorded in `docs/sprint-logs/Se173ef/decisions.json`.
