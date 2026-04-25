import { useEffect, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

import { api } from '../../lib/api'

import styles from './file-preview.module.css'
import type { FileBody } from './types'

interface Props {
  apiBase: string
  path: string
}

export function FilePreview({ apiBase, path }: Props) {
  const [body, setBody] = useState<FileBody | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

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
        <pre className={styles.code}>{body.content}</pre>
      )}
      {body.truncated && (
        <p className={styles.truncated}>
          File truncated. Open in your editor for the full contents.
        </p>
      )}
    </div>
  )
}
