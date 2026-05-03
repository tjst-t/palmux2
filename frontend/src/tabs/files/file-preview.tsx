// File-preview dispatcher (S010 + S011 editor wiring).
//
// Pre-S010 this file was a self-contained Markdown / image / drawio
// renderer. Then S010 turned it into a **viewer dispatcher**: it picks
// a viewer kind from path + size + MIME and lazy-loads the matching
// component, with a size gate that skips the body fetch entirely for
// files above `previewMaxBytes`.
//
// S011 adds *editing* on top:
//
//   - Capture the server's `ETag` header on every GET so we can drive
//     `If-Match` on the next PUT.
//   - Render an Edit / Save button pair in the preview header (only for
//     viewer kinds we can edit — Monaco and Drawio).
//   - Wire Monaco's onChange / onSave (and Drawio's postMessage save)
//     to the editor-store, which is keyed by `{repoId, branchId, path}`.
//   - Surface a conflict dialog when `PUT /raw` comes back 412.
//
// The dirty / mode / etag state lives in `useEditorStore` (not in
// component state) so tab switches inside the same branch don't lose
// drafts and the FileList can render dirty badges.

import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'

import { ApiError, api } from '../../lib/api'
import { selectBranchById } from '../../stores/palmux-store'
import {
  hasAnyDirty,
  isDirty,
  makeEditorKey,
  useEditorStore,
} from '../../stores/editor-store'
import { usePalmuxStore } from '../../stores/palmux-store'

import { ConflictDialog } from './conflict-dialog'
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
const HtmlView = lazy(() =>
  import('./viewers/html-view').then((m) => ({ default: m.HtmlView })),
)

interface Stat {
  path: string
  size: number
  mime: string
  isBinary: boolean
}

interface Props {
  apiBase: string
  repoId: string
  branchId: string
  path: string
  /** 1-based line to scroll to and briefly highlight (for grep results). */
  lineNum?: number
}

const DEFAULT_PREVIEW_MAX_BYTES = 10 * 1024 * 1024

/** Editable viewer kinds (S011 + S011-fix-1 + S026).
 *
 *  - `monaco` / `drawio`: inline editors — original S011 scope.
 *  - `markdown`: rendered preview by default; toggling Edit swaps in
 *    MonacoView with `language=markdown` so users can edit the raw
 *    source. The save path (PUT /files/raw + If-Match) is shared
 *    with monaco/drawio. (S011-fix-1, regression discovered after
 *    the original S011 implementation hard-coded markdown to
 *    read-only — see docs/sprint-logs/S011-fix-1/decisions.md.)
 *  - `html`: rendered iframe preview by default; toggling to Source
 *    swaps in MonacoView with `language=html` so users can edit. Save
 *    path is shared with monaco/drawio/markdown. (S026.)
 *  - `image`: read-only; inline image editing stays in the backlog.
 */
function isEditable(kind: ViewerKind): boolean {
  return (
    kind === 'monaco' ||
    kind === 'drawio' ||
    kind === 'markdown' ||
    kind === 'html'
  )
}

export function FilePreview({ apiBase, repoId, branchId, path, lineNum }: Props) {
  const previewMaxBytes = usePalmuxStore(
    (s) => s.globalSettings.previewMaxBytes ?? DEFAULT_PREVIEW_MAX_BYTES,
  )

  // S013: navigate into the Git tab's File History / Blame sub-views.
  // We resolve the Git tab's id from the open branch's tabSet so the
  // URL stays correct even if a future patch renames the singleton.
  const navigate = useNavigate()
  const gitTabId = usePalmuxStore((s) => {
    const branch = selectBranchById(repoId, branchId)(s)
    return branch?.tabSet.tabs.find((t) => t.type === 'git')?.id ?? 'git'
  })
  const gotoGitHistory = useCallback(() => {
    navigate(
      `/${encodeURIComponent(repoId)}/${encodeURIComponent(branchId)}/${encodeURIComponent(gitTabId)}?fileHistory=${encodeURIComponent(path)}`,
    )
  }, [navigate, repoId, branchId, gitTabId, path])
  const gotoGitBlame = useCallback(() => {
    navigate(
      `/${encodeURIComponent(repoId)}/${encodeURIComponent(branchId)}/${encodeURIComponent(gitTabId)}?blame=${encodeURIComponent(path)}`,
    )
  }, [navigate, repoId, branchId, gitTabId, path])

  const editorKey = useMemo(() => makeEditorKey(repoId, branchId, path), [repoId, branchId, path])
  const entry = useEditorStore((s) => s.entries[editorKey])
  const setMode = useEditorStore((s) => s.setMode)
  const setEtag = useEditorStore((s) => s.setEtag)
  const setPristine = useEditorStore((s) => s.setPristine)
  const setDraft = useEditorStore((s) => s.setDraft)
  const clearDraft = useEditorStore((s) => s.clearDraft)
  const setSaveError = useEditorStore((s) => s.setSaveError)
  const setConflict = useEditorStore((s) => s.setConflict)
  const forget = useEditorStore((s) => s.forget)

  const mode = entry?.mode ?? 'view'
  const dirty = useEditorStore((s) => isDirty(s, editorKey))
  const conflict = entry?.conflict
  const saveError = entry?.saveError

  const [stat, setStat] = useState<Stat | null>(null)
  const [body, setBody] = useState<FileBody | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)

  // S026: HTML viewer maintains a Source / Preview toggle. The default
  // is `preview` for `.html` / `.htm` (rendered iframe) and `source`
  // for everything else (current behavior — Monaco). We track this in
  // local state because the toggle is purely a UI concern; we do not
  // pushState it (URL changes only when navigating to a new file).
  const [htmlViewMode, setHtmlViewMode] = useState<'preview' | 'source'>('preview')
  // Reset the toggle whenever the path changes so opening a new HTML
  // file always starts in the configured default (preview).
  useEffect(() => {
    setHtmlViewMode('preview')
  }, [path])

  // S026: cache-bust counter. Bumped on every successful Save so the
  // HtmlView appends `?_=<n>` to the iframe `src` and the browser
  // refetches the document with its CSS / JS / image siblings.
  const [cacheBust, setCacheBust] = useState(0)

  // S026: load-error banner. Set when the iframe / preflight fetch
  // reports failure; cleared when the user manually toggles back to
  // Preview or opens a different file.
  const [previewError, setPreviewError] = useState<string | null>(null)
  useEffect(() => {
    setPreviewError(null)
  }, [path])

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
  // text body. We capture the server's ETag header for `If-Match` on
  // PUT (S011-1-2).
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
        const res = await fetch(
          `${apiBase}/raw?path=${encodeURIComponent(path)}`,
          { credentials: 'include', headers: { Accept: 'application/json' } },
        )
        if (!res.ok) {
          throw new Error(`${res.status} ${res.statusText}`)
        }
        const etag = res.headers.get('ETag') ?? undefined
        const data = (await res.json()) as FileBody
        if (cancelled) return
        setBody(data)
        // Seed editor store with pristine + etag so on-edit dirty
        // detection works (and the next PUT can send If-Match).
        if (isEditable(viewerKind) && data.content != null) {
          setPristine(editorKey, data.content, etag)
        } else if (etag) {
          setEtag(editorKey, etag)
        }
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [apiBase, path, stat, viewerKind, editorKey, setPristine, setEtag])

  // Cleanup the editor entry when the user navigates away from the
  // preview entirely (component unmount). We deliberately do NOT clear
  // it on path-change here — the dirty buffer should survive switching
  // files inside the same Files tab.
  useEffect(() => {
    return () => {
      // Only forget if there's no unsaved draft. Drafts are kept until
      // the user discards or saves.
      const cur = useEditorStore.getState().entries[editorKey]
      if (!cur) return
      if (cur.draft != null && cur.pristine != null && cur.draft !== cur.pristine) return
      forget(editorKey)
    }
  }, [editorKey, forget])

  /** Save handler — performs the actual PUT and routes 412 to the
   *  conflict dialog. `overrideEtag` is used by the Overwrite branch of
   *  the conflict resolution: we resend the local draft against the
   *  newly reported server ETag. */
  const doSave = useCallback(
    async (overrideEtag?: string) => {
      const cur = useEditorStore.getState().entries[editorKey]
      if (!cur) return
      const ifMatch = overrideEtag ?? cur.etag
      const content = cur.draft ?? cur.pristine ?? ''
      if (!ifMatch) {
        setSaveError(editorKey, 'no ETag captured — reload the file before saving')
        return
      }
      setSaveError(editorKey, undefined)
      setSaving(true)
      try {
        const res = await fetch(
          `${apiBase}/raw?path=${encodeURIComponent(path)}`,
          {
            method: 'PUT',
            credentials: 'include',
            headers: {
              'Content-Type': 'application/json',
              'If-Match': ifMatch,
            },
            body: JSON.stringify({ content }),
          },
        )
        if (res.status === 412) {
          const data = await res.json().catch(() => ({}))
          const serverEtag: string =
            data?.currentEtag ?? res.headers.get('ETag') ?? ''
          setConflict(editorKey, { serverEtag, localContent: content })
          throw new ApiError(412, 'precondition failed')
        }
        if (!res.ok) {
          const data = await res.json().catch(() => ({}))
          throw new ApiError(res.status, data?.error ?? `${res.status} ${res.statusText}`)
        }
        const data = (await res.json()) as { etag: string; size: number }
        // Save succeeded: the new content becomes the pristine baseline,
        // dirty state clears, conflict dialog (if any) closes.
        setPristine(editorKey, content, data.etag)
        clearDraft(editorKey)
        setConflict(editorKey, undefined)
        // S011-fix-1: keep the local `body` (used by MarkdownView /
        // ImageView re-render paths) in sync with what we just wrote
        // to disk. Without this, toggling MD back to view-mode after
        // a save shows the *pre-save* rendered content because
        // MarkdownView consumes `body.content` directly.
        setBody((prev) => (prev ? { ...prev, content, size: data.size } : prev))
        // S026: bump cache-bust so when the user toggles back to
        // Preview the iframe refetches with the new content.
        setCacheBust((n) => n + 1)
      } catch (err) {
        if (err instanceof ApiError && err.status === 412) {
          // Already routed to the conflict dialog above.
        } else {
          setSaveError(editorKey, err instanceof Error ? err.message : String(err))
        }
      } finally {
        setSaving(false)
      }
    },
    [apiBase, path, editorKey, setSaveError, setConflict, setPristine, clearDraft],
  )

  /** Reload the file from disk, dropping any local draft. Used by the
   *  conflict dialog's Reload button and by the Edit-toggle confirm. */
  const doReload = useCallback(async () => {
    try {
      const res = await fetch(
        `${apiBase}/raw?path=${encodeURIComponent(path)}`,
        { credentials: 'include', headers: { Accept: 'application/json' } },
      )
      if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
      const etag = res.headers.get('ETag') ?? undefined
      const data = (await res.json()) as FileBody
      setBody(data)
      if (data.content != null) {
        setPristine(editorKey, data.content, etag)
      }
      clearDraft(editorKey)
      setConflict(editorKey, undefined)
    } catch (err) {
      setSaveError(editorKey, err instanceof Error ? err.message : String(err))
    }
  }, [apiBase, path, editorKey, setPristine, clearDraft, setConflict, setSaveError])

  // Handler for the Edit/View toggle button.
  const onToggleMode = useCallback(() => {
    if (mode === 'edit' && dirty) {
      const ok = window.confirm(
        'You have unsaved changes. Discard them and switch back to view mode?',
      )
      if (!ok) return
      clearDraft(editorKey)
    }
    setMode(editorKey, mode === 'edit' ? 'view' : 'edit')
  }, [editorKey, mode, dirty, setMode, clearDraft])

  // Beforeunload guard for any dirty file across the app (S011-1-8).
  useEffect(() => {
    function onBeforeUnload(e: BeforeUnloadEvent) {
      const dirtyAny = hasAnyDirty(useEditorStore.getState())
      if (!dirtyAny) return
      e.preventDefault()
      // Modern browsers ignore the message but require a string for the
      // dialog to fire.
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [])

  // The Save button needs to read the latest `dirty` reactively.
  const onSaveClick = useCallback(() => {
    if (!dirty || saving) return
    void doSave()
  }, [dirty, saving, doSave])

  // Detect mobile viewport (< 900px) for drawio gating (S011-2-6).
  const [isMobile, setIsMobile] = useState(false)
  useEffect(() => {
    function update() {
      setIsMobile(window.innerWidth < 900)
    }
    update()
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])

  // Reuse a stable handler ref for Monaco's onSave (closure-captured
  // from doSave / dirty / saving above). Monaco's onKeyDown listener
  // reads the latest via the editor's ref-based forwarding.
  const onMonacoSaveRef = useRef(onSaveClick)
  onMonacoSaveRef.current = onSaveClick

  if (error) return <p className={styles.error}>{error}</p>
  if (!stat || !viewerKind) {
    return <p className={styles.placeholder}>{loading ? 'Loading…' : ''}</p>
  }

  const editable = isEditable(viewerKind)
  const drawioMobileBlocked = viewerKind === 'drawio' && isMobile && mode === 'view'
  // S026: HTML files have an additional Source / Preview toggle in
  // the header. Editing is only meaningful in Source mode (the
  // rendered iframe is read-only by definition), so we disable the
  // Edit button while Preview is active and surface a tooltip.
  const isHtml = viewerKind === 'html'
  const htmlInPreview = isHtml && htmlViewMode === 'preview'
  const editBlockedReason = drawioMobileBlocked
    ? 'Drawio editing is desktop-only — open this file on a wider screen.'
    : htmlInPreview
      ? 'Switch to Source mode to edit the HTML file.'
      : 'Edit this file'
  const editBlocked = drawioMobileBlocked || htmlInPreview

  // Render the chosen viewer. Each receives the same props envelope so
  // adding a new viewer is one-liner-cheap from here.
  return (
    <div
      className={styles.wrap}
      data-testid="file-preview"
      data-viewer={viewerKind}
      data-mode={mode}
      data-dirty={dirty ? 'true' : 'false'}
      data-html-view-mode={isHtml ? htmlViewMode : undefined}
    >
      <header className={styles.header}>
        <span className={styles.path}>
          {stat.path}
          {dirty && (
            <span className={styles.dirtyDot} data-testid="dirty-indicator" title="Unsaved changes">
              {' '}
              ●
            </span>
          )}
        </span>
        <span className={styles.actions}>
          <span className={styles.meta}>
            {fmtBytes(stat.size)} · {stat.mime || 'unknown'}
          </span>
          <button
            type="button"
            className={styles.editButton}
            onClick={gotoGitHistory}
            data-testid="file-history-button"
            title="Show this file's git history"
          >
            History
          </button>
          <button
            type="button"
            className={styles.editButton}
            onClick={gotoGitBlame}
            data-testid="file-blame-button"
            title="Show git blame for this file"
          >
            Blame
          </button>
          {/* S026: Source / Preview toggle for HTML files. Lives to
              the left of the Edit button so the spatial flow reads as
              "view mode → edit mode". The toggle is purely UI state
              (no URL change), and Edit is disabled in Preview because
              the iframe is read-only by design. */}
          {isHtml && (
            <button
              type="button"
              className={styles.editButton}
              onClick={() => {
                // If the user is mid-edit when they hit Preview we
                // confirm the discard, mirroring the Edit ↔ View toggle.
                if (htmlViewMode === 'source' && mode === 'edit' && dirty) {
                  const ok = window.confirm(
                    'You have unsaved changes. Discard them and switch to Preview?',
                  )
                  if (!ok) return
                  clearDraft(editorKey)
                  setMode(editorKey, 'view')
                }
                setPreviewError(null)
                setHtmlViewMode((m) => (m === 'preview' ? 'source' : 'preview'))
              }}
              data-testid="html-mode-toggle"
              data-html-mode={htmlViewMode}
              title={
                htmlViewMode === 'preview'
                  ? 'Show HTML source (Monaco editor)'
                  : 'Show rendered HTML preview'
              }
            >
              {htmlViewMode === 'preview' ? 'Source' : 'Preview'}
            </button>
          )}
          {editable && (
            <>
              {mode === 'view' ? (
                <button
                  type="button"
                  className={styles.editButton}
                  onClick={onToggleMode}
                  data-testid="edit-button"
                  disabled={editBlocked}
                  title={editBlockedReason}
                >
                  Edit
                </button>
              ) : (
                <>
                  <button
                    type="button"
                    className={styles.cancelButton}
                    onClick={onToggleMode}
                    data-testid="cancel-edit-button"
                  >
                    Done
                  </button>
                  <button
                    type="button"
                    className={styles.saveButton}
                    onClick={onSaveClick}
                    disabled={!dirty || saving}
                    data-testid="save-button"
                    title="Save (Ctrl+S / Cmd+S)"
                  >
                    {saving ? 'Saving…' : 'Save'}
                  </button>
                </>
              )}
            </>
          )}
        </span>
      </header>
      {saveError && (
        <p className={styles.saveError} data-testid="save-error">
          Save failed: {saveError}
        </p>
      )}
      {/* S026: HTML preview load-error banner. Surfaced when the
          iframe / preflight fetch reports failure (e.g. file vanished,
          server 5xx). We auto-fallback to Source mode so the user can
          at least edit the file. */}
      {isHtml && previewError && (
        <p className={styles.saveError} data-testid="html-preview-error">
          Preview unavailable: {previewError}. Showing source instead.
        </p>
      )}
      <div className={styles.body}>
        <Suspense fallback={<p className={styles.placeholder}>Loading viewer…</p>}>
          {viewerKind === 'too-large' && (
            <TooLargeView path={stat.path} size={stat.size} maxBytes={previewMaxBytes} />
          )}
          {viewerKind === 'markdown' && mode === 'view' && (
            <MarkdownView apiBase={apiBase} path={path} body={body} lineNum={lineNum} />
          )}
          {viewerKind === 'markdown' && mode === 'edit' && (
            // S011-fix-1: editing markdown source uses Monaco with the
            // `markdown` language (not the rendered preview). Save
            // path (PUT /files/raw + If-Match) is identical to
            // monaco/drawio.
            <MonacoView
              key={`${editorKey}::md::${mode}`}
              apiBase={apiBase}
              path={path}
              body={
                entry?.draft != null && body
                  ? { ...body, content: entry.draft }
                  : body
              }
              lineNum={lineNum}
              language="markdown"
              mode={mode}
              onChange={(v) => setDraft(editorKey, v)}
              onSave={() => onMonacoSaveRef.current?.()}
            />
          )}
          {viewerKind === 'image' && (
            <ImageView apiBase={apiBase} path={path} body={body} />
          )}
          {viewerKind === 'monaco' && (
            <MonacoView
              key={`${editorKey}::${mode}`}
              apiBase={apiBase}
              path={path}
              // Hand the editor the draft (if any) when remounting so
              // edits survive tab-switch / view↔edit toggles. Otherwise
              // fall through to the pristine server content.
              body={
                entry?.draft != null && body
                  ? { ...body, content: entry.draft }
                  : body
              }
              lineNum={lineNum}
              mode={mode}
              onChange={(v) => setDraft(editorKey, v)}
              onSave={() => onMonacoSaveRef.current?.()}
            />
          )}
          {viewerKind === 'drawio' && (
            <DrawioView
              key={`${editorKey}::${mode}`}
              apiBase={apiBase}
              path={path}
              body={body}
              mode={mode}
              onDraft={(xml) => setDraft(editorKey, xml)}
              onSave={() => onMonacoSaveRef.current?.()}
            />
          )}
          {viewerKind === 'html' && htmlViewMode === 'preview' && (
            // S026: rendered preview via sandboxed iframe.
            <HtmlView
              apiBase={apiBase}
              path={path}
              cacheBust={cacheBust}
              onLoadError={(reason) => {
                // Auto-fallback to Source mode so the user can still
                // see / edit the file, with a banner explaining why.
                setPreviewError(reason)
                setHtmlViewMode('source')
              }}
            />
          )}
          {viewerKind === 'html' && htmlViewMode === 'source' && (
            // S026: source mode reuses Monaco with `html` language so
            // syntax highlighting + Save flow stay identical to other
            // editable file types (markdown / monaco / drawio).
            <MonacoView
              key={`${editorKey}::html::${mode}`}
              apiBase={apiBase}
              path={path}
              body={
                entry?.draft != null && body
                  ? { ...body, content: entry.draft }
                  : body
              }
              lineNum={lineNum}
              language="html"
              mode={mode}
              onChange={(v) => setDraft(editorKey, v)}
              onSave={() => onMonacoSaveRef.current?.()}
            />
          )}
        </Suspense>
      </div>
      {body?.truncated && (
        <p className={styles.truncated}>
          File truncated. Open in your editor for the full contents.
        </p>
      )}
      <ConflictDialog
        path={stat.path}
        open={!!conflict}
        saving={saving}
        onCancel={() => setConflict(editorKey, undefined)}
        onReload={() => {
          void doReload()
        }}
        onOverwrite={() => {
          if (!conflict) return
          void doSave(conflict.serverEtag)
        }}
      />
    </div>
  )
}

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`
  return `${(n / 1024 / 1024).toFixed(1)} MiB`
}
