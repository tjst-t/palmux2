import AnsiToHtml from 'ansi-to-html'
import { useEffect, useMemo, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { useNavigate, useParams } from 'react-router-dom'
import remarkGfm from 'remark-gfm'

import { DiffView, buildSyntheticDiff } from '../../components/diff/diff-view'
import { relativeToWorktree, urlForFiles } from '../../lib/tab-nav'
import { selectBranchById, usePalmuxStore } from '../../stores/palmux-store'

import type { AskOption, AskQuestion, Block, Turn } from './types'
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

/** AskHandlers wires the AskQuestionBlock to the WS layer. `onRespond`
 *  ships the chosen labels (one inner array per question) to the
 *  backend. `canRespond` is true while the question is still pending —
 *  a permission_id is registered server-side and no answer has been
 *  submitted yet. */
interface AskHandlers {
  onRespond: (answers: string[][]) => void
  canRespond: boolean
}

interface BlockProps {
  block: Block
  permissionHandlers?: PermissionHandlers
  planHandlers?: PlanHandlers
  askHandlers?: AskHandlers
  /** When the block is a `Task` tool_use that spawned a sub-agent, the
   *  caller passes in a render function that produces the nested child
   *  turn list. The Task block then expands into a tree (header on top,
   *  children indented underneath). Undefined ⇒ render the block flat
   *  as before. */
  renderTaskChildren?: () => React.ReactNode
}

export function BlockView({ block, permissionHandlers, planHandlers, askHandlers, renderTaskChildren }: BlockProps) {
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
    case 'ask':         return <AskQuestionBlock block={block} handlers={askHandlers} />
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

// AskQuestionBlock renders an AskUserQuestion tool_use as a list of
// option buttons (or checkboxes for multiSelect). The CLI emits the
// question via the same `tool_use` envelope it uses for any other tool
// — Palmux re-tags it to kind:"ask" in normalize.go, then the
// permission_prompt MCP request that backs the tool stamps the
// permission_id onto the block once the request arrives.
//
// While the questions are still streaming (`!block.done`) we render a
// best-effort partial preview so the user sees the question forming.
// Once finalised the action row is enabled (provided the handlers say
// canRespond), and clicking submits the chosen labels back to the CLI.
//
// The "decided" view (chosen options highlighted, others disabled) is
// driven by `block.askAnswers` — set by the backend after the user
// answers, and replayed from the snapshot on reconnect so the UI never
// loses track of an answered question.
function AskQuestionBlock({ block, handlers }: { block: Block; handlers?: AskHandlers }) {
  const questions = useMemo(() => extractAskQuestions(block), [block])
  const streaming = !block.done
  const decidedAnswers = block.askAnswers
  const decided = !!decidedAnswers && decidedAnswers.length > 0
  // Local draft selections while the user is browsing options. One
  // string[] per question: chosen labels. Cleared once the answer
  // round-trips back from the server (decidedAnswers takes over).
  // Slot count tracks `questions.length` reactively without a useEffect
  // (the linter prefers we derive instead of mirroring) — when the
  // length grows mid-stream we pad with empty arrays on read.
  const [draft, setDraft] = useState<string[][]>([])
  const liveDraft = useMemo(() => {
    if (draft.length === questions.length) return draft
    const next = draft.slice(0, questions.length)
    while (next.length < questions.length) next.push([])
    return next
  }, [draft, questions.length])

  const canRespond = !decided && !streaming && (handlers?.canRespond ?? false)
  const allMultiSelect = questions.length > 0 && questions.every((q) => q.multiSelect === true)
  const allSingleSelect = questions.length > 0 && questions.every((q) => !q.multiSelect)

  const submit = (answers: string[][]) => {
    if (!handlers || !canRespond) return
    handlers.onRespond(answers)
  }

  // Single-question single-select shortcut: clicking an option submits
  // immediately. This matches the natural reading flow (questions come
  // one at a time the vast majority of the time).
  const handleSingleSelectClick = (qi: number, label: string) => {
    if (!canRespond) return
    if (questions.length === 1 && allSingleSelect) {
      submit([[label]])
      return
    }
    // Multi-question case: stash the selection in the draft and let the
    // user hit Submit when ready. (No CLI today emits multi-question
    // AskUserQuestion calls but the schema permits it.)
    setDraft((prev) => {
      const padded = padDraft(prev, questions.length)
      const next = padded.map((arr) => arr.slice())
      next[qi] = [label]
      return next
    })
  }

  const handleMultiSelectToggle = (qi: number, label: string) => {
    if (!canRespond) return
    setDraft((prev) => {
      const padded = padDraft(prev, questions.length)
      const next = padded.map((arr) => arr.slice())
      const arr = next[qi] ?? []
      const idx = arr.indexOf(label)
      if (idx >= 0) arr.splice(idx, 1)
      else arr.push(label)
      next[qi] = arr
      return next
    })
  }

  const handleSubmit = () => submit(liveDraft)

  return (
    <div className={styles.ask} data-testid="ask-question-block">
      {questions.length === 0 && streaming && (
        <div className={styles.askPending}>
          <span className={`${styles.toolBadge} ${styles.running}`}>asking</span>
          <span className={styles.askPendingText}>Preparing question…</span>
        </div>
      )}
      {questions.map((q, qi) => {
        const draftAns = liveDraft[qi] ?? []
        const decidedAns = decidedAnswers?.[qi] ?? []
        const isMulti = !!q.multiSelect
        return (
          <div key={qi} className={styles.askQuestion} data-testid={`ask-question-${qi}`}>
            <div className={styles.askHeader}>
              <span className={styles.askLabel}>Question</span>
              {streaming && <span className={`${styles.toolBadge} ${styles.running}`}>asking</span>}
              {q.header && <span className={styles.askSubheader}>{q.header}</span>}
            </div>
            <div className={styles.askPrompt}>{q.question || '…'}</div>
            <div className={styles.askOptions}>
              {q.options.map((opt: AskOption, oi) => {
                const isChosen = decided
                  ? decidedAns.includes(opt.label)
                  : draftAns.includes(opt.label)
                const isDimmed = decided && !isChosen
                const cls = [
                  styles.askOption,
                  isChosen ? styles.askOptionChosen : '',
                  isDimmed ? styles.askOptionDimmed : '',
                ].filter(Boolean).join(' ')
                if (isMulti) {
                  return (
                    <label
                      key={oi}
                      className={cls}
                      data-testid={`ask-option-${qi}-${oi}`}
                    >
                      <input
                        type="checkbox"
                        className={styles.askOptionCheckbox}
                        checked={isChosen}
                        disabled={!canRespond}
                        onChange={() => handleMultiSelectToggle(qi, opt.label)}
                      />
                      <span className={styles.askOptionBody}>
                        <span className={styles.askOptionLabel}>{opt.label}</span>
                        {opt.description && (
                          <span className={styles.askOptionDesc}>{opt.description}</span>
                        )}
                      </span>
                    </label>
                  )
                }
                return (
                  <button
                    key={oi}
                    type="button"
                    className={cls}
                    disabled={!canRespond && !isChosen}
                    onClick={() => handleSingleSelectClick(qi, opt.label)}
                    data-testid={`ask-option-${qi}-${oi}`}
                  >
                    <span className={styles.askOptionBody}>
                      <span className={styles.askOptionLabel}>{opt.label}</span>
                      {opt.description && (
                        <span className={styles.askOptionDesc}>{opt.description}</span>
                      )}
                    </span>
                  </button>
                )
              })}
            </div>
          </div>
        )
      })}
      {!decided && canRespond && (allMultiSelect || questions.length > 1) && (
        <div className={styles.askActions}>
          <button
            type="button"
            className={`${styles.askActionBtn} ${styles.askSubmit}`}
            onClick={handleSubmit}
            disabled={liveDraft.every((a) => a.length === 0)}
            data-testid="ask-submit"
          >
            Submit
          </button>
        </div>
      )}
      {decided && (
        <div className={styles.askDecision} data-testid="ask-decided">
          Answer{decidedAnswers && decidedAnswers.flat().length > 1 ? 's' : ''} sent: {' '}
          {decidedAnswers!.flat().join(', ') || '(empty)'}
        </div>
      )}
    </div>
  )
}

// padDraft grows or shrinks a draft answer array to match the current
// question count. Empty inner arrays fill any new slots.
function padDraft(prev: string[][], len: number): string[][] {
  if (prev.length === len) return prev
  const out = prev.slice(0, len)
  while (out.length < len) out.push([])
  return out
}

// extractAskQuestions tolerantly parses the AskUserQuestion input into
// a stable shape. Mirrors extractPlanText's idiom — we accept both the
// finalised `block.input` object and the partial-JSON accumulator in
// `block.text` so streaming questions render incrementally.
function extractAskQuestions(block: Block): AskQuestion[] {
  const obj = parseInputObject(block)
  const fromObj = (raw: Record<string, unknown> | null): AskQuestion[] | null => {
    if (!raw) return null
    const arr = raw.questions
    if (!Array.isArray(arr)) return null
    const out: AskQuestion[] = []
    for (const item of arr) {
      if (!item || typeof item !== 'object') continue
      const q = item as Record<string, unknown>
      const question = typeof q.question === 'string' ? q.question : ''
      const header = typeof q.header === 'string' ? q.header : undefined
      const multiSelect = q.multiSelect === true
      let options: AskOption[] = []
      if (Array.isArray(q.options)) {
        options = q.options
          .map((o: unknown) => {
            if (!o || typeof o !== 'object') return null
            const op = o as Record<string, unknown>
            if (typeof op.label !== 'string') return null
            const out: AskOption = { label: op.label }
            if (typeof op.description === 'string') out.description = op.description
            return out
          })
          .filter((x: AskOption | null): x is AskOption => x !== null)
      }
      out.push({ question, header, multiSelect, options })
    }
    return out
  }
  if (obj) {
    const parsed = fromObj(obj)
    if (parsed && parsed.length > 0) return parsed
  }
  // Streaming partial — try to parse the accumulator. Permissively
  // tolerate trailing junk by attempting JSON.parse first and falling
  // back to an empty list rather than crashing the renderer.
  const text = (block.text ?? '').trim()
  if (text.startsWith('{')) {
    try {
      const parsed = JSON.parse(text) as Record<string, unknown>
      const fromParsed = fromObj(parsed)
      if (fromParsed) return fromParsed
    } catch {
      // partial JSON — return nothing so the "Preparing question…"
      // pending state keeps showing instead of garbled text.
    }
  }
  return []
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
