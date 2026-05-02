// S017+S018: standalone test harness that mounts ConversationList against
// a synthetic Turn[] generated from URL search params. The route is
// `/__test/claude` and is only meaningful during automated E2E runs —
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

import { useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'

import { BlockView } from './blocks'
import { ConversationExportDialog } from './conversation-export'
import {
  ConversationList,
  type ConversationListHandle,
  scrollStorageKey,
  usePersistScroll,
  useScrollRestore,
} from './conversation-list'
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

  const baseTurns = useMemo(
    () => syntheticTurns(turnsCount, readLines, showCompactBoundary),
    [turnsCount, readLines, showCompactBoundary],
  )
  const turns = useMemo(
    () => (showSearch ? injectSearchNeedles(baseTurns) : baseTurns),
    [baseTurns, showSearch],
  )

  const listHandleRef = useRef<ConversationListHandle | null>(null)
  const containerRef = useRef<HTMLDivElement | null>(null)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const [exportOpen, setExportOpen] = useState(false)

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
  useScrollRestore({
    sessionId,
    storageKey,
    containerRef: containerRef as React.RefObject<HTMLDivElement | null>,
    hasTurns: turns.length > 0,
  })
  usePersistScroll({
    sessionId,
    storageKey,
    containerRef: containerRef as React.RefObject<HTMLDivElement | null>,
  })

  const renderTurn = (turn: Turn) => (
    <div className={styles.virtualTurnRow} data-testid={`harness-turn-${turn.id}`}>
      {turn.blocks.map((b) => (
        <BlockView key={b.id} block={b} />
      ))}
    </div>
  )

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
            turns={turns}
            sessionKey={sessionId}
            renderTurn={renderTurn}
          />
        </ClaudeSearchProvider>
      </div>
    </div>
  )
}
