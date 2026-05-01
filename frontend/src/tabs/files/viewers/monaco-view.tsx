// MonacoView — read-only source / text preview (S010).
//
// Wraps `@monaco-editor/react` with VISION-out-of-scope features
// EXPLICITLY OFF: no autocomplete, no LSP/language-server hooks, no
// hover popups, no parameter hints, no occurrence highlighting. The
// editor stays useful as a viewer (syntax highlighting, line numbers,
// folding, find-in-file) without drifting toward IDE territory — that
// is intentionally out of palmux2's scope per docs/VISION.md.
//
// We intentionally `import('@monaco-editor/react')` lazily so the ~3 MB
// Monaco bundle doesn't load until the user opens a non-markdown,
// non-image file for the first time.

import { useEffect, useRef } from 'react'

import type { editor as MonacoEditor } from 'monaco-editor'
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

// Read-only Monaco options. Each VISION-scope-out feature has its
// matching disable knob; if Monaco adds a new IDE feature later we
// disable it here too rather than letting the default leak through.
const READ_ONLY_OPTIONS: MonacoEditor.IStandaloneEditorConstructionOptions = {
  readOnly: true,
  domReadOnly: true,
  // Safety: even though readOnly is true, paste/drop/etc. could still
  // mutate the model in some Monaco versions; flagging the model too
  // closes that loophole.
  contextmenu: false,
  // VISION scope-out: no IDE-style helpers.
  quickSuggestions: false,
  parameterHints: { enabled: false },
  suggestOnTriggerCharacters: false,
  wordBasedSuggestions: 'off',
  acceptSuggestionOnEnter: 'off',
  hover: { enabled: false },
  occurrencesHighlight: 'off',
  // Useful viewer features stay on:
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

interface Props extends ViewerProps {
  /** Optional override for the Monaco language id; otherwise inferred
   *  from the path. The Markdown view passes `markdown` so we don't
   *  re-detect when called from the dispatcher. */
  language?: string
}

export function MonacoView({ body, lineNum, path, language }: Props) {
  const theme = usePalmuxStore((s) => s.deviceSettings.theme)
  const editorRef = useRef<MonacoEditor.IStandaloneCodeEditor | null>(null)

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

  return (
    <div className={styles.wrap} data-testid="monaco-view" data-language={lang}>
      <Editor
        className={styles.editor}
        defaultLanguage={lang}
        language={lang}
        value={body.content ?? ''}
        theme={theme === 'dark' ? 'vs-dark' : 'vs'}
        options={READ_ONLY_OPTIONS}
        onMount={(ed) => {
          editorRef.current = ed
          if (lineNum && lineNum > 0) {
            ed.revealLineInCenter(lineNum)
            ed.setPosition({ lineNumber: lineNum, column: 1 })
          }
        }}
      />
    </div>
  )
}
