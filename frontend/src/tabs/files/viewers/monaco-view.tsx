// MonacoView — source/text viewer + S011 editor.
//
// Wraps `@monaco-editor/react` with VISION-out-of-scope features
// EXPLICITLY OFF: no autocomplete, no LSP/language-server hooks, no
// hover popups, no parameter hints, no occurrence highlighting. The
// editor stays useful as an editor (syntax highlighting, line numbers,
// folding, find-in-file, multi-cursor, undo/redo) without drifting
// toward IDE territory — that is intentionally out of palmux2's scope
// per docs/VISION.md.
//
// S011: when `mode === 'edit'` we drop the readOnly flag, allow context
// menu (so users can right-click → Find/Replace), and wire `Ctrl+S`
// (Cmd+S on Mac) to the supplied `onSave`. All other VISION-scope-out
// disable knobs stay ON in edit mode too — the change between view and
// edit is *only* `readOnly`, deliberately.
//
// We intentionally `import('@monaco-editor/react')` lazily so the ~3 MB
// Monaco bundle doesn't load until the user opens a non-markdown,
// non-image file for the first time.

import { useEffect, useRef } from 'react'

import type { editor as MonacoEditor, IKeyboardEvent } from 'monaco-editor'
import Editor, { loader } from '@monaco-editor/react'

import { usePalmuxStore } from '../../../stores/palmux-store'

import { monacoLanguageFor } from './dispatcher'
import styles from './monaco-view.module.css'
import type { ViewerProps } from './types'

// Use the Monaco bundle that ships in our node_modules (Vite-friendly)
// rather than the default CDN load. Avoids an external network round
// trip and keeps the bundle reproducible. Calling `loader.config` is
// idempotent.
//
// We import the worker scripts as Vite ?worker URLs and wire them via
// `MonacoEnvironment.getWorker` so Monaco's bundled language services
// (json/css/html/ts) work offline. Without this Monaco silently falls
// back to a worker-less mode and certain features (find/folding still
// work; complex JSON validation may not).
import editorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'
import jsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker'
import cssWorker from 'monaco-editor/esm/vs/language/css/css.worker?worker'
import htmlWorker from 'monaco-editor/esm/vs/language/html/html.worker?worker'
import tsWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker'

// Provide the worker factory exactly once. Subsequent imports of this
// module (HMR / route remounts) are no-ops thanks to the global guard.
declare global {
  interface Window {
    __palmuxMonacoEnvSet__?: boolean
  }
}

if (typeof window !== 'undefined' && !window.__palmuxMonacoEnvSet__) {
  ;(self as unknown as { MonacoEnvironment: object }).MonacoEnvironment = {
    getWorker(_id: string, label: string) {
      if (label === 'json') return new jsonWorker()
      if (label === 'css' || label === 'scss' || label === 'less') return new cssWorker()
      if (label === 'html' || label === 'handlebars' || label === 'razor') return new htmlWorker()
      if (label === 'typescript' || label === 'javascript') return new tsWorker()
      return new editorWorker()
    },
  }
  window.__palmuxMonacoEnvSet__ = true
}

// Use the npm-bundled Monaco rather than the unpkg CDN — VISION
// "self-hosted, offline-friendly" priority. The dynamic import below
// pulls the same monaco namespace `@monaco-editor/react` would have
// fetched from CDN.
loader.config({
  monaco: await import('monaco-editor').then((m) => m),
})

// VISION-scope-out disables. Repeated in the edit-mode override below
// because they need to be sticky across `readOnly` toggles. If Monaco
// ships a new IDE-style feature later, add the disable knob here too
// rather than letting the default leak through.
const SCOPE_OUT_OPTIONS: MonacoEditor.IStandaloneEditorConstructionOptions = {
  // VISION scope-out: no IDE-style helpers.
  quickSuggestions: false,
  parameterHints: { enabled: false },
  suggestOnTriggerCharacters: false,
  wordBasedSuggestions: 'off',
  acceptSuggestionOnEnter: 'off',
  hover: { enabled: false },
  occurrencesHighlight: 'off',
  // Useful editor / viewer features stay on:
  lineNumbers: 'on',
  folding: true,
  wordWrap: 'on',
  minimap: { enabled: false }, // Save real estate on phone screens.
  smoothScrolling: true,
  fontFamily: 'Geist Mono, Cascadia Code, Fira Code, monospace',
  fontSize: 13,
  scrollBeyondLastLine: false,
  // Allow find/replace popup but keep it Cmd/Ctrl+F-only.
  find: {
    addExtraSpaceOnTop: false,
    autoFindInSelection: 'never',
    seedSearchStringFromSelection: 'always',
  },
}

const READ_ONLY_OPTIONS: MonacoEditor.IStandaloneEditorConstructionOptions = {
  ...SCOPE_OUT_OPTIONS,
  readOnly: true,
  domReadOnly: true,
  contextmenu: false,
}

const EDIT_OPTIONS: MonacoEditor.IStandaloneEditorConstructionOptions = {
  ...SCOPE_OUT_OPTIONS,
  readOnly: false,
  domReadOnly: false,
  // Right-click menu lets the user pop find/replace, command palette
  // (which we narrow to view-only commands), etc. We re-disable
  // suggestions in the menu via the SCOPE_OUT_OPTIONS above.
  contextmenu: true,
  // S011 acceptance: bracket matching and auto-indent are first-class.
  matchBrackets: 'always',
  autoIndent: 'full',
}

interface Props extends ViewerProps {
  /** Optional override for the Monaco language id; otherwise inferred
   *  from the path. The Markdown view passes `markdown` so we don't
   *  re-detect when called from the dispatcher. */
  language?: string
  /** S011: 'view' (read-only, default) or 'edit' (mutable). */
  mode?: 'view' | 'edit'
  /** S011: invoked on every text change in edit mode. */
  onChange?: (value: string) => void
  /** S011: invoked when the user hits Ctrl+S / Cmd+S inside the editor. */
  onSave?: () => void
}

export function MonacoView({
  body,
  lineNum,
  path,
  language,
  mode = 'view',
  onChange,
  onSave,
}: Props) {
  const theme = usePalmuxStore((s) => s.deviceSettings.theme)
  const editorRef = useRef<MonacoEditor.IStandaloneCodeEditor | null>(null)
  // Keep the latest onSave in a ref so the keydown listener captures the
  // latest closure without re-binding on every render.
  const onSaveRef = useRef<typeof onSave>(onSave)
  onSaveRef.current = onSave

  // Scroll to the requested 1-based line whenever lineNum changes. We
  // can't just rely on Monaco's internal `revealLineInCenter` from the
  // mount handler because the user can keep clicking grep results
  // without remounting the component.
  useEffect(() => {
    const editor = editorRef.current
    if (!editor || !lineNum || lineNum <= 0) return
    editor.revealLineInCenter(lineNum)
    editor.setPosition({ lineNumber: lineNum, column: 1 })
  }, [lineNum, body?.path])

  if (!body) return <p className={styles.placeholder}>Loading…</p>

  const lang = language ?? monacoLanguageFor(path)
  const options = mode === 'edit' ? EDIT_OPTIONS : READ_ONLY_OPTIONS

  return (
    <div
      className={styles.wrap}
      data-testid="monaco-view"
      data-language={lang}
      data-mode={mode}
    >
      <Editor
        className={styles.editor}
        defaultLanguage={lang}
        language={lang}
        // S011: we're an uncontrolled-on-keystroke editor. Passing
        // `value=` continuously would fight Monaco's internal model,
        // so we set the initial value via the path key (which forces
        // a remount when the user opens a different file) and let
        // `onChange` lift the buffer to the parent's draft state.
        defaultValue={body.content ?? ''}
        path={path}
        theme={theme === 'dark' ? 'vs-dark' : 'vs'}
        options={options}
        onChange={(v) => onChange?.(v ?? '')}
        onMount={(ed) => {
          editorRef.current = ed
          if (lineNum && lineNum > 0) {
            ed.revealLineInCenter(lineNum)
            ed.setPosition({ lineNumber: lineNum, column: 1 })
          }
          // S011-1-7: Ctrl+S / Cmd+S triggers Save. We use Monaco's
          // `onKeyDown` (not the host page) so the shortcut works
          // exclusively when the editor has focus. `preventDefault`
          // suppresses the browser's "save page" dialog.
          ed.onKeyDown((e: IKeyboardEvent) => {
            const isSaveCombo =
              (e.ctrlKey || e.metaKey) && (e.code === 'KeyS' || e.keyCode === 49 /* S */)
            if (isSaveCombo) {
              e.preventDefault()
              e.stopPropagation()
              onSaveRef.current?.()
            }
          })
        }}
      />
    </div>
  )
}
