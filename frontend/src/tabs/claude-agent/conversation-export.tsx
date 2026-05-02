// Conversation export (S018).
//
// Renders the "Export" dialog launched from the Claude tab top bar.
// Lets the user pick Markdown or JSON, customise the filename, and
// click Download. Both serialisers operate on the FE-side normalised
// snapshot — see docs/sprint-logs/S018/decisions.md for why we don't
// dump raw stream-json (BE-side) here.

import { useEffect, useMemo, useRef, useState } from 'react'

import type { Block, Turn } from './types'

interface ConversationExportDialogProps {
  open: boolean
  onClose: () => void
  turns: Turn[]
  /** Display label for the {branch}-{date}.{ext} default filename.
   *  Sanitised by makeDefaultFilename. */
  branchId: string
  /** Optional metadata included in the JSON envelope for context. */
  sessionId: string
  repoId: string
  model: string
}

type Format = 'markdown' | 'json'

export function ConversationExportDialog(props: ConversationExportDialogProps) {
  const { open, onClose, turns, branchId, sessionId, repoId, model } = props

  const [format, setFormat] = useState<Format>('markdown')
  const [filename, setFilename] = useState(() => makeDefaultFilename(branchId, 'markdown'))
  // When the user changes format, regenerate the default filename
  // unless they've already typed a custom name (we detect that by
  // checking the extension matches what the previous format produced).
  // Simpler: just regenerate to the new default on format toggle.
  useEffect(() => {
    if (!open) return
    setFilename(makeDefaultFilename(branchId, format))
  }, [format, branchId, open])

  // Re-seed defaults whenever the dialog opens (covers the case where a
  // user previously exported and we want the date to be fresh).
  const dialogRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    if (!open) return
    dialogRef.current?.focus()
  }, [open])

  const exportPayload = useMemo(() => {
    if (!open) return ''
    if (format === 'markdown') {
      return toMarkdown(turns, { branchId, sessionId, model })
    }
    return toJSON(turns, { branchId, sessionId, repoId, model })
  }, [open, format, turns, branchId, sessionId, repoId, model])

  if (!open) return null

  const handleDownload = () => {
    const blob = new Blob([exportPayload], {
      type: format === 'markdown' ? 'text/markdown' : 'application/json',
    })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = filename || makeDefaultFilename(branchId, format)
    document.body.appendChild(a)
    a.click()
    a.remove()
    // Defer revoke so the browser has a chance to read the blob.
    setTimeout(() => URL.revokeObjectURL(url), 1000)
    onClose()
  }

  return (
    <div
      className="palmux-export-overlay"
      role="dialog"
      aria-modal="true"
      aria-label="Export conversation"
      data-testid="export-dialog"
      onMouseDown={(e) => {
        // Backdrop click closes — only fire when click started on the
        // overlay element itself (not propagated up from inner content).
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        className="palmux-export-dialog"
        ref={dialogRef}
        tabIndex={-1}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.preventDefault()
            onClose()
          }
        }}
      >
        <h3 className="palmux-export-title">Export conversation</h3>
        <fieldset className="palmux-export-format">
          <legend>Format</legend>
          <label>
            <input
              type="radio"
              name="palmux-export-format"
              value="markdown"
              checked={format === 'markdown'}
              onChange={() => setFormat('markdown')}
              data-testid="export-format-markdown"
            />
            <span>Markdown (.md)</span>
            <small>Human-readable. Tool blocks fold into &lt;details&gt;.</small>
          </label>
          <label>
            <input
              type="radio"
              name="palmux-export-format"
              value="json"
              checked={format === 'json'}
              onChange={() => setFormat('json')}
              data-testid="export-format-json"
            />
            <span>JSON (.json)</span>
            <small>Full normalised snapshot. Round-trippable.</small>
          </label>
        </fieldset>
        <label className="palmux-export-filename">
          <span>Filename</span>
          <input
            type="text"
            value={filename}
            onChange={(e) => setFilename(e.target.value)}
            spellCheck={false}
            data-testid="export-filename"
          />
        </label>
        <div className="palmux-export-actions">
          <button
            type="button"
            onClick={onClose}
            data-testid="export-cancel"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={handleDownload}
            className="palmux-export-primary"
            data-testid="export-download"
          >
            Download
          </button>
        </div>
      </div>
    </div>
  )
}

interface MarkdownContext {
  branchId: string
  sessionId: string
  model: string
}

interface JSONContext {
  branchId: string
  sessionId: string
  repoId: string
  model: string
}

/** Serialise the turn list to a Slack/issue-friendly Markdown blob.
 *  Each turn is a heading; tool blocks are collapsed into `<details>`
 *  blocks so the prose stays scannable while the diagnostic content
 *  is still recoverable.
 */
export function toMarkdown(turns: Turn[], ctx: MarkdownContext): string {
  const lines: string[] = []
  lines.push(`# Claude session — ${ctx.branchId}`)
  lines.push('')
  if (ctx.sessionId) lines.push(`- session: \`${ctx.sessionId}\``)
  if (ctx.model) lines.push(`- model: \`${ctx.model}\``)
  lines.push(`- exported: ${new Date().toISOString()}`)
  lines.push('')
  for (const t of turns) {
    if (t.parentToolUseId) continue // skip sub-agent transcripts in markdown
    if (t.role === 'system') {
      lines.push(...renderSystemTurn(t))
      continue
    }
    const heading = roleHeading(t.role)
    if (heading) {
      lines.push(`## ${heading}`)
      lines.push('')
    }
    for (const b of t.blocks) {
      lines.push(...renderBlockMarkdown(b))
    }
    lines.push('')
  }
  return lines.join('\n').trimEnd() + '\n'
}

function roleHeading(role: Turn['role']): string {
  switch (role) {
    case 'user':      return 'User'
    case 'assistant': return 'Assistant'
    case 'tool':      return 'Tool result'
    case 'hook':      return 'Hook'
    default:          return ''
  }
}

function renderSystemTurn(t: Turn): string[] {
  const out: string[] = []
  for (const b of t.blocks) {
    if (b.kind === 'compact') {
      const turns = b.compactTurns ?? 0
      const tokens = (b.compactPreTokens ?? 0) > 0
        ? `${b.compactPreTokens} → ${b.compactPostTokens} tokens`
        : ''
      out.push(`> _Compacted ${turns} turn${turns === 1 ? '' : 's'} into 1 summary${tokens ? ` (${tokens})` : ''}._`)
      out.push('')
    }
  }
  return out
}

function renderBlockMarkdown(b: Block): string[] {
  switch (b.kind) {
    case 'text':
    case 'thinking': {
      const body = (b.text ?? '').trimEnd()
      if (!body) return []
      if (b.kind === 'thinking') {
        return ['<details><summary>thinking</summary>', '', body, '', '</details>', '']
      }
      return [body, '']
    }
    case 'tool_use': {
      const name = b.name || b.toolName || 'tool'
      const input = b.input
      const inputJSON = input == null ? '' : safeJSON(input)
      return [
        `<details><summary>tool: <code>${escHtml(name)}</code></summary>`,
        '',
        '```json',
        inputJSON || '{}',
        '```',
        '',
        '</details>',
        '',
      ]
    }
    case 'tool_result': {
      const out = (b.output ?? '').trimEnd()
      if (!out) return []
      return [
        `<details><summary>tool result${b.isError ? ' (error)' : ''}</summary>`,
        '',
        '```',
        out,
        '```',
        '',
        '</details>',
        '',
      ]
    }
    case 'todo': {
      const todos = b.todos
      if (!todos) return []
      return [
        '<details><summary>todos</summary>',
        '',
        '```json',
        safeJSON(todos),
        '```',
        '',
        '</details>',
        '',
      ]
    }
    case 'permission': {
      const tool = b.toolName || 'unknown'
      const decision = b.decision || 'pending'
      return [`> permission requested for \`${tool}\` — ${decision}`, '']
    }
    case 'plan': {
      const decision = b.planDecision ?? 'pending'
      return [
        `<details><summary>plan (${decision})</summary>`,
        '',
        b.text ?? '',
        '',
        '</details>',
        '',
      ]
    }
    case 'ask': {
      return [
        `<details><summary>ask user question</summary>`,
        '',
        '```json',
        safeJSON({ input: b.input, answers: b.askAnswers }),
        '```',
        '',
        '</details>',
        '',
      ]
    }
    case 'hook': {
      const label = b.hookName || b.hookEvent || 'hook'
      return [
        `<details><summary>hook: <code>${escHtml(label)}</code></summary>`,
        '',
        b.hookOutput || b.hookStdout || b.hookStderr || '',
        '',
        '</details>',
        '',
      ]
    }
    case 'compact': {
      const turns = b.compactTurns ?? 0
      return [
        `> _Compacted ${turns} turn${turns === 1 ? '' : 's'} into 1 summary._`,
        '',
      ]
    }
    default:
      return []
  }
}

/** Serialise to a portable JSON envelope. The shape is a Palmux export
 *  v1 schema — the version field lets us evolve while keeping older
 *  exports importable.
 */
export function toJSON(turns: Turn[], ctx: JSONContext): string {
  const payload = {
    palmuxExport: 1,
    exportedAt: new Date().toISOString(),
    repoId: ctx.repoId,
    branchId: ctx.branchId,
    sessionId: ctx.sessionId,
    model: ctx.model,
    turns,
  }
  return JSON.stringify(payload, null, 2) + '\n'
}

function safeJSON(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

function escHtml(s: string): string {
  return s.replace(/[&<>"']/g, (ch) =>
    ch === '&' ? '&amp;' : ch === '<' ? '&lt;' : ch === '>' ? '&gt;' : ch === '"' ? '&quot;' : '&#39;',
  )
}

/** "branch-YYYY-MM-DD.md" or ".json", with the branch sanitised down
 *  to filesystem-friendly characters. */
export function makeDefaultFilename(branchId: string, format: Format): string {
  const safe = branchId.replace(/[^a-zA-Z0-9._-]+/g, '-').replace(/^-+|-+$/g, '') || 'session'
  const today = new Date()
  const yyyy = today.getFullYear()
  const mm = String(today.getMonth() + 1).padStart(2, '0')
  const dd = String(today.getDate()).padStart(2, '0')
  const ext = format === 'markdown' ? 'md' : 'json'
  return `${safe}-${yyyy}-${mm}-${dd}.${ext}`
}
