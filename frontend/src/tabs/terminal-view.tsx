import { useEffect, useRef, useState } from 'react'

import { FitAddon } from '@xterm/addon-fit'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { Terminal } from '@xterm/xterm'

import { terminalManager } from '../lib/terminal-manager'
import { type ConnState, ReconnectingWebSocket } from '../lib/ws'

import '@xterm/xterm/css/xterm.css'
import styles from './terminal-view.module.css'

interface Props {
  repoId: string
  branchId: string
  tabId: string
}

function buildAttachURL(
  repoId: string,
  branchId: string,
  tabId: string,
  size?: { cols: number; rows: number },
): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  let url = `${proto}//${window.location.host}/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/tabs/${encodeURIComponent(tabId)}/attach`
  if (size && size.cols > 0 && size.rows > 0) {
    url += `?cols=${size.cols}&rows=${size.rows}`
  }
  return url
}

async function handlePaste(sendInput: (data: string) => void): Promise<void> {
  // Try the async clipboard `read` (gives both text and blobs); fall back to
  // readText if blob read isn't available or the user has only text.
  if (typeof navigator !== 'undefined' && 'clipboard' in navigator && 'read' in navigator.clipboard) {
    try {
      const items = await navigator.clipboard.read()
      for (const item of items) {
        const imgType = item.types.find((t) => t.startsWith('image/'))
        if (imgType) {
          const blob = await item.getType(imgType)
          await uploadAndSend(blob, sendInput)
          return
        }
      }
    } catch {
      // permission denied or insecure context — fall through to text.
    }
  }
  try {
    const text = await navigator.clipboard.readText()
    if (text) sendInput(text)
  } catch {
    // ignore
  }
}

async function uploadAndSend(blob: Blob, sendInput: (data: string) => void): Promise<void> {
  const fd = new FormData()
  // Some pickers don't supply a name; provide a fallback so multer-style
  // parsers find the file.
  const file = blob instanceof File ? blob : new File([blob], guessName(blob), { type: blob.type })
  fd.append('file', file)
  try {
    const res = await fetch('/api/upload', {
      method: 'POST',
      body: fd,
      credentials: 'include',
    })
    if (!res.ok) return
    const data = (await res.json()) as { path?: string }
    if (data.path) sendInput(data.path)
  } catch {
    // network or auth failure — the user can drag-drop or attach manually.
  }
}

function guessName(blob: Blob): string {
  const ext = blob.type === 'image/png' ? 'png' : blob.type === 'image/jpeg' ? 'jpg' : 'bin'
  return `pasted-${Date.now()}.${ext}`
}

function readThemeVar(name: string, fallback: string): string {
  if (typeof window === 'undefined') return fallback
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return v || fallback
}

function buildTheme(): Terminal['options']['theme'] {
  return {
    background: readThemeVar('--color-terminal-bg', '#0c0e14'),
    foreground: readThemeVar('--color-fg', '#d4d4d8'),
    cursor: readThemeVar('--color-accent', '#7c8aff'),
    selectionBackground: 'rgba(124, 138, 255, 0.3)',
    green: readThemeVar('--color-terminal-green', '#64d2a0'),
    yellow: readThemeVar('--color-terminal-yellow', '#e8b45a'),
    blue: readThemeVar('--color-terminal-blue', '#7c8aff'),
    brightGreen: readThemeVar('--color-terminal-green', '#64d2a0'),
  }
}

export function TerminalView({ repoId, branchId, tabId }: Props) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const [connState, setConnState] = useState<ConnState>('connecting')

  useEffect(() => {
    if (!containerRef.current) return
    const key = `${repoId}/${branchId}/${tabId}`

    const term = new Terminal({
      cursorBlink: true,
      fontFamily: readThemeVar('--font-mono', 'monospace'),
      fontSize: 14,
      lineHeight: 1.2,
      scrollback: 5000,
      allowProposedApi: true,
      theme: buildTheme(),
    })

    const fit = new FitAddon()
    term.loadAddon(fit)
    term.loadAddon(new Unicode11Addon())
    term.loadAddon(new WebLinksAddon())
    term.unicode.activeVersion = '11'

    term.open(containerRef.current)
    // Fit synchronously before connecting the WS so we can pass the real
    // cols/rows in the attach URL — the server creates the underlying pty at
    // that size, which prevents tmux from briefly showing 80x24 first.
    try {
      fit.fit()
    } catch {
      // ignore — measurement may fail if the container has 0 size; the WS
      // will still send a resize once xterm reports its actual dimensions.
    }
    const initialSize = { cols: term.cols, rows: term.rows }

    let ws: ReconnectingWebSocket | null = null
    const sendInput = (data: string) => ws?.send(JSON.stringify({ type: 'input', data }))
    const sendResize = (cols: number, rows: number) =>
      ws?.send(JSON.stringify({ type: 'resize', cols, rows }))

    ws = new ReconnectingWebSocket({
      url: buildAttachURL(repoId, branchId, tabId, initialSize),
      binaryType: 'arraybuffer',
      onState: (s) => {
        setConnState(s)
        // On (re)connect, re-announce the current size so the freshly created
        // server-side pty matches our actual viewport. Covers two cases:
        //   1. the user resized the window between disconnect and reconnect;
        //   2. the URL-embedded cols/rows were too small because fit() failed
        //      on the initial mount.
        if (s === 'open') {
          sendResize(term.cols, term.rows)
        }
      },
      onMessage: (ev) => {
        if (ev.data instanceof ArrayBuffer) {
          term.write(new Uint8Array(ev.data))
        } else if (typeof ev.data === 'string') {
          term.write(ev.data)
        }
      },
    })
    ws.connect()

    const onDataDisp = term.onData(sendInput)

    const ro = new ResizeObserver(() => {
      try {
        fit.fit()
      } catch {
        // ignore — mount/unmount race
      }
    })
    ro.observe(containerRef.current)

    const onResizeDisp = term.onResize(({ cols, rows }) => sendResize(cols, rows))

    // Custom key handler for Ctrl+V / Cmd+V — intercept to read from system
    // clipboard. Plain text → terminal input. Image (Blob) → POST /api/upload
    // and send the resulting absolute path so Claude / shells can pick it up.
    term.attachCustomKeyEventHandler((ev) => {
      if (ev.type !== 'keydown') return true
      const isPaste = (ev.ctrlKey || ev.metaKey) && (ev.key === 'v' || ev.key === 'V')
      if (!isPaste) return true
      void handlePaste(sendInput)
      return false
    })

    // Also intercept browser paste events (covers right-click → paste, mobile,
    // and any case where the keyhandler missed).
    const pasteHandler = (e: ClipboardEvent) => {
      if (!containerRef.current?.contains(document.activeElement)) return
      if (!e.clipboardData) return
      const item = Array.from(e.clipboardData.items).find((i) => i.kind === 'file')
      if (!item) return
      const file = item.getAsFile()
      if (!file) return
      e.preventDefault()
      void uploadAndSend(file, sendInput)
    }
    document.addEventListener('paste', pasteHandler)

    // OSC 52 — tmux Set/Get clipboard. xterm.js fires this back to us via
    // the onData hook *after* we install a listener. Mirror to navigator.
    const onClipboardDisp = term.parser.registerOscHandler(52, (data: string) => {
      // Format: "<destinations>;<base64>" or "<destinations>;?" for Get.
      const semi = data.indexOf(';')
      if (semi < 0) return false
      const payload = data.slice(semi + 1)
      if (payload === '?') return true
      try {
        const decoded = atob(payload)
        void navigator.clipboard.writeText(decoded)
      } catch {
        // ignore — malformed payload
      }
      return true
    })

    const dispose = () => {
      ro.disconnect()
      onDataDisp.dispose()
      onResizeDisp.dispose()
      document.removeEventListener('paste', pasteHandler)
      onClipboardDisp.dispose()
    }

    terminalManager.acquire({ key, terminal: term, ws, dispose })

    return () => {
      terminalManager.remove(key)
    }
  }, [repoId, branchId, tabId])

  return (
    <div className={styles.wrap}>
      <div ref={containerRef} className={styles.term} />
      {connState !== 'open' && (
        <div className={styles.overlay}>
          <span className={styles.spinner} />
          <span>{connState === 'connecting' ? 'Connecting…' : 'Reconnecting…'}</span>
        </div>
      )}
    </div>
  )
}
