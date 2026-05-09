/** ToolResultBody — picks a renderer for tool_result output:
 *    - looks like a list of files (every non-empty line is a path) → clickable
 *    - contains ANSI escapes → ANSI-rendered
 *    - looks like markdown → ReactMarkdown
 *    - everything else → plain pre
 */
import { useMemo } from 'react'
import ReactMarkdown from 'react-markdown'
import { useNavigate, useParams } from 'react-router-dom'
import remarkGfm from 'remark-gfm'

import { relativeToWorktree, urlForFiles } from '../../../../lib/tab-nav'
import { selectBranchById, usePalmuxStore } from '../../../../stores/palmux-store'
import { ansiConverter } from '../helpers/format'

import styles from '../../blocks.module.css'

export function ToolResultBody({ output }: { output: string }) {
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

  // Markdown heuristic: ATX headers (`# `, `## `), fenced code blocks, or
  // a non-trivial mix of bullet lists + bold. Sub-agent (Task) outputs are
  // the canonical markdown source — they reliably ship `## High` /
  // `## Medium` style sections, code fences, and link-style file refs.
  // We skip when the output looks like a path list or has ANSI escapes
  // (those have dedicated renderers).
  const looksMarkdown = useMemo(() => {
    if (lookLikePaths) return false
    let score = 0
    if (/^#{1,6}\s/m.test(output)) score += 2
    if (/^```/m.test(output)) score += 2
    if (/^\s*[-*+]\s+\S/m.test(output)) score += 1
    if (/\*\*[^*]+\*\*/.test(output)) score += 1
    if (/^\s*>\s+/m.test(output)) score += 1
    if (/\[[^\]]+\]\([^)]+\)/.test(output)) score += 1
    return score >= 2
  }, [output, lookLikePaths])

  const ansiHtml = useMemo(() => {
    if (!output.includes('[')) return null
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
  if (looksMarkdown) {
    return (
      <div className={styles.toolResultMarkdown}>
        <ReactMarkdown remarkPlugins={[remarkGfm]}>{output}</ReactMarkdown>
      </div>
    )
  }
  return <pre className={styles.toolResultPre}>{output}</pre>
}
