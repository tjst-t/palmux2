// TerminalManager — three-tier cache for terminal connections.
//
//   Active   — the terminal currently mounted in the DOM. Its WebSocket and
//              xterm.js Terminal both live.
//   Cached   — recently visible. xterm.js Terminal kept in memory, WebSocket
//              still open. Up to MAX_CACHED instances; LRU evicts the oldest.
//   Evicted  — disposed. Re-mounting allocates a fresh Terminal, which
//              re-attaches the WebSocket and replays from tmux scrollback.
//
// The actual xterm.js Terminal + WebSocket plumbing lives in TerminalView.
// The manager only owns the lifecycle policy; the view registers/unregisters
// instances as it mounts.

import { Terminal } from '@xterm/xterm'

import type { ReconnectingWebSocket } from './ws'

export const MAX_CACHED = 6

export interface ManagedTerminal {
  key: string
  terminal: Terminal
  ws: ReconnectingWebSocket
  /** Optional cleanup hook the view registers (e.g. ResizeObserver disconnect). */
  dispose?: () => void
}

class TerminalManager {
  private active = new Map<string, ManagedTerminal>()
  private cached: ManagedTerminal[] = [] // LRU: index 0 = oldest

  /** Register a freshly-mounted terminal as Active. Promotes from Cached if
   *  it was already known. */
  acquire(t: ManagedTerminal): void {
    const cachedIdx = this.cached.findIndex((c) => c.key === t.key)
    if (cachedIdx >= 0) {
      // Promote: drop from Cached, ignore the new Terminal because we already
      // have a live one. Caller is expected to check exists() first.
      const existing = this.cached.splice(cachedIdx, 1)[0]
      this.active.set(t.key, existing)
      return
    }
    this.active.set(t.key, t)
  }

  /** Move an Active terminal into Cached. Triggered when a tab becomes
   *  hidden but isn't being torn down. */
  cache(key: string): void {
    const t = this.active.get(key)
    if (!t) return
    this.active.delete(key)
    // Move to MRU position.
    this.cached = [...this.cached.filter((c) => c.key !== key), t]
    while (this.cached.length > MAX_CACHED) {
      const evicted = this.cached.shift()
      if (evicted) {
        evicted.dispose?.()
        evicted.ws.close()
        evicted.terminal.dispose()
      }
    }
  }

  /** Drop a terminal entirely. Used when a tab is removed by the user. */
  remove(key: string): void {
    const fromActive = this.active.get(key)
    if (fromActive) {
      this.active.delete(key)
      fromActive.dispose?.()
      fromActive.ws.close()
      fromActive.terminal.dispose()
      return
    }
    const idx = this.cached.findIndex((c) => c.key === key)
    if (idx >= 0) {
      const [evicted] = this.cached.splice(idx, 1)
      evicted.dispose?.()
      evicted.ws.close()
      evicted.terminal.dispose()
    }
  }

  /** Get a known terminal (Active or Cached). */
  get(key: string): ManagedTerminal | undefined {
    return this.active.get(key) ?? this.cached.find((c) => c.key === key)
  }

  /** True if we already have a live Terminal for this key. */
  exists(key: string): boolean {
    return this.get(key) !== undefined
  }

  size(): { active: number; cached: number } {
    return { active: this.active.size, cached: this.cached.length }
  }

  /** Send the same JSON input frame TerminalView uses. Returns true if the
   *  terminal exists and the message was queued; false otherwise. */
  sendInput(key: string, data: string): boolean {
    const m = this.get(key)
    if (!m) return false
    m.ws.send(JSON.stringify({ type: 'input', data }))
    return true
  }

  /** Move keyboard focus to the terminal so subsequent keystrokes flow
   *  through xterm.js. */
  focus(key: string): boolean {
    const m = this.get(key)
    if (!m) return false
    m.terminal.focus()
    return true
  }
}

export const terminalManager = new TerminalManager()
