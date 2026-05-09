/** TextBlock + ThinkingBlock — assistant prose blocks. */
import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

import { useClaudeSearch } from '../search-context'
import {
  buildHighlightComponents,
  highlightText,
  searchMatchProps,
} from './helpers/search-highlight'
import {
  splitTextWithAttachments,
  uploadURLForPath,
  summary,
} from './helpers/format'
import styles from '../blocks.module.css'

export function TextBlock({ text, blockId }: { text: string; blockId?: string }) {
  const { query, openedBlocks, activeBlockId } = useClaudeSearch()
  if (!text) return null
  const { text: prose, images } = splitTextWithAttachments(text)
  const match = searchMatchProps(blockId, query, openedBlocks, activeBlockId)
  const isActiveMatch = !!query && blockId === activeBlockId
  // ReactMarkdown v10 doesn't surface text-node hooks via `components`,
  // so we override every common text-bearing element (p, li, td, em,
  // strong, h1..h6, code-inline) and recursively wrap any string child
  // through highlightText. Cheap when query is empty — just defaults
  // to the standard element so formatting is preserved.
  const components = query
    ? buildHighlightComponents(query, isActiveMatch)
    : undefined
  return (
    <div className={styles.text} {...match}>
      {prose && (
        <ReactMarkdown
          remarkPlugins={[remarkGfm]}
          components={components as never}
        >
          {prose}
        </ReactMarkdown>
      )}
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

export function ThinkingBlock({ text, blockId }: { text: string; blockId?: string }) {
  const [manualExpanded, setManualExpanded] = useState(false)
  const { query, openedBlocks, activeBlockId } = useClaudeSearch()
  // Auto-expand when this thinking block carries a search match —
  // otherwise the user sees a "3/12" count but nothing scrolls into
  // view since the body is collapsed.
  const forceExpand = !!query && !!blockId && openedBlocks.has(blockId)
  const expanded = manualExpanded || forceExpand
  const isActiveMatch = !!query && blockId === activeBlockId
  if (!text) return null
  return (
    <div className={styles.thinking} {...searchMatchProps(blockId, query, openedBlocks, activeBlockId)}>
      <button
        type="button"
        className={styles.thinkingToggle}
        onClick={() => setManualExpanded((v) => !v)}
      >
        <span className={`${styles.chevron} ${expanded ? styles.expanded : ''}`}>›</span>
        Thought {!expanded && summary(text, 60)}
      </button>
      {expanded && (
        <div className={styles.thinkingBody}>
          {query ? highlightText(text, query, isActiveMatch) : text}
        </div>
      )}
    </div>
  )
}
