# Comprehension Report — Release-blocker fix milestone (Sprint Sc4f091)

_Generated at milestone arrival. Read this before `autopilot review`._

## How to run it

- `make dev` or the already-running dev instance at `http://<this box>:8200` (rebuilt on this exact commit, `v0.16.0-38-gaa2c652`) — try codex/opencode tabs the same way as the previous milestone, both release-blocking bugs from that review are now fixed here.

## What changed

- **Activity Inbox notifications for in-container agents (claude, codex, and opencode) now work on a standard palmuxOS appliance that hasn't configured a public domain yet** — previously they silently never arrived in that (common, first-boot-default) configuration. Public-domain-configured deployments are unaffected (verified byte-identical).
- **codex and opencode running inside an isolated workspace container are now meaningfully more reliable** — a real, previously-undiagnosed bug where any palmux2 process without agents configured would strip other instances' codex/opencode container shares out from under them, roughly every 10 seconds, has a working mitigation. This doesn't (and can't, by itself) fix already-running unpatched processes sharing the same host — it takes effect once everything sharing that host is on the fixed code.
- A subtle timing bug introduced by the reliability fix's own safety check (it could briefly slow down unrelated tab reconnections under load) was caught by review and fixed before merge.

## Why this way

- The notify-hook fix separates "does palmux2 need a second network listener" from "what address can a container reach palmux2 at" — these were incorrectly sharing the same logic, which is why the bug existed. Splitting them fixes it without touching the original, still-valid reason the old logic existed.
- The reliability fix does the minimally invasive thing that's provably correct (skip removing a shared resource you don't recognize, if you have no opinion of your own) rather than attempting a full redesign of the underlying shared-resource system in this Sprint — the full redesign is real, but Story-sized scope discipline pushed it to backlog instead of risking a rushed rewrite.
- Verification intentionally used two separate measurements: real conditions on this actual, busy, multi-tenant dev host (which stayed failing throughout, on purpose — the already-running processes were deliberately left unpatched so as not to disrupt other real work) and a fully isolated, fully-patched pair of throwaway instances (which passed consistently) — kept separate rather than blended into one misleading number.

## What to verify

- No high-severity items from this batch — every review finding either got fixed or was filed as an explicitly-scoped backlog item, and both fixes were confirmed via real hardware, not just unit tests.
- Worth a human glance: the dev instance rebuilt at the top of this report is running both fixes now — a quick try of codex/opencode tabs (and checking the Activity Inbox actually shows something) is the fastest sanity check.

## What was assumed

- The reliability fix's "instance with no [agents.*] configured defers to others" rule assumes at least ONE process sharing a host eventually declares an opinion; if literally none ever do, an old, abandoned share can sit forever unaudited (harmless clutter, filed to backlog, not urgent).
- Full resolution of the underlying shared-resource design (so two DIFFERENT configured instances can never disagree either) is assumed to be a dedicated future effort, not something to bolt on here under time pressure.
