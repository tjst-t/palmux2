import { useCallback, useEffect, useRef, useState } from 'react'

import { FitAddon } from '@xterm/addon-fit'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { Terminal } from '@xterm/xterm'

import { api } from '../../lib/api'
import type { TabViewProps } from '../../lib/tab-registry'
import { terminalManager } from '../../lib/terminal-manager'
import { ReconnectingWebSocket } from '../../lib/ws'
import { usePalmuxStore } from '../../stores/palmux-store'
import { isMobile, useViewport } from '../../hooks/use-viewport'

import '@xterm/xterm/css/xterm.css'
import styles from './styles.module.css'

// SIGWINCH debounce — avoids spamming POST /resize on every intermediate
// resize event while the user is dragging the window edge.
const RESIZE_DEBOUNCE_MS = 200

// ── Upload helpers ────────────────────────────────────────────────────────────
// These mirror the logic in terminal-view.tsx but adapted for the claude-tui
// raw-binary WS model (sendRaw sends the path + \r as literal bytes, not JSON).

function uploadEndpointTui(repoId: string, branchId: string): string {
  return `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/upload`
}

function guessNameTui(blob: Blob): string {
  const ext = blob.type === 'image/png' ? 'png' : blob.type === 'image/jpeg' ? 'jpg' : 'bin'
  return `pasted-${Date.now()}.${ext}`
}

async function uploadAndSendTui(
  blob: Blob,
  sendRaw: (data: string) => void,
  repoId: string,
  branchId: string,
  setUploading: React.Dispatch<React.SetStateAction<boolean>>,
): Promise<void> {
  const fd = new FormData()
  const file = blob instanceof File ? blob : new File([blob], guessNameTui(blob), { type: blob.type })
  fd.append('file', file)
  setUploading(true)
  try {
    const res = await fetch(uploadEndpointTui(repoId, branchId), {
      method: 'POST',
      body: fd,
      credentials: 'include',
    })
    if (!res.ok) return
    const data = (await res.json()) as { path?: string }
    if (data.path) sendRaw(data.path + '\r')
  } catch {
    // network or auth failure — silent
  } finally {
    setUploading(false)
  }
}

async function uploadFilesSequentiallyTui(
  files: File[],
  sendRaw: (data: string) => void,
  repoId: string,
  branchId: string,
  setUploading: React.Dispatch<React.SetStateAction<boolean>>,
): Promise<void> {
  for (const f of files) {
    await uploadAndSendTui(f, sendRaw, repoId, branchId, setUploading)
  }
}

function readThemeVar(name: string, fallback: string): string {
  if (typeof window === 'undefined') return fallback
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return v || fallback
}

function buildTheme(): Terminal['options']['theme'] {
  return {
    background:    readThemeVar('--color-terminal-bg', '#0c0e14'),
    foreground:    readThemeVar('--color-fg', '#d4d4d8'),
    cursor:        readThemeVar('--color-accent', '#7c8aff'),
    selectionBackground: 'rgba(124, 138, 255, 0.3)',
    black:         '#2a2d36',
    red:           readThemeVar('--color-error', '#ef4444'),
    green:         readThemeVar('--color-terminal-green', '#64d2a0'),
    yellow:        readThemeVar('--color-terminal-yellow', '#e8b45a'),
    blue:          readThemeVar('--color-terminal-blue', '#7c8aff'),
    magenta:       '#b794f6',
    cyan:          '#5dd5d7',
    white:         readThemeVar('--color-fg', '#d4d4d8'),
    brightBlack:   readThemeVar('--color-terminal-gray', '#6b6f7b'),
    brightRed:     '#fb7185',
    brightGreen:   readThemeVar('--color-terminal-green', '#64d2a0'),
    brightYellow:  '#fcd34d',
    brightBlue:    readThemeVar('--color-accent-light', '#9ba6ff'),
    brightMagenta: '#d6bcfa',
    brightCyan:    '#a5f3fc',
    brightWhite:   '#f4f4f5',
  }
}

// Sadf90e: claudetui's WS / resize endpoints are now keyed by {tabId} so two
// Claude(tui) tabs on the same branch each get their own daemon.
function buildWsUrl(repoId: string, branchId: string, tabId: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return (
    `${proto}//${window.location.host}` +
    `/api/repos/${encodeURIComponent(repoId)}` +
    `/branches/${encodeURIComponent(branchId)}` +
    `/tabs/${encodeURIComponent(tabId)}/tui/attach`
  )
}

function buildResizeUrl(repoId: string, branchId: string, tabId: string): string {
  return (
    `/api/repos/${encodeURIComponent(repoId)}` +
    `/branches/${encodeURIComponent(branchId)}` +
    `/tabs/${encodeURIComponent(tabId)}/tui/resize`
  )
}

// Status type maps to data-testid=claude-tui-status values used in E2E.
type Status = 'connecting' | 'connected' | 'streaming' | 'disconnected'

// Role type mirrors the server-side RoleActive / RoleViewer constants (Story 3).
// undefined means no role event has been received from the server yet.
type Role = 'active' | 'viewer'

export function ClaudeTuiTab({ repoId, branchId, tabId }: TabViewProps) {
  const viewport = useViewport()
  return (
    <ClaudeTuiDesktop
      repoId={repoId}
      branchId={branchId}
      tabId={tabId}
      showFilePicker={isMobile(viewport)}
    />
  )
}

function ClaudeTuiDesktop({
  repoId,
  branchId,
  tabId,
  showFilePicker,
}: {
  repoId: string
  branchId: string
  tabId: string
  showFilePicker: boolean
}) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const [status, setStatus] = useState<Status>('connecting')
  // role tracks the multi-client active/viewer assignment for this connection.
  // undefined means no role event has been received yet.
  const [role, setRole] = useState<Role | undefined>(undefined)
  const fontSize = usePalmuxStore((s) => s.deviceSettings.fontSize)
  // Manual reconnect trigger: incrementing this counter causes the effect to
  // re-run and tear down / re-create the WS + terminal.
  const [reconnectSeq, setReconnectSeq] = useState(0)
  // drop zone overlay visibility
  const [isDragOver, setIsDragOver] = useState(false)
  // upload in-flight indicator
  const [isUploading, setIsUploading] = useState(false)
  // ref to the WS instance so event handlers (paste/drop) can access it
  const wsRef = useRef<ReconnectingWebSocket | null>(null)
  // Queue of image blobs waiting to be uploaded. Filled by the paste-event
  // handler (sync); drained by a useEffect that runs OUTSIDE the paste
  // event call stack — fetch/XHR from inside a paste-event-context hangs
  // indefinitely in Chromium, so we defer the network call.
  const [pendingImage, setPendingImage] = useState<Blob | null>(null)
  // Dedupe guard for the upload effect below. We deliberately do NOT clear
  // pendingImage with setState inside that effect: pendingImage is one of its
  // dependencies, so resetting it there re-triggers the effect — a cascading
  // render that react-hooks/set-state-in-effect (correctly) rejects. A ref
  // records the last Blob we uploaded instead; each paste produces a fresh
  // Blob reference, and the ref survives StrictMode's double-invoke, so the
  // same image is never uploaded twice.
  const uploadedImageRef = useRef<Blob | null>(null)

  const handleReconnect = useCallback(() => {
    setStatus('connecting')
    setRole(undefined)
    setReconnectSeq((n) => n + 1)
  }, [])

  // sendRaw sends a string as raw UTF-8 bytes over the WS (no JSON framing).
  const sendRaw = useCallback((data: string) => {
    if (!wsRef.current) return
    const enc = new TextEncoder()
    wsRef.current.send(enc.encode(data).buffer)
  }, [])

  // ── Paste handler ─────────────────────────────────────────────────────────
  // Attached to the terminal container div. If the clipboard has an image
  // blob we intercept and upload; text falls through to xterm.js bracketed
  // paste mode (no regression).
  const handlePaste = useCallback(
    (e: ClipboardEvent) => {
      if (!e.clipboardData) return
      const items = Array.from(e.clipboardData.items)
      const imageItem = items.find((item) => item.kind === 'file' && item.type.startsWith('image/'))
      if (imageItem) {
        const blob = imageItem.getAsFile()
        if (blob) {
          e.preventDefault()
          void uploadAndSendTui(blob, sendRaw, repoId, branchId, setIsUploading)
        }
      }
      // If no image, do NOT preventDefault — let xterm.js handle text paste.
    },
    [sendRaw, repoId, branchId],
  )

  // ── Drop handlers ──────────────────────────────────────────────────────────
  const handleDragOver = useCallback((e: React.DragEvent<HTMLDivElement>) => {
    const types = e.dataTransfer?.types ?? []
    const hasFiles = Array.from(types).includes('Files')
    if (hasFiles) {
      e.preventDefault()
      setIsDragOver(true)
    }
  }, [])

  const handleDragLeave = useCallback((e: React.DragEvent<HTMLDivElement>) => {
    // Only hide if leaving the container entirely (not entering a child).
    if (containerRef.current && !containerRef.current.contains(e.relatedTarget as Node | null)) {
      setIsDragOver(false)
    }
  }, [])

  const handleDrop = useCallback(
    (e: React.DragEvent<HTMLDivElement>) => {
      e.preventDefault()
      setIsDragOver(false)
      const files = Array.from(e.dataTransfer?.files ?? [])
      if (files.length === 0) return
      void uploadFilesSequentiallyTui(files, sendRaw, repoId, branchId, setIsUploading)
    },
    [sendRaw, repoId, branchId],
  )

  // ── Mobile file picker ─────────────────────────────────────────────────────
  const handleFilePickerChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      const files = Array.from(e.target.files ?? [])
      if (files.length === 0) return
      void uploadFilesSequentiallyTui(files, sendRaw, repoId, branchId, setIsUploading)
      // Reset the input so the same file can be picked again if needed.
      if (fileInputRef.current) fileInputRef.current.value = ''
    },
    [sendRaw, repoId, branchId],
  )

  useEffect(() => {
    if (!containerRef.current) return

    const key = `${repoId}/${branchId}/${tabId}`

    const term = new Terminal({
      cursorBlink: true,
      fontFamily: readThemeVar('--font-mono', 'monospace'),
      fontSize,
      lineHeight: 1.2,
      // Real scrollback: the backend now replays the emulator's CURRENT
      // rendered screen on attach (Daemon.RenderSnapshotAndSubscribe) instead
      // of the raw repaint-history byte ring, so the scrollback only fills with
      // genuine forward output, not stacked stale frames. This restores normal
      // scroll-up behaviour without the "broken logs" garbage.
      scrollback: 5000,
      allowProposedApi: true,
      minimumContrastRatio: 4.5,
      theme: buildTheme(),
    })

    const fit = new FitAddon()
    term.loadAddon(fit)
    term.loadAddon(new Unicode11Addon())
    term.loadAddon(new WebLinksAddon())
    term.unicode.activeVersion = '11'

    term.open(containerRef.current)
    try { fit.fit() } catch { /* ignore — 0-size container at mount */ }
    // Focus the terminal immediately so the user can type right away. This
    // effect re-runs on every tab switch (tabId is a dependency), so switching
    // to a Claude(tui) tab puts the caret in its terminal without an extra
    // click — the tab-bar click would otherwise leave focus on the tab button.
    // rAF defers until after layout so focus reliably sticks on the now-visible
    // container.
    requestAnimationFrame(() => {
      try { term.focus() } catch { /* terminal disposed before rAF — ignore */ }
    })

    // --- WebSocket (raw binary, no JSON framing) ---
    let streamingTimer: ReturnType<typeof setTimeout> | null = null

    const ws = new ReconnectingWebSocket({
      url: buildWsUrl(repoId, branchId, tabId),
      binaryType: 'arraybuffer',
      onState: (s) => {
        if (s === 'open') {
          setStatus('connected')
          // Re-announce current size on (re)connect so the PTY matches viewport.
          sendResizeNow(term.cols, term.rows)
        } else if (s === 'closed') {
          setStatus('disconnected')
        } else if (s === 'connecting') {
          setStatus('connecting')
        }
      },
      onMessage: (ev) => {
        if (ev.data instanceof ArrayBuffer) {
          // Binary frame: raw PTY bytes → write to xterm.
          term.write(new Uint8Array(ev.data))
          // Only count binary frames as "streaming" (actual terminal output).
          setStatus('streaming')
          if (streamingTimer) clearTimeout(streamingTimer)
          streamingTimer = setTimeout(() => {
            setStatus((prev) => (prev === 'streaming' ? 'connected' : prev))
          }, 1000)
        } else if (typeof ev.data === 'string') {
          // Text frame: JSON control event (role, grid.init, grid.diff, …).
          // Route by type; silently ignore unrecognised types.
          try {
            const msg = JSON.parse(ev.data) as { type?: string; role?: string }
            if (msg.type === 'role') {
              const r = msg.role
              if (r === 'active' || r === 'viewer') {
                setRole(r)
              }
              // Role events do not count as PTY streaming output.
            }
            // grid.init / grid.diff: silently ignored (not used by desktop terminal).
          } catch {
            // Non-JSON text frame: pass to terminal as a fallback (legacy compat).
            term.write(ev.data)
          }
        }
      },
    })
    ws.connect()
    // Store the ws instance in the ref so paste/drop handlers (defined outside
    // this effect) can call sendRaw without being re-bound on every render.
    wsRef.current = ws

    // --- Raw binary keyboard input (xterm.js → WS) ---
    // The claude-tui backend accepts raw bytes, not JSON-framed {type:"input"}.
    const onDataDisp = term.onData((data) => {
      // Encode to binary and send raw.
      const enc = new TextEncoder()
      ws.send(enc.encode(data).buffer)
    })

    // --- Ctrl+V / Cmd+V paste handler ---
    // hotfix 2026-05-27: without this, xterm.js handles Ctrl+V as a literal
    // \x16 control char and sends it to the PTY — so neither image upload
    // nor plain text paste works. Mirror the bash terminal-view.tsx pattern:
    // intercept the keydown, read clipboard manually, route image vs text.
    // The container-level `paste` event listener still covers right-click
    // → paste / mobile / OS-level paste menus.
    // Ctrl+V / Cmd+V paste handler. Mirrors the bash terminal-view.tsx
    // pattern: intercept the keydown via xterm.js's custom key handler,
    // read clipboard async, route image (upload + path) vs text (sendRaw).
    // Returning false suppresses xterm.js's default Ctrl+V handling so the
    // literal \x16 control char is not sent to the PTY.
    // Ctrl+V / Cmd+V paste handler. Mirrors the bash terminal-view.tsx
    // pattern: intercept the keydown via xterm.js's custom key handler,
    // read clipboard async, route image (upload + path) vs text (sendRaw).
    // Returning false suppresses xterm.js's default Ctrl+V handling so the
    // literal \x16 is not sent to the PTY.
    //
    // Note: the document-level capture-phase paste listener below also
    // catches the native paste event that fires on xterm.js's textarea
    // when Ctrl+V is pressed. The two paths are complementary — whichever
    // resolves first wins via the `handled` guard. The async clipboard.read
    // path covers browsers where the native paste event is suppressed by
    // xterm.js's own handler; the sync paste-event path covers browsers
    // where clipboard.read async hangs from inside a key-event call stack.
    let pasteHandled = false
    term.attachCustomKeyEventHandler((ev) => {
      if (ev.type !== 'keydown') return true

      // Ctrl+C / Cmd+C with active selection → copy to clipboard.
      // Without a selection, fall through so xterm.js sends ETX (^C) to
      // the PTY for SIGINT. hotfix 2026-05-28.
      const isCopy = (ev.ctrlKey || ev.metaKey) && (ev.key === 'c' || ev.key === 'C')
      if (isCopy) {
        const sel = term.getSelection()
        if (sel) {
          ev.preventDefault()
          void navigator.clipboard.writeText(sel).catch(() => { /* ignore */ })
          term.clearSelection()
          return false
        }
        // No selection → let xterm.js handle Ctrl+C normally (send ^C).
        return true
      }

      // Ctrl+N → send the literal ^N byte (\x0e) to the PTY so TUI apps
      // can use it for "next line" navigation. The browser's default for
      // Ctrl+N is "open new window", which most browsers do NOT let pages
      // preventDefault — calling preventDefault here is best-effort.
      // hotfix 2026-05-28.
      if (ev.ctrlKey && !ev.metaKey && !ev.altKey && !ev.shiftKey
          && (ev.key === 'n' || ev.key === 'N')) {
        ev.preventDefault()
        sendRaw('\x0e')
        return false
      }

      // Ctrl+Z → do NOT let xterm.js send the literal ^Z byte (\x1a /
      // SIGTSTP) to the PTY: there is no job-control shell here to `fg` it
      // back, so suspending claude just sends it to the background and the
      // tab appears dead. Remap Ctrl+Z to Claude Code's in-TUI Undo
      // (readline-style C-_, byte \x1f) instead. hotfix 2026-06-04.
      if (ev.ctrlKey && !ev.metaKey && !ev.altKey && !ev.shiftKey
          && (ev.key === 'z' || ev.key === 'Z')) {
        ev.preventDefault()
        sendRaw('\x1f')
        return false
      }

      const isPaste = (ev.ctrlKey || ev.metaKey) && (ev.key === 'v' || ev.key === 'V')
      if (!isPaste) return true
      pasteHandled = false
      void (async () => {
        if (typeof navigator !== 'undefined' && navigator.clipboard
            && 'read' in navigator.clipboard) {
          try {
            const items = await navigator.clipboard.read()
            for (const item of items) {
              const imgType = item.types.find((t) => t.startsWith('image/'))
              if (imgType) {
                if (pasteHandled) return
                pasteHandled = true
                const blob = await item.getType(imgType)
                setPendingImage(blob)
                return
              }
            }
          } catch {
            // permission denied / non-secure context — fall through to text.
          }
        }
        if (pasteHandled) return
        try {
          const text = await navigator.clipboard.readText()
          if (text) {
            pasteHandled = true
            sendRaw(text)
          }
        } catch {
          // ignore.
        }
      })()
      return false
    })

    // Document-level capture-phase paste listener. The image blob is
    // captured synchronously from clipboardData, then stashed in React
    // state — the actual upload runs in a useEffect, OUTSIDE the
    // paste-event call stack (fetch from a paste-event hangs in Chromium).
    const onDocPaste = (e: Event) => {
      const ce = e as ClipboardEvent
      if (!ce.composedPath().includes(container)) return
      if (!ce.clipboardData) return
      let file: File | null = null
      const files = ce.clipboardData.files
      if (files) {
        for (let i = 0; i < files.length; i++) {
          const f = files.item(i)
          if (f && f.type.startsWith('image/')) { file = f; break }
        }
      }
      if (!file && ce.clipboardData.items) {
        for (let i = 0; i < ce.clipboardData.items.length; i++) {
          const it = ce.clipboardData.items[i]
          if (it.kind === 'file' && it.type.startsWith('image/')) {
            const f = it.getAsFile()
            if (f) { file = f; break }
          }
        }
      }
      if (!file) return
      ce.preventDefault()
      ce.stopImmediatePropagation()
      if (pasteHandled) return
      pasteHandled = true
      setPendingImage(file)
    }
    document.addEventListener('paste', onDocPaste, true)

    // --- Paste event: image blob intercept --------------------------------
    // Listen on the container div so we only fire when the xterm is focused
    // (or the container has an active element). Image blobs are intercepted;
    // text paste falls through to xterm.js bracketed paste mode unchanged.
    const container = containerRef.current!
    container.addEventListener('paste', handlePaste as EventListener)


    // --- SIGWINCH chain: ResizeObserver → FitAddon → POST /resize ---
    let resizeDebounceTimer: ReturnType<typeof setTimeout> | null = null

    function sendResizeNow(cols: number, rows: number): void {
      if (cols <= 0 || rows <= 0) return
      void api.post(buildResizeUrl(repoId, branchId, tabId), { cols, rows }).catch(() => {
        // Non-fatal — the WS connection may not have started the daemon yet.
      })
    }

    const onResizeDisp = term.onResize(({ cols, rows }) => {
      if (resizeDebounceTimer) clearTimeout(resizeDebounceTimer)
      resizeDebounceTimer = setTimeout(() => {
        sendResizeNow(cols, rows)
      }, RESIZE_DEBOUNCE_MS)
    })

    const ro = new ResizeObserver(() => {
      try { fit.fit() } catch { /* ignore */ }
    })
    ro.observe(containerRef.current!)

    // --- OSC 52 clipboard passthrough ---
    const onClipboardDisp = term.parser.registerOscHandler(52, (data: string) => {
      const semi = data.indexOf(';')
      if (semi < 0) return false
      const payload = data.slice(semi + 1)
      if (payload === '?') return true
      try {
        // OSC 52 base64 carries the selection's UTF-8 BYTES. atob() yields a
        // per-byte binary string, so it must be decoded as UTF-8 before hitting
        // the clipboard — otherwise multibyte text lands mojibaked (予測 → äºæ¸¬).
        const bytes = Uint8Array.from(atob(payload), (c) => c.charCodeAt(0))
        void navigator.clipboard.writeText(new TextDecoder().decode(bytes))
      } catch { /* ignore */ }
      return true
    })

    // --- Single-finger vertical scroll (mobile / touch) ---
    // xterm.js translates a touch drag into text selection / input focus, not
    // scrollback movement, so on a phone the only way to scroll history is the
    // thin scrollbar. We convert a one-finger vertical drag into
    // term.scrollLines(). Two-finger gestures are left untouched so the
    // MainArea pinch (font size) / horizontal swipe (tab switch) handlers keep
    // working. We only preventDefault once we've actually consumed a line, so a
    // tap still reaches xterm (focus / cursor) normally.
    let touchLastY = 0
    let touchAccum = 0
    let touchActive = false

    const cellHeight = (): number => {
      const rows = term.rows || 1
      const h = container.clientHeight
      return h > 0 ? h / rows : fontSize * 1.2
    }

    const onTouchStart = (e: TouchEvent) => {
      if (e.touches.length !== 1) { touchActive = false; return }
      touchActive = true
      touchLastY = e.touches[0].clientY
      touchAccum = 0
    }
    const onTouchMove = (e: TouchEvent) => {
      if (!touchActive || e.touches.length !== 1) return
      const y = e.touches[0].clientY
      touchAccum += y - touchLastY
      touchLastY = y
      const step = cellHeight()
      if (step <= 0) return
      // Dragging DOWN (positive accum, finger toward bottom) reveals OLDER
      // lines → scroll up → negative scrollLines() argument.
      const lines = Math.trunc(touchAccum / step)
      if (lines !== 0) {
        term.scrollLines(-lines)
        touchAccum -= lines * step
        e.preventDefault()
      }
    }
    const onTouchEnd = () => { touchActive = false }

    container.addEventListener('touchstart', onTouchStart, { passive: true })
    container.addEventListener('touchmove', onTouchMove, { passive: false })
    container.addEventListener('touchend', onTouchEnd, { passive: true })
    container.addEventListener('touchcancel', onTouchEnd, { passive: true })

    const dispose = () => {
      ro.disconnect()
      onDataDisp.dispose()
      onResizeDisp.dispose()
      onClipboardDisp.dispose()
      container.removeEventListener('paste', handlePaste as EventListener)
      document.removeEventListener('paste', onDocPaste, true)
      container.removeEventListener('touchstart', onTouchStart)
      container.removeEventListener('touchmove', onTouchMove)
      container.removeEventListener('touchend', onTouchEnd)
      container.removeEventListener('touchcancel', onTouchEnd)
      wsRef.current = null
      if (streamingTimer) clearTimeout(streamingTimer)
      if (resizeDebounceTimer) clearTimeout(resizeDebounceTimer)
    }

    // encoding 'raw': the claude-tui daemon's PTY consumes bare UTF-8 bytes,
    // not the `{type:'input'}` JSON the Bash terminal-view uses. This lets the
    // Toolbar drive this terminal via terminalManager.sendInput() (see
    // useFocusedTerminal, which now resolves a termKey for TUI-mode claude
    // tabs) without the buttons sending a literal JSON string to claude.
    terminalManager.acquire({ key, terminal: term, ws, encoding: 'raw', dispose })

    return () => {
      terminalManager.remove(key)
    }
    // tabId MUST be a dependency: panel.tsx reuses this same component
    // instance when the user switches between two Claude(tui) tabs in the
    // same workspace (React keeps the element at the same tree position and
    // only changes props). Without tabId here the effect never re-runs on a
    // tab switch, so the WS + terminal stay bound to the first tab's daemon
    // and both tabs render the same session. Mirrors terminal-view.tsx,
    // which already keys its mount effect on tabId.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [repoId, branchId, tabId, reconnectSeq])

  // Live font-size update without tearing down the WS.
  useEffect(() => {
    const key = `${repoId}/${branchId}/${tabId}`
    const m = terminalManager.get(key)
    if (!m) return
    if (m.terminal.options.fontSize !== fontSize) {
      m.terminal.options.fontSize = fontSize
    }
  }, [fontSize, repoId, branchId, tabId])

  // Process pending pasted image OUTSIDE the paste-event call stack.
  // Chromium's fetch/XHR initiated from inside a paste-event handler
  // never completes the underlying network request (observed empirically
  // in headless Chromium, and the user reported the same symptom in real
  // browsers). Scheduling the upload from a useEffect that fires after
  // React commits the setPendingImage state unblocks the request.
  useEffect(() => {
    if (!pendingImage || uploadedImageRef.current === pendingImage) return
    const blob = pendingImage
    uploadedImageRef.current = pendingImage
    const fd = new FormData()
    const file = blob instanceof File ? blob : new File([blob], guessNameTui(blob), { type: blob.type })
    fd.append('file', file)
    setIsUploading(true)
    void fetch(uploadEndpointTui(repoId, branchId), {
      method: 'POST',
      body: fd,
      credentials: 'include',
    })
      .then((res) => {
        if (!res.ok) throw new Error(`upload ${res.status}`)
        return res.json() as Promise<{ path?: string }>
      })
      .then((data) => {
        if (data.path && wsRef.current) {
          const enc = new TextEncoder()
          wsRef.current.send(enc.encode(data.path + '\r').buffer)
        }
      })
      .catch(() => { /* silent: network / parse error */ })
      .finally(() => setIsUploading(false))
  }, [pendingImage, repoId, branchId])

  const statusLabel: Record<Status, string> = {
    connecting:   'connecting',
    connected:    'connected',
    streaming:    'streaming',
    disconnected: 'disconnected',
  }

  return (
    <div
      className={styles.wrap}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      <div className={styles.statusBar}>
        <span data-testid="claude-tui-status" className={styles.status}>
          {statusLabel[status]}
        </span>
        <div className={styles.statusBarRight}>
          {role !== undefined && (
            <span
              data-testid="claude-tui-role-badge"
              className={`${styles.roleBadge} ${role === 'active' ? styles.roleBadgeActive : styles.roleBadgeViewer}`}
            >
              {role}
            </span>
          )}
          {showFilePicker && (
            <button
              data-testid="claude-tui-file-picker-btn"
              className={styles.filePickerBtn}
              onClick={() => fileInputRef.current?.click()}
              type="button"
              aria-label="Attach file"
              title="Attach file"
            >
              📎
            </button>
          )}
        </div>
      </div>
      {showFilePicker && (
        <input
          ref={fileInputRef}
          type="file"
          multiple
          accept="image/*,*/*"
          className={styles.filePickerInput}
          onChange={handleFilePickerChange}
          aria-hidden="true"
        />
      )}
      <div className={styles.termPad}>
        <div
          ref={containerRef}
          className={styles.term}
          data-testid="claude-tui-terminal"
        />
      </div>
      {isDragOver && (
        <div
          data-testid="claude-tui-paste-zone"
          className={styles.pasteZone}
        >
          <span className={styles.pasteZoneLabel}>Drop files to attach</span>
        </div>
      )}
      {isUploading && (
        <div
          data-testid="claude-tui-upload-progress"
          className={styles.uploadProgress}
        >
          Uploading…
        </div>
      )}
      {status === 'disconnected' && (
        <div className={styles.overlay}>
          <span>Disconnected</span>
          <button
            data-testid="claude-tui-reconnect-btn"
            className={styles.reconnectBtn}
            onClick={handleReconnect}
          >
            Reconnect
          </button>
        </div>
      )}
    </div>
  )
}
