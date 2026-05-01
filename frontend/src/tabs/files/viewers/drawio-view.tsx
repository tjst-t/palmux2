// DrawioView — embedded read-only diagram viewer (S010).
//
// We host the drawio webapp ourselves under `/static/drawio/` (see
// `internal/static/drawio/README.md`) so the Files tab works offline
// and stays inside the VISION "self-hosted, no external CDN" mandate.
// The iframe boots the standard drawio embed protocol and waits for
// `event:'init'` before posting the file body via `action:'load'` with
// `editable:0`, which puts drawio into chromeless read-only mode.
//
// `.drawio.svg` and `.drawio.png` files carry the XML in metadata
// (drawio embeds it on save). The simplest robust approach: hand the
// full body to drawio and let it decide which path to take. For pure
// `.drawio` (XML) files this is the file content; for `.drawio.svg`
// the body fetched from the Files /raw endpoint is the SVG with the
// embedded `<mxfile>` element drawio extracts.

import { useEffect, useRef, useState } from 'react'

import styles from './drawio-view.module.css'
import type { ViewerProps } from './types'

// drawio embed mode — `embed=1` selects the postMessage protocol,
// `proto=json` opts into JSON-encoded messages (the default is
// XML-tagged, which is harder to parse), `chrome=0` hides the toolbar,
// `spin=1` shows drawio's own loading spinner while the file loads.
// We host the webapp under `/static/drawio/`, so the SPA's same-origin
// security boundary still applies.
const DRAWIO_SRC = '/static/drawio/?embed=1&proto=json&chrome=0&spin=1&libraries=0&nav=0'

export function DrawioView({ body, path }: ViewerProps) {
  const iframeRef = useRef<HTMLIFrameElement | null>(null)
  const [ready, setReady] = useState(false)

  useEffect(() => {
    function onMessage(e: MessageEvent) {
      // Same-origin: e.origin matches window.origin (we ship /static
      // from the same host as the SPA). Bail otherwise to avoid
      // accepting messages from third-party iframes.
      if (e.origin !== window.location.origin) return
      if (typeof e.data !== 'string' || !iframeRef.current) return
      let msg: { event?: string }
      try {
        msg = JSON.parse(e.data)
      } catch {
        return
      }
      if (msg.event === 'init') {
        // Push XML / SVG body. `editable:0` selects read-only mode —
        // drawio hides the edit chrome, disables saves, and treats
        // postMessage `save` events as no-ops.
        iframeRef.current.contentWindow?.postMessage(
          JSON.stringify({
            action: 'load',
            xml: body?.content ?? '',
            autosave: 0,
            editable: 0,
            modified: 'unsavedChanges',
          }),
          window.location.origin,
        )
        setReady(true)
      }
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [body?.content])

  return (
    <div className={styles.wrap} data-testid="drawio-view">
      <div className={styles.relWrap}>
        <iframe
          ref={iframeRef}
          title={`drawio: ${path}`}
          src={DRAWIO_SRC}
          className={styles.frame}
          // sandbox allows scripts (drawio is a JS app) and same-origin
          // (it loads styles / resources from the same host). It does
          // *not* allow top-level navigation, popups, or forms — there
          // is no flow inside drawio that would need those in the
          // read-only embed.
          sandbox="allow-scripts allow-same-origin"
        />
        {!ready && <p className={styles.placeholder}>Loading drawio viewer…</p>}
      </div>
    </div>
  )
}
