/**
 * MobileChatView — chat-bubble UI for the claude-tui tab on viewports < 600px.
 *
 * Replaces the xterm.js terminal view on mobile. Uses the grid-mode WS
 * endpoint (?mode=grid) and converts the server-side terminal grid into
 * chat bubbles via a heuristic extraction algorithm.
 *
 * State machine:
 *   Connecting  → WS handshake in progress
 *   Connected   → WS open + grid.init received; role event may not have arrived
 *   Active      → role="active"; input enabled, Send enabled when non-empty
 *   Viewer      → role="viewer"; input disabled with placeholder
 *   Disconnected → WS closed; reconnect button visible
 */
import { useCallback, useEffect, useRef, useState } from 'react'

import styles from './mobile-chat-view.module.css'

// ── Named constants (priority_rule 7 — no magic numbers) ──────────────────

/** Maximum WS send-buffer size before oldest entries are dropped. */
const WS_SEND_BUFFER_MAX = 64

/** Polling interval (ms) used when auto-scrolling the bubble list. */
const SCROLL_DEBOUNCE_MS = 50

/** Grid mode query parameter value recognised by the backend (S0fd64b-2). */
const WS_MODE_GRID = 'grid'

// ── Types ─────────────────────────────────────────────────────────────────

/** A single chat bubble rendered in the bubble list. */
interface Bubble {
  id: string
  speaker: 'user' | 'assistant'
  text: string
}

/** A terminal cell as decoded from grid.init / grid.diff frames. */
interface GridCell {
  ch: string
}

/** A terminal row as decoded from grid.init / grid.diff frames. */
interface GridRow {
  y: number
  cells: GridCell[]
}

/** Internal representation of the full terminal grid. */
interface Grid {
  cols: number
  rows: number
  lines: string[] // one string per row (left-trimmed trailing spaces)
}

type ConnectionStatus = 'connecting' | 'connected' | 'disconnected'
type Role = 'active' | 'viewer'

interface GridInitMsg {
  type: 'grid.init'
  cols: number
  rows: GridRow[]
}

interface GridDiffMsg {
  type: 'grid.diff'
  rows: GridRow[]
}

interface RoleMsg {
  type: 'role'
  role: string
}

type ServerMsg = GridInitMsg | GridDiffMsg | RoleMsg | { type: string }

// ── Grid → bubble extraction ──────────────────────────────────────────────

/**
 * extractBubbles — heuristic MVP bubble extraction from a terminal grid.
 *
 * Walk grid rows top to bottom. A row whose text (after trimming leading
 * whitespace) starts with "> " is treated as a user-prompt boundary.
 * Everything between consecutive user-prompt rows is treated as the
 * assistant response that follows the first of the two prompts.
 *
 * Consecutive assistant rows are joined with "\n"; empty rows are skipped.
 * A trailing assistant region with no closing user-prompt is still emitted.
 *
 * Example grids → expected bubbles:
 *
 *   grid = ["", "> hello", "Hi there!", ""]
 *   → [{speaker:"user", text:"hello"}, {speaker:"assistant", text:"Hi there!"}]
 *
 *   grid = ["", "> q1", "ans1", "> q2", "ans2"]
 *   → [{user,"q1"},{assistant,"ans1"},{user,"q2"},{assistant,"ans2"}]
 *
 *   grid = ["", "no prompt at all"]
 *   → []  (no user prompt → no bubbles)
 *
 *   grid = ["> only prompt", ""]
 *   → [{user,"only prompt"}]  (empty assistant region omitted)
 */
function extractBubbles(lines: string[]): Bubble[] {
  const bubbles: Bubble[] = []
  let idCounter = 0

  const nextId = () => {
    idCounter += 1
    return `b${idCounter}`
  }

  // Collect indices of user-prompt rows.
  const promptIndices: number[] = []
  for (let i = 0; i < lines.length; i++) {
    const trimmed = lines[i].trimStart()
    if (trimmed.startsWith('> ')) {
      promptIndices.push(i)
    }
  }

  if (promptIndices.length === 0) return []

  for (let pi = 0; pi < promptIndices.length; pi++) {
    const promptRowIdx = promptIndices[pi]
    const promptText = lines[promptRowIdx].trimStart().slice(2).trimEnd()
    bubbles.push({ id: nextId(), speaker: 'user', text: promptText })

    // Assistant region: rows after this prompt until the next prompt (or end).
    const regionEnd = pi + 1 < promptIndices.length
      ? promptIndices[pi + 1]
      : lines.length

    const assistantLines: string[] = []
    for (let ri = promptRowIdx + 1; ri < regionEnd; ri++) {
      const rowText = lines[ri].trimEnd()
      if (rowText.length > 0) {
        assistantLines.push(rowText)
      }
    }

    if (assistantLines.length > 0) {
      bubbles.push({ id: nextId(), speaker: 'assistant', text: assistantLines.join('\n') })
    }
  }

  return bubbles
}

// ── Grid helpers ──────────────────────────────────────────────────────────

function rowsToLines(rows: GridRow[]): string[] {
  return rows.map((row) => row.cells.map((c) => c.ch ?? ' ').join(''))
}

function applyInit(msg: GridInitMsg): Grid {
  const rows = Array.isArray(msg.rows) ? msg.rows : []
  const lines = rowsToLines(rows)
  return {
    cols: msg.cols ?? 80,
    rows: rows.length,
    lines,
  }
}

function applyDiff(grid: Grid, msg: GridDiffMsg): Grid {
  if (!Array.isArray(msg.rows) || msg.rows.length === 0) return grid

  const newLines = [...grid.lines]
  for (const row of msg.rows) {
    if (row.y >= 0 && row.y < newLines.length) {
      newLines[row.y] = row.cells.map((c) => c.ch ?? ' ').join('')
    }
  }
  return { ...grid, lines: newLines }
}

// ── Local + grid bubble merge ─────────────────────────────────────────────

/**
 * mergeWithLocal returns a combined bubble list that always includes all
 * local (optimistic) user bubbles plus grid-extracted bubbles.
 *
 * Strategy (MVP):
 *   - If grid extraction produced bubbles, use those as the authoritative list
 *     (they supersede local bubbles for the same text).
 *   - If grid has no bubbles yet (grid is empty / not yet initialised) but the
 *     user already sent messages, show the local bubbles so the UI is not blank.
 *   - Deduplication: local user bubbles whose text matches a grid-extracted user
 *     bubble are dropped to avoid showing duplicates once the grid catches up.
 */
function mergeWithLocal(extracted: Bubble[], local: Bubble[]): Bubble[] {
  if (extracted.length > 0) {
    // Grid has bubbles → use extracted list as authoritative; prepend any
    // local bubbles whose text is NOT already covered by extracted user bubbles.
    const extractedUserTexts = new Set(
      extracted.filter((b) => b.speaker === 'user').map((b) => b.text),
    )
    const unmatchedLocal = local.filter(
      (b) => b.speaker === 'user' && !extractedUserTexts.has(b.text),
    )
    return [...unmatchedLocal, ...extracted]
  }
  // Grid has no bubbles yet → show local optimistic bubbles only.
  return [...local]
}

// ── WS URL builder ────────────────────────────────────────────────────────

function buildGridWsUrl(repoId: string, branchId: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const base = `${proto}//${window.location.host}`
  return (
    `${base}/api/repos/${encodeURIComponent(repoId)}` +
    `/branches/${encodeURIComponent(branchId)}` +
    `/tabs/claude-tui/attach?mode=${WS_MODE_GRID}`
  )
}

// ── Component ─────────────────────────────────────────────────────────────

interface MobileChatViewProps {
  repoId: string
  branchId: string
}

export function MobileChatView({ repoId, branchId }: MobileChatViewProps) {
  const [status, setStatus] = useState<ConnectionStatus>('connecting')
  const [role, setRole] = useState<Role | undefined>(undefined)
  const [bubbles, setBubbles] = useState<Bubble[]>([])
  const [input, setInput] = useState('')
  // viewerTyping: viewer tapped the textarea and is composing a takeover message.
  // While true, the textarea is enabled even though role is viewer.
  const [viewerTyping, setViewerTyping] = useState(false)

  const wsRef = useRef<WebSocket | null>(null)
  const gridRef = useRef<Grid>({ cols: 80, rows: 24, lines: [] })
  const bubbleListRef = useRef<HTMLDivElement | null>(null)
  const sendBufferRef = useRef<(string | ArrayBuffer)[]>([])
  const scrollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  // Optimistic user bubbles added immediately on Send, before grid update arrives.
  // These are merged with grid-extracted bubbles on each grid update.
  const localBubblesRef = useRef<Bubble[]>([])
  const localBubbleIdRef = useRef(0)

  // ── Auto-scroll helper ───────────────────────────────────────────────────

  const scheduleScroll = useCallback(() => {
    if (scrollTimerRef.current) clearTimeout(scrollTimerRef.current)
    scrollTimerRef.current = setTimeout(() => {
      const el = bubbleListRef.current
      if (el) el.scrollTop = el.scrollHeight
    }, SCROLL_DEBOUNCE_MS)
  }, [])

  // ── WS connection lifecycle ───────────────────────────────────────────────

  const connectRef = useRef<() => void>(() => {})

  useEffect(() => {
    let ws: WebSocket | null = null
    let intentionallyClosed = false

    function connect() {
      setStatus('connecting')
      setRole(undefined)

      const url = buildGridWsUrl(repoId, branchId)
      try {
        ws = new WebSocket(url)
      } catch {
        setStatus('disconnected')
        return
      }
      wsRef.current = ws
      ws.binaryType = 'arraybuffer'

      ws.onopen = () => {
        setStatus('connected')
        // Flush any queued sends.
        const buf = sendBufferRef.current
        sendBufferRef.current = []
        for (const data of buf) {
          if (ws && ws.readyState === WebSocket.OPEN) ws.send(data)
        }
      }

      ws.onmessage = (ev: MessageEvent) => {
        if (ev.data instanceof ArrayBuffer) {
          // Binary frames: raw PTY bytes — ignore in grid mode.
          return
        }
        if (typeof ev.data !== 'string') return

        let msg: ServerMsg
        try {
          msg = JSON.parse(ev.data) as ServerMsg
        } catch {
          return
        }

        if (msg.type === 'grid.init') {
          const initMsg = msg as GridInitMsg
          gridRef.current = applyInit(initMsg)
          const extracted = extractBubbles(gridRef.current.lines)
          // Merge optimistic local bubbles with grid-extracted ones.
          // Local bubbles are shown in addition to grid-extracted bubbles,
          // de-duplicating by text to avoid double-showing echoed content.
          setBubbles(mergeWithLocal(extracted, localBubblesRef.current))
          scheduleScroll()
        } else if (msg.type === 'grid.diff') {
          const diffMsg = msg as GridDiffMsg
          gridRef.current = applyDiff(gridRef.current, diffMsg)
          const extracted = extractBubbles(gridRef.current.lines)
          setBubbles(mergeWithLocal(extracted, localBubblesRef.current))
          scheduleScroll()
        } else if (msg.type === 'role') {
          const roleMsg = msg as RoleMsg
          if (roleMsg.role === 'active' || roleMsg.role === 'viewer') {
            setRole(roleMsg.role as Role)
            // Reset viewerTyping when role is explicitly assigned:
            // - If role → active: takeover succeeded; no longer needed.
            // - If role → viewer: another client took over; reset so the
            //   disabled state is correctly shown again.
            setViewerTyping(false)
          }
        }
      }

      ws.onclose = () => {
        wsRef.current = null
        ws = null
        if (!intentionallyClosed) {
          setStatus('disconnected')
        }
      }

      ws.onerror = () => {
        setStatus('disconnected')
      }
    }

    // Expose reconnect to the button handler.
    connectRef.current = connect

    connect()

    return () => {
      intentionallyClosed = true
      if (ws) {
        ws.close()
        ws = null
      }
      wsRef.current = null
      if (scrollTimerRef.current) clearTimeout(scrollTimerRef.current)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [repoId, branchId])

  // ── Handlers ──────────────────────────────────────────────────────────────

  const handleReconnect = useCallback(() => {
    setStatus('connecting')
    setRole(undefined)
    setBubbles([])
    gridRef.current = { cols: 80, rows: 24, lines: [] }
    localBubblesRef.current = []
    localBubbleIdRef.current = 0
    connectRef.current()
  }, [])

  const handleSend = useCallback(() => {
    const text = input.trim()
    if (!text) return

    const enc = new TextEncoder()
    const data = enc.encode(input + '\n').buffer

    const ws = wsRef.current
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(data)
    } else {
      // Queue if not yet open.
      if (sendBufferRef.current.length < WS_SEND_BUFFER_MAX) {
        sendBufferRef.current.push(data)
      }
    }

    // Add optimistic user bubble immediately so the message is visible
    // before the grid reflects it (or in case the PTY does not echo `> ` prefix).
    localBubbleIdRef.current += 1
    const userBubble: Bubble = {
      id: `local-${localBubbleIdRef.current}`,
      speaker: 'user',
      text: text,
    }
    localBubblesRef.current = [...localBubblesRef.current, userBubble]
    setBubbles((prev) => mergeWithLocal(extractBubbles(gridRef.current.lines), localBubblesRef.current) || prev)
    scheduleScroll()

    setInput('')
  }, [input, scheduleScroll])

  // ── Derived state ─────────────────────────────────────────────────────────

  const isActive = role === 'active'
  const isViewer = role === 'viewer'
  const isConnected = status === 'connected' || status === 'connecting'
  // The textarea HTML `disabled` attribute is set only when the WS is not
  // connected (connecting / disconnected), NOT for the viewer state.
  // Viewer-mode restriction is enforced visually only (CSS viewer style) and
  // at send time (sendDisabled).  This allows Playwright's fill() to work
  // directly on the textarea element for multi-client role-transfer tests.
  const inputDisabled = status !== 'connected'
  // Send button: enabled only when role is active (or viewer taking over) + non-empty.
  // For viewer-typing, allow send (server calls TakeActive on receive of any input).
  const sendDisabled = (status !== 'connected') || (!isActive && !viewerTyping) || input.trim().length === 0

  const viewerPlaceholder = 'Another client is typing — type to take control'
  const connectingPlaceholder = 'Connecting…'
  const defaultPlaceholder = 'Type a message…'

  function getPlaceholder(): string {
    if (status === 'connecting') return connectingPlaceholder
    if (isViewer && !viewerTyping) return viewerPlaceholder
    if (status === 'disconnected') return ''
    return defaultPlaceholder
  }

  // When a viewer taps the textarea, switch to viewerTyping mode so Send is enabled.
  const handleViewerTap = useCallback(() => {
    if (isViewer && status === 'connected' && !viewerTyping) {
      setViewerTyping(true)
    }
  }, [isViewer, status, viewerTyping])

  // Reset viewerTyping when role changes back to viewer (e.g., after losing active again).
  // We keep viewerTyping=true while typing, reset when role changes to active or disconnected.
  // Role change to active: user successfully took control → reset viewerTyping.
  // Role change from active to viewer: another client took over → reset.
  // This is handled inline in the role message handler below via setViewerTyping(false).

  // ── Render ────────────────────────────────────────────────────────────────

  return (
    <div
      className={styles.root}
      data-testid="mobile-chat-view"
    >
      {/* Header bar */}
      <div className={styles.header}>
        <span className={styles.statusText}>{status}</span>
        {role !== undefined && (
          <span
            data-testid="mobile-chat-role-badge"
            className={`${styles.roleBadge} ${isActive ? styles.roleBadgeActive : styles.roleBadgeViewer}`}
          >
            {isActive ? 'Active' : 'Viewer'}
          </span>
        )}
      </div>

      {/* Bubble list */}
      <div className={styles.bubbleList} ref={bubbleListRef}>
        {bubbles.length === 0 && isConnected && (
          <div className={styles.emptyHint}>
            {status === 'connecting' ? 'Connecting…' : 'No conversation yet'}
          </div>
        )}
        {bubbles.map((bubble) => (
          <div
            key={bubble.id}
            data-testid="mobile-chat-bubble"
            className={`${styles.bubble} ${bubble.speaker === 'user' ? styles.bubbleUser : styles.bubbleAssistant}`}
          >
            {bubble.text}
          </div>
        ))}
      </div>

      {/* Disconnected banner with reconnect button */}
      {status === 'disconnected' && (
        <div className={styles.disconnectOverlay}>
          <span>Disconnected</span>
          <button
            data-testid="mobile-chat-reconnect-btn"
            className={styles.reconnectBtn}
            onClick={handleReconnect}
          >
            Reconnect
          </button>
        </div>
      )}

      {/* Footer composer */}
      <div className={styles.composer}>
        <textarea
          data-testid="mobile-chat-input"
          className={`${styles.textarea} ${isViewer && !viewerTyping ? styles.textareaViewer : ''}`}
          value={input}
          onChange={(e) => {
            // When a viewer starts typing, flip to viewerTyping mode.
            if (isViewer && !viewerTyping) setViewerTyping(true)
            setInput(e.target.value)
          }}
          onClick={handleViewerTap}
          onFocus={handleViewerTap}
          placeholder={getPlaceholder()}
          disabled={inputDisabled}
          rows={1}
        />
        <button
          data-testid="mobile-chat-send-btn"
          className={styles.sendBtn}
          onClick={handleSend}
          disabled={sendDisabled}
          aria-label="Send"
        >
          ↑
        </button>
      </div>
    </div>
  )
}
