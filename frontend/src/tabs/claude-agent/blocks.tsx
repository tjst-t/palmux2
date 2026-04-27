import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

import type { Block } from './types'
import styles from './blocks.module.css'

interface PermissionHandlers {
  onAllow: (scope: 'once' | 'session') => void
  onDeny: (reason?: string) => void
}

interface BlockProps {
  block: Block
  permissionHandlers?: PermissionHandlers
}

export function BlockView({ block, permissionHandlers }: BlockProps) {
  switch (block.kind) {
    case 'text':        return <TextBlock text={block.text ?? ''} />
    case 'thinking':    return <ThinkingBlock text={block.text ?? ''} />
    case 'tool_use':    return <ToolUseBlock block={block} />
    case 'tool_result': return <ToolResultBlock block={block} />
    case 'todo':        return <TodoBlock block={block} />
    case 'permission':  return <PermissionBlock block={block} handlers={permissionHandlers} />
    default:            return null
  }
}

function TextBlock({ text }: { text: string }) {
  if (!text) return null
  return (
    <div className={styles.text}>
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
    </div>
  )
}

function ThinkingBlock({ text }: { text: string }) {
  const [expanded, setExpanded] = useState(false)
  if (!text) return null
  return (
    <div className={styles.thinking}>
      <button
        type="button"
        className={styles.thinkingToggle}
        onClick={() => setExpanded((v) => !v)}
      >
        <span className={`${styles.chevron} ${expanded ? styles.expanded : ''}`}>›</span>
        Thought {!expanded && summary(text, 60)}
      </button>
      {expanded && <div className={styles.thinkingBody}>{text}</div>}
    </div>
  )
}

function ToolUseBlock({ block }: { block: Block }) {
  // Default-collapsed once the tool finishes; expanded while running so the
  // user can see the input forming live (mirrors Claude Code Desktop where
  // the latest in-flight tool stays visible until completion).
  const [expanded, setExpanded] = useState(!block.done)
  const summaryText = toolSummary(block)
  const badge = !block.done ? 'running' : ''
  return (
    <div className={styles.toolUse}>
      <div
        className={styles.toolHeader}
        role="button"
        tabIndex={0}
        onClick={() => setExpanded((v) => !v)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            setExpanded((v) => !v)
          }
        }}
      >
        <span className={`${styles.chevron} ${expanded ? styles.expanded : ''}`}>›</span>
        <span className={styles.toolName}>{block.name ?? 'tool'}</span>
        {summaryText && (
          <span className={styles.toolSummary}>{summaryText}</span>
        )}
        {badge && <span className={`${styles.toolBadge} ${styles.running}`}>{badge}</span>}
      </div>
      {expanded && (
        <div className={styles.toolBody}>
          <div className={styles.toolLabel}>input</div>
          <pre className={styles.toolPre}>{formatToolInput(block)}</pre>
        </div>
      )}
    </div>
  )
}

function ToolResultBlock({ block }: { block: Block }) {
  const [expanded, setExpanded] = useState(false)
  const output = block.output ?? ''
  const preview = firstLine(output)
  const showToggle = output.includes('\n') || output.length > preview.length
  return (
    <div className={`${styles.toolResult} ${block.isError ? styles.error : ''}`.trim()}>
      <div
        className={styles.toolHeader}
        role={showToggle ? 'button' : undefined}
        tabIndex={showToggle ? 0 : -1}
        onClick={() => showToggle && setExpanded((v) => !v)}
        onKeyDown={(e) => {
          if (!showToggle) return
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            setExpanded((v) => !v)
          }
        }}
      >
        {showToggle && (
          <span className={`${styles.chevron} ${expanded ? styles.expanded : ''}`}>›</span>
        )}
        <span className={`${styles.toolBadge} ${block.isError ? styles.error : ''}`.trim()}>
          {block.isError ? 'error' : 'result'}
        </span>
        {!expanded && preview && (
          <span className={styles.toolSummary}>{preview}</span>
        )}
      </div>
      {(expanded || !showToggle) && output && (
        <pre className={styles.toolResultPre}>{output}</pre>
      )}
    </div>
  )
}

function TodoBlock({ block }: { block: Block }) {
  const todos = parseTodos(block.todos)
  if (!todos || todos.length === 0) return null
  return (
    <div className={styles.todo}>
      <div className={styles.todoHeader}>Todo</div>
      <ul className={styles.todoList}>
        {todos.map((t, i) => (
          <li key={i} className={styles.todoItem}>
            <span className={`${styles.todoStatus} ${todoClass(t.status)}`.trim()}>
              {todoIcon(t.status)}
            </span>
            <span className={todoClass(t.status)}>{t.content || t.activeForm}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

function PermissionBlock({ block, handlers }: { block: Block; handlers?: PermissionHandlers }) {
  const inputStr = block.input == null ? '' : safeStringify(block.input)
  const decided = !!block.decision
  return (
    <div className={styles.permission}>
      <div className={styles.permissionHeader}>
        <span>Tool permission requested:</span>
        <span className={styles.permissionToolName}>{block.toolName}</span>
      </div>
      {inputStr && <div className={styles.permissionInput}>{inputStr}</div>}
      {decided ? (
        <div className={styles.permissionDecision}>Decision: {block.decision}</div>
      ) : handlers ? (
        <div className={styles.permissionActions}>
          <button className={styles.allow} onClick={() => handlers.onAllow('once')}>
            Allow (y)
          </button>
          <button onClick={() => handlers.onAllow('session')}>Allow for session</button>
          <button className={styles.deny} onClick={() => handlers.onDeny()}>
            Deny (n)
          </button>
        </div>
      ) : null}
    </div>
  )
}

// ──────────── helpers ────────────────────────────────────────────────────────

interface TodoEntry {
  status?: string
  content?: string
  activeForm?: string
}

function parseTodos(input: unknown): TodoEntry[] | null {
  if (!input) return null
  let value: unknown = input
  if (typeof input === 'string') {
    try { value = JSON.parse(input) } catch { return null }
  }
  if (Array.isArray(value)) return value as TodoEntry[]
  if (value && typeof value === 'object' && 'todos' in (value as Record<string, unknown>)) {
    const inner = (value as { todos: unknown }).todos
    if (Array.isArray(inner)) return inner as TodoEntry[]
  }
  return null
}

function todoClass(status?: string): string {
  switch (status) {
    case 'completed':   return styles.todoCompleted
    case 'in_progress': return styles.todoInProgress
    default:            return styles.todoPending
  }
}

function todoIcon(status?: string): string {
  switch (status) {
    case 'completed':   return '✔'
    case 'in_progress': return '◉'
    default:            return '○'
  }
}

// toolSummary builds the inline preview shown next to the tool name in the
// collapsed header. Different tools deserve different one-liners.
function toolSummary(block: Block): string {
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
  if (name === 'task') {
    return shorten((input.description as string) ?? (input.subagent_type as string) ?? '', 100)
  }
  if (name === 'webfetch' || name === 'websearch') {
    return shorten((input.url as string) ?? (input.query as string) ?? '', 100)
  }
  if (name === 'todowrite') return ''
  // Fallback: stringify the object compactly.
  return shorten(JSON.stringify(input), 100)
}

function parseInputObject(block: Block): Record<string, unknown> | null {
  const raw = block.input
  if (raw == null) return null
  if (typeof raw === 'object') return raw as Record<string, unknown>
  if (typeof raw === 'string') {
    try { return JSON.parse(raw) as Record<string, unknown> } catch { return null }
  }
  return null
}

function formatToolInput(block: Block): string {
  if (block.input != null) return safeStringify(block.input)
  return block.text ?? ''
}

function safeStringify(v: unknown): string {
  if (typeof v === 'string') {
    try { return JSON.stringify(JSON.parse(v), null, 2) } catch { return v }
  }
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}

function shorten(s: string, n: number): string {
  if (!s) return ''
  if (s.length <= n) return s
  return s.slice(0, n - 1) + '…'
}

function summary(s: string, n: number): string {
  return shorten(s.replace(/\s+/g, ' ').trim(), n)
}

function firstLine(s: string): string {
  if (!s) return ''
  const idx = s.indexOf('\n')
  const head = idx === -1 ? s : s.slice(0, idx)
  return shorten(head, 100)
}
