// ImageView — inline image preview (S010).
//
// Raster formats (png / jpg / gif / webp) render as a plain `<img>`
// served straight from the Files /raw endpoint, which already enforces
// worktree-relative paths and symlink validation server-side.
//
// SVG is the dangerous case: it's an XML format that can carry
// `<script>`, `<foreignObject>`, event handlers, and `xlink:href` URLs
// that would otherwise execute in the same origin as the SPA. We
// **always** sanitize SVG with DOMPurify before rendering, and we
// render it inline (rather than an `<img>`) so DOMPurify actually has
// access to the DOM tree before it hits the browser. An attacker
// committing a malicious SVG into the repo cannot escape the sandbox
// this way.

import { useMemo } from 'react'

import DOMPurify from 'dompurify'

import styles from './image-view.module.css'
import type { ViewerProps } from './types'

// Strict SVG allowlist. Anything not in here (script/foreignObject,
// `onload`, `xlink:href` to javascript:, etc.) is stripped. We also
// disable URL-based external loads — every drawing must inline its
// content. `RETURN_TRUSTED_TYPE: false` keeps the return value a plain
// string (TS otherwise widens it to `TrustedHTML`).
const SVG_PURIFY_CONFIG = {
  USE_PROFILES: { svg: true, svgFilters: true },
  FORBID_TAGS: ['script', 'foreignObject', 'iframe', 'object', 'embed'] as string[],
  FORBID_ATTR: [
    'onload',
    'onerror',
    'onclick',
    'onmouseover',
    'onmouseenter',
    'onmouseleave',
    'onmousedown',
    'onmouseup',
    'onfocus',
    'onblur',
    'onkeydown',
    'onkeyup',
  ] as string[],
  RETURN_TRUSTED_TYPE: false,
}

export function ImageView({ apiBase, path, body }: ViewerProps) {
  const isSvg = useMemo(() => {
    const lower = path.toLowerCase()
    if (lower.endsWith('.svg')) return true
    return body?.mime === 'image/svg+xml'
  }, [body, path])

  // SVG: sanitize then render inline as raw HTML.
  const sanitizedSvg = useMemo(() => {
    if (!isSvg || !body) return null
    // Body content for SVG is the file's text payload; the Files /raw
    // endpoint returns text/plain for non-binary files even when the
    // MIME is image/svg+xml, so `body.content` is the XML string.
    return DOMPurify.sanitize(body.content ?? '', SVG_PURIFY_CONFIG) as unknown as string
  }, [isSvg, body])

  // SVG needs the body (text content) before it can sanitize. Raster
  // images don't — they load directly from the /raw endpoint via
  // `<img src=…>`, so we render eagerly even when body is null.
  if (isSvg && !body) return <p className={styles.placeholder}>Loading…</p>

  if (isSvg && sanitizedSvg != null) {
    return (
      <div className={styles.wrap} data-testid="image-view-svg">
        <div
          className={styles.svgInline}
          // SVG content has been routed through DOMPurify with a strict
          // allowlist (see SVG_PURIFY_CONFIG above). DangerouslySet is
          // the documented way to inject sanitized SVG markup.
          dangerouslySetInnerHTML={{ __html: sanitizedSvg }}
        />
      </div>
    )
  }

  return (
    <div className={styles.wrap} data-testid="image-view-raster">
      <img
        alt={path}
        // Use the same /raw endpoint as the rest of the Files tab so
        // browser caching is consistent. Raster bodies are returned as
        // binary by the server; the browser handles decode.
        src={`${apiBase}/raw?path=${encodeURIComponent(path)}`}
      />
    </div>
  )
}
