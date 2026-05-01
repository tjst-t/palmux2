// File-preview dispatcher (S010).
//
// Pre-S010 this file was a self-contained Markdown / image / drawio
// renderer. It now drives the **viewer dispatcher** under
// `./viewers/`: pick a viewer kind from path + size + MIME, then lazy-
// load the matching component. The dispatcher does the size gate
// before fetching, so files above `previewMaxBytes` (default 10 MiB)
// never round-trip their body — important on mobile.

import { Suspense, lazy, useEffect, useMemo, useState } from 'react'

import { api } from '../../lib/api'
import { usePalmuxStore } from '../../stores/palmux-store'

import styles from './file-preview.module.css'
import type { FileBody } from './types'
import { pickViewer, type ViewerKind } from './viewers/dispatcher'
import { TooLargeView } from './viewers/too-large-view'

// Lazy-load the heavyweight viewers. Monaco is ~3 MB even after
// tree-shaking; drawio's iframe is cheaper but still pulls in some
// CSS. The markdown / image viewers are tiny but lazy-loading them
// keeps the dispatcher uniform — no special-case branches.
const MarkdownView = lazy(() =>
  import('./viewers/markdown-view').then((m) => ({ default: m.MarkdownView })),
)
const MonacoView = lazy(() =>
  import('./viewers/monaco-view').then((m) => ({ default: m.MonacoView })),
)
const ImageView = lazy(() =>
  import('./viewers/image-view').then((m) => ({ default: m.ImageView })),
)
const DrawioView = lazy(() =>
  import('./viewers/drawio-view').then((m) => ({ default: m.DrawioView })),
)

interface Stat {
  path: string
  size: number
  mime: string
  isBinary: boolean
}

interface Props {
  apiBase: string
  path: string
  /** 1-based line to scroll to and briefly highlight (for grep results). */
  lineNum?: number
}

const DEFAULT_PREVIEW_MAX_BYTES = 10 * 1024 * 1024

export function FilePreview({ apiBase, path, lineNum }: Props) {
  const previewMaxBytes = usePalmuxStore(
    (s) => s.globalSettings.previewMaxBytes ?? DEFAULT_PREVIEW_MAX_BYTES,
  )

  const [stat, setStat] = useState<Stat | null>(null)
  const [body, setBody] = useState<FileBody | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  // Step 1: stat the file (size + MIME) without loading the body. This
  // lets us decide whether to bother fetching at all (the too-large
  // case skips the body fetch entirely).
  useEffect(() => {
    let cancelled = false
    setStat(null)
    setBody(null)
    setError(null)
    setLoading(true)
    ;(async () => {
      try {
        const data = await api.get<Stat>(
          `${apiBase}/raw?path=${encodeURIComponent(path)}&stat=1`,
        )
        if (!cancelled) setStat(data)
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err))
          setLoading(false)
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [apiBase, path])

  // Decide which viewer kind we want, based purely on the stat result.
  // The decision is stable across re-renders so the lazy-import dance
  // doesn't toggle between viewers as the body streams in.
  const viewerKind: ViewerKind | null = useMemo(() => {
    if (!stat) return null
    return pickViewer({
      path: stat.path,
      size: stat.size,
      mime: stat.mime,
      maxBytes: previewMaxBytes,
    })
  }, [stat, previewMaxBytes])

  // Step 2: fetch the body — but only when the chosen viewer needs it.
  // Raster images load via `<img src=…>` directly so we don't waste
  // bandwidth here; SVG / Markdown / Monaco / Drawio all need the
  // text body.
  useEffect(() => {
    if (!stat || !viewerKind) return
    if (viewerKind === 'too-large') {
      setLoading(false)
      return
    }
    // Raster images: viewer fetches via <img>, we don't need body.
    const isSvg = stat.mime === 'image/svg+xml' || stat.path.toLowerCase().endsWith('.svg')
    if (viewerKind === 'image' && !isSvg) {
      setLoading(false)
      return
    }
    let cancelled = false
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
  }, [apiBase, path, stat, viewerKind])

  if (error) return <p className={styles.error}>{error}</p>
  if (!stat || !viewerKind) {
    return <p className={styles.placeholder}>{loading ? 'Loading…' : ''}</p>
  }

  // Render the chosen viewer. Each receives the same props envelope so
  // adding a new viewer is one-liner-cheap from here.
  return (
    <div className={styles.wrap} data-testid="file-preview" data-viewer={viewerKind}>
      <header className={styles.header}>
        <span className={styles.path}>{stat.path}</span>
        <span className={styles.meta}>
          {fmtBytes(stat.size)} · {stat.mime || 'unknown'}
        </span>
      </header>
      <div className={styles.body}>
        <Suspense fallback={<p className={styles.placeholder}>Loading viewer…</p>}>
          {viewerKind === 'too-large' && (
            <TooLargeView path={stat.path} size={stat.size} maxBytes={previewMaxBytes} />
          )}
          {viewerKind === 'markdown' && (
            <MarkdownView apiBase={apiBase} path={path} body={body} lineNum={lineNum} />
          )}
          {viewerKind === 'image' && (
            <ImageView apiBase={apiBase} path={path} body={body} />
          )}
          {viewerKind === 'monaco' && (
            <MonacoView apiBase={apiBase} path={path} body={body} lineNum={lineNum} />
          )}
          {viewerKind === 'drawio' && (
            <DrawioView apiBase={apiBase} path={path} body={body} />
          )}
        </Suspense>
      </div>
      {body?.truncated && (
        <p className={styles.truncated}>
          File truncated. Open in your editor for the full contents.
        </p>
      )}
    </div>
  )
}

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`
  return `${(n / 1024 / 1024).toFixed(1)} MiB`
}
