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

// ── Grid helpers ──────────────────────────────────────────────────────────
//
// We keep the grid state internally for activity / status detection, but
// we DO NOT derive chat bubbles from it any more — claude's TUI uses
// alternate screen so prior turns are not in the grid. Bubbles come from
// the JSONL transcript via the /transcript REST endpoint instead.

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

function buildTranscriptUrl(repoId: string, branchId: string): string {
  return (
    `/api/repos/${encodeURIComponent(repoId)}` +
    `/branches/${encodeURIComponent(branchId)}` +
    `/tabs/claude-tui/transcript`
  )
}

// Transcript bubbles delay before refetching on grid activity. Debounce in
// milliseconds — long enough that we don't hammer the server during a
// streaming response, short enough that the bubble list updates "promptly"
// once claude finishes a turn.
const TRANSCRIPT_REFETCH_DEBOUNCE_MS = 800

interface TranscriptResponse {
  sessionId: string
  bubbles: { id: string; speaker: 'user' | 'assistant'; text: string }[]
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
  // Optimistic user bubbles added immediately on Send, before the transcript
  // catches up. These are merged with transcript-derived bubbles on each
  // refetch (dedup by text).
  const localBubblesRef = useRef<Bubble[]>([])
  const localBubbleIdRef = useRef(0)

  // Transcript (JSONL) is the authoritative source for conversation history.
  // Grid extraction only reflects the live TUI frame, which in alt-screen
  // mode does not preserve prior turns — so we read the .jsonl via a REST
  // endpoint and re-fetch on activity.
  const transcriptBubblesRef = useRef<Bubble[]>([])
  const transcriptDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const transcriptInflightRef = useRef(false)

  // ── Auto-scroll helper ───────────────────────────────────────────────────

  const scheduleScroll = useCallback(() => {
    if (scrollTimerRef.current) clearTimeout(scrollTimerRef.current)
    scrollTimerRef.current = setTimeout(() => {
      const el = bubbleListRef.current
      if (el) el.scrollTop = el.scrollHeight
    }, SCROLL_DEBOUNCE_MS)
  }, [])

  // ── Transcript fetch ─────────────────────────────────────────────────────

  // recomputeBubbles merges transcript + optimistic local bubbles and
  // pushes the result into setBubbles.
  const recomputeBubbles = useCallback(() => {
    const transcript = transcriptBubblesRef.current
    const local = localBubblesRef.current
    if (transcript.length === 0 && local.length === 0) {
      setBubbles([])
      return
    }
    // Dedup local user bubbles whose text matches an already-recorded
    // transcript user bubble.
    const transcriptUserTexts = new Set(
      transcript.filter((b) => b.speaker === 'user').map((b) => b.text),
    )
    const unmatchedLocal = local.filter(
      (b) => b.speaker === 'user' && !transcriptUserTexts.has(b.text),
    )
    setBubbles([...transcript, ...unmatchedLocal])
  }, [])

  const fetchTranscript = useCallback(async () => {
    if (transcriptInflightRef.current) return
    transcriptInflightRef.current = true
    try {
      const res = await fetch(buildTranscriptUrl(repoId, branchId))
      if (!res.ok) return
      const body = (await res.json()) as TranscriptResponse
      const bubbles: Bubble[] = body.bubbles.map((b) => ({
        id: b.id,
        speaker: b.speaker,
        text: b.text,
      }))
      transcriptBubblesRef.current = bubbles
      recomputeBubbles()
      scheduleScroll()
    } catch {
      // Network errors during transcript refresh are non-fatal — the UI
      // continues with whatever bubbles it last had.
    } finally {
      transcriptInflightRef.current = false
    }
  }, [repoId, branchId, recomputeBubbles, scheduleScroll])

  const scheduleTranscriptRefetch = useCallback(() => {
    if (transcriptDebounceRef.current) clearTimeout(transcriptDebounceRef.current)
    transcriptDebounceRef.current = setTimeout(() => {
      void fetchTranscript()
    }, TRANSCRIPT_REFETCH_DEBOUNCE_MS)
  }, [fetchTranscript])

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
          // Keep the grid state for connection-streaming detection, but DO
          // NOT use grid-derived bubbles — claude's TUI runs on alt-screen
          // so the grid never contains prior conversation. Bubbles come
          // from the transcript REST endpoint instead.
          const initMsg = msg as GridInitMsg
          gridRef.current = applyInit(initMsg)
          // Kick off the first transcript fetch (no debounce — initial load).
          void fetchTranscript()
        } else if (msg.type === 'grid.diff') {
          const diffMsg = msg as GridDiffMsg
          gridRef.current = applyDiff(gridRef.current, diffMsg)
          // Grid diff signals claude activity — schedule a debounced
          // transcript refetch so newly-completed turns show up in chat.
          scheduleTranscriptRefetch()
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

    // Initial transcript fetch happens BEFORE the WS opens so the chat
    // history shows up as soon as possible (the WS handshake + grid.init
    // round-trip can add several hundred ms on a slow network).
    void fetchTranscript()
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
    transcriptBubblesRef.current = []
    connectRef.current()
  }, [])

  const handleSend = useCallback(() => {
    const text = input.trim()
    if (!text) return

    // Send the trimmed `text` (not raw `input`) so the PTY receives exactly
    // what the local bubble displays — sprint-level review F2.
    //
    // Use '\r' (carriage return) as the submit terminator, NOT '\n'. claude's
    // TUI input handling treats '\n' as a literal newline (multi-line input)
    // and '\r' as Enter/submit — same as xterm.js does for the keyboard Enter
    // key on desktop. Sending '\n' leaves the text in the prompt box without
    // actually submitting.
    const enc = new TextEncoder()
    const data = enc.encode(text + '\r').buffer

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
    // before the transcript catches up.
    localBubbleIdRef.current += 1
    const userBubble: Bubble = {
      id: `local-${localBubbleIdRef.current}`,
      speaker: 'user',
      text: text,
    }
    localBubblesRef.current = [...localBubblesRef.current, userBubble]
    recomputeBubbles()
    scheduleScroll()

    // Trigger a transcript refetch after a short delay — claude will
    // persist the user turn to .jsonl almost immediately on receiving the
    // CR, and the assistant reply lands shortly after.
    scheduleTranscriptRefetch()

    setInput('')
  }, [input, recomputeBubbles, scheduleScroll, scheduleTranscriptRefetch])

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
