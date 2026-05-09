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
// S43cfb1-5: the 827-line original file was split into:
//   - composer/index.tsx (this file): textarea + key handler + draft
//     persistence + submit + drag-drop wiring.
//   - composer/selectors.tsx: Model / Effort / PermissionMode pills
//     with their optimistic local state.
//   - composer/completions.ts: INTERNAL_COMMANDS + the slash + @
//     trigger config.
//   - composer/use-upload.ts: Attachment type + upload pipeline +
//     drag-event helper.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import {
  InlineCompletionPopup,
  useInlineCompletion,
  type CompletionOption,
} from '../../../components/inline-completion'
import { confirmDialog } from '../../../components/context-menu/confirm-dialog'

import styles from '../claude-agent-view.module.css'
import type { InitInfo } from '../types'
import { buildCompletionTriggers } from './completions'
import { ComposerSelectors, FALLBACK_MODELS } from './selectors'
import { eventCarriesFile, useUpload } from './use-upload'

interface ComposerProps {
  repoId: string
  branchId: string
  /** S009: per-Claude-tab id (e.g. `claude:claude`, `claude:claude-2`).
   *  Mixed into the draft localStorage key so two Claude tabs on the
   *  same workspace don't share each other's in-progress messages. */
  tabId: string
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

export function Composer(props: ComposerProps) {
  const {
    repoId,
    branchId,
    tabId,
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
  // and full page reloads via localStorage keyed by
  // `${repoId}/${branchId}/${tabId}` so two Claude tabs in the same
  // workspace each keep their own in-progress message instead of
  // clobbering each other's drafts. Only the text is persisted —
  // attachments hold blob URLs that don't round-trip safely, so they
  // reset to empty on remount.
  const tabKey = tabId || 'claude'
  const draftKey = `palmux:claude-draft:${repoId}/${branchId}/${tabKey}`
  const legacyDraftKey = `palmux:claude-draft:${repoId}/${branchId}`
  const [value, setValue] = useState(() =>
    loadDraftWithMigration(draftKey, legacyDraftKey, tabKey === 'claude:claude'),
  )
  useEffect(() => {
    setValue(loadDraftWithMigration(draftKey, legacyDraftKey, tabKey === 'claude:claude'))
  }, [draftKey, legacyDraftKey, tabKey])
  useEffect(() => {
    saveDraft(draftKey, value)
  }, [draftKey, value])

  const [composing, setComposing] = useState(false)
  // S008: drag-over indicator. `dragDepth` counts dragenter vs dragleave
  // to avoid flicker when the cursor crosses inner element boundaries.
  const [dragDepth, setDragDepth] = useState(0)
  const taRef = useRef<HTMLTextAreaElement | null>(null)
  const composerRef = useRef<HTMLDivElement | null>(null)
  const fileInputRef = useRef<HTMLInputElement | null>(null)

  const { attachments, addFiles, removeAttachment, clearAttachments, isUploading } = useUpload({
    repoId,
    branchId,
  })

  // Auto-grow the textarea up to a third of the viewport height; beyond
  // that, switch to internal scrolling.
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

  // Build completion triggers — recreated only when the underlying data
  // (commands list, repo/branch) changes, otherwise the inline-completion
  // state would re-trigger fetches on every keystroke.
  const triggers = useMemo(
    () => buildCompletionTriggers({ repoId, branchId, initInfo }),
    [repoId, branchId, initInfo],
  )
  const completion = useInlineCompletion(triggers)

  const onChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const v = e.target.value
    setValue(v)
    completion.update(v, e.target.selectionEnd)
  }

  const onSelectionMove = useCallback(() => {
    if (!taRef.current) return
    completion.update(taRef.current.value, taRef.current.selectionEnd)
  }, [completion])

  const submit = async () => {
    if (isStreaming || disabled) return
    if (isUploading) return
    const text = value.trim()
    const ready = attachments.filter((a) => a.status === 'ready' && a.path)
    if (!text && ready.length === 0) return
    // S018: /compact is destructive (the pre-compaction context is
    // replaced by a summary). Surface a confirm dialog before letting
    // the message go to the CLI.
    if (isCompactCommand(text)) {
      const ok = await confirmDialog.ask({
        title: 'Compact conversation?',
        message:
          'Past conversation will be summarised and the original turns replaced. Compacted content cannot be restored within this session.',
        confirmLabel: 'Compact',
        cancelLabel: 'Cancel',
        danger: true,
      })
      if (!ok) return
    }
    // Build the submission payload from the chips:
    //   - image  → `[image: <abspath>]` line in the body
    //   - file   → `@<abspath>` reference in the body so the CLI's
    //              Read tool picks it up.
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
    clearAttachments()
    completion.cancel()
  }

  const applyCompletion = (opt?: CompletionOption) => {
    if (!taRef.current) return false
    const result = completion.apply(taRef.current.value, taRef.current.selectionEnd, opt)
    if (!result) return false
    setValue(result.text)
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

  // S008: paste handler — process every File in clipboardData.
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
    addFiles(files)
  }

  // S008: drag-and-drop. The drop target is the composer wrapper, not
  // just the textarea, so dropping anywhere over the input area attaches.
  const onDragEnter = (e: React.DragEvent<HTMLDivElement>) => {
    if (!eventCarriesFile(e)) return
    e.preventDefault()
    setDragDepth((d) => d + 1)
  }
  const onDragOver = (e: React.DragEvent<HTMLDivElement>) => {
    if (!eventCarriesFile(e)) return
    e.preventDefault()
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
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
    addFiles(files)
  }

  const placeholder = disabled
    ? 'Authenticate Claude Code first'
    : 'Message Claude…  (Enter to send, Shift+Enter for newline, /, @ to autocomplete)'

  const models = initInfo?.models?.length ? initInfo.models : FALLBACK_MODELS
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
              multiple lets the user pick several at once. */}
          <input
            ref={fileInputRef}
            type="file"
            multiple
            style={{ display: 'none' }}
            onChange={(e) => {
              const files = Array.from(e.target.files ?? [])
              if (files.length > 0) addFiles(files)
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

          <ComposerSelectors
            models={models}
            model={model}
            effort={effort}
            permissionMode={permissionMode}
            permissionModes={permissionModes}
            onModelChange={onModelChange}
            onEffortChange={onEffortChange}
            onPermissionModeChange={onPermissionModeChange}
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

// isCompactCommand: returns true when the user's message is a
// `/compact` invocation (with or without arguments). Stripped so a
// future `/compact reason` shape is also caught.
function isCompactCommand(s: string): boolean {
  if (!s.startsWith('/')) return false
  const firstLine = s.split('\n', 2)[0]
  const head = firstLine.split(/\s+/, 2)[0]
  return head === '/compact'
}

// One-time migration from the pre-tabId shared draft key. When the user
// upgrades while a draft is in-flight we hand it to the primary
// `claude:claude` tab (the only tab that existed under the old key) so
// the in-progress message doesn't silently vanish, then drop the legacy
// entry so it can never resurface on secondary tabs.
function loadDraftWithMigration(key: string, legacyKey: string, isPrimaryTab: boolean): string {
  if (typeof localStorage === 'undefined') return ''
  try {
    const current = localStorage.getItem(key)
    if (current && current.length > 0) return current
    if (!isPrimaryTab) return ''
    const legacy = localStorage.getItem(legacyKey)
    if (!legacy) return ''
    localStorage.setItem(key, legacy)
    localStorage.removeItem(legacyKey)
    return legacy
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
