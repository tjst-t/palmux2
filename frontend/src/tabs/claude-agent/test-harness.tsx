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

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'

import { BlockView } from './blocks'
import { ConversationExportDialog } from './conversation-export'
import { UserTurnEditor } from './user-turn-editor'
import {
  ConversationList,
  type ConversationListHandle,
} from './conversation-list'
import {
  scrollStorageKey,
  usePersistScroll,
  useScrollRestore,
} from './scroll-hooks'
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
  const turnsCount = Math.max(0, parseInt(params.get('turns') ?? '20', 10) || 0)
  const readLines = Math.max(0, parseInt(params.get('readLines') ?? '0', 10) || 0)
  const sessionId = params.get('sessionId') ?? `harness-${turnsCount}-${readLines}`
  const showSearch = params.get('search') === '1'
  const showExport = params.get('export') === '1'
  const showCompactBoundary = params.get('compactBoundary') === '1'
  const showCompactingSpinner = params.get('compacting') === '1'
  const showRewind = params.get('rewind') === '1'
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
    () => syntheticTurns(turnsCount, readLines, showCompactBoundary),
    [turnsCount, readLines, showCompactBoundary],
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

  const listHandleRef = useRef<ConversationListHandle | null>(null)
  const containerRef = useRef<HTMLDivElement | null>(null)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const [exportOpen, setExportOpen] = useState(false)
  const [autoFollow, setAutoFollow] = useState(true)
  const autoFollowRef = useRef<boolean>(true)
  // Hotfix: mirror claude-agent-view's user-input timestamp so the
  // streaming auto-follow effect below skips yank-to-bottom while
  // the user is actively scrolling.
  const lastUserInputAtRef = useRef<number>(0)
  const onUserInput = useCallback(() => {
    lastUserInputAtRef.current = performance.now()
  }, [])
  // Mirror claude-agent-view's logic: only user-driven scrolls flip
  // autoFollow off; programmatic ones can only confirm (set true) but
  // not unset.
  const onListScroll = useCallback(
    (
      scrollTop: number,
      scrollHeight: number,
      clientHeight: number,
      isUserDriven: boolean,
    ) => {
      const atBottom = scrollHeight - scrollTop - clientHeight < 32
      if (isUserDriven) {
        autoFollowRef.current = atBottom
        setAutoFollow(atBottom)
      } else if (atBottom) {
        autoFollowRef.current = true
        setAutoFollow(true)
      }
    },
    [],
  )

  // Hotfix regression knob: `?stream=N` appends a new dummy assistant
  // turn every N ms so an E2E test can reproduce the race where a
  // streaming chunk commits in the same React batch as a user wheel
  // (the wheel's deferred `scroll` event fires after the auto-follow
  // effect, leaving autoFollowRef stale-true and yanking the user
  // back to bottom). Mirrors the parent's auto-follow effect with
  // the same guards (autoFollowRef + lastUserInputAtRef).
  const streamRate = Math.max(0, parseInt(params.get('stream') ?? '0', 10) || 0)
  const [streamCount, setStreamCount] = useState(0)
  useEffect(() => {
    if (streamRate <= 0) return
    const id = window.setInterval(() => {
      setStreamCount((n) => n + 1)
    }, streamRate)
    return () => window.clearInterval(id)
  }, [streamRate])
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
  const effectiveDisplayTurns = streamedTurns ?? overrideTurns ?? turns

  // Mirror parent's auto-follow effect for the streaming case.
  useEffect(() => {
    if (streamRate <= 0) return
    if (!autoFollowRef.current) return
    if (performance.now() - lastUserInputAtRef.current < 250) return
    listHandleRef.current?.scrollToBottom('instant')
  }, [streamCount, streamRate])

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

  // Re-resolve the underlying scroll element whenever the list re-mounts
  // (sessionId change). The ref callback alone fires before the List
  // has installed its DOM, so on first mount element() is still null —
  // a useEffect after paint catches the populated element.
  useEffect(() => {
    const tick = () => {
      const el = listHandleRef.current?.element() ?? null
      if (el) containerRef.current = el
    }
    tick()
    const t = window.setTimeout(tick, 50)
    return () => window.clearTimeout(t)
  }, [sessionId, turns.length])

  const storageKey = scrollStorageKey('test', 'harness', sessionId)
  const turnIds = useMemo(() => turns.map((t) => t.id), [turns])
  const harnessScrollToRow = useCallback((index: number) => {
    listHandleRef.current?.scrollToRow(index, {
      align: 'start',
      behavior: 'instant',
    })
  }, [])
  useScrollRestore({
    sessionId,
    storageKey,
    containerRef: containerRef as React.RefObject<HTMLDivElement | null>,
    hasTurns: turns.length > 0,
    turnIds,
    scrollToRow: harnessScrollToRow,
  })
  usePersistScroll({
    sessionId,
    storageKey,
    containerRef: containerRef as React.RefObject<HTMLDivElement | null>,
  })

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
        {turn.blocks.map((b) => (
          <BlockView key={b.id} block={b} />
        ))}
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
            ref={(h) => {
              listHandleRef.current = h
              // Resolve the underlying scroll element so persist/restore hooks fire.
              containerRef.current = h?.element() ?? null
            }}
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
            onClick={() => {
              listHandleRef.current?.scrollToBottom(buttonBehavior)
              setAutoFollow(true)
            }}
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
    </div>
  )
}
