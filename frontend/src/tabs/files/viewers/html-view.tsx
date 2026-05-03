// HtmlView — rendered HTML preview inside a sandboxed iframe (S026).
//
// Pre-S026 the Files tab routed `.html` / `.htm` through MonacoView so
// users only saw the source. S026 turns those extensions into a
// real-render-in-an-iframe path so a developer can edit + Save and
// immediately see CSS / JS / image changes the same way the browser
// would render the file standalone — without standing up a static
// server or opening a separate tab.
//
// Security model (the load-bearing piece of this component):
//
//   1. `sandbox="allow-scripts"` — and DELIBERATELY **not**
//      `allow-same-origin`. The browser then treats the iframe as a
//      unique opaque origin: the iframe's JS cannot read palmux2's
//      cookies / localStorage, and `fetch('/api/...')` from inside the
//      iframe is a cross-origin request that hits palmux2 *without*
//      the session cookie (so it 401s) and gets blocked by the
//      browser's CORS policy on top.
//   2. `Content-Security-Policy` from the server (set in
//      `internal/tab/files/handler.go::rawCSP`) gives a defense-in-
//      depth second rail. Even if a future change accidentally relaxed
//      the iframe sandbox, the document-level CSP still pins the
//      script / connect / img sources to the iframe's own opaque
//      origin.
//
// The iframe `src` points at the same `/files/raw` endpoint the
// dispatcher uses to read source — but with no `Accept` header, so the
// server's content-negotiation path (S026) returns the raw HTML body
// with `Content-Type: text/html` instead of the JSON envelope. Relative
// `<link href="style.css">` / `<script src="app.js">` resolve against
// the iframe's URL and pull through the same endpoint, served with the
// matching MIME type.
//
// Cache-busting: when the user saves the file in Source mode, the
// parent component bumps the `cacheBust` prop. We append it as
// `?_=<n>` to the iframe `src` so the browser refetches and the
// preview reflects the new contents immediately.
//
// Error handling: if the iframe fails to load (network error, server
// 5xx) we surface the failure via `onLoadError`. The dispatcher uses
// that to swap back to Source mode + show a banner.

import { useEffect, useMemo, useRef } from 'react'

import styles from './html-view.module.css'

export interface HtmlViewProps {
  /** Per-branch Files-API base, e.g.
   *  `/api/repos/<r>/branches/<b>/files`. */
  apiBase: string
  /** Worktree-relative path of the HTML file. */
  path: string
  /** Bumped each time the user saves; appended as `?_=<n>` to the
   *  iframe `src` so the browser refetches the document. */
  cacheBust: number
  /** Called when the iframe fires a `load` error or the parent's
   *  preflight fetch detects a non-OK response. The dispatcher uses
   *  this signal to fall back to Source mode. */
  onLoadError?: (reason: string) => void
}

/** Build the iframe URL.
 *
 *  We hit `/files/preview/<path>` rather than the dispatcher's
 *  `/files/raw?path=<path>` because the iframe needs **relative URL
 *  resolution to work**. With `?path=`, a `<link href="style.css">`
 *  inside the loaded HTML would resolve against the *query string*
 *  (clobbering it), producing a wrong file. The path-based endpoint
 *  puts the worktree path in the URL path itself, so the browser's
 *  default relative resolution lines up:
 *
 *    iframe.src = '.../files/preview/preview/index.html'
 *    <link href="style.css"> → '.../files/preview/preview/style.css'
 *
 *  Cache-bust still rides as `?_=<n>` — a query param the server
 *  ignores but the browser uses as part of the cache key.
 */
function buildSrc(apiBase: string, path: string, cacheBust: number): string {
  // Encode each segment individually so embedded slashes / spaces are
  // safe; do NOT encode the path separators themselves.
  const segs = path.split('/').filter(Boolean).map(encodeURIComponent)
  const url = `${apiBase}/preview/${segs.join('/')}`
  return cacheBust > 0 ? `${url}?_=${cacheBust}` : url
}

export function HtmlView({ apiBase, path, cacheBust, onLoadError }: HtmlViewProps) {
  const src = useMemo(() => buildSrc(apiBase, path, cacheBust), [apiBase, path, cacheBust])
  const iframeRef = useRef<HTMLIFrameElement | null>(null)

  // The browser's `iframe.onerror` only fires for network-level
  // failures (very rare in same-origin practice). Server-side 4xx /
  // 5xx still trigger an `onload` event with a populated document, so
  // we preflight via fetch to catch those — the dispatcher should
  // know to fall back to Source mode if the file isn't readable.
  useEffect(() => {
    if (!onLoadError) return
    let cancelled = false
    const ctrl = new AbortController()
    ;(async () => {
      try {
        // Use HEAD-style probe via a tiny GET (the server doesn't
        // implement HEAD on /files/raw and we don't need the body —
        // but the request is cheap relative to the iframe document
        // load that's about to happen anyway).
        const res = await fetch(src, {
          credentials: 'include',
          signal: ctrl.signal,
          // No Accept header so we exercise the same branch the iframe
          // will hit. (Default browser Accept varies; the server only
          // checks for `application/json`.)
        })
        if (cancelled) return
        if (!res.ok) {
          onLoadError(`preflight ${res.status} ${res.statusText}`)
        }
      } catch (err) {
        if (cancelled) return
        if ((err as Error)?.name === 'AbortError') return
        onLoadError(err instanceof Error ? err.message : String(err))
      }
    })()
    return () => {
      cancelled = true
      ctrl.abort()
    }
  }, [src, onLoadError])

  return (
    <div className={styles.wrap} data-testid="html-view" data-cache-bust={cacheBust}>
      <iframe
        ref={iframeRef}
        // Sandbox WITHOUT allow-same-origin (intentional, security-
        // critical). `allow-scripts` lets the rendered page run its
        // own JS; `allow-forms` lets demos with forms submit. We
        // deliberately omit `allow-popups`, `allow-modals`,
        // `allow-top-navigation`, `allow-pointer-lock`, etc. Adding
        // any of those — especially `allow-same-origin` — would
        // re-open the door to palmux2 session theft.
        sandbox="allow-scripts allow-forms"
        src={src}
        title={`HTML preview: ${path}`}
        className={styles.frame}
        data-testid="html-view-iframe"
        // referrerPolicy=no-referrer ensures the iframe doesn't leak
        // the parent palmux2 URL to any external `<img src=...>` /
        // `<script src=https://...>` pulls inside the document.
        referrerPolicy="no-referrer"
        onError={() => onLoadError?.('iframe load error')}
      />
    </div>
  )
}
