/** Generic format / parse helpers used by multiple block kinds.
 *
 *  Kept free of React-specific imports so any block file can pull in
 *  exactly what it needs without dragging the whole renderer
 *  dependency tree.
 */
import AnsiToHtml from 'ansi-to-html'

import type { Block } from '../../types'

export const ansiConverter = new AnsiToHtml({
  fg: '#d4d4d8',
  bg: '#0c0e14',
  newline: false,
  escapeXML: true,
  stream: false,
})

// splitTextWithAttachments strips `[image: /abs/path]` lines (the format
// Composer inlines when the user attaches images) out of the prose and
// returns the matched paths separately so we can render thumbnails.
export function splitTextWithAttachments(text: string): { text: string; images: string[] } {
  const images: string[] = []
  // Repeatedly match per-line image tags and strip them out.
  const cleaned = text.replace(/^\s*\[image:\s+(\S.*?)\]\s*$/gim, (_, p) => {
    if (typeof p === 'string') images.push(p.trim())
    return ''
  }).replace(/\n{3,}/g, '\n\n').trim()
  return { text: cleaned, images }
}

// uploadURLForPath turns an absolute path served by the upload endpoint
// (canonically `/tmp/palmux-uploads/<name>` but the user can configure
// `attachmentUploadDir` since S008) into a fetchable HTTP URL. The
// route keys on the basename and the server's locator walks the per-
// branch directories under the root, so any uploaded file resolves
// regardless of which branch it landed in.
export function uploadURLForPath(path: string): string | null {
  if (!path) return null
  // Take the last path segment (POSIX or Windows-ish). filename only.
  const idx = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  const name = idx >= 0 ? path.slice(idx + 1) : path
  if (!name) return null
  return `/api/upload/${encodeURIComponent(name)}`
}

// parseInputObject parses block.input (a value or a JSON-string) into a
// plain object, returning null when the field is missing or unparseable.
export function parseInputObject(block: Block): Record<string, unknown> | null {
  const raw = block.input
  if (raw == null) return null
  if (typeof raw === 'object') return raw as Record<string, unknown>
  if (typeof raw === 'string') {
    try { return JSON.parse(raw) as Record<string, unknown> } catch { return null }
  }
  return null
}

// blockHasContent decides whether a tool_use block has anything worth
// rendering. Returns false for the brief window between content_block_start
// and the first input_json_delta (Anthropic ships start with input={}),
// and for orphans left over from interrupted turns.
export function blockHasContent(block: Block): boolean {
  const obj = parseInputObject(block)
  if (obj && Object.keys(obj).length > 0) return true
  if (block.text && block.text.trim()) return true
  return false
}

// partialField extracts a single string-valued field from partial-streaming
// JSON. The agent ships tool input as a sequence of input_json_delta chunks;
// the accumulated text in `block.text` is valid JSON only after the final
// delta lands. Until then a regex pull lets us surface the description /
// subagent_type / prompt the moment each lands, instead of waiting for the
// whole envelope to close. Returns null when the field is missing or
// unparseable; callers fall back to '' for display.
export function partialField(text: string | undefined, key: string): string | null {
  if (!text) return null
  const trimmed = text.trim()
  if (!trimmed.startsWith('{')) return null
  const re = new RegExp(`"${key}"\\s*:\\s*"((?:[^"\\\\]|\\\\.)*)`)
  const m = trimmed.match(re)
  if (!m) return null
  try { return JSON.parse(`"${m[1]}"`) as string } catch { return m[1] }
}

export function formatToolInput(block: Block): string {
  const obj = parseInputObject(block)
  if (obj && Object.keys(obj).length > 0) return safeStringify(obj)
  // Either no input yet, or input is the start-of-stream `{}` placeholder.
  // Fall back to the partial-JSON delta accumulator so streaming tools
  // show the input building up rather than a misleading "{}".
  return block.text ?? ''
}

export function safeStringify(v: unknown): string {
  if (typeof v === 'string') {
    try { return JSON.stringify(JSON.parse(v), null, 2) } catch { return v }
  }
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}

export function shorten(s: string, n: number): string {
  if (!s) return ''
  if (s.length <= n) return s
  return s.slice(0, n - 1) + '…'
}

export function summary(s: string, n: number): string {
  return shorten(s.replace(/\s+/g, ' ').trim(), n)
}

export function firstLine(s: string): string {
  if (!s) return ''
  const idx = s.indexOf('\n')
  const head = idx === -1 ? s : s.slice(0, idx)
  return shorten(head, 100)
}

// toolSummary builds the inline preview shown next to the tool name in the
// collapsed header. Different tools deserve different one-liners.
export function toolSummary(block: Block): string {
  const input = parseInputObject(block)
  const name = (block.name || '').toLowerCase()
  if (!input) return ''
  if (name === 'bash') {
    return shorten((input.command as string) ?? '', 100)
  }
  if (name === 'edit' || name === 'write' || name === 'notebookedit') {
    return shorten((input.file_path as string) ?? '', 100)
  }
  if (name === 'read') {
    const p = (input.file_path as string) ?? ''
    const offset = input.offset ? `:${input.offset}` : ''
    return shorten(p + offset, 100)
  }
  if (name === 'glob' || name === 'grep') {
    const p = (input.pattern as string) ?? (input.glob as string) ?? ''
    return shorten(p, 100)
  }
  if (name === 'task' || name === 'agent') {
    return shorten((input.description as string) ?? (input.subagent_type as string) ?? '', 100)
  }
  if (name === 'webfetch' || name === 'websearch') {
    return shorten((input.url as string) ?? (input.query as string) ?? '', 100)
  }
  if (name === 'todowrite') return ''
  // Fallback: stringify the object compactly.
  return shorten(JSON.stringify(input), 100)
}
