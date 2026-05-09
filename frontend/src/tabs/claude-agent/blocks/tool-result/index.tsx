/** ToolResultBlock — collapsible result panel with preview/showAll
 *  affordance. The body renderer (ToolResultBody) lives in body.tsx.
 */
import { useMemo, useState } from 'react'

import { usePalmuxStore } from '../../../../stores/palmux-store'
import { DEFAULT_READ_PREVIEW_LINE_COUNT } from '../../read-preview'
import { useClaudeSearch } from '../../search-context'
import { searchMatchProps } from '../helpers/search-highlight'
import { firstLine } from '../helpers/format'
import { ToolResultBody } from './body'

import type { Block } from '../../types'
import styles from '../../blocks.module.css'

export function ToolResultBlock({ block }: { block: Block }) {
  const [manualExpanded, setManualExpanded] = useState(false)
  // S018 — auto-expand when this tool_result carries a search match,
  // and force "show all" so the matched line is actually visible (the
  // preview slice could otherwise hide it past line N).
  const { query, openedBlocks, activeBlockId } = useClaudeSearch()
  const forceExpand = !!query && openedBlocks.has(block.id)
  const expanded = manualExpanded || forceExpand
  const setExpanded = (next: boolean | ((v: boolean) => boolean)) => {
    if (typeof next === 'function') setManualExpanded((v) => next(v))
    else setManualExpanded(next)
  }
  // S017: when expanded, large outputs default to a leading-N-lines
  // preview with a "Show all (X lines)" affordance. The preview cap
  // comes from globalSettings.readPreviewLineCount (default 50). This
  // applies to ANY tool_result — Read is the canonical case but Bash
  // / Grep / etc. all benefit from the same throttle.
  const previewLineCount = usePalmuxStore(
    (s) => s.globalSettings.readPreviewLineCount ?? DEFAULT_READ_PREVIEW_LINE_COUNT,
  )
  const [manualShowAll, setShowAll] = useState(false)
  const showAll = manualShowAll || forceExpand
  const output = block.output ?? ''
  const preview = firstLine(output)
  const showToggle = output.includes('\n') || output.length > preview.length
  // Compute total lines once. `output.split('\n')` keeps a trailing
  // empty cell for a trailing newline; we strip that for display so
  // "Show all (51 lines)" doesn't become "52 lines" because of one
  // trailing \n in the CLI output.
  const totalLines = useMemo(() => {
    if (!output) return 0
    const n = output.split('\n').length
    return output.endsWith('\n') ? n - 1 : n
  }, [output])
  const isLong = totalLines > previewLineCount
  const previewBody = useMemo(() => {
    if (!isLong || showAll) return output
    // Slice on \n so we don't break in the middle of a line, and don't
    // include a trailing newline that produces a phantom blank row.
    const parts = output.split('\n')
    return parts.slice(0, previewLineCount).join('\n')
  }, [output, isLong, showAll, previewLineCount])
  return (
    <div
      className={`${styles.toolResult} ${block.isError ? styles.error : ''}`.trim()}
      {...searchMatchProps(block.id, query, openedBlocks, activeBlockId)}
    >
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
        {!expanded && isLong && (
          <span className={styles.toolLines}>{totalLines} lines</span>
        )}
      </div>
      {(expanded || !showToggle) && output && (
        <>
          <ToolResultBody output={previewBody} />
          {(expanded || !showToggle) && isLong && (
            <button
              type="button"
              className={styles.showAllBtn}
              data-testid="tool-result-toggle"
              data-mode={showAll ? 'expanded' : 'preview'}
              onClick={(e) => {
                e.stopPropagation()
                setShowAll((v) => !v)
              }}
            >
              {showAll
                ? `Show preview (first ${previewLineCount} lines)`
                : `Show all (${totalLines} lines)`}
            </button>
          )}
        </>
      )}
    </div>
  )
}
