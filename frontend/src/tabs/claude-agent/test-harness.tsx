// S017: standalone test harness that mounts ConversationList against a
// synthetic Turn[] generated from URL search params. The route is
// `/__test/claude` and is only meaningful during automated E2E runs —
// it has no entry in the app's UI. The harness lets us verify the
// virtualisation + Read-preview behaviour without spinning up the
// claude CLI process for a real conversation, which would be slow and
// non-deterministic.
//
// Search params:
//   turns=N        — generate N synthetic turns (assistant + tool_result pairs)
//   readLines=N    — embed a tool_result with N text lines (Read preview test)
//   sessionId=...  — override the session key (for scroll-restore tests)
//
// The harness stores the same Zustand state shape as the live tab, so
// any code that reads globalSettings.readPreviewLineCount through the
// store works unchanged.

import { useEffect, useMemo, useRef } from 'react'
import { useSearchParams } from 'react-router-dom'

import { BlockView } from './blocks'
import {
  ConversationList,
  type ConversationListHandle,
  scrollStorageKey,
  usePersistScroll,
  useScrollRestore,
} from './conversation-list'
import type { Block, Turn } from './types'
import styles from './claude-agent-view.module.css'

function makeBlock(id: string, kind: Block['kind'], extra: Partial<Block> = {}): Block {
  return { id, kind, ...extra }
}

/** Generate N synthetic turns. Each turn is (a) a user request, then
 *  (b) an assistant text response. Variation in length is deliberate
 *  so dynamic row heights actually exercise. */
function syntheticTurns(n: number, readLines: number): Turn[] {
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
  return out
}

export function TestHarness() {
  const [params] = useSearchParams()
  const turnsCount = Math.max(0, parseInt(params.get('turns') ?? '20', 10) || 0)
  const readLines = Math.max(0, parseInt(params.get('readLines') ?? '0', 10) || 0)
  const sessionId = params.get('sessionId') ?? `harness-${turnsCount}-${readLines}`

  const turns = useMemo(
    () => syntheticTurns(turnsCount, readLines),
    [turnsCount, readLines],
  )

  const listHandleRef = useRef<ConversationListHandle | null>(null)
  const containerRef = useRef<HTMLDivElement | null>(null)

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

  return (
    <div
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
        }}
        data-testid="harness-stats"
      >
        turns={turns.length}, readLines={readLines}, sessionId={sessionId}
      </div>
      <div
        className={styles.conversation}
        style={{ flex: 1 }}
        data-testid="harness-conversation"
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
      </div>
    </div>
  )
}

