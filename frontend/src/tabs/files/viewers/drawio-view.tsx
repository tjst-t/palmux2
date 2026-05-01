// DrawioView — embedded diagram viewer + S011 editor.
//
// We host the drawio webapp ourselves under `/static/drawio/` (see
// `internal/static/drawio/README.md`) so the Files tab works offline
// and stays inside the VISION "self-hosted, no external CDN" mandate.
// The iframe boots the standard drawio embed protocol and waits for
// `event:'init'` before posting the file body via `action:'load'`.
//
// S010 (view): `editable:0` puts drawio into chromeless read-only mode
// (`chrome=0` in the URL hides the toolbar).
//
// S011 (edit): `editable:1` plus `chrome=1` brings up the full drawio
// UI (palettes, toolbar, properties panel). drawio emits postMessage
// events on the user's edit / save:
//
//   - `event:'autosave'` (or model-changed) → we treat the embedded XML
//     as the draft and let the editor-store mark the file dirty.
//   - `event:'save'`     → user hit Cmd/Ctrl+S inside drawio, or
//     clicked the Save button in the chrome. We surface this to the
//     parent via `onSave`, which triggers the same PUT/If-Match flow
//     as Monaco.
//
// `.drawio.svg` and `.drawio.png` files carry the XML in metadata
// (drawio embeds it on save). The simplest robust approach: hand the
// full body to drawio and let it decide which path to take. For pure
// `.drawio` (XML) files this is the file content; for `.drawio.svg`
// the body fetched from the Files /raw endpoint is the SVG with the
// embedded `<mxfile>` element drawio extracts.

import { useCallback, useEffect, useRef, useState } from 'react'

import styles from './drawio-view.module.css'
import type { ViewerProps } from './types'

// drawio embed URL for the read-only path. `embed=1` selects the
// postMessage protocol; `proto=json` opts into JSON-encoded messages
// (the default is XML-tagged and harder to parse). `chrome=0` hides
// the toolbar; `spin=1` shows drawio's own loading spinner.
const DRAWIO_VIEW_SRC =
  '/static/drawio/?embed=1&proto=json&chrome=0&spin=1&libraries=0&nav=0'
// Edit mode: full drawio chrome + libraries + autosave hook so we get
// `autosave` events as the user mutates the diagram.
const DRAWIO_EDIT_SRC =
  '/static/drawio/?embed=1&proto=json&chrome=1&spin=1&libraries=1&nav=1&saveAndExit=0&noSaveBtn=0'

interface Props extends ViewerProps {
  /** S011: 'view' (read-only) or 'edit' (full drawio chrome). */
  mode?: 'view' | 'edit'
  /** S011: dirty signal from drawio's autosave events. */
  onDraft?: (xml: string) => void
  /** S011: drawio fired its `save` event — parent should PUT the
   *  current draft to disk. */
  onSave?: () => void
}

export function DrawioView({ body, path, mode = 'view', onDraft, onSave }: Props) {
  const iframeRef = useRef<HTMLIFrameElement | null>(null)
  const [ready, setReady] = useState(false)

  // Stable refs so the message handler captures the latest props
  // without re-binding (and re-loading drawio) on every render.
  const onDraftRef = useRef(onDraft)
  const onSaveRef = useRef(onSave)
  onDraftRef.current = onDraft
  onSaveRef.current = onSave

  const editing = mode === 'edit'

  useEffect(() => {
    function onMessage(e: MessageEvent) {
      // Same-origin: e.origin matches window.origin (we ship /static
      // from the same host as the SPA). Bail otherwise to avoid
      // accepting messages from third-party iframes.
      if (e.origin !== window.location.origin) return
      if (typeof e.data !== 'string' || !iframeRef.current) return
      let msg: { event?: string; xml?: string }
      try {
        msg = JSON.parse(e.data)
      } catch {
        return
      }
      if (msg.event === 'init') {
        // Push XML / SVG body. `editable:1` enables the editor; drawio
        // will then emit `autosave` and `save` events on user actions.
        iframeRef.current.contentWindow?.postMessage(
          JSON.stringify({
            action: 'load',
            xml: body?.content ?? '',
            autosave: editing ? 1 : 0,
            editable: editing ? 1 : 0,
            modified: 'unsavedChanges',
          }),
          window.location.origin,
        )
        setReady(true)
        return
      }
      if (msg.event === 'autosave' && msg.xml != null) {
        // drawio re-emits the full XML on every model change. Forward
        // it to the parent as the latest draft so the dirty badge
        // lights up and the next Save sends the right content.
        onDraftRef.current?.(msg.xml)
        return
      }
      if (msg.event === 'save') {
        // The drawio chrome's Save button (and Cmd/Ctrl+S inside the
        // iframe) emits this event. The XML is in `msg.xml` — capture
        // it as the draft *and* trigger the actual file save.
        if (msg.xml != null) onDraftRef.current?.(msg.xml)
        onSaveRef.current?.()
        return
      }
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [body?.content, editing])

  // S011-2-3: forward the host page's Ctrl+S / Cmd+S into drawio. The
  // drawio chrome catches the shortcut on its own when focused, but
  // when the user clicks outside the iframe the host page eats it
  // first. We listen at the document level *only when this iframe is
  // visible* and post the save action into drawio's protocol.
  const triggerSave = useCallback(() => {
    iframeRef.current?.contentWindow?.postMessage(
      JSON.stringify({ action: 'save' }),
      window.location.origin,
    )
  }, [])

  useEffect(() => {
    if (!editing) return
    function onKey(e: KeyboardEvent) {
      const isSaveCombo = (e.ctrlKey || e.metaKey) && (e.key === 's' || e.key === 'S')
      if (!isSaveCombo) return
      // Only intercept if the iframe is on screen — drawio inside its
      // own document handles its own focus path.
      if (!iframeRef.current) return
      e.preventDefault()
      e.stopPropagation()
      triggerSave()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [editing, triggerSave])

  return (
    <div className={styles.wrap} data-testid="drawio-view" data-mode={mode}>
      <div className={styles.relWrap}>
        <iframe
          ref={iframeRef}
          title={`drawio: ${path}`}
          src={editing ? DRAWIO_EDIT_SRC : DRAWIO_VIEW_SRC}
          className={styles.frame}
          // sandbox allows scripts (drawio is a JS app) and same-origin
          // (it loads styles / resources from the same host). It does
          // *not* allow top-level navigation, popups, or forms — there
          // is no flow inside drawio that would need those in the
          // embed.
          sandbox="allow-scripts allow-same-origin"
        />
        {!ready && <p className={styles.placeholder}>Loading drawio viewer…</p>}
      </div>
    </div>
  )
}
