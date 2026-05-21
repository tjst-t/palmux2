# autopilot / sprint billing-path analysis

**Story**: S1d2278-4  
**Date**: 2026-05-17  
**Question**: Does autopilot / sprint runner invoke `claude -p` / `--input-format stream-json` /
Agent SDK behind the scenes? If yes, the 2026-06-15 Anthropic billing split that carves out a
separate Agent SDK credit pool affects the skill runner itself — not just palmux2's claude-agent tab.

---

## Section 1: Grep commands executed and full results

### Command 1 — non-interactive claude invocations and Agent SDK imports

```
rg -n 'claude --print|claude -p\b|--input-format stream-json|--output-format stream-json|@anthropic-ai/sdk|anthropic-sdk|claude_sdk|anthropic\.Anthropic' \
    ~/.claude/skills/autopilot/ \
    ~/.claude/skills/sprint/
```

**Result**: No matches (exit code 1 — ripgrep returns 1 when there are zero matches).

Searched directories:
- `~/.claude/skills/autopilot/` — SKILL.md, references/*.md, references/*.json
- `~/.claude/skills/sprint/` — SKILL.md, references/*.md, references/*.json

### Command 2 — RemoteTrigger and other external-process patterns

```
rg -n 'Skill\s*tool|invoke.*skill|RemoteTrigger|claude.*-p\b' \
    ~/.claude/skills/autopilot/ \
    ~/.claude/skills/sprint/
```

**Result**: Matches found are _documentation_ references only — e.g.,  
`~/.claude/skills/sprint/SKILL.md:44: — "invoke the /review skill yourself via the Skill tool"`  
`~/.claude/skills/autopilot/references/autopilot-start.md:33: — "Launch an Agent to invoke sprint auto via the Skill tool"`

None of these are code that spawns an external `claude` process. They are natural-language
instructions to Claude telling it how to call Claude Code's built-in Skill tool.

### Conclusion of grep: zero hits for Agent SDK / non-interactive claude paths.

---

## Section 2: Agent-tool dispatch pattern findings

### How sub-agents are dispatched

autopilot and sprint invoke sub-agents exclusively through **Claude Code's built-in `Agent` tool**
(the in-process tool that Claude Code exposes to the main agent). This is confirmed in the skill
source at the natural-language instruction level:

**File**: `~/.claude/skills/autopilot/references/autopilot-start.md`, line 33  
```
Launch an Agent to invoke `sprint auto` via the Skill tool. The agent prompt must include:
  - The Sprint ID to execute
  - The full contents of `docs/VISION.json` and `docs/DESIGN_PRINCIPLES.json`
  - Instruction to return: completion status, decision summary, and any warnings
```

**File**: `~/.claude/skills/sprint/references/sprint-run.md`, line 22  
```
Launch an Agent with `model: "sonnet"` and `isolation: "worktree"` for each Story
```

**File**: `~/.claude/skills/sprint/references/sprint-run.md`, line 34  
```
For each completed implementation, launch a new Agent with `model: "sonnet"` (no worktree —
it reviews the branch diff)
```

### Billing pathway of the Agent tool

The `Agent` tool is a built-in Claude Code tool. When the main Claude Code process calls `Agent`,
it spawns a child Claude Code agent inside the same interactive session. This child agent:

1. Operates within the **user's interactive Claude Code session** (Max plan subscription)
2. Does NOT invoke `claude --print` / `claude -p` / `--input-format stream-json` as a subprocess
3. Does NOT use `@anthropic-ai/sdk` or any Agent SDK HTTP call
4. Consumes tokens from the **interactive plan quota** — the same pool as a human typing in Claude Code

Therefore, sub-agents launched by autopilot/sprint are billed **identically to the main agent**:
against the interactive Claude Code subscription (Max 20x), not against the new Agent SDK credit pool.

### Dispatch flow diagram

```
User types "autopilot start"
    │
    ▼
Main Claude Code agent (interactive session, Max plan)
    │
    ├─ calls Agent tool ──► child agent: "sprint auto S001" (Max plan, same session)
    │       │
    │       ├─ calls Agent tool ──► grandchild agent: Story implementation (Max plan)
    │       │       (model: "sonnet", isolation: "worktree")
    │       │
    │       └─ calls Agent tool ──► grandchild agent: Story review (Max plan)
    │               (model: "sonnet")
    │
    └─ merge, drift-check, next Sprint …
```

No `claude -p` / `claude --print` / `--input-format stream-json` / Agent SDK call appears
anywhere in this chain. All agent invocations are in-process via Claude Code's Agent tool.

---

## Section 3: 結論

**結論: no impact**

autopilot/sprint skills do not use the Agent SDK, `claude -p`, `claude --print`,
`--input-format stream-json`, or `--output-format stream-json` anywhere in their implementation.
All sub-agent dispatch goes through Claude Code's built-in `Agent` tool, which bills against the
user's interactive Claude Code subscription (Max plan). The 2026-06-15 Anthropic billing split
that introduces a separate Agent SDK credit pool does **not** affect the autopilot or sprint runner.

Track B work (S1d2278) can therefore focus exclusively on palmux2's `claude-agent` tab
(which currently uses `--output-format stream-json` to drive claude in non-interactive mode).
Fixing the claude-agent tab is sufficient to eliminate the billing-pool leak for palmux2 users.
There is no hidden autopilot/sprint runner leak that would require a separate sprint.
