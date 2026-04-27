// Composer — the message input box at the bottom of the Claude tab.
//
// Wires three completion / convenience surfaces:
//   - `/` triggers a slash-command popup. Sources are the CLI-reported
//     commands list (from initialize) plus our two internal commands
//     (/clear, /model).
//   - `@` triggers a file mention popup that hits the Files API search.
//   - Image paste/drag-drop posts to /api/upload, then injects the
//     returned absolute path so Claude can read the file.

import { useCallback, useMemo, useRef, useState } from 'react'

import {
  InlineCompletionPopup,
  useInlineCompletion,
  type CompletionOption,
  type CompletionTrigger,
} from '../../components/inline-completion'

import styles from './claude-agent-view.module.css'
import type { InitInfo, ModelDescriptor } from './types'

interface ComposerProps {
  repoId: string
  branchId: string
  onSend: (content: string) => void
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

  const [value, setValue] = useState('')
  const [composing, setComposing] = useState(false)
  const [uploading, setUploading] = useState(false)
  const taRef = useRef<HTMLTextAreaElement | null>(null)

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
    if (!value.trim() || isStreaming || disabled) return
    onSend(value)
    setValue('')
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
    if (uploading || !file.type.startsWith('image/')) return
    setUploading(true)
    try {
      const fd = new FormData()
      fd.append('file', file)
      const res = await fetch('/api/upload', {
        method: 'POST',
        credentials: 'include',
        body: fd,
      })
      if (!res.ok) return
      const data = (await res.json()) as { path?: string }
      if (!data.path) return
      // Insert the absolute path into the message so Claude can Read it.
      const insertion = (value && !value.endsWith(' ') ? ' ' : '') + data.path + ' '
      const next = value + insertion
      setValue(next)
      requestAnimationFrame(() => {
        if (taRef.current) {
          taRef.current.focus()
          taRef.current.setSelectionRange(next.length, next.length)
        }
      })
    } finally {
      setUploading(false)
    }
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
  const currentModelDescriptor = models.find((m) => m.value === model)
  const effortLevels = currentModelDescriptor?.supportedEffortLevels ?? []
  const showEffort = !!currentModelDescriptor?.supportsEffort && effortLevels.length > 0

  return (
    <div className={styles.composer}>
      <div className={styles.composerInner} style={{ position: 'relative' }}>
        <InlineCompletionPopup
          state={completion.state}
          onPick={(opt) => applyCompletion(opt)}
        />
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
          <span className={styles.modePill}>
            <select
              aria-label="Model"
              value={model}
              onChange={(e) => onModelChange(e.target.value)}
            >
              {models.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.displayName ?? o.value ?? 'default'}
                </option>
              ))}
            </select>
          </span>
          {showEffort && (
            <span className={styles.modePill}>
              <select
                aria-label="Effort"
                value={effort}
                onChange={(e) => onEffortChange(e.target.value)}
              >
                <option value="">effort: default</option>
                {effortLevels.map((lvl) => (
                  <option key={lvl} value={lvl}>{lvl}</option>
                ))}
              </select>
            </span>
          )}
          <span className={styles.modePill}>
            <select
              aria-label="Permission mode"
              value={permissionMode}
              onChange={(e) => onPermissionModeChange(e.target.value)}
            >
              {permissionModes.map((m) => (
                <option key={m} value={m}>{modeLabel(m)}</option>
              ))}
            </select>
          </span>

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
              disabled={!value.trim() || disabled}
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
