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

function buildAttachURL(repoId: string, branchId: string, tabId: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/tabs/${encodeURIComponent(tabId)}/attach`
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
    requestAnimationFrame(() => fit.fit())

    const ws = new ReconnectingWebSocket({
      url: buildAttachURL(repoId, branchId, tabId),
      binaryType: 'arraybuffer',
      onState: (s) => setConnState(s),
      onMessage: (ev) => {
        if (ev.data instanceof ArrayBuffer) {
          term.write(new Uint8Array(ev.data))
        } else if (typeof ev.data === 'string') {
          term.write(ev.data)
        }
      },
    })
    ws.connect()

    const sendInput = (data: string) => ws.send(JSON.stringify({ type: 'input', data }))
    const sendResize = (cols: number, rows: number) => ws.send(JSON.stringify({ type: 'resize', cols, rows }))

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

    // Open WS → kick the initial size to the server so the pty matches.
    const announceSize = () => {
      if (ws.getState() === 'open') {
        sendResize(term.cols, term.rows)
      }
    }
    const stateInterval = setInterval(announceSize, 1000)

    // Custom key handler for Ctrl+V / Cmd+V — intercept to read from system
    // clipboard and send as input. Phase 10 polish adds image-paste and
    // OSC-52 copy handling.
    term.attachCustomKeyEventHandler((ev) => {
      if (ev.type !== 'keydown') return true
      const isPaste = (ev.ctrlKey || ev.metaKey) && (ev.key === 'v' || ev.key === 'V')
      if (!isPaste) return true
      navigator.clipboard
        .readText()
        .then((text) => {
          if (text) sendInput(text)
        })
        .catch(() => {})
      return false
    })

    const dispose = () => {
      clearInterval(stateInterval)
      ro.disconnect()
      onDataDisp.dispose()
      onResizeDisp.dispose()
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
