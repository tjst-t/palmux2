// MCP server status popup. Read-only list of CLI-reported MCP servers
// and their connection states. Phase 3 scope is display-only — restart
// / re-auth are deferred (see ROADMAP S004 and the Phase 4+ backlog).
//
// Data source: `state.mcpServers`, populated from session.init.
// Anchored under the TopBar `mcp` button (HistoryPopup pattern: parent
// owns open/close, child handles click-outside via anchorRef).

import { useEffect, useRef } from 'react'

import { statusTone, type MCPStatusTone } from './mcp-status'
import type { MCPServerInfo } from './types'

import styles from './mcp-popup.module.css'

interface Props {
  servers: MCPServerInfo[]
  open: boolean
  onClose: () => void
  /** Trigger element so click-outside detection can ignore it (the
   *  trigger has its own toggle). */
  anchorRef?: React.RefObject<HTMLElement | null>
}

export function MCPPopup({ servers, open, onClose, anchorRef }: Props) {
  const ref = useRef<HTMLDivElement | null>(null)

  // Click-outside / Esc closes. Same shape as HistoryPopup.
  useEffect(() => {
    if (!open) return
    const onPointer = (e: PointerEvent) => {
      if (!ref.current) return
      const target = e.target as Node
      if (ref.current.contains(target)) return
      if (anchorRef?.current?.contains(target)) return
      onClose()
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('pointerdown', onPointer, true)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', onPointer, true)
      window.removeEventListener('keydown', onKey)
    }
  }, [open, onClose, anchorRef])

  if (!open) return null
  return (
    <div
      ref={ref}
      className={styles.popup}
      role="dialog"
      aria-label="MCP server status"
      data-testid="mcp-popup"
    >
      <header className={styles.head}>
        <h3 className={styles.title}>MCP servers</h3>
        <button
          type="button"
          className={styles.closeBtn}
          onClick={onClose}
          aria-label="Close"
        >
          ×
        </button>
      </header>
      {servers.length === 0 ? (
        <p className={styles.empty} data-testid="mcp-popup-empty">
          No MCP servers configured.
        </p>
      ) : (
        <ul className={styles.list}>
          {servers.map((srv) => {
            const tone = statusTone(srv.status)
            return (
              <li
                key={srv.name}
                className={styles.item}
                data-testid={`mcp-row-${srv.name}`}
              >
                <span
                  className={`${styles.dot} ${dotClassFor(tone)}`}
                  aria-hidden
                  data-testid={`mcp-dot-${srv.name}`}
                  data-tone={tone}
                />
                <span className={styles.name}>{srv.name}</span>
                <span
                  className={`${styles.badge} ${badgeClassFor(tone)}`}
                  data-testid={`mcp-status-${srv.name}`}
                >
                  {srv.status || 'unknown'}
                </span>
              </li>
            )
          })}
        </ul>
      )}
      <footer className={styles.foot}>
        Display-only. Restart / re-auth are not yet supported.
      </footer>
    </div>
  )
}

function dotClassFor(tone: MCPStatusTone): string {
  switch (tone) {
    case 'ok':      return styles.dotOk
    case 'warn':    return styles.dotWarn
    case 'err':     return styles.dotErr
    case 'unknown': return styles.dotUnknown
  }
}

function badgeClassFor(tone: MCPStatusTone): string {
  switch (tone) {
    case 'ok':      return styles.badgeOk
    case 'warn':    return styles.badgeWarn
    case 'err':     return styles.badgeErr
    case 'unknown': return styles.badgeUnknown
  }
}
