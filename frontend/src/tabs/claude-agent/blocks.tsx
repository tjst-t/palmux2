import AnsiToHtml from 'ansi-to-html'
import { useEffect, useMemo, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { useNavigate, useParams } from 'react-router-dom'
import remarkGfm from 'remark-gfm'

import { DiffView, buildSyntheticDiff } from '../../components/diff/diff-view'
import { relativeToWorktree, urlForFiles } from '../../lib/tab-nav'
import { selectBranchById, usePalmuxStore } from '../../stores/palmux-store'

import type { Block, Turn } from './types'
import styles from './blocks.module.css'

const ansiConverter = new AnsiToHtml({
  fg: '#d4d4d8',
  bg: '#0c0e14',
  newline: false,
  escapeXML: true,
  stream: false,
})

interface PermissionHandlers {
  onAllow: (scope: 'once' | 'session' | 'always', updatedInput?: unknown) => void
  onDeny: (reason?: string) => void
}

interface PlanHandlers {
  onApprove: () => void
  onStayInPlan: () => void
  /** Whether the agent is still in plan mode. When false the buttons
   *  hide entirely — plan blocks from earlier turns are read-only. */
  canActOnPlan: boolean
  /** True once the user has clicked one of the plan actions in this
   *  session. Used to fade the buttons out after a single use. */
  decided?: 'approved' | 'rejected'
}

interface BlockProps {
  block: Block
  permissionHandlers?: PermissionHandlers
  planHandlers?: PlanHandlers
  /** When the block is a `Task` tool_use that spawned a sub-agent, the
   *  caller passes in a render function that produces the nested child
   *  turn list. The Task block then expands into a tree (header on top,
   *  children indented underneath). Undefined ⇒ render the block flat
   *  as before. */
  renderTaskChildren?: () => React.ReactNode
}

export function BlockView({ block, permissionHandlers, planHandlers, renderTaskChildren }: BlockProps) {
  switch (block.kind) {
    case 'text':        return <TextBlock text={block.text ?? ''} />
    case 'thinking':    return <ThinkingBlock text={block.text ?? ''} />
    case 'tool_use':
      if (renderTaskChildren) {
        return <TaskTreeBlock block={block} renderChildren={renderTaskChildren} />
      }
      return <ToolUseBlock block={block} />
    case 'tool_result': return <ToolResultBlock block={block} />
    case 'todo':        return <TodoBlock block={block} />
    case 'permission':  return <PermissionBlock block={block} handlers={permissionHandlers} />
    case 'plan':        return <PlanBlock block={block} handlers={planHandlers} />
    default:            return null
  }
}

/** TaskTreeBlock wraps the regular ToolUseBlock with a children panel
 *  housing the sub-agent's turn transcript. Behaviour:
 *    - while the Task tool_use is still running (`!block.done`), the
 *      children panel is rendered expanded so the user watches the
 *      sub-agent's progress live;
 *    - once `block.done` flips true, the children collapse to a one-
 *      liner with a "show sub-agent transcript" toggle;
 *    - the regular ToolUseBlock chevron continues to govern the parent
 *      block's input panel (independent of the children panel). */
function TaskTreeBlock({
  block,
  renderChildren,
}: {
  block: Block
  renderChildren: () => React.ReactNode
}) {
  // Default state: expanded while running, auto-collapsed once done.
  // We track the previous `done` value so we only auto-toggle on the
  // false→true transition — a user who manually re-expanded a finished
  // task on reload shouldn't have it slammed shut.
  const running = !block.done
  const [showChildren, setShowChildren] = useState(running)
  const prevDone = useRef(block.done)
  useEffect(() => {
    if (!prevDone.current && block.done) {
      // task just finished — auto-collapse the sub-agent transcript
      setShowChildren(false)
    }
    prevDone.current = block.done
  }, [block.done])
  return (
    <div className={styles.taskTree}>
      <ToolUseBlock block={block} />
      {!running && (
        <button
          type="button"
          className={styles.taskToggle}
          onClick={() => setShowChildren((v) => !v)}
          title={showChildren ? 'Hide sub-agent transcript' : 'Show sub-agent transcript'}
        >
          <span className={`${styles.chevron} ${showChildren ? styles.expanded : ''}`}>›</span>
          {showChildren ? 'Hide sub-agent transcript' : (
            <span className={styles.taskToggleSummary}>Sub-agent transcript</span>
          )}
        </button>
      )}
      {showChildren && (
        <div className={styles.taskChildren}>{renderChildren()}</div>
      )}
    </div>
  )
}

// Re-export Turn for downstream callers that do tree assembly.
export type { Turn }

// splitTextWithAttachments strips `[image: /abs/path]` lines (the format
// Composer inlines when the user attaches images) out of the prose and
// returns the matched paths separately so we can render thumbnails.
function splitTextWithAttachments(text: string): { text: string; images: string[] } {
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
// `imageUploadDir`) into a fetchable HTTP URL. We only proxy images that
// live under the configured upload dir; the basename is what the route
// keys on, so any path whose basename matches a real upload resolves.
function uploadURLForPath(path: string): string | null {
  if (!path) return null
  // Take the last path segment (POSIX or Windows-ish). filename only.
  const idx = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  const name = idx >= 0 ? path.slice(idx + 1) : path
  if (!name) return null
  return `/api/upload/${encodeURIComponent(name)}`
}

function TextBlock({ text }: { text: string }) {
  if (!text) return null
  const { text: prose, images } = splitTextWithAttachments(text)
  return (
    <div className={styles.text}>
      {prose && <ReactMarkdown remarkPlugins={[remarkGfm]}>{prose}</ReactMarkdown>}
      {images.length > 0 && (
        <div className={styles.inlineAttachments}>
          {images.map((p, i) => {
            const url = uploadURLForPath(p)
            if (!url) {
              return (
                <span key={i} className={styles.inlineAttachmentMissing}>
                  [image: {p}]
                </span>
              )
            }
            return (
              <a
                key={i}
                href={url}
                target="_blank"
                rel="noreferrer"
                className={styles.inlineAttachment}
                title={p}
              >
                <img src={url} alt={p} className={styles.inlineAttachmentImg} />
              </a>
            )
          })}
        </div>
      )}
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

// PlanBlock renders an ExitPlanMode block. The CLI emits the plan via the
// same `tool_use` envelope it uses for any other tool — Palmux re-tags it
// to kind:"plan" in normalize.go so the frontend can present the plan as
// authored content (Markdown) rather than a tool input dump.
//
// While the plan is still streaming (`!block.done`) it is rendered
// expanded so the user can watch it form. Once finalised it collapses to
// a header preview and the user can expand on demand. The action row
// (Approve & Run / Stay in plan) appears only when `permission_mode`
// is still "plan" and the user hasn't yet acted on this block.
function PlanBlock({ block, handlers }: { block: Block; handlers?: PlanHandlers }) {
  const planText = useMemo(() => extractPlanText(block), [block])
  const streaming = !block.done
  // Default state mirrors ToolUseBlock: expanded while drafting so the
  // user watches the plan form, default-expanded once done so the
  // current plan is visible without an extra click — it's the most
  // recent thing the agent said. The chevron lets the user collapse
  // long plans on demand.
  const [expanded, setExpanded] = useState(true)
  const showActions = !!handlers && handlers.canActOnPlan && !handlers.decided
  const decisionLabel =
    handlers?.decided === 'approved'
      ? 'Approved — switched to execution mode'
      : handlers?.decided === 'rejected'
        ? 'Staying in plan mode'
        : ''
  // First non-blank line of the plan as a one-line preview when collapsed.
  const previewLine = useMemo(() => firstNonBlankLine(planText), [planText])

  return (
    <div className={styles.plan}>
      <div
        className={styles.planHeader}
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
        <span className={styles.planLabel}>Plan</span>
        {streaming && <span className={`${styles.toolBadge} ${styles.running}`}>drafting</span>}
        {!expanded && previewLine && (
          <span className={styles.planPreview}>{previewLine}</span>
        )}
      </div>
      {(expanded || streaming) && planText && (
        <div className={styles.planBody}>
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{planText}</ReactMarkdown>
        </div>
      )}
      {showActions && (
        <div className={styles.planActions}>
          <button
            type="button"
            className={`${styles.planActionBtn} ${styles.planApprove}`}
            onClick={handlers!.onApprove}
            title="Switch out of plan mode and run the plan"
          >
            Approve & Run
          </button>
          <button
            type="button"
            className={styles.planActionBtn}
            onClick={handlers!.onStayInPlan}
            title="Keep working in plan mode — agent will draft another plan"
          >
            Stay in plan
          </button>
        </div>
      )}
      {decisionLabel && <div className={styles.planDecision}>{decisionLabel}</div>}
    </div>
  )
}

// extractPlanText pulls the human-readable plan markdown out of the
// block's payload. The CLI's ExitPlanMode tool input has the shape
// `{"plan": "..."}` once finalised; while streaming, the partial JSON
// accumulates in `block.text`. We tolerate both shapes (and a couple of
// near-future schema variants) so a CLI version bump doesn't silently
// regress to an empty plan.
function extractPlanText(block: Block): string {
  const obj = parseInputObject(block)
  if (obj) {
    if (typeof obj.plan === 'string') return obj.plan
    if (typeof obj.markdown === 'string') return obj.markdown
    if (typeof obj.content === 'string') return obj.content
  }
  // Fall back to the partial-JSON accumulator. We try to parse it
  // optimistically — if the streaming chunk is already a valid JSON
  // object we grab its `.plan`; otherwise show the partial as-is so
  // the user sees something rather than nothing.
  const text = block.text ?? ''
  if (!text) return ''
  const trimmed = text.trim()
  if (trimmed.startsWith('{')) {
    try {
      const parsed = JSON.parse(trimmed) as Record<string, unknown>
      if (typeof parsed.plan === 'string') return parsed.plan
    } catch {
      // Streaming chunk: try to extract the literal plan string body
      // from the partial. Look for `"plan":"..."` and decode it.
      const m = trimmed.match(/"plan"\s*:\s*"((?:[^"\\]|\\.)*)/)
      if (m) {
        try {
          // Re-quote so JSON.parse handles escape sequences (\n, \t, …).
          return JSON.parse(`"${m[1]}"`) as string
        } catch {
          return m[1]
        }
      }
    }
  }
  return text
}

function firstNonBlankLine(s: string): string {
  if (!s) return ''
  for (const line of s.split('\n')) {
    const t = line.trim()
    if (t) return shorten(t.replace(/^#+\s*/, ''), 100)
  }
  return ''
}

function ToolUseBlock({ block }: { block: Block }) {
  // Default-collapsed once the tool finishes; expanded while running so the
  // user can see the input forming live (mirrors Claude Code Desktop where
  // the latest in-flight tool stays visible until completion).
  // …with one exception: a block whose input is still empty (Anthropic
  // emits content_block_start with `input: {}` before any input_json_delta)
  // should stay collapsed until at least one delta lands, otherwise we
  // render a useless `INPUT {}` panel and — worse — leave that panel
  // visible forever if the turn was interrupted before any delta arrived.
  const hasContent = blockHasContent(block)
  const [expanded, setExpanded] = useState(!block.done && hasContent)
  const summaryText = toolSummary(block)
  const badge = !block.done ? 'running' : ''
  // Drop entirely if the block finalised with no payload at all — that's
  // an orphan from an interrupted turn / dropped delta and only adds noise.
  if (block.done && !hasContent) return null
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
      {expanded && hasContent && (
        <div className={styles.toolBody}>
          <ToolInputRich block={block} />
        </div>
      )}
    </div>
  )
}

// blockHasContent decides whether a tool_use block has anything worth
// rendering. Returns false for the brief window between content_block_start
// and the first input_json_delta (Anthropic ships start with input={}),
// and for orphans left over from interrupted turns.
function blockHasContent(block: Block): boolean {
  const obj = parseInputObject(block)
  if (obj && Object.keys(obj).length > 0) return true
  if (block.text && block.text.trim()) return true
  return false
}

// ToolInputRich renders the tool input panel with a tool-specific layout
// when we recognise the tool, otherwise falls back to a JSON dump.
function ToolInputRich({ block }: { block: Block }) {
  const params = useParams()
  const navigate = useNavigate()
  const repoId = params.repoId
  const branchId = params.branchId
  const worktreePath = usePalmuxStore(
    repoId && branchId ? selectBranchById(repoId, branchId) : () => undefined,
  )?.worktreePath
  const input = parseInputObject(block) ?? {}
  const name = (block.name || '').toLowerCase()
  const filePath = (input.file_path as string) ?? ''
  const openInFiles = filePath && repoId && branchId
    ? () => navigate(urlForFiles(repoId, branchId, relativeToWorktree(filePath, worktreePath)))
    : undefined

  if (name === 'edit') {
    const oldStr = (input.old_string as string) ?? ''
    const newStr = (input.new_string as string) ?? ''
    if (filePath) {
      const file = buildSyntheticDiff(filePath, oldStr, newStr)
      return (
        <>
          {openInFiles && (
            <button type="button" className={styles.openInFilesBtn} onClick={openInFiles}>
              Open in Files →
            </button>
          )}
          <DiffView files={[file]} />
        </>
      )
    }
  }
  if (name === 'write') {
    const content = (input.content as string) ?? ''
    if (filePath) {
      const file = buildSyntheticDiff(filePath, '', content)
      return (
        <>
          {openInFiles && (
            <button type="button" className={styles.openInFilesBtn} onClick={openInFiles}>
              Open in Files →
            </button>
          )}
          <DiffView files={[file]} />
        </>
      )
    }
  }
  if (name === 'read' && filePath && openInFiles) {
    const offset = input.offset as number | undefined
    const limit = input.limit as number | undefined
    return (
      <>
        <div className={styles.toolLabel}>read</div>
        <button type="button" className={styles.openInFilesBtn} onClick={openInFiles}>
          {filePath}{offset ? `:${offset}` : ''}{limit ? `+${limit}` : ''} →
        </button>
      </>
    )
  }
  // Generic fallback.
  return (
    <>
      <div className={styles.toolLabel}>input</div>
      <pre className={styles.toolPre}>{formatToolInput(block)}</pre>
    </>
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
      {(expanded || !showToggle) && output && <ToolResultBody output={output} />}
    </div>
  )
}

// ToolResultBody picks a renderer for the output:
//   - looks like a list of files (every non-empty line is a path) → clickable
//   - contains ANSI escapes → ANSI-rendered
//   - everything else → plain pre
function ToolResultBody({ output }: { output: string }) {
  const params = useParams()
  const navigate = useNavigate()
  const repoId = params.repoId
  const branchId = params.branchId
  const worktreePath = usePalmuxStore(
    repoId && branchId ? selectBranchById(repoId, branchId) : () => undefined,
  )?.worktreePath

  const lines = useMemo(() => output.split('\n'), [output])
  const lookLikePaths = useMemo(() => {
    if (lines.length < 2) return false
    let pathish = 0
    let total = 0
    for (const ln of lines) {
      const s = ln.trim()
      if (!s) continue
      total++
      // Path-like: contains / or starts with a typical filename, no
      // shell punctuation that would suggest free-form text.
      if (/^[\w./_-]+(:[0-9]+)?$/.test(s)) pathish++
    }
    return total > 0 && pathish / total > 0.85
  }, [lines])

  const ansiHtml = useMemo(() => {
    if (!output.includes('[')) return null
    try {
      return ansiConverter.toHtml(output)
    } catch {
      return null
    }
  }, [output])

  if (lookLikePaths && repoId && branchId) {
    return (
      <ul className={styles.pathList}>
        {lines.filter((l) => l.trim()).map((line, i) => {
          const trimmed = line.trim()
          // Strip trailing line:N if present so urlForFiles gets a clean path.
          const m = trimmed.match(/^(.*?)(?::(\d+))?$/)
          const cleanPath = m?.[1] ?? trimmed
          return (
            <li key={i} className={styles.pathListItem}>
              <button
                type="button"
                className={styles.pathLink}
                onClick={() => navigate(urlForFiles(repoId, branchId, relativeToWorktree(cleanPath, worktreePath)))}
              >
                {trimmed}
              </button>
            </li>
          )
        })}
      </ul>
    )
  }

  if (ansiHtml !== null) {
    return (
      <pre
        className={styles.toolResultPre}
        // Safe-ish: ansi-to-html escapes XML; we've set escapeXML:true.
        dangerouslySetInnerHTML={{ __html: ansiHtml }}
      />
    )
  }
  return <pre className={styles.toolResultPre}>{output}</pre>
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
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [editError, setEditError] = useState<string | null>(null)

  const startEdit = () => {
    setDraft(inputStr)
    setEditError(null)
    setEditing(true)
  }

  const submitEdit = () => {
    if (!handlers) return
    let parsed: unknown
    try {
      parsed = JSON.parse(draft)
    } catch (e) {
      setEditError(e instanceof Error ? e.message : String(e))
      return
    }
    handlers.onAllow('once', parsed)
    setEditing(false)
  }

  return (
    <div className={styles.permission}>
      <div className={styles.permissionHeader}>
        <span>Tool permission requested:</span>
        <span className={styles.permissionToolName}>{block.toolName}</span>
      </div>
      {!editing && inputStr && <div className={styles.permissionInput}>{inputStr}</div>}
      {editing && (
        <div className={styles.permissionEdit}>
          <textarea
            className={styles.permissionEditArea}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            spellCheck={false}
            rows={Math.min(12, draft.split('\n').length + 1)}
          />
          {editError && <div className={styles.permissionEditError}>JSON parse error: {editError}</div>}
        </div>
      )}
      {decided ? (
        <div className={styles.permissionDecision}>Decision: {block.decision}</div>
      ) : handlers ? (
        editing ? (
          <div className={styles.permissionActions}>
            <button className={styles.allow} onClick={submitEdit}>
              Allow with edits
            </button>
            <button onClick={() => setEditing(false)}>Cancel</button>
          </div>
        ) : (
          <div className={styles.permissionActions}>
            <button className={styles.allow} onClick={() => handlers.onAllow('once')}>
              Allow (y)
            </button>
            <button onClick={() => handlers.onAllow('session')}>Allow for session</button>
            <button
              onClick={() => handlers.onAllow('always')}
              title="Add this tool to .claude/settings.json permissions.allow"
            >
              Always allow
            </button>
            <button onClick={startEdit}>Edit…</button>
            <button className={styles.deny} onClick={() => handlers.onDeny()}>
              Deny (n)
            </button>
          </div>
        )
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
  const obj = parseInputObject(block)
  if (obj && Object.keys(obj).length > 0) return safeStringify(obj)
  // Either no input yet, or input is the start-of-stream `{}` placeholder.
  // Fall back to the partial-JSON delta accumulator so streaming tools
  // show the input building up rather than a misleading "{}".
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
