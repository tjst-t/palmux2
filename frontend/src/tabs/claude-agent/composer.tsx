// Composer — the message input box at the bottom of the Claude tab.
//
// Wires three completion / convenience surfaces:
//   - `/` triggers a slash-command popup. Sources are the CLI-reported
//     commands list (from initialize) plus our two internal commands
//     (/clear, /model).
//   - `@` triggers a file mention popup that hits the Files API search.
//   - Image paste/drag-drop posts to /api/upload, then injects the
//     returned absolute path so Claude can read the file.

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
  /** Send the user's message. addDirs holds absolute filesystem paths
   *  for any directory chips currently attached; the agent uses them
   *  to spawn / respawn the CLI with `--add-dir <path>`. File chips do
   *  not appear in addDirs — their paths are inlined into `content`
   *  as `@<abspath>` references (S006 decision D-1). */
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
// message — an image (uploaded to imageUploadDir, paste/drag flow), a
// host-filesystem directory (S006: passed as `--add-dir <path>`), or a
// host-filesystem file (S006: inlined as `@<abspath>` in the message
// body so Claude's Read tool reads it). previewUrl is the blob:/data:
// URL for the thumbnail (image only). path is the absolute server-side
// path the agent ultimately receives. relPath is the worktree-relative
// display string used in the chip label so the user sees a familiar
// short form rather than a long absolute path. We hold these as chips
// rather than dumping paths into the textarea so the message reads
// cleanly and ChiP removal lets the user back out cleanly.
interface Attachment {
  id: string
  name: string
  path: string
  previewUrl: string
  kind: 'image' | 'dir' | 'file'
  /** Worktree-relative display path (S006). Empty for paste-uploaded
   *  images, which use absolute imageUploadDir paths the user never
   *  sees as a "location" anyway. */
  relPath?: string
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
  const [uploading, setUploading] = useState(false)
  const [attachments, setAttachments] = useState<Attachment[]>([])
  // S006: per-message picker for "Add directory" / "Add file".
  // 'closed' → nothing visible; 'menu' → the `+` button's dropdown
  // showing the action list; 'dir' / 'file' → the search picker open
  // for that kind. We keep one state machine because the menu and
  // picker are mutually exclusive (clicking outside any of them
  // dismisses).
  const [pickerMode, setPickerMode] = useState<'closed' | 'menu' | 'dir' | 'file'>('closed')
  const taRef = useRef<HTMLTextAreaElement | null>(null)
  const plusBtnRef = useRef<HTMLButtonElement | null>(null)
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

  const submit = () => {
    if (isStreaming || disabled) return
    const text = value.trim()
    if (!text && attachments.length === 0) return
    // Build the submission payload from the chips:
    //   - image  → `[image: <abspath>]` line in the body (existing
    //              behaviour from paste / drag-drop uploads)
    //   - file   → `@<relpath>` reference in the body so the CLI's
    //              Read tool picks it up (S006 decision D-1: --file
    //              is for Anthropic file API resources, not local
    //              files; @-mentions are the canonical Claude Code
    //              idiom for "read this file's contents")
    //   - dir    → ship as addDirs[] in the WS frame so the agent
    //              spawns / respawns the CLI with `--add-dir <path>`.
    //              We also drop a one-line annotation in the message
    //              so the conversation transcript records that a
    //              directory was attached (otherwise the user picks
    //              a dir and Claude has no signal it was added).
    const lines: string[] = []
    if (text) lines.push(text)
    const addDirs: string[] = []
    for (const a of attachments) {
      switch (a.kind) {
        case 'image':
          lines.push(`[image: ${a.path}]`)
          break
        case 'file':
          // a.path here is the worktree-relative path returned by
          // /files/search; the CLI resolves it against its own cwd
          // (the same worktree). @-mentions tolerate spaces in paths
          // because Claude Code's parser is tolerant; for safety we
          // still URI-escape only spaces if they happen to appear,
          // but in practice paths inside the worktree are mundane.
          lines.push(`@${a.path}`)
          break
        case 'dir':
          // a.path is the worktree-relative directory path. The
          // backend's validateAddDirs converts it to abs and bounds
          // it inside the worktree before passing to argv.
          addDirs.push(a.path)
          // Cosmetic marker so the user can later see in their own
          // history that they attached a dir on this turn.
          lines.push(`[dir: ${a.path}]`)
          break
      }
    }
    const body = lines.filter((s) => s).join('\n')
    onSend(body, addDirs.length > 0 ? addDirs : undefined)
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

  const uploadFile = async (file: File) => {
    if (uploading) return
    const isImage = file.type.startsWith('image/')
    if (!isImage) return
    // Show the chip optimistically with a local blob preview while the
    // upload is in flight. Replace nothing on failure — just remove the
    // pending chip.
    const previewUrl = URL.createObjectURL(file)
    const tempId = newAttachmentId()
    setAttachments((prev) => [
      ...prev,
      { id: tempId, name: file.name || 'image', path: '', previewUrl, kind: 'image' },
    ])
    setUploading(true)
    try {
      const fd = new FormData()
      fd.append('file', file)
      const res = await fetch('/api/upload', {
        method: 'POST',
        credentials: 'include',
        body: fd,
      })
      if (!res.ok) {
        URL.revokeObjectURL(previewUrl)
        setAttachments((prev) => prev.filter((a) => a.id !== tempId))
        return
      }
      const data = (await res.json()) as { path?: string }
      if (!data.path) {
        URL.revokeObjectURL(previewUrl)
        setAttachments((prev) => prev.filter((a) => a.id !== tempId))
        return
      }
      setAttachments((prev) =>
        prev.map((a) => (a.id === tempId ? { ...a, path: data.path! } : a)),
      )
    } finally {
      setUploading(false)
    }
  }

  const removeAttachment = (id: string) => {
    setAttachments((prev) => {
      const dropped = prev.find((a) => a.id === id)
      if (dropped?.previewUrl.startsWith('blob:')) URL.revokeObjectURL(dropped.previewUrl)
      return prev.filter((a) => a.id !== id)
    })
  }

  // ──────────── S006: Add directory / Add file picker ──────────────
  // Both "Add directory" and "Add file" share one picker UI: a search
  // box that hits the Files API search and a result list. The kind
  // controls the filter (`isDir`) and the resulting attachment shape.
  // Closing the picker discards in-progress query state — that's fine
  // because the `+` menu will re-open it cleanly next time.

  const openMenu = () => setPickerMode((m) => (m === 'menu' ? 'closed' : 'menu'))
  const closeAll = () => setPickerMode('closed')

  const addDirAttachment = (relPath: string) => {
    // De-dupe: if the user picks the same dir twice, no second chip.
    setAttachments((prev) => {
      if (prev.some((a) => a.kind === 'dir' && a.path === relPath)) return prev
      const id = newAttachmentId()
      return [
        ...prev,
        {
          id,
          name: relPath || '.',
          path: relPath || '.',
          relPath: relPath || '.',
          previewUrl: '',
          kind: 'dir',
        },
      ]
    })
    closeAll()
  }

  const addFileAttachment = (relPath: string) => {
    setAttachments((prev) => {
      if (prev.some((a) => a.kind === 'file' && a.path === relPath)) return prev
      const id = newAttachmentId()
      // Display only the basename in the chip if the path is long, but
      // keep the full relPath in `name` so hover-title and the
      // injected `@<relPath>` stay accurate.
      return [
        ...prev,
        {
          id,
          name: relPath,
          path: relPath,
          relPath,
          previewUrl: '',
          kind: 'file',
        },
      ]
    })
    closeAll()
  }

  const onPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const items = Array.from(e.clipboardData?.items ?? [])
    const fileItem = items.find((i) => i.kind === 'file')
    if (!fileItem) return
    const file = fileItem.getAsFile()
    if (!file) return
    e.preventDefault()
    void uploadFile(file)
  }

  const onDrop = (e: React.DragEvent<HTMLTextAreaElement>) => {
    const file = e.dataTransfer?.files?.[0]
    if (!file) return
    e.preventDefault()
    void uploadFile(file)
  }

  const placeholder = disabled
    ? 'Authenticate Claude Code first'
    : 'Message Claude…  (Enter to send, Shift+Enter for newline, /, @ to autocomplete)'

  // Models — prefer CLI-reported list with effort/thinking metadata.
  const models = initInfo?.models?.length ? initInfo.models : FALLBACK_MODELS
  const currentModelDescriptor = models.find((m) => m.value === localModel)
  const effortLevels = currentModelDescriptor?.supportedEffortLevels ?? []
  const showEffort = !!currentModelDescriptor?.supportsEffort && effortLevels.length > 0

  return (
    <div className={styles.composer}>
      <div className={styles.composerInner} style={{ position: 'relative' }}>
        <InlineCompletionPopup
          state={completion.state}
          onPick={(opt) => applyCompletion(opt)}
        />
        {attachments.length > 0 && (
          <div className={styles.attachments} data-testid="composer-attachments">
            {attachments.map((a) => (
              <div
                key={a.id}
                className={styles.attachment}
                title={a.name}
                data-testid={`attachment-chip-${a.kind}`}
                data-attachment-kind={a.kind}
                data-attachment-path={a.path}
              >
                {a.kind === 'image' ? (
                  <img src={a.previewUrl} alt={a.name} className={styles.attachmentThumb} />
                ) : a.kind === 'dir' ? (
                  <span className={styles.attachmentFileIcon} aria-hidden>
                    📁
                  </span>
                ) : (
                  <span className={styles.attachmentFileIcon} aria-hidden>
                    📄
                  </span>
                )}
                <span className={styles.attachmentName}>
                  {a.kind === 'dir'
                    ? (a.relPath || a.name).replace(/\/?$/, '/')
                    : a.relPath || a.name}
                </span>
                {!a.path && <span className={styles.attachmentSpinner}>…</span>}
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
          onDrop={onDrop}
          onDragOver={(e) => e.preventDefault()}
          placeholder={placeholder}
          rows={1}
          disabled={disabled}
        />
        <div className={styles.composerFooter}>
          {/* S006: hidden file input the "+" menu's "Upload image…"
              entry triggers. We keep paste/drag-drop as the primary
              upload paths; this is the explicit-click affordance for
              touch devices that don't support drag-drop. */}
          <input
            ref={fileInputRef}
            type="file"
            accept="image/*"
            style={{ display: 'none' }}
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (f) void uploadFile(f)
              e.target.value = ''
            }}
          />
          <button
            ref={plusBtnRef}
            type="button"
            className={styles.attachBtn}
            onClick={openMenu}
            aria-label="Add attachment"
            aria-expanded={pickerMode !== 'closed'}
            title="Add directory, file, or image"
            data-testid="composer-plus-btn"
            disabled={disabled}
          >
            +
          </button>
          {pickerMode === 'menu' && (
            <AttachMenu
              onPickDir={() => setPickerMode('dir')}
              onPickFile={() => setPickerMode('file')}
              onPickImage={() => {
                setPickerMode('closed')
                fileInputRef.current?.click()
              }}
              onClose={closeAll}
              anchorRef={plusBtnRef}
            />
          )}
          {(pickerMode === 'dir' || pickerMode === 'file') && (
            <PathPicker
              repoId={repoId}
              branchId={branchId}
              kind={pickerMode}
              onClose={closeAll}
              onPick={pickerMode === 'dir' ? addDirAttachment : addFileAttachment}
            />
          )}

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

          {uploading && <span className={styles.connBanner}>uploading…</span>}
          {connState !== 'open' && !uploading && (
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
              disabled={(!value.trim() && attachments.length === 0) || disabled}
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

// ──────────── S006: Attach menu (the `+` button's dropdown) ──────────────

interface AttachMenuProps {
  onPickDir: () => void
  onPickFile: () => void
  onPickImage: () => void
  onClose: () => void
  anchorRef: React.RefObject<HTMLButtonElement | null>
}

function AttachMenu(props: AttachMenuProps) {
  const { onPickDir, onPickFile, onPickImage, onClose, anchorRef } = props
  const ref = useRef<HTMLDivElement | null>(null)

  // Click-outside / Esc to dismiss. The anchor ref is excluded from the
  // outside check because clicking the `+` button again is meant to
  // toggle the menu off via openMenu()'s own state machine — we don't
  // want both effects firing at once.
  useEffect(() => {
    const onDocDown = (e: MouseEvent) => {
      const target = e.target as Node | null
      if (!target) return
      if (ref.current?.contains(target)) return
      if (anchorRef.current?.contains(target)) return
      onClose()
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('mousedown', onDocDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDocDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [anchorRef, onClose])

  return (
    <div
      ref={ref}
      className={styles.attachMenu}
      role="menu"
      data-testid="composer-attach-menu"
    >
      <button
        type="button"
        role="menuitem"
        className={styles.attachMenuItem}
        onClick={onPickDir}
        data-testid="attach-menu-dir"
      >
        <span aria-hidden>📁</span>
        <span>Add directory</span>
      </button>
      <button
        type="button"
        role="menuitem"
        className={styles.attachMenuItem}
        onClick={onPickFile}
        data-testid="attach-menu-file"
      >
        <span aria-hidden>📄</span>
        <span>Add file</span>
      </button>
      <button
        type="button"
        role="menuitem"
        className={styles.attachMenuItem}
        onClick={onPickImage}
        data-testid="attach-menu-image"
      >
        <span aria-hidden>🖼️</span>
        <span>Upload image…</span>
      </button>
    </div>
  )
}

// ──────────── S006: PathPicker (worktree-relative search dialog) ─────────

interface PathPickerProps {
  repoId: string
  branchId: string
  kind: 'dir' | 'file'
  onPick: (relPath: string) => void
  onClose: () => void
}

interface PickerEntry {
  path: string
  name: string
  isDir: boolean
}

// PathPicker renders a small search dialog. As the user types, it hits
// the existing Files API search endpoint — the same one the `@`-mention
// autocomplete uses — and filters by isDir on the client side. We
// deliberately reuse the Files API instead of rolling a parallel
// host-filesystem walker (decision D-3): single auth surface, single
// traversal/symlink hardening (`resolveSafePath`), nothing new to
// review for security.
function PathPicker(props: PathPickerProps) {
  const { repoId, branchId, kind, onPick, onClose } = props
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<PickerEntry[]>([])
  const [highlight, setHighlight] = useState(0)
  const [loading, setLoading] = useState(false)
  const inputRef = useRef<HTMLInputElement | null>(null)
  const ref = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  useEffect(() => {
    const onDocDown = (e: MouseEvent) => {
      const target = e.target as Node | null
      if (!target) return
      if (ref.current?.contains(target)) return
      onClose()
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('mousedown', onDocDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDocDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [onClose])

  // Debounced search — the Files API walks the worktree, so spamming it
  // on every keystroke is wasteful even though responses come back fast.
  // 120ms is short enough that the user perceives "live" results.
  useEffect(() => {
    const q = query.trim()
    if (!q) {
      setResults([])
      setLoading(false)
      return
    }
    setLoading(true)
    const ctl = new AbortController()
    const timer = setTimeout(async () => {
      try {
        const url =
          `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}` +
          `/files/search?query=${encodeURIComponent(q)}`
        const res = await fetch(url, { credentials: 'include', signal: ctl.signal })
        if (!res.ok) {
          setResults([])
          return
        }
        const data = (await res.json()) as { results?: PickerEntry[] }
        const all = data.results ?? []
        // Filter to the kind we want. The picker stays sharp: "Add
        // directory" never offers a file, "Add file" never offers a
        // directory. The user can flip menus to switch.
        const filtered = all.filter((e) => (kind === 'dir' ? e.isDir : !e.isDir))
        setResults(filtered.slice(0, 50))
        setHighlight(0)
      } catch {
        if (!ctl.signal.aborted) setResults([])
      } finally {
        if (!ctl.signal.aborted) setLoading(false)
      }
    }, 120)
    return () => {
      ctl.abort()
      clearTimeout(timer)
      setLoading(false)
    }
  }, [query, repoId, branchId, kind])

  const onInputKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setHighlight((h) => Math.min(h + 1, Math.max(0, results.length - 1)))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setHighlight((h) => Math.max(0, h - 1))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const pick = results[highlight]
      if (pick) onPick(pick.path)
    }
  }

  return (
    <div ref={ref} className={styles.pathPicker} data-testid="composer-path-picker">
      <div className={styles.pathPickerHeader}>
        <span className={styles.pathPickerKind}>
          {kind === 'dir' ? '📁 Add directory' : '📄 Add file'}
        </span>
        <button
          type="button"
          className={styles.pathPickerClose}
          onClick={onClose}
          aria-label="Close picker"
        >
          ×
        </button>
      </div>
      <input
        ref={inputRef}
        type="text"
        className={styles.pathPickerInput}
        placeholder={kind === 'dir' ? 'Search directories…' : 'Search files…'}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={onInputKey}
        data-testid="path-picker-input"
      />
      <div className={styles.pathPickerResults} data-testid="path-picker-results">
        {loading && <div className={styles.pathPickerHint}>searching…</div>}
        {!loading && query.trim() === '' && (
          <div className={styles.pathPickerHint}>Type to search inside the worktree.</div>
        )}
        {!loading && query.trim() !== '' && results.length === 0 && (
          <div className={styles.pathPickerHint}>No matches.</div>
        )}
        {results.map((r, i) => (
          <button
            key={r.path}
            type="button"
            className={
              i === highlight
                ? `${styles.pathPickerItem} ${styles.pathPickerItemActive}`
                : styles.pathPickerItem
            }
            onMouseEnter={() => setHighlight(i)}
            onClick={() => onPick(r.path)}
            data-testid="path-picker-item"
            data-path={r.path}
          >
            <span aria-hidden>{r.isDir ? '📁' : '📄'}</span>
            <span className={styles.pathPickerItemPath}>{r.path}</span>
          </button>
        ))}
      </div>
      <div className={styles.pathPickerFooter}>
        Worktree only · Esc to close
      </div>
    </div>
  )
}
