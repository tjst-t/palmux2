import { useEffect, useMemo, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

import { api } from '../../lib/api'

import styles from './file-preview.module.css'
import type { FileBody } from './types'

interface Props {
  apiBase: string
  path: string
  /** 1-based line to scroll to and briefly highlight (for grep results). */
  lineNum?: number
}

export function FilePreview({ apiBase, path, lineNum }: Props) {
  const [body, setBody] = useState<FileBody | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const codeRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    let cancelled = false
    setBody(null)
    setError(null)
    setLoading(true)
    ;(async () => {
      try {
        const data = await api.get<FileBody>(`${apiBase}/raw?path=${encodeURIComponent(path)}`)
        if (!cancelled) setBody(data)
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [apiBase, path])

  const lines = useMemo(() => (body?.content != null ? body.content.split('\n') : []), [body])

  // Scroll to lineNum once the body is in the DOM. Re-runs when lineNum
  // changes too so a user clicking another grep result re-targets the same
  // file's preview.
  useEffect(() => {
    if (!body || !lineNum || lineNum <= 0) return
    const target = codeRef.current?.querySelector<HTMLElement>(`[data-line="${lineNum}"]`)
    if (target) target.scrollIntoView({ block: 'center', behavior: 'auto' })
  }, [body, lineNum])

  if (loading) return <p className={styles.placeholder}>Loading…</p>
  if (error) return <p className={styles.error}>{error}</p>
  if (!body) return null

  if (body.mime?.startsWith('image/')) {
    return (
      <div className={styles.imageWrap}>
        <img alt={body.path} src={`${apiBase}/raw?path=${encodeURIComponent(path)}`} />
      </div>
    )
  }

  return (
    <div className={styles.wrap}>
      <header className={styles.header}>
        <span className={styles.path}>{body.path}</span>
        <span className={styles.meta}>{body.size} bytes • {body.mime}</span>
      </header>
      {body.mime === 'text/markdown' ? (
        <div className={styles.markdown}>
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{body.content}</ReactMarkdown>
        </div>
      ) : (
        <div className={styles.code} ref={codeRef}>
          {lines.map((ln, i) => {
            const num = i + 1
            const hl = num === lineNum
            return (
              <div
                key={num}
                data-line={num}
                className={hl ? `${styles.line} ${styles.lineHl}` : styles.line}
              >
                <span className={styles.lineNum}>{num}</span>
                <span className={styles.lineCode}>{ln === '' ? ' ' : ln}</span>
              </div>
            )
          })}
        </div>
      )}
      {body.truncated && (
        <p className={styles.truncated}>
          File truncated. Open in your editor for the full contents.
        </p>
      )}
    </div>
  )
}
