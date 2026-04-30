// Pure helpers for classifying MCP server status strings into a small
// set of presentation tones. Lives outside mcp-popup.tsx because Vite's
// fast-refresh plugin (react-refresh/only-export-components) wants
// component files to export *only* components.

import type { MCPServerInfo } from './types'

export type MCPStatusTone = 'ok' | 'warn' | 'err' | 'unknown'

/** statusTone classifies a raw CLI status string into one of four
 *  presentation tones. The CLI's vocabulary is "open" — newer Claude
 *  Code releases have introduced statuses like "needs-auth" or
 *  "connecting" — so unknown values fall through to a neutral pill
 *  rather than being dropped. */
export function statusTone(raw: string): MCPStatusTone {
  const s = raw.trim().toLowerCase()
  if (s === 'connected' || s === 'ok' || s === 'ready') return 'ok'
  if (s === 'connecting' || s === 'starting' || s === 'pending') return 'warn'
  if (
    s === 'failed' ||
    s === 'error' ||
    s === 'disconnected' ||
    s === 'needs-auth' ||
    s === 'auth-required' ||
    s === 'closed'
  )
    return 'err'
  return 'unknown'
}

/** rollupTone reduces a list of server tones into a single pip tone for
 *  the TopBar. Worst-status-wins. Empty input → 'unknown'. */
export function rollupTone(servers: MCPServerInfo[]): MCPStatusTone {
  if (servers.length === 0) return 'unknown'
  let worst: MCPStatusTone = 'ok'
  for (const s of servers) {
    const t = statusTone(s.status)
    if (t === 'err') return 'err'
    if (t === 'warn' && worst === 'ok') worst = 'warn'
    if (t === 'unknown' && worst === 'ok') worst = 'unknown'
  }
  return worst
}
