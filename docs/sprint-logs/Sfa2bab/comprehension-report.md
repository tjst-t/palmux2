# Comprehension Report — Phase D milestone (Sprints S2b5691, Sfa2bab)

_Generated at milestone arrival. Read this before `autopilot review`._

## How to run it

- `make dev` → http://localhost:PORT (portman-assigned; check the printed URL) — codex/opencode tabs only appear once `[agents.codex]`/`[agents.opencode]` are added to your `config.toml` (see "What was assumed" below for exact config).
- No release build has been tagged/pushed this milestone — everything below lives on `main` only, unreleased.

## What changed

- palmux can now register **codex** and **opencode** as first-class agent kinds alongside Claude Code: a config-gated `[agents.codex]`/`[agents.opencode]` section in `config.toml`, a new `GET /api/agents` endpoint, and `palmux hook --agent=codex/opencode` dispatch that correctly parses each tool's own notification wire format (codex: JSON as the last CLI arg; opencode: JSON on stdin).
- The web UI now exposes both agents: an "agent picker" menu appears on the TabBar `+` button once more than one agent kind is enabled (single-kind installs are unaffected — the `+` still adds immediately, exactly as before), ⌘K gained `new codex`/`new opencode` commands, and the Activity Inbox now labels notifications by agent ("Open Codex" / "Open opencode") with a capability badge showing whether an agent only notifies at turn-end or not at all.
- Workspace containers can now bind-mount a registered agent's own binary + auth directory in, so codex/opencode can run **inside** an isolated incus container the same way Claude Code already does — no image rebuild needed.
- A genuine, unrelated crash bug was fixed along the way: the vendored terminal emulator could panic the entire palmux2 process under a specific ANSI sequence (setting scroll margins beyond the visible screen), found live while testing codex in a container.
- **A full release-readiness dogfood was run on a freshly built appliance image** (not just dev-box tests) — it confirmed the build/boot/tab UI all work, but surfaced that **Activity Inbox notifications for any in-container agent (including Claude Code) silently never arrive** on a standard first-boot appliance (no public domain configured yet). The underlying agent turns themselves complete correctly — only the notification is missing.

## Why this way

- Instead of re-implementing codex/opencode support from scratch, this milestone **ported the design from a stale local branch** (`maultiagent`, from before a major agent-hosting rewrite) rather than merging it directly — a straight `git merge` would have conflicted on nearly every line of the rewritten files. This mirrors how the underlying framework (Adapter abstraction) was already integrated the same way in an earlier milestone.
- codex/opencode are **off by default** and must be explicitly enabled per-install (`[agents.codex]` in config.toml), not auto-detected from binaries being present — consistent with this project's "explicit over implicit" rule, so nobody's TabBar sprouts unexpected new buttons just because they happen to have `codex` on their PATH.
- The existing `claude-tui` UI component was generalized (renamed to `agent-tui`) to render any agent kind rather than writing a parallel codex/opencode-specific UI, but every Claude-specific feature it already had (role badges, clipboard support, mobile touch-scroll, file picker) was deliberately preserved, not dropped, during the generalization.
- The release-readiness dogfood deliberately targeted the riskiest configuration (a workspace container, not a plain host shell) specifically because that's where an earlier reliability concern about opencode had been flagged — testing on the easy path would have looked green while saying nothing about the actual risk.

## What to verify

- ⚠️ (high — release-blocking discussion) **In-container agent notifications don't work on a standard first-boot appliance.** This affects Claude Code too, not just codex/opencode — any workspace running in an isolated container, on any palmuxOS install that hasn't configured a public domain yet, will silently never show a turn-complete/permission-needed notification in the Activity Inbox for that workspace, even though the agent itself is working fine. This is the central open question for this milestone: ship as-is (users lose in-container notifications until they set a public domain, which most already do for the appliance's own SSO) or hold for a fix first.
- ⚠️ (medium) **A separate, still-unresolved concern**: earlier automated testing suggested opencode running inside a container sometimes crashes outright (a different, more severe symptom than the notification gap above — no crash was observed in this milestone's fresh dogfood run, but that single clean run neither confirms nor rules out the earlier crash reports on a busier host). Worth a dedicated investigation before leaning on in-container opencode for anything important.
- The agent-picker menu's behavior when only Claude Code is enabled (the vast majority of installs today) — confirmed unchanged in testing, but worth a glance since it's the most common path.

## What was assumed

- To try codex/opencode locally: `config.toml` needs
  ```toml
  [agents.codex]
  command = "codex"
  [agents.opencode]
  command = "opencode"
  ```
  restart palmux2, and both binaries must already be `npm install -g`'d and logged in on whichever machine runs palmux2 (or, for containers, the workspace-runtime host).
- The Activity Inbox notification gap was root-caused to code that intentionally disables in-container notifications whenever palmux2 listens on a wildcard address (an earlier, unrelated fix to avoid a different bind conflict) — this is a real code path, independently double-checked against the source, not a guess. No fix has been attempted yet; it's filed as a backlog bug awaiting a decision on the right fix shape (an alternate listen address for that one purpose, vs. an explicit warning to the operator).
