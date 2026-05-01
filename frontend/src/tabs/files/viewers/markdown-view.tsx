// MarkdownView — preserves the pre-S010 Markdown rendering path.
//
// We deliberately keep ReactMarkdown + remark-gfm here (not Monaco)
// because users were already relying on the rendered look, and S010's
// charter says "preserve existing behaviour" for `.md`. The look-and-
// feel CSS is copied verbatim from the previous file-preview.module.css.

import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

import styles from './markdown-view.module.css'
import type { ViewerProps } from './types'

export function MarkdownView({ body }: ViewerProps) {
  if (!body) return <p className={styles.placeholder}>Loading…</p>
  return (
    <div className={styles.wrap} data-testid="markdown-view">
      <div className={styles.markdown}>
        <ReactMarkdown remarkPlugins={[remarkGfm]}>{body.content}</ReactMarkdown>
      </div>
    </div>
  )
}
