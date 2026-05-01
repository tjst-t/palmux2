// Composer — the message input box at the bottom of the Claude tab.
//
// Wires three completion / convenience surfaces:
//   - `/` triggers a slash-command popup. Sources are the CLI-reported
//     commands list (from initialize) plus our two internal commands
//     (/clear, /model).
//   - `@` triggers a file mention popup that hits the Files API search.
//   - Local-file attachment via three routes (S008): the `+` button's
//     "Attach file" item opens the system file picker; drag-and-drop
//     onto the composer; clipboard paste. All three POST to the
//     per-branch /api/repos/{repoId}/branches/{branchId}/upload
//     endpoint and append the resulting absolute path either as
//     `[image: <abspath>]` (kind=image) or `@<abspath>` (other) when
//     the user submits the message.
//
// S008 removed the S006 server-side picker UI (`Add directory` /
// `Add file` items + PathPicker) — that surface has been replaced by
// the `@` autocomplete which already calls the Files API search and
// inlines `@<relpath>` in the body. The `--add-dir` plumbing on the
// backend is preserved (Manager auto-registers the per-branch
// attachment dir on every CLI spawn).

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import {
  InlineCompletionPopup,
  useInlineCompletion,
  type CompletionOption,
  type CompletionTrigger,
} from '../../components/inline-completion'
import { PillSelect, type PillSelectOption } from '../../components/pill-select'

import styles from './claude-agent-view.module.css'
import type { InitInfo, ModelDescriptor } from './types'

interface ComposerProps {
  repoId: string
  branchId: string
  /** Send the user's message body. The composer encodes attachments
   *  inline (`[image: <abspath>]` for images, `@<abspath>` otherwise)
   *  so the agent only sees text. addDirs is currently unused at the
   *  call site but kept on the type so a future feature can re-enable
   *  user-supplied --add-dir without touching every caller. */
  onSend: (content: string, addDirs?: string[]) => void
  onInterrupt: () => void
  isStreaming: boolean
  disabled: boolean
  connState: 'connecting' | 'open' | 'closed' | 'closing'
  model: string
  effort: string
  permissionMode: string
  permissionModes: string[]
  onModelChange: (model: string) => void
  onEffortChange: (effort: string) => void
  onPermissionModeChange: (mode: string) => void
  initInfo?: InitInfo
}

function modeLabel(mode: string): string {
  switch (mode) {
    case 'default':           return 'default'
    case 'acceptEdits':       return 'accept edits'
    case 'plan':              return 'plan'
    case 'auto':              return 'auto'
    case 'dontAsk':           return "don't ask"
    case 'bypassPermissions': return 'bypass'
    default:                  return mode
  }
}

const FALLBACK_MODELS: ModelDescriptor[] = [
  { value: '', displayName: 'default' },
  { value: 'sonnet', displayName: 'sonnet' },
  { value: 'opus', displayName: 'opus' },
  { value: 'haiku', displayName: 'haiku' },
]

const INTERNAL_COMMANDS: { name: string; description: string }[] = [
  { name: 'clear', description: 'Start a fresh session (drop the active session_id)' },
  { name: 'model', description: 'Switch model: /model <name>' },
]

// Attachment is one piece of context the user has added to their pending
// message. S008 generalised this to any file kind — the only branch in
// the rendering and submission paths is `kind === 'image'` (which gets
// a thumbnail and the `[image: ...]` injection) versus everything else
// (📄 chip, `@<abspath>` injection). status tracks the upload lifecycle
// so the chip can show a spinner while the POST is in flight and a
// muted error state if it failed.
interface Attachment {
  id: string
  /** Display name shown in the chip. Server returns the original
   *  filename in `originalName`; we fall back to the local File.name
   *  while the POST is pending. */
  name: string
  /** Absolute server-side filesystem path. Empty until the POST
   *  resolves. */
  path: string
  /** blob: URL for image previews; empty for non-image attachments. */
  previewUrl: string
  kind: 'image' | 'file'
  /** Resolved MIME from the server (filled after upload). Empty for
   *  pre-upload chips. */
  mime?: string
  status: 'uploading' | 'ready' | 'error'
  /** Set when status === 'error' so the chip can show a tooltip. */
  errorMessage?: string
}

let attachmentCounter = 0
function newAttachmentId(): string {
  attachmentCounter += 1
  return `a${attachmentCounter}-${Date.now().toString(36)}`
}

export function Composer(props: ComposerProps) {
  const {
    repoId,
    branchId,
    onSend,
    onInterrupt,
    isStreaming,
    disabled,
    connState,
    model,
    effort,
    permissionMode,
    permissionModes,
    onModelChange,
    onEffortChange,
    onPermissionModeChange,
    initInfo,
  } = props

  // Draft persistence: the textarea contents survive tab/branch switches
  // and full page reloads via localStorage keyed by `${repoId}/${branchId}`.
  // Only the text is persisted — attachments hold blob URLs that don't
  // round-trip safely, so they reset to empty on remount.
  const draftKey = `palmux:claude-draft:${repoId}/${branchId}`
  const [value, setValue] = useState(() => loadDraft(draftKey))
  // Re-load when the user switches branches without unmounting (rare, but
  // happens when the same Composer is reused for a different (repo, branch)).
  useEffect(() => {
    setValue(loadDraft(draftKey))
  }, [draftKey])
  // Save on every keystroke. localStorage writes are cheap; debouncing
  // adds complexity for no measurable win at typing speeds.
  useEffect(() => {
    saveDraft(draftKey, value)
  }, [draftKey, value])
  const [composing, setComposing] = useState(false)
  const [attachments, setAttachments] = useState<Attachment[]>([])
  // S008: drag-over indicator. `dragDepth` counts dragenter vs dragleave
  // to avoid flicker when the cursor crosses inner element boundaries.
  // We only show the overlay when the depth is positive AND a file is
  // actually present in the dataTransfer.
  const [dragDepth, setDragDepth] = useState(0)
  const taRef = useRef<HTMLTextAreaElement | null>(null)
  const composerRef = useRef<HTMLDivElement | null>(null)
  const fileInputRef = useRef<HTMLInputElement | null>(null)

  // Auto-grow the textarea up to a third of the viewport height; beyond
  // that, switch to internal scrolling. Re-runs whenever the content
  // (or the viewport) changes.
  useEffect(() => {
    const ta = taRef.current
    if (!ta) return
    const grow = () => {
      ta.style.height = 'auto'
      const cap = Math.max(120, Math.floor(window.innerHeight / 3))
      ta.style.height = `${Math.min(ta.scrollHeight, cap)}px`
    }
    grow()
    window.addEventListener('resize', grow)
    return () => window.removeEventListener('resize', grow)
  }, [value])

  // Optimistic state for the three pill selectors. The parent passes the
  // server-confirmed value via props, but model.set / effort.set /
  // permission_mode.set go out as fire-and-forget WS frames — there's no
  // round-trip event that updates state.model on success. So the
  // selector would visually snap back to the old value the moment the
  // user picked one. We mirror the prop into local state and let the
  // user see their choice immediately; the prop updates whenever a
  // session.init carries a new value, which we sync via effect.
  const [localModel, setLocalModel] = useState(model)
  const [localEffort, setLocalEffort] = useState(effort)
  const [localPermissionMode, setLocalPermissionMode] = useState(permissionMode)
  useEffect(() => setLocalModel(model), [model])
  useEffect(() => setLocalEffort(effort), [effort])
  useEffect(() => setLocalPermissionMode(permissionMode), [permissionMode])

  const handleModelChange = (m: string) => {
    setLocalModel(m)
    onModelChange(m)
  }
  const handleEffortChange = (e: string) => {
    setLocalEffort(e)
    onEffortChange(e)
  }
  const handlePermissionModeChange = (m: string) => {
    setLocalPermissionMode(m)
    onPermissionModeChange(m)
  }

  // Build completion triggers — recreated only when the underlying data
  // (commands list, repo/branch) changes, otherwise the inline-completion
  // state would re-trigger fetches on every keystroke.
  const cliCommands = initInfo?.commands ?? []
  const triggers: CompletionTrigger[] = useMemo(() => {
    const slashTrigger: CompletionTrigger = {
      char: '/',
      name: 'Commands',
      fetchOptions: async (q) => {
        const all: CompletionOption[] = []
        // Internal commands first.
        for (const c of INTERNAL_COMMANDS) {
          all.push({
            id: 'internal:' + c.name,
            label: '/' + c.name,
            detail: c.description,
            insertText: '/' + c.name + ' ',
          })
        }
        for (const c of cliCommands) {
          const insertText = c.argumentHint ? '/' + c.name + ' ' : '/' + c.name + ' '
          all.push({
            id: 'cli:' + c.name,
            label: '/' + c.name + (c.argumentHint ? ' ' + c.argumentHint : ''),
            detail: c.description,
            insertText,
          })
          for (const a of c.aliases ?? []) {
            all.push({
              id: 'cli:' + c.name + ':' + a,
              label: '/' + a,
              detail: c.description,
              insertText: '/' + a + ' ',
            })
          }
        }
        const ql = q.toLowerCase()
        return all
          .filter((o) => !ql || o.label.toLowerCase().includes(ql))
          .slice(0, 30)
      },
    }
    const mentionTrigger: CompletionTrigger = {
      char: '@',
      name: 'Files',
      fetchOptions: async (q, signal) => {
        const query = q.trim()
        if (!query) return []
        try {
          const url =
            `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}` +
            `/files/search?query=${encodeURIComponent(query)}`
          const res = await fetch(url, { credentials: 'include', signal })
          if (!res.ok) return []
          const data = (await res.json()) as { results?: { path: string; isDir?: boolean }[] }
          return (data.results ?? []).slice(0, 30).map((e) => ({
            id: e.path,
            label: '@' + e.path,
            detail: e.isDir ? 'directory' : '',
            insertText: '@' + e.path + ' ',
          }))
        } catch {
          return []
        }
      },
    }
    return [slashTrigger, mentionTrigger]
  }, [cliCommands, repoId, branchId])

  const completion = useInlineCompletion(triggers)

  // Keep completion state in sync with the textarea content.
  const onChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const v = e.target.value
    setValue(v)
    completion.update(v, e.target.selectionEnd)
  }

  const onSelectionMove = useCallback(() => {
    if (!taRef.current) return
    completion.update(taRef.current.value, taRef.current.selectionEnd)
  }, [completion])

  const isUploading = useMemo(
    () => attachments.some((a) => a.status === 'uploading'),
    [attachments],
  )

  const submit = () => {
    if (isStreaming || disabled) return
    if (isUploading) return
    const text = value.trim()
    const ready = attachments.filter((a) => a.status === 'ready' && a.path)
    if (!text && ready.length === 0) return
    // Build the submission payload from the chips:
    //   - image  → `[image: <abspath>]` line in the body (existing
    //              behaviour from paste / drag-drop uploads — Claude
    //              CLI vision input)
    //   - file   → `@<abspath>` reference in the body so the CLI's
    //              Read tool picks it up. The Manager auto-registered
    //              the per-branch attachment dir as `--add-dir` at
    //              spawn time, so Claude can read these absolute
    //              paths even though they live outside the worktree.
    const lines: string[] = []
    if (text) lines.push(text)
    for (const a of ready) {
      if (a.kind === 'image') {
        lines.push(`[image: ${a.path}]`)
      } else {
        lines.push(`@${a.path}`)
      }
    }
    const body = lines.filter((s) => s).join('\n')
    onSend(body)
    setValue('')
    // Free the preview URLs we created with URL.createObjectURL.
    for (const a of attachments) {
      if (a.previewUrl.startsWith('blob:')) URL.revokeObjectURL(a.previewUrl)
    }
    setAttachments([])
    completion.cancel()
  }

  const applyCompletion = (opt?: CompletionOption) => {
    if (!taRef.current) return false
    const result = completion.apply(taRef.current.value, taRef.current.selectionEnd, opt)
    if (!result) return false
    setValue(result.text)
    // Move cursor after applying. requestAnimationFrame so the DOM
    // reflects the new value before we set selection.
    requestAnimationFrame(() => {
      if (!taRef.current) return
      taRef.current.value = result.text
      taRef.current.setSelectionRange(result.cursor, result.cursor)
      taRef.current.focus()
    })
    return true
  }

  const handleKey = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (composing) return
    // Inline completion intercepts ↑↓/Enter/Tab/Esc when active.
    if (completion.handleKey(e)) {
      if (e.key === 'Enter' || e.key === 'Tab') {
        applyCompletion()
        e.preventDefault()
      }
      return
    }
    if (e.key === 'Escape' && isStreaming) {
      e.preventDefault()
      onInterrupt()
      return
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submit()
    }
  }

  // uploadFile is the unified upload pipeline (file picker / drop /
  // paste). Each call creates a chip in `uploading` state, POSTs the
  // file to the per-branch upload endpoint, and replaces the chip with
  // the server-confirmed metadata. On failure the chip flips to `error`
  // so the user can see why and retry by dropping the file again.
  const uploadFile = async (file: File) => {
    const isImage = file.type.startsWith('image/') ||
      // Some browsers don't infer MIME for clipboard images: fall back
      // to the file extension. Keeps paste-of-PNG-from-screenshot
      // working when the OS gives us application/octet-stream.
      /\.(png|jpe?g|gif|webp|bmp|svg|avif|heic)$/i.test(file.name)
    const previewUrl = isImage ? URL.createObjectURL(file) : ''
    const tempId = newAttachmentId()
    setAttachments((prev) => [
      ...prev,
      {
        id: tempId,
        name: file.name || (isImage ? 'image' : 'file'),
        path: '',
        previewUrl,
        kind: isImage ? 'image' : 'file',
        status: 'uploading',
      },
    ])
    try {
      const fd = new FormData()
      fd.append('file', file)
      const url =
        `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/upload`
      const res = await fetch(url, {
        method: 'POST',
        credentials: 'include',
        body: fd,
      })
      if (!res.ok) {
        const detail = await safeReadError(res)
        setAttachments((prev) =>
          prev.map((a) =>
            a.id === tempId
              ? { ...a, status: 'error' as const, errorMessage: detail || `HTTP ${res.status}` }
              : a,
          ),
        )
        return
      }
      const data = (await res.json()) as {
        path?: string
        name?: string
        originalName?: string
        mime?: string
        kind?: 'image' | 'file'
      }
      if (!data.path) {
        setAttachments((prev) =>
          prev.map((a) =>
            a.id === tempId ? { ...a, status: 'error' as const, errorMessage: 'no path' } : a,
          ),
        )
        return
      }
      setAttachments((prev) =>
        prev.map((a) =>
          a.id === tempId
            ? {
                ...a,
                path: data.path!,
                name: data.originalName || a.name,
                mime: data.mime,
                // Trust the server's classification when available — it
                // resolves the MIME from the multipart header, which is
                // more reliable than our file.type sniff for
                // clipboard-pasted blobs.
                kind: (data.kind as 'image' | 'file') || a.kind,
                status: 'ready' as const,
              }
            : a,
        ),
      )
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'upload failed'
      setAttachments((prev) =>
        prev.map((a) =>
          a.id === tempId ? { ...a, status: 'error' as const, errorMessage: msg } : a,
        ),
      )
    }
  }

  const removeAttachment = (id: string) => {
    setAttachments((prev) => {
      const dropped = prev.find((a) => a.id === id)
      if (dropped?.previewUrl.startsWith('blob:')) URL.revokeObjectURL(dropped.previewUrl)
      return prev.filter((a) => a.id !== id)
    })
  }

  // ────────────────── S008: paste handler ───────────────────────────
  // Process every File in clipboardData (image, text/PDF/etc.). The
  // textarea gives us first refusal: if the user has copied plain text
  // we fall through and the browser pastes it as text. If clipboard
  // contains files (images on most platforms; richer file lists on
  // Linux file managers), we upload all of them.
  const onPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const fileList = e.clipboardData?.files
    if (!fileList || fileList.length === 0) return
    const files: File[] = []
    for (let i = 0; i < fileList.length; i++) {
      const f = fileList.item(i)
      if (f) files.push(f)
    }
    if (files.length === 0) return
    e.preventDefault()
    for (const f of files) void uploadFile(f)
  }

  // ────────────────── S008: drag-and-drop ───────────────────────────
  // The drop target is the composer wrapper, not just the textarea, so
  // dropping anywhere over the input area attaches. We track depth via
  // dragenter/dragleave because crossing inner elements would otherwise
  // toggle the overlay rapidly.
  const onDragEnter = (e: React.DragEvent<HTMLDivElement>) => {
    if (!eventCarriesFile(e)) return
    e.preventDefault()
    setDragDepth((d) => d + 1)
  }
  const onDragOver = (e: React.DragEvent<HTMLDivElement>) => {
    if (!eventCarriesFile(e)) return
    // preventDefault is required so the drop event fires later.
    e.preventDefault()
    if (e.dataTransfer) {
      e.dataTransfer.dropEffect = 'copy'
    }
  }
  const onDragLeave = (e: React.DragEvent<HTMLDivElement>) => {
    if (!eventCarriesFile(e)) return
    setDragDepth((d) => Math.max(0, d - 1))
  }
  const onDrop = (e: React.DragEvent<HTMLDivElement>) => {
    setDragDepth(0)
    if (!eventCarriesFile(e)) return
    e.preventDefault()
    const files = Array.from(e.dataTransfer?.files ?? [])
    if (files.length === 0) return
    for (const f of files) void uploadFile(f)
  }

  const placeholder = disabled
    ? 'Authenticate Claude Code first'
    : 'Message Claude…  (Enter to send, Shift+Enter for newline, /, @ to autocomplete)'

  // Models — prefer CLI-reported list with effort/thinking metadata.
  const models = initInfo?.models?.length ? initInfo.models : FALLBACK_MODELS
  const currentModelDescriptor = models.find((m) => m.value === localModel)
  const effortLevels = currentModelDescriptor?.supportedEffortLevels ?? []
  const showEffort = !!currentModelDescriptor?.supportsEffort && effortLevels.length > 0

  const showDragOverlay = dragDepth > 0

  return (
    <div className={styles.composer}>
      <div
        ref={composerRef}
        className={styles.composerInner}
        style={{ position: 'relative' }}
        onDragEnter={onDragEnter}
        onDragOver={onDragOver}
        onDragLeave={onDragLeave}
        onDrop={onDrop}
        data-testid="composer-root"
      >
        <InlineCompletionPopup
          state={completion.state}
          onPick={(opt) => applyCompletion(opt)}
        />
        {showDragOverlay && (
          <div className={styles.dropOverlay} aria-hidden data-testid="composer-drop-overlay">
            <span>Drop to attach</span>
          </div>
        )}
        {attachments.length > 0 && (
          <div className={styles.attachments} data-testid="composer-attachments">
            {attachments.map((a) => (
              <div
                key={a.id}
                className={
                  a.status === 'error'
                    ? `${styles.attachment} ${styles.attachmentError}`
                    : styles.attachment
                }
                title={
                  a.status === 'error'
                    ? `${a.name} — ${a.errorMessage || 'upload failed'}`
                    : a.name
                }
                data-testid={`attachment-chip-${a.kind}`}
                data-attachment-kind={a.kind}
                data-attachment-path={a.path}
                data-attachment-status={a.status}
              >
                {a.kind === 'image' && a.previewUrl ? (
                  <img src={a.previewUrl} alt={a.name} className={styles.attachmentThumb} />
                ) : (
                  <span className={styles.attachmentFileIcon} aria-hidden>
                    {a.kind === 'image' ? '🖼️' : '📄'}
                  </span>
                )}
                <span className={styles.attachmentName}>{a.name}</span>
                {a.status === 'uploading' && (
                  <span className={styles.attachmentSpinner} aria-label="uploading">…</span>
                )}
                {a.status === 'error' && (
                  <span
                    className={styles.attachmentSpinner}
                    aria-label="upload failed"
                    title={a.errorMessage || 'upload failed'}
                  >
                    !
                  </span>
                )}
                <button
                  type="button"
                  className={styles.attachmentRemove}
                  onClick={() => removeAttachment(a.id)}
                  aria-label={`Remove ${a.name}`}
                  title="Remove"
                >
                  ×
                </button>
              </div>
            ))}
          </div>
        )}
        <textarea
          ref={taRef}
          value={value}
          onChange={onChange}
          onCompositionStart={() => setComposing(true)}
          onCompositionEnd={() => setComposing(false)}
          onKeyDown={handleKey}
          onKeyUp={onSelectionMove}
          onClick={onSelectionMove}
          onPaste={onPaste}
          placeholder={placeholder}
          rows={1}
          disabled={disabled}
        />
        <div className={styles.composerFooter}>
          {/* S008: a single hidden file input handles the "Attach
              file" item. accept="*" so any file kind is selectable;
              multiple lets the user pick several at once on platforms
              that surface that affordance. */}
          <input
            ref={fileInputRef}
            type="file"
            multiple
            style={{ display: 'none' }}
            onChange={(e) => {
              const files = Array.from(e.target.files ?? [])
              for (const f of files) void uploadFile(f)
              e.target.value = ''
            }}
            data-testid="composer-file-input"
          />
          <button
            type="button"
            className={styles.attachBtn}
            onClick={() => fileInputRef.current?.click()}
            aria-label="Attach file"
            title="Attach file (also: drag-and-drop or paste)"
            data-testid="composer-plus-btn"
            disabled={disabled}
          >
            +
          </button>

          <PillSelect
            ariaLabel="Model"
            value={localModel}
            onChange={handleModelChange}
            options={models.map<PillSelectOption>((m) => ({
              value: m.value,
              label: m.displayName ?? m.value ?? 'default',
              detail: m.description,
            }))}
          />
          {showEffort && (
            <PillSelect
              ariaLabel="Effort"
              prefix="effort"
              value={localEffort}
              onChange={handleEffortChange}
              options={[
                { value: '', label: 'default' },
                ...effortLevels.map<PillSelectOption>((lvl) => ({ value: lvl, label: lvl })),
              ]}
            />
          )}
          <PillSelect
            ariaLabel="Permission mode"
            value={localPermissionMode}
            onChange={handlePermissionModeChange}
            options={permissionModes.map<PillSelectOption>((m) => ({
              value: m,
              label: modeLabel(m),
            }))}
          />

          <span className={styles.composerFooterSpacer} />

          {isUploading && <span className={styles.connBanner}>uploading…</span>}
          {connState !== 'open' && !isUploading && (
            <span className={styles.connBanner}>{connState}…</span>
          )}

          {isStreaming ? (
            <button
              type="button"
              className={`${styles.sendBtn} ${styles.interrupt}`}
              onClick={onInterrupt}
              title="Esc to interrupt"
              aria-label="Interrupt"
            >
              ■
            </button>
          ) : (
            <button
              type="button"
              className={styles.sendBtn}
              onClick={submit}
              disabled={
                isUploading ||
                (!value.trim() &&
                  attachments.filter((a) => a.status === 'ready').length === 0) ||
                disabled
              }
              title="Send (Enter)"
              aria-label="Send"
            >
              ↑
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

// Draft persistence — single keystroke read/write per change. Empty
// strings are removed entirely so localStorage doesn't grow stale keys
// for branches the user briefly typed in and abandoned.
function loadDraft(key: string): string {
  if (typeof localStorage === 'undefined') return ''
  try {
    return localStorage.getItem(key) ?? ''
  } catch {
    return ''
  }
}

function saveDraft(key: string, value: string): void {
  if (typeof localStorage === 'undefined') return
  try {
    if (value) localStorage.setItem(key, value)
    else localStorage.removeItem(key)
  } catch {
    // ignore quota / disabled storage
  }
}

// eventCarriesFile checks whether the dragged/dropped object is a file
// (vs. a text selection or an internal DOM drag). Browsers expose this
// on dataTransfer.types as a "Files" entry; the items collection is
// the future-proof check but Safari only populates it during drop.
function eventCarriesFile(e: React.DragEvent): boolean {
  const dt = e.dataTransfer
  if (!dt) return false
  if (dt.types) {
    for (let i = 0; i < dt.types.length; i++) {
      if (dt.types[i] === 'Files') return true
    }
  }
  return false
}

// safeReadError extracts a message from a non-2xx response. Server
// returns `{error: string}` for upload failures; we tolerate empty
// bodies and fall back to the status text.
async function safeReadError(res: Response): Promise<string> {
  try {
    const text = await res.text()
    if (!text) return ''
    try {
      const data = JSON.parse(text) as { error?: string }
      if (typeof data.error === 'string') return data.error
    } catch {
      // not JSON
    }
    return text
  } catch {
    return ''
  }
}
