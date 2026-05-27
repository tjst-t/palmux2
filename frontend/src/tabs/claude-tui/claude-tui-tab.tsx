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

    const key = `${repoId}/${branchId}/claude-tui`

    const term = new Terminal({
      cursorBlink: true,
      fontFamily: readThemeVar('--font-mono', 'monospace'),
      fontSize,
      lineHeight: 1.2,
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
        const decoded = atob(payload)
        void navigator.clipboard.writeText(decoded)
      } catch { /* ignore */ }
      return true
    })

    const dispose = () => {
      ro.disconnect()
      onDataDisp.dispose()
      onResizeDisp.dispose()
      onClipboardDisp.dispose()
      container.removeEventListener('paste', handlePaste as EventListener)
      wsRef.current = null
      if (streamingTimer) clearTimeout(streamingTimer)
      if (resizeDebounceTimer) clearTimeout(resizeDebounceTimer)
    }

    terminalManager.acquire({ key, terminal: term, ws, dispose })

    return () => {
      terminalManager.remove(key)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [repoId, branchId, reconnectSeq])

  // Live font-size update without tearing down the WS.
  useEffect(() => {
    const key = `${repoId}/${branchId}/claude-tui`
    const m = terminalManager.get(key)
    if (!m) return
    if (m.terminal.options.fontSize !== fontSize) {
      m.terminal.options.fontSize = fontSize
    }
  }, [fontSize, repoId, branchId])

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
        {role !== undefined && (
          <span
            data-testid="claude-tui-role-badge"
            className={`${styles.roleBadge} ${role === 'active' ? styles.roleBadgeActive : styles.roleBadgeViewer}`}
          >
            {role}
          </span>
        )}
      </div>
      {showFilePicker && (
        <div className={styles.filePickerBar}>
          <button
            data-testid="claude-tui-file-picker-btn"
            className={styles.filePickerBtn}
            onClick={() => fileInputRef.current?.click()}
            type="button"
          >
            Attach file
          </button>
          <input
            ref={fileInputRef}
            type="file"
            multiple
            accept="image/*,*/*"
            className={styles.filePickerInput}
            onChange={handleFilePickerChange}
            aria-hidden="true"
          />
        </div>
      )}
      <div
        ref={containerRef}
        className={styles.term}
        data-testid="claude-tui-terminal"
      />
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
