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

  if (path.endsWith('.drawio') || path.endsWith('.drawio.svg') || path.endsWith('.drawio.png')) {
    return <DrawioPreview path={body.path} content={body.content} />
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

// DrawioPreview embeds the diagrams.net public viewer in an iframe and pushes
// the file's XML over postMessage. We don't render mxgraph ourselves — that
// would be a 1MB+ runtime; the cross-origin viewer is the standard approach.
function DrawioPreview({ path, content }: { path: string; content: string }) {
  const iframeRef = useRef<HTMLIFrameElement | null>(null)
  const [ready, setReady] = useState(false)

  useEffect(() => {
    function onMessage(e: MessageEvent) {
      if (typeof e.data !== 'string' || !iframeRef.current) return
      let msg: { event?: string }
      try {
        msg = JSON.parse(e.data)
      } catch {
        return
      }
      if (msg.event === 'init') {
        // Viewer is ready; push the XML to render.
        iframeRef.current.contentWindow?.postMessage(
          JSON.stringify({ action: 'load', xml: content, autosave: 0 }),
          '*',
        )
        setReady(true)
      }
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [content])

  return (
    <div className={styles.wrap}>
      <header className={styles.header}>
        <span className={styles.path}>{path}</span>
        <span className={styles.meta}>drawio</span>
      </header>
      <iframe
        ref={iframeRef}
        title={`drawio: ${path}`}
        src="https://viewer.diagrams.net/?embed=1&proto=json&spinner=1&chrome=0&toolbar=0"
        style={{ flex: 1, minHeight: 0, border: 0, background: 'var(--color-elevated)' }}
        sandbox="allow-scripts allow-same-origin"
      />
      {!ready && <p className={styles.placeholder}>Loading drawio viewer…</p>}
    </div>
  )
}
