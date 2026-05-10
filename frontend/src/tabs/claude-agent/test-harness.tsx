// S017+S018+S019: standalone test harness that mounts ConversationList
// against a synthetic Turn[] generated from URL search params. The route
// is `/__test/claude` and is only meaningful during automated E2E runs —
// it has no entry in the app's UI.
//
// Search params:
//   turns=N        — generate N synthetic turns (assistant + tool_result pairs)
//   readLines=N    — embed a tool_result with N text lines (Read preview test)
//   sessionId=...  — override the session key (for scroll-restore tests)
//   search=1       — show the ConversationSearchBar + Cmd+F binding (S018)
//   export=1       — show the Export button + ConversationExportDialog (S018)
//   compact=1      — start with the compactingState true so the spinner shows
//   compactBoundary=1 — synthesise a kind:"compact" turn so its rendering can
//                       be inspected in isolation (S018)
//   compacting=1   — show the "Compacting…" spinner banner (S018)
//   rewind=1       — wire up the UserTurnEditor (S019). The first user turn
//                    has one archived version pre-populated so the
//                    `< 1/2 >` arrows render. Edit + submit are stubbed
//                    locally (no real backend) so the optimistic apply
//                    still exercises through.
//   blocks=all     — S43cfb1-1-8: synthesise one Turn per Block kind so an
//                    E2E test can verify every renderer mounts with a
//                    deterministic data-testid="block-{kind}-{id}" wrapper.
//                    Composes with `composer=1` for a full kitchen-sink
//                    surface.
//   composer=1     — S43cfb1-5-6: mount the real Composer below the
//                    ConversationList with stubbed onSend / onInterrupt
//                    handlers that surface their last invocation via
//                    `[data-testid="harness-composer-state"]` so an
//                    E2E test can drive slash / mention / paste / Esc /
//                    Cmd+Enter without a live WS backend. The composer's
//                    repoId / branchId are synthetic so the @-mention
//                    fetch goes through the stub Files API exposed via
//                    `composerRepoId` + `composerBranchId` query params.
//   composerRepoId=...    — repoId passed to the composer (defaults to
//                    `harness-repo`). The harness installs a fetch
//                    interceptor for `/api/repos/.../files/search` that
//                    returns README + a couple of fake hits so the
//                    `@README` mention test can go green without any
//                    real BE.
//   composerBranchId=...  — branchId passed to the composer (defaults to
//                    `harness-branch`).
//   composerTabIds=a,b   — comma-separated tab ids to render side-by-side
//                    so a tab-switch test can verify per-tab draft
//                    isolation. The harness shows a button row to switch
//                    which tab id is "active" (which composer is mounted)
//                    so localStorage isolation can be exercised end-to-end.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'

import { BlockView } from './blocks'
import { Composer } from './composer'
import { ConversationExportDialog } from './conversation-export'
import { UserTurnEditor } from './user-turn-editor'
import {
  ConversationList,
  type ConversationListHandle,
} from './conversation-list'
import { useScrollAutoFollow } from './hooks/use-scroll-auto-follow'
import {
  ConversationSearchBar,
  useConversationSearch,
} from './conversation-search'
import { ClaudeSearchProvider } from './search-context'
import type { Block, Turn } from './types'
import styles from './claude-agent-view.module.css'

function makeBlock(id: string, kind: Block['kind'], extra: Partial<Block> = {}): Block {
  return { id, kind, ...extra }
}

/** Generate N synthetic turns. Each turn is (a) a user request, then
 *  (b) an assistant text response. Variation in length is deliberate
 *  so dynamic row heights actually exercise. */
function syntheticTurns(n: number, readLines: number, withCompactBoundary: boolean): Turn[] {
  const out: Turn[] = []
  for (let i = 0; i < n; i++) {
    const userText = `User turn ${i + 1}: ${'lorem ipsum '.repeat((i % 5) + 1)}`
    out.push({
      role: 'user',
      id: `turn-user-${i}`,
      blocks: [makeBlock(`b-user-${i}`, 'text', { text: userText })],
    })
    const assistantText = `Assistant response ${i + 1}.\n` +
      Array.from({ length: (i % 7) + 1 }, (_, j) => `  • point ${j + 1}`).join('\n')
    out.push({
      role: 'assistant',
      id: `turn-asst-${i}`,
      blocks: [makeBlock(`b-asst-${i}`, 'text', { text: assistantText })],
    })
  }
  // Trailing tool_result with `readLines` lines if requested. This is
  // the canonical "1000-line Read" surface for the preview test.
  if (readLines > 0) {
    const lines = Array.from({ length: readLines }, (_, i) => `${i + 1}\tline ${i + 1} of synthetic Read result`)
    out.push({
      role: 'tool',
      id: 'turn-readresult',
      blocks: [
        makeBlock('b-readresult', 'tool_result', {
          output: lines.join('\n'),
          done: true,
        }),
      ],
    })
  }
  if (withCompactBoundary) {
    out.push({
      role: 'system',
      id: 'turn-compact-boundary',
      blocks: [
        makeBlock('b-compact-boundary', 'compact', {
          done: true,
          compactTrigger: 'manual',
          compactPreTokens: 24696,
          compactPostTokens: 844,
          compactDurationMs: 13356,
          compactTurns: n * 2,
        }),
      ],
    })
  }
  return out
}

/** S43cfb1-1-8 kitchen sink: one synthetic Turn per Block kind so E2E
 *  can verify every renderer mounts. Each block's payload is the
 *  minimum that exercises a non-empty rendering path — e.g. the
 *  permission block needs `toolName`, the ask block needs `input`, the
 *  task-tree needs a Task tool_use with a child sub-agent turn. */
function syntheticAllBlocksTurns(): Turn[] {
  const out: Turn[] = []
  // 1) text — assistant prose
  out.push({
    role: 'assistant',
    id: 'turn-text',
    blocks: [
      makeBlock('b-text', 'text', {
        text: 'kitchen-sink-text-payload Hello from harness.',
        done: true,
      }),
    ],
  })
  // 2) thinking — assistant thinking
  out.push({
    role: 'assistant',
    id: 'turn-thinking',
    blocks: [
      makeBlock('b-thinking', 'thinking', {
        text: 'kitchen-sink-thinking-payload reasoning step',
        done: true,
      }),
    ],
  })
  // 3) tool_use — Bash invocation, finished
  out.push({
    role: 'assistant',
    id: 'turn-tooluse',
    blocks: [
      makeBlock('b-tooluse', 'tool_use', {
        name: 'Bash',
        input: { command: 'echo kitchen-sink-tooluse-payload', description: 'echo a sentinel' },
        done: true,
      }),
    ],
  })
  // 4) tool_result — Bash output
  out.push({
    role: 'tool',
    id: 'turn-toolresult',
    blocks: [
      makeBlock('b-toolresult', 'tool_result', {
        output: 'kitchen-sink-toolresult-payload\nsecond line',
        done: true,
      }),
    ],
  })
  // 5) plan — ExitPlanMode block. Plan extractor reads `text` (markdown).
  out.push({
    role: 'assistant',
    id: 'turn-plan',
    blocks: [
      makeBlock('b-plan', 'plan', {
        text: '# kitchen-sink-plan-payload\n\n1. step one\n2. step two',
        done: true,
        // Mark the plan as decided so the action row stays hidden — the
        // harness has no real handler wiring.
        planDecision: 'approved',
        planTargetMode: 'auto',
      }),
    ],
  })
  // 6) permission — pending Bash permission prompt
  out.push({
    role: 'assistant',
    id: 'turn-permission',
    blocks: [
      makeBlock('b-permission', 'permission', {
        toolName: 'Bash',
        input: { command: 'rm -rf kitchen-sink-permission-payload' },
        permissionId: 'perm-harness',
        // Mark decided=allow so the actions don't render (no handler).
        decision: 'allow',
        done: true,
      }),
    ],
  })
  // 7) ask — AskUserQuestion. The body parses block.input.questions.
  out.push({
    role: 'assistant',
    id: 'turn-ask',
    blocks: [
      makeBlock('b-ask', 'ask', {
        name: 'AskUserQuestion',
        input: {
          questions: [
            {
              question: 'kitchen-sink-ask-payload pick one',
              options: [
                { label: 'option-a', description: 'first choice' },
                { label: 'option-b', description: 'second choice' },
              ],
            },
          ],
        },
        // Pre-decided so the buttons don't need a handler.
        askAnswers: [['option-a']],
        done: true,
      }),
    ],
  })
  // 8) todo — TodoBlock reads block.todos
  out.push({
    role: 'assistant',
    id: 'turn-todo',
    blocks: [
      makeBlock('b-todo', 'todo', {
        todos: [
          { content: 'kitchen-sink-todo-payload first', activeForm: 'doing first', status: 'completed' },
          { content: 'second item', activeForm: 'doing second', status: 'in_progress' },
          { content: 'third item', activeForm: 'doing third', status: 'pending' },
        ],
        done: true,
      }),
    ],
  })
  // 9) hook — CLI hook lifecycle event
  out.push({
    role: 'hook',
    id: 'turn-hook',
    blocks: [
      makeBlock('b-hook', 'hook', {
        hookId: 'hook-harness',
        hookEvent: 'PreToolUse',
        hookName: 'PreToolUse:Bash',
        hookStdout: 'kitchen-sink-hook-payload stdout',
        hookStderr: '',
        hookExitCode: 0,
        hookOutcome: 'ok',
        done: true,
      }),
    ],
  })
  // 10) task-tree — Task tool_use with a child sub-agent turn. The
  //     dispatcher produces a TaskTreeBlock when `renderTaskChildren`
  //     is supplied. We render the child as a plain text turn.
  const taskParentToolUseId = 'tool-harness-task'
  out.push({
    role: 'assistant',
    id: 'turn-task',
    blocks: [
      makeBlock('b-task', 'tool_use', {
        name: 'Task',
        toolUseId: taskParentToolUseId,
        input: {
          description: 'kitchen-sink-task-payload',
          subagent_type: 'general',
          prompt: 'do the thing',
        },
        done: true,
      }),
    ],
  })
  // child sub-agent turn
  out.push({
    role: 'assistant',
    id: 'turn-task-child-1',
    parentToolUseId: taskParentToolUseId,
    blocks: [
      makeBlock('b-task-child-1', 'text', {
        text: 'kitchen-sink-task-child-payload sub-agent reply',
        done: true,
      }),
    ],
  })
  // 11) compact — synthetic role:"system" boundary
  out.push({
    role: 'system',
    id: 'turn-compact',
    blocks: [
      makeBlock('b-compact', 'compact', {
        compactTrigger: 'manual',
        compactPreTokens: 12000,
        compactPostTokens: 600,
        compactDurationMs: 5000,
        compactTurns: 8,
        done: true,
      }),
    ],
  })
  return out
}

/** Inject a few "needle" turns into the snapshot so search has
 *  something deterministic to find. Returns the modified turns. */
function injectSearchNeedles(turns: Turn[]): Turn[] {
  if (turns.length < 4) return turns
  const out = turns.slice()
  // Drop the marker into a turn ~ middle of the list so navigation
  // genuinely scrolls.
  const target = Math.floor(out.length / 2)
  out[target] = {
    ...out[target],
    blocks: [
      ...out[target].blocks,
      makeBlock(`b-needle-${target}`, 'text', { text: 'palmux-search-needle FIRST occurrence here' }),
    ],
  }
  // And another near the end so "next match" cycles to a different row.
  const tail = Math.min(out.length - 1, out.length - 3)
  out[tail] = {
    ...out[tail],
    blocks: [
      ...out[tail].blocks,
      makeBlock(`b-needle-${tail}`, 'text', { text: 'palmux-search-needle SECOND occurrence here' }),
    ],
  }
  return out
}

export function TestHarness() {
  const [params] = useSearchParams()
  const blocksMode = params.get('blocks') === 'all'
  // When `blocks=all` is set, we ignore `turns=N` (which would otherwise
  // default to 20 synthetic loremipsum pairs) so the kitchen-sink list
  // is the only content rendered. The E2E test asserts presence by
  // data-testid, so a deterministic small list is what we want.
  const turnsCount = blocksMode
    ? 0
    : Math.max(0, parseInt(params.get('turns') ?? '20', 10) || 0)
  const readLines = Math.max(0, parseInt(params.get('readLines') ?? '0', 10) || 0)
  const sessionId = params.get('sessionId') ?? `harness-${turnsCount}-${readLines}`
  const showSearch = params.get('search') === '1'
  const showExport = params.get('export') === '1'
  const showCompactBoundary = params.get('compactBoundary') === '1'
  const showCompactingSpinner = params.get('compacting') === '1'
  const showRewind = params.get('rewind') === '1'
  const showComposer = params.get('composer') === '1'
  const composerRepoId = params.get('composerRepoId') ?? 'harness-repo'
  const composerBranchId = params.get('composerBranchId') ?? 'harness-branch'
  const composerTabIdsParam = params.get('composerTabIds') ?? ''
  const composerTabIds = composerTabIdsParam
    ? composerTabIdsParam.split(',').map((s) => s.trim()).filter(Boolean)
    : ['claude:claude']
  // Hotfix probe: wire onScroll → autoFollow + render the
  // "scroll-to-latest" button so E2E can verify the bug where the
  // button never appeared (because ConversationList's scroll listener
  // wasn't getting attached).
  const showAutoFollow = params.get('autofollow') === '1'
  // E2E knob — pass `behavior=smooth` to make the harness button use
  // smooth animation, matching the real Claude tab's ↓ button.
  // Without this, harness tests only cover the 'instant' path and
  // miss smooth-specific regressions.
  const buttonBehavior = (params.get('behavior') === 'smooth' ? 'smooth' : 'instant') as ScrollBehavior

  const baseTurns = useMemo(
    () =>
      blocksMode
        ? syntheticAllBlocksTurns()
        : syntheticTurns(turnsCount, readLines, showCompactBoundary),
    [blocksMode, turnsCount, readLines, showCompactBoundary],
  )
  // S019: when rewind=1, inject a versions[] entry on the first user
  // turn so the `< 1/2 >` arrows render and an archived version is
  // queryable in E2E. The optimistic-apply flow (submit) goes through
  // local state below.
  const seededTurns = useMemo(() => {
    if (!showRewind) return baseTurns
    const out = baseTurns.slice()
    const idx = out.findIndex((t) => t.role === 'user')
    if (idx >= 0) {
      out[idx] = {
        ...out[idx],
        blocks: [
          { id: 'b-user-active', kind: 'text', text: 'Current edited message (active)', done: true },
        ],
        versions: [
          {
            content: 'Original user message before rewind',
            createdAt: new Date('2026-04-01T12:00:00Z').toISOString(),
            subsequentTurnIds: [],
          },
        ],
      }
    }
    return out
  }, [baseTurns, showRewind])
  const turns = useMemo(
    () => (showSearch ? injectSearchNeedles(seededTurns) : seededTurns),
    [seededTurns, showSearch],
  )

  // S019 harness: local state mirrors claude-agent-view's
  // activeVersionByTurnId + a stub rewind flow that just calls
  // applyRewind in-memory (no backend round-trip). E2E exercises the
  // pencil → editor → submit → arrows path against this harness.
  const [activeVersionByTurnId, setActiveVersionByTurnId] = useState<Record<string, number>>({})
  const [overrideTurns, setOverrideTurns] = useState<Turn[] | null>(null)
  // Editing state lives here (above the virtualised List) so it
  // survives row unmount/remount when the user scrolls past the
  // editing turn. Single string is enough — only one turn is edited
  // at a time in the user flow.
  const [editingTurnId, setEditingTurnId] = useState<string | null>(null)
  const onEditingChange = useCallback((turnId: string, editing: boolean) => {
    setEditingTurnId((prev) => {
      if (editing) return turnId
      return prev === turnId ? null : prev
    })
  }, [])
  const onRewindLocal = async (turnId: string, newMessage: string): Promise<void> => {
    // Mirror BE behaviour: archive the active version + truncate
    // subsequent turns. Then update overrideTurns so the harness
    // re-renders with the new state.
    setOverrideTurns((prev) => {
      const base = prev ?? turns
      const idx = base.findIndex((t) => t.id === turnId && t.role === 'user')
      if (idx < 0) return prev
      const target = base[idx]
      const archive = {
        content: target.blocks[0]?.text ?? '',
        createdAt: new Date().toISOString(),
        subsequentTurnIds: base.slice(idx + 1).map((t) => t.id),
      }
      const newTurn: Turn = {
        ...target,
        blocks: [{ ...target.blocks[0], text: newMessage, done: true }],
        versions: [...(target.versions ?? []), archive],
      }
      return [...base.slice(0, idx), newTurn]
    })
  }
  const onRewindApplyLocalNoop = () => { /* harness applies directly via onRewindLocal */ }

  // S43cfb1-5-6: composer test surface. Stub the WS-bound handlers and
  // record their last invocation so an E2E test can assert that
  // Cmd+Enter sent text and Esc fired interrupt without a real
  // backend. We expose state via a hidden DOM element so Playwright
  // can read attributes synchronously.
  const [composerState, setComposerState] = useState<{
    activeTabId: string
    lastSendBody: string | null
    lastSendAt: number | null
    lastInterruptAt: number | null
    sendCount: number
    interruptCount: number
    isStreaming: boolean
  }>({
    activeTabId: composerTabIds[0] ?? 'claude:claude',
    lastSendBody: null,
    lastSendAt: null,
    lastInterruptAt: null,
    sendCount: 0,
    interruptCount: 0,
    // Default to streaming=true so the Esc-interrupt path is reachable
    // out of the box. The E2E test toggles this via the
    // `harness-toggle-streaming` button when it needs the non-streaming
    // path (e.g. to test Cmd+Enter submit).
    isStreaming: params.get('composerStreaming') !== '0',
  })

  // S43cfb1-5-6: install a fetch interceptor for the Files API search
  // endpoint so the @-mention completion popup gets deterministic
  // results without a live BE. Restored on unmount.
  useEffect(() => {
    if (!showComposer) return
    const original = window.fetch.bind(window)
    const stubMatch = new RegExp(
      `^/api/repos/${composerRepoId.replace(/[.*+?^${}()|[\\]\\\\]/g, '\\\\$&')}/branches/[^/]+/files/search`,
    )
    const stubUploadMatch = new RegExp(
      `^/api/repos/${composerRepoId.replace(/[.*+?^${}()|[\\]\\\\]/g, '\\\\$&')}/branches/[^/]+/upload`,
    )
    window.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url
      if (stubMatch.test(url)) {
        const u = new URL(url, window.location.origin)
        const q = (u.searchParams.get('query') || '').toLowerCase()
        const candidates = [
          { path: 'README.md', isDir: false },
          { path: 'README.harness.md', isDir: false },
          { path: 'docs/README.md', isDir: false },
          { path: 'src/index.ts', isDir: false },
        ]
        const results = candidates.filter((c) => !q || c.path.toLowerCase().includes(q))
        return Promise.resolve(
          new Response(JSON.stringify({ results }), {
            status: 200,
            headers: { 'content-type': 'application/json' },
          }),
        )
      }
      if (stubUploadMatch.test(url)) {
        // Return a fake upload response so paste / drop attachments
        // resolve to "ready". The `path` is synthetic but realistic.
        let name = 'pasted'
        let kind: 'image' | 'file' = 'file'
        let mime = 'application/octet-stream'
        if (init?.body instanceof FormData) {
          const f = init.body.get('file')
          if (f instanceof File) {
            name = f.name || (f.type.startsWith('image/') ? 'pasted.png' : 'pasted')
            mime = f.type || mime
            if (mime.startsWith('image/')) kind = 'image'
          }
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({
              path: `/tmp/palmux-uploads/harness-${Date.now()}-${name}`,
              originalName: name,
              name,
              mime,
              kind,
            }),
            { status: 200, headers: { 'content-type': 'application/json' } },
          ),
        )
      }
      return original(input as RequestInfo, init)
    }) as typeof window.fetch
    return () => {
      window.fetch = original
    }
  }, [showComposer, composerRepoId])

  const listHandleRef = useRef<ConversationListHandle | null>(null)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const [exportOpen, setExportOpen] = useState(false)

  // Hotfix regression knob: `?stream=N` appends a new dummy assistant
  // turn every N ms so an E2E test can reproduce the race where a
  // streaming chunk commits in the same React batch as a user wheel.
  // S43cfb1-4: the auto-follow + scroll-restore wiring is now provided
  // by useScrollAutoFollow (same hook the real Claude tab uses), so
  // the "yank only when user hasn't touched the scrollbar" guard is
  // shared across the harness and the production view — no more
  // double-maintained logic.
  const streamRate = Math.max(0, parseInt(params.get('stream') ?? '0', 10) || 0)
  const [streamCount, setStreamCount] = useState(0)
  useEffect(() => {
    if (streamRate <= 0) return
    const id = window.setInterval(() => {
      setStreamCount((n) => n + 1)
    }, streamRate)
    return () => window.clearInterval(id)
  }, [streamRate])

  // Hotfix regression knob: `?idlePulseMs=N` forces a new turns array
  // reference every N ms WITHOUT changing content — simulates an
  // idle-time WS event (status="idle" but a generic event arrived
  // and the reducer produced a new state object). Reproduces the
  // reported "scroll yanks back to bottom while no LLM output is
  // happening" bug: any dep change re-fires the auto-follow effect,
  // which calls scrollToBottom even though the agent is idle.
  const idlePulseRate = Math.max(0, parseInt(params.get('idlePulseMs') ?? '0', 10) || 0)
  const [idlePulseTick, setIdlePulseTick] = useState(0)
  useEffect(() => {
    if (idlePulseRate <= 0) return
    const id = window.setInterval(() => {
      setIdlePulseTick((n) => n + 1)
    }, idlePulseRate)
    return () => window.clearInterval(id)
  }, [idlePulseRate])
  const streamedTurns = useMemo(() => {
    if (streamRate <= 0) return null
    const extras: Turn[] = []
    for (let i = 0; i < streamCount; i++) {
      extras.push({
        id: `stream-${i}`,
        role: 'assistant',
        blocks: [{ id: `stream-b-${i}`, kind: 'text', text: `streamed line ${i}`, done: true }],
      })
    }
    return [...turns, ...extras]
  }, [streamRate, streamCount, turns])
  // idlePulseMs: spread the same turns into a new array every tick so
  // the reference changes (reproducing real-world WS events that fire
  // the agent-state reducer at idle).
  const pulsedTurns = useMemo(() => {
    if (idlePulseRate <= 0) return null
    const base = streamedTurns ?? overrideTurns ?? turns
    // Returning a new array reference each tick is the whole point.
    return [...base]
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [idlePulseRate, idlePulseTick, streamedTurns, overrideTurns, turns])
  const rawDisplayTurns = pulsedTurns ?? streamedTurns ?? overrideTurns ?? turns
  // S43cfb1-1-8: in blocks=all mode, hide sub-agent (parentToolUseId)
  // child turns from the top-level list — they're rendered nested
  // under the Task block via renderTaskChildren. Without this filter
  // the child turn would render twice (once flat, once inside the
  // tree) AND occupy a virtualised row slot.
  const effectiveDisplayTurns = useMemo(
    () =>
      blocksMode
        ? rawDisplayTurns.filter((t) => !t.parentToolUseId)
        : rawDisplayTurns,
    [blocksMode, rawDisplayTurns],
  )
  // Full turn list (including child sub-agent turns) so renderTaskChildren
  // can find them by parentToolUseId lookup.
  const allTurnsForChildLookup = rawDisplayTurns
  const turnIds = useMemo(() => effectiveDisplayTurns.map((t) => t.id), [effectiveDisplayTurns])

  // S43cfb1-4: replace the harness-local autoFollow state +
  // user-input timestamp + onListScroll + scroll-to-bottom effect
  // with the shared hook, so a future fix to the scroll race
  // doesn't have to be applied to two files.
  //
  // contentSeq simulates the reducer-side counter:
  //   - Each `?stream=N` tick bumps it (= a new chunk landed).
  //   - `?idlePulseMs=N` does NOT bump it (= unrelated state churn
  //     that happens to mutate `turns` reference but doesn't
  //     correspond to a content event). The auto-follow effect must
  //     therefore never fire for idle pulses.
  const contentSeq = streamCount  // grows monotonically with stream chunks
  const {
    autoFollow,
    onListScroll,
    onUserInput,
    scrollToLatest,
  } = useScrollAutoFollow({
    repoId: 'test',
    branchId: 'harness',
    tabId: sessionId,
    sessionId,
    contentSeq,
    hasTurns: effectiveDisplayTurns.length > 0,
    turnIds,
    listHandleRef,
  })

  // S018: Cmd+F search wiring. Always created so the harness can be
  // queried by E2E even when search=0 — but the bar only renders when
  // the user opens it (or search=1).
  const search = useConversationSearch(turns, (idx) => {
    listHandleRef.current?.scrollToRow(idx, { align: 'center', behavior: 'smooth' })
  })
  // Auto-open the bar when search=1 so E2E can drive it from the URL.
  useEffect(() => {
    if (showSearch && !search.state.open) search.open()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [showSearch])
  // Keyboard binding mirroring claude-agent-view's behaviour.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey)) return
      if (e.key !== 'f' && e.key !== 'F') return
      const wrap = wrapRef.current
      if (!wrap) return
      const active = document.activeElement
      if (!(wrap.contains(active) || active === document.body)) return
      e.preventDefault()
      search.open()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [search])

  // S43cfb1-4: scroll restore + persist now wired through
  // useScrollAutoFollow above (same as the real Claude tab).

  // S43cfb1-1-8: in blocks=all mode the harness renders a special
  // "task-tree" combination where the assistant turn carrying the
  // Task tool_use block is rendered with renderTaskChildren wired so
  // the TaskTreeBlock dispatches (vs the flat ToolUseBlock renderer
  // that's used by default). We surface child sub-agent turns by
  // looking up `parentToolUseId === block.toolUseId` in the displayed
  // turns list.
  const renderBlockWithTestId = (_turn: Turn, b: Block, allTurns: Turn[]): React.ReactNode => {
    const renderTaskChildren =
      blocksMode && b.kind === 'tool_use' && (b.name || '').toLowerCase() === 'task' && b.toolUseId
        ? () => {
            const children = allTurns.filter(
              (t) => t.parentToolUseId && b.toolUseId && t.parentToolUseId === b.toolUseId,
            )
            return (
              <>
                {children.map((c) => (
                  <div key={c.id} data-testid={`harness-task-child-${c.id}`}>
                    {c.blocks.map((cb) => (
                      <div key={cb.id} data-testid={`block-${cb.kind}-${cb.id}`}>
                        <BlockView block={cb} />
                      </div>
                    ))}
                  </div>
                ))}
              </>
            )
          }
        : undefined
    // The TaskTreeBlock dispatcher only kicks in when renderTaskChildren
    // is supplied; otherwise the same Block rendered as a tool_use stays
    // a flat ToolUseBlock. We tag the wrapper as `task-tree` only when
    // the dispatcher actually produces a TaskTreeBlock — i.e. when
    // renderTaskChildren is supplied — so the E2E selector matches the
    // renderer that ran.
    const testIdKind = renderTaskChildren ? 'task-tree' : b.kind
    return (
      <div key={b.id} data-testid={`block-${testIdKind}-${b.id}`}>
        <BlockView block={b} renderTaskChildren={renderTaskChildren} />
      </div>
    )
  }

  const renderTurn = (turn: Turn) => {
    if (showRewind && turn.role === 'user') {
      return (
        <div className={styles.virtualTurnRow} data-testid={`harness-turn-${turn.id}`}>
          <div className={styles.turnUser}>
            <UserTurnEditor
              turn={turn}
              activeVersionIndex={activeVersionByTurnId[turn.id] ?? -1}
              onSetVersion={(idx) =>
                setActiveVersionByTurnId((prev) => {
                  const next = { ...prev }
                  if (idx === -1) delete next[turn.id]
                  else next[turn.id] = idx
                  return next
                })
              }
              onRewind={onRewindLocal}
              onRewindApplyLocal={onRewindApplyLocalNoop}
              editing={editingTurnId === turn.id}
              onEditingChange={onEditingChange}
            />
          </div>
        </div>
      )
    }
    return (
      <div className={styles.virtualTurnRow} data-testid={`harness-turn-${turn.id}`}>
        {turn.blocks.map((b) => renderBlockWithTestId(turn, b, allTurnsForChildLookup))}
      </div>
    )
  }

  const activeBlockId = search.state.matches[search.state.active]?.blockId

  return (
    <div
      ref={wrapRef}
      style={{
        position: 'fixed',
        inset: 0,
        background: 'var(--color-bg)',
        color: 'var(--color-fg)',
        display: 'flex',
        flexDirection: 'column',
      }}
      data-testid="harness-root"
    >
      <div
        style={{
          padding: '4px 8px',
          fontSize: 11,
          color: 'var(--color-fg-muted)',
          borderBottom: '1px solid var(--color-border)',
          flex: '0 0 auto',
          display: 'flex',
          alignItems: 'center',
          gap: 8,
        }}
        data-testid="harness-stats"
      >
        <span>turns={turns.length}, readLines={readLines}, sessionId={sessionId}</span>
        {showExport && (
          <button
            type="button"
            data-testid="harness-export-btn"
            onClick={() => setExportOpen(true)}
          >
            export
          </button>
        )}
      </div>
      <ConversationSearchBar
        state={search.state}
        setQuery={search.setQuery}
        onNext={search.next}
        onPrev={search.prev}
        onClose={search.close}
        inputRef={search.inputRef}
      />
      {showCompactingSpinner && (
        <div className={styles.compactSpinner} data-testid="compacting-spinner">
          <span>Compacting conversation…</span>
        </div>
      )}
      <ConversationExportDialog
        open={exportOpen}
        onClose={() => setExportOpen(false)}
        turns={turns}
        branchId="harness"
        repoId="test"
        sessionId={sessionId}
        model="harness"
      />
      <div
        className={styles.conversation}
        style={{ flex: 1 }}
        data-testid="harness-conversation"
      >
        <ClaudeSearchProvider
          query={search.state.query}
          openedBlocks={search.state.openedBlocks}
          activeBlockId={activeBlockId}
        >
          <ConversationList
            ref={listHandleRef}
            turns={effectiveDisplayTurns}
            sessionKey={sessionId}
            renderTurn={renderTurn}
            onScroll={showAutoFollow ? onListScroll : undefined}
            onUserInput={showAutoFollow ? onUserInput : undefined}
          />
        </ClaudeSearchProvider>
        {showAutoFollow && !autoFollow && (
          <button
            type="button"
            data-testid="harness-scroll-to-bottom"
            data-autofollow="false"
            onClick={() => scrollToLatest(buttonBehavior)}
            style={{
              position: 'absolute',
              bottom: 16,
              left: '50%',
              transform: 'translateX(-50%)',
              padding: '6px 12px',
              borderRadius: 16,
              border: '1px solid var(--color-border)',
              background: 'var(--color-elevated)',
              color: 'var(--color-fg)',
              cursor: 'pointer',
              zIndex: 5,
            }}
          >
            ↓ Scroll to latest
          </button>
        )}
        {showAutoFollow && (
          <div
            data-testid="harness-autofollow"
            data-value={autoFollow ? 'true' : 'false'}
            style={{ display: 'none' }}
          />
        )}
      </div>
      {showComposer && (
        <div data-testid="harness-composer-wrap">
          <div
            style={{
              display: 'flex',
              gap: 4,
              padding: '4px 8px',
              borderTop: '1px solid var(--color-border)',
              background: 'var(--color-surface)',
              fontSize: 11,
            }}
          >
            <button
              type="button"
              data-testid="harness-toggle-streaming"
              data-streaming={composerState.isStreaming ? 'true' : 'false'}
              onClick={() =>
                setComposerState((prev) => ({ ...prev, isStreaming: !prev.isStreaming }))
              }
              style={{
                padding: '2px 8px',
                borderRadius: 4,
                border: '1px solid var(--color-border)',
                background: 'var(--color-elevated)',
                color: 'var(--color-fg)',
                cursor: 'pointer',
              }}
            >
              {composerState.isStreaming ? 'streaming=true' : 'streaming=false'}
            </button>
          </div>
          {composerTabIds.length > 1 && (
            <div
              data-testid="harness-composer-tabs"
              style={{
                display: 'flex',
                gap: 4,
                padding: '4px 8px',
                borderTop: '1px solid var(--color-border)',
                background: 'var(--color-surface)',
              }}
            >
              {composerTabIds.map((tid) => (
                <button
                  key={tid}
                  type="button"
                  data-testid={`harness-tab-${tid}`}
                  data-active={composerState.activeTabId === tid ? 'true' : 'false'}
                  onClick={() =>
                    setComposerState((prev) => ({ ...prev, activeTabId: tid }))
                  }
                  style={{
                    padding: '4px 10px',
                    borderRadius: 4,
                    border: '1px solid var(--color-border)',
                    background:
                      composerState.activeTabId === tid
                        ? 'var(--color-elevated)'
                        : 'transparent',
                    color: 'var(--color-fg)',
                    cursor: 'pointer',
                  }}
                >
                  {tid}
                </button>
              ))}
            </div>
          )}
          <Composer
            // The Composer's draft persistence is keyed by tabId, so
            // remounting on activeTabId change restores that tab's
            // draft from localStorage. We KEY by tabId to force the
            // remount (otherwise React reuses the textarea state and
            // the typed text from tab A leaks into tab B).
            key={composerState.activeTabId}
            repoId={composerRepoId}
            branchId={composerBranchId}
            tabId={composerState.activeTabId}
            onSend={(content: string) => {
              // eslint-disable-next-line no-console
              console.log('[harness] composer.onSend', content)
              setComposerState((prev) => ({
                ...prev,
                lastSendBody: content,
                lastSendAt: Date.now(),
                sendCount: prev.sendCount + 1,
              }))
            }}
            onInterrupt={() => {
              // eslint-disable-next-line no-console
              console.log('[harness] composer.onInterrupt')
              setComposerState((prev) => ({
                ...prev,
                lastInterruptAt: Date.now(),
                interruptCount: prev.interruptCount + 1,
              }))
            }}
            isStreaming={composerState.isStreaming}
            disabled={false}
            connState="open"
            model="sonnet"
            effort=""
            permissionMode="default"
            permissionModes={['default', 'plan', 'acceptEdits']}
            onModelChange={() => undefined}
            onEffortChange={() => undefined}
            onPermissionModeChange={() => undefined}
            initInfo={{
              commands: [
                { name: 'help', description: 'Show help' },
                { name: 'plan', description: 'Enter plan mode' },
              ],
            }}
          />
          <div
            data-testid="harness-composer-state"
            data-active-tab={composerState.activeTabId}
            data-last-send-body={composerState.lastSendBody ?? ''}
            data-last-send-at={composerState.lastSendAt ?? ''}
            data-last-interrupt-at={composerState.lastInterruptAt ?? ''}
            data-send-count={composerState.sendCount}
            data-interrupt-count={composerState.interruptCount}
            style={{ display: 'none' }}
          />
        </div>
      )}
    </div>
  )
}
