/** ToolUseBlock + ToolInputRich — tool invocation blocks.
 *
 *  ToolUseBlock is the collapsible container; ToolInputRich is the
 *  body renderer that picks a tool-specific layout (Edit / Write /
 *  Read / Task / fallback JSON dump).
 */
import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { useNavigate, useParams } from 'react-router-dom'
import remarkGfm from 'remark-gfm'

import { DiffView, buildSyntheticDiff } from '../../../components/diff/diff-view'
import { relativeToWorktree, urlForFiles } from '../../../lib/tab-nav'
import { selectBranchById, usePalmuxStore } from '../../../stores/palmux-store'
import { useClaudeSearch } from '../search-context'
import { searchMatchProps } from './helpers/search-highlight'
import {
  blockHasContent,
  formatToolInput,
  parseInputObject,
  partialField,
  toolSummary,
} from './helpers/format'

import type { Block } from '../types'
import styles from '../blocks.module.css'

export function ToolUseBlock({ block }: { block: Block }) {
  // Default-collapsed once the tool finishes; expanded while running so the
  // user can see the input forming live (mirrors Claude Code Desktop where
  // the latest in-flight tool stays visible until completion).
  // …with one exception: a block whose input is still empty (Anthropic
  // emits content_block_start with `input: {}` before any input_json_delta)
  // should stay collapsed until at least one delta lands, otherwise we
  // render a useless `INPUT {}` panel and — worse — leave that panel
  // visible forever if the turn was interrupted before any delta arrived.
  const hasContent = blockHasContent(block)
  const [manualExpanded, setManualExpanded] = useState(!block.done && hasContent)
  // S018 — auto-expand a tool_use that carries a search match.
  const { query, openedBlocks, activeBlockId } = useClaudeSearch()
  const forceExpand = !!query && openedBlocks.has(block.id)
  const expanded = manualExpanded || forceExpand
  const setExpanded = (next: boolean | ((v: boolean) => boolean)) => {
    if (typeof next === 'function') setManualExpanded((v) => next(v))
    else setManualExpanded(next)
  }
  const summaryText = toolSummary(block)
  const badge = !block.done ? 'running' : ''
  // Drop entirely if the block finalised with no payload at all — that's
  // an orphan from an interrupted turn / dropped delta and only adds noise.
  if (block.done && !hasContent) return null
  return (
    <div className={styles.toolUse} {...searchMatchProps(block.id, query, openedBlocks, activeBlockId)}>
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

// ToolInputRich renders the tool input panel with a tool-specific layout
// when we recognise the tool, otherwise falls back to a JSON dump.
export function ToolInputRich({ block }: { block: Block }) {
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
  // Sub-agent dispatch tool. Older CLIs called this "Task", recent
  // releases renamed it to "Agent" — accept both, and also fall through
  // for any tool whose input shape carries the canonical fields. The raw
  // JSON dump is unreadable for any non-trivial prompt; we break it out
  // into proper labeled sections with the prompt rendered as markdown.
  const isSubAgentTool =
    name === 'task' || name === 'agent' ||
    (typeof input.subagent_type === 'string' && typeof input.prompt === 'string')
  if (isSubAgentTool) {
    // While the input JSON is still streaming, parseInputObject returns
    // null and `input` is empty {}. Fall back to extracting individual
    // fields from the partial JSON in block.text so the panel shows
    // useful content during the stream instead of a raw brace dump.
    const description =
      (input.description as string) ??
      partialField(block.text, 'description') ??
      ''
    const subagentType =
      (input.subagent_type as string) ??
      partialField(block.text, 'subagent_type') ??
      ''
    const prompt =
      (input.prompt as string) ??
      partialField(block.text, 'prompt') ??
      ''
    return (
      <div className={styles.taskInputBox}>
        {(description || subagentType) && (
          <div className={styles.taskInputHeader}>
            {description && (
              <div className={styles.taskInputDescription}>{description}</div>
            )}
            {subagentType && (
              <span className={styles.taskInputAgent}>{subagentType}</span>
            )}
          </div>
        )}
        {prompt && (
          <>
            <div className={styles.toolLabel}>prompt</div>
            <div className={styles.taskInputPrompt}>
              <ReactMarkdown remarkPlugins={[remarkGfm]}>{prompt}</ReactMarkdown>
            </div>
          </>
        )}
        {!description && !subagentType && !prompt && (
          <pre className={styles.toolPre}>{formatToolInput(block)}</pre>
        )}
      </div>
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
