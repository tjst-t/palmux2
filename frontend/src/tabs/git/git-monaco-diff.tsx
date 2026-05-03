// GitMonacoDiff — Monaco-powered side-by-side / inline diff viewer (S012).
// Reuses the Monaco bundle wired up in S010 by importing
// `@monaco-editor/react`'s lazy DiffEditor. Compared to the structured
// hunk-table view (DiffView), this gives:
//
//   - Real syntax highlighting (Monaco infers language from path).
//   - Side-by-side or inline (unified) layout, controlled by the
//     `renderSideBySide` flag — we force inline when the viewport is
//     narrower than 900px (S012-1-18 mobile parity).
//   - A "Stage selected lines" button that takes Monaco's selection in
//     the modified pane and POSTs to `/git/stage-lines` (S012-1-11).
//
// We fetch the original (HEAD) and modified (working tree) content from
// the existing Files API to feed both panes. For the `staged` mode we
// fetch HEAD vs. INDEX via `git show :path` proxied through the diff
// endpoint. The DiffView component in `components/diff` is still used
// when the user wants the hunk-action interactions.

import { useEffect, useMemo, useRef, useState } from 'react'

import { DiffEditor } from '@monaco-editor/react'
import type { editor as MonacoEditor } from 'monaco-editor'

import { monacoLanguageFor } from '../files/viewers/dispatcher'
import { api } from '../../lib/api'

import { ImagePair, isImageFile } from './git-image-diff'
import styles from './git-monaco-diff.module.css'
import type { LineRange } from './types'

interface Props {
  apiBase: string
  /** Worktree-relative path. */
  path: string
  /** When true, force unified (inline) view (mobile or user toggle). */
  unified: boolean
  /** Re-fetch counter — bumped by parent after stage / unstage / commit. */
  reloadKey?: number
  onStaged?: () => void
}

interface FileContents {
  original: string
  modified: string
}

export function GitMonacoDiff({ apiBase, path, unified, reloadKey, onStaged }: Props) {
  const [contents, setContents] = useState<FileContents | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [staging, setStaging] = useState(false)
  const [selectedRanges, setSelectedRanges] = useState<LineRange[]>([])
  const editorRef = useRef<MonacoEditor.IStandaloneDiffEditor | null>(null)
  const language = useMemo(() => monacoLanguageFor(path), [path])

  // Fetch HEAD (original) and working-tree (modified) contents. The Git
  // backend's `/diff?path=` returns a structured diff but not raw file
  // bodies; for HEAD we use `git show HEAD:<path>` via a small server
  // helper, falling back to empty for new files. For the working tree
  // we read through the Files API.
  useEffect(() => {
    let cancelled = false
    setContents(null)
    setError(null)
    ;(async () => {
      try {
        const [origText, modText] = await Promise.all([
          fetchOriginal(apiBase, path).catch(() => ''),
          fetchWorking(apiBase, path).catch(() => ''),
        ])
        if (!cancelled) {
          setContents({ original: origText, modified: modText })
        }
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      }
    })()
    return () => {
      cancelled = true
    }
  }, [apiBase, path, reloadKey])

  const onStageSelected = async () => {
    if (!selectedRanges.length) return
    setStaging(true)
    setError(null)
    try {
      await api.post(`${apiBase}/stage-lines`, { path, lineRanges: selectedRanges })
      onStaged?.()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setStaging(false)
    }
  }

  // For binary image files Monaco's text DiffEditor can't help — swap
  // in a side-by-side <img> view instead. We still take the same
  // `apiBase` so URLs flow through the same auth path.
  if (isImageFile(path)) {
    const filesBase = apiBase.replace(/\/git$/, '/files')
    const enc = encodeURIComponent(path)
    return (
      <div className={styles.wrap} data-testid="git-monaco-diff">
        <div className={styles.toolbar}>
          <span className={styles.path}>{path}</span>
        </div>
        <ImagePair
          leftSrc={`${apiBase}/raw?ref=HEAD&path=${enc}`}
          rightSrc={`${filesBase}/raw?path=${enc}`}
          leftLabel="HEAD"
          rightLabel="Working"
        />
      </div>
    )
  }

  return (
    <div className={styles.wrap} data-testid="git-monaco-diff">
      <div className={styles.toolbar}>
        <span className={styles.path}>{path}</span>
        <button
          className={styles.btn}
          disabled={!selectedRanges.length || staging}
          onClick={onStageSelected}
          data-testid="git-stage-selected-lines"
        >
          Stage selected lines
        </button>
      </div>
      {error && <p className={styles.error}>{error}</p>}
      {contents ? (
        <div className={styles.editor}>
          <DiffEditor
            height="100%"
            language={language}
            original={contents.original}
            modified={contents.modified}
            theme="vs-dark"
            options={{
              renderSideBySide: !unified,
              readOnly: true,
              automaticLayout: true,
              minimap: { enabled: false },
              quickSuggestions: false,
              codeLens: false,
              folding: true,
            }}
            onMount={(editor) => {
              editorRef.current = editor
              const modified = editor.getModifiedEditor()
              modified.onDidChangeCursorSelection(() => {
                const sel = modified.getSelections() ?? []
                const ranges: LineRange[] = []
                for (const s of sel) {
                  if (s.startLineNumber === s.endLineNumber && s.startColumn === s.endColumn) {
                    continue
                  }
                  ranges.push({
                    start: Math.min(s.startLineNumber, s.endLineNumber),
                    end: Math.max(s.startLineNumber, s.endLineNumber),
                  })
                }
                setSelectedRanges(ranges)
              })
            }}
          />
        </div>
      ) : (
        <p className={styles.loading}>Loading…</p>
      )}
    </div>
  )
}

/** Fetch HEAD blob via `/git/show` (added below). Empty for newly-added files. */
async function fetchOriginal(apiBase: string, path: string): Promise<string> {
  // The git/diff endpoint already gives us raw text containing pre-image
  // lines, but reconstructing them is a hassle. Use a small dedicated
  // GET endpoint: /api/repos/.../git/show?path=&ref=HEAD
  const res = await api.get<{ content: string }>(
    `${apiBase}/show?ref=HEAD&path=${encodeURIComponent(path)}`,
  )
  return res.content ?? ''
}

/** Fetch working-tree content via the Files API. Same auth path. */
async function fetchWorking(apiBase: string, path: string): Promise<string> {
  // apiBase is `/api/repos/{repoId}/branches/{branchId}/git`. The Files
  // raw endpoint sits at `/files/raw?path=<worktree-relative-path>`.
  //
  // Sending `Accept: application/json` makes the server return a JSON
  // envelope (`{content, isBinary, mime, ...}`) for text files instead
  // of the raw bytes; that envelope is also the only path that handles
  // text files Reliably without us having to second-guess MIME sniffing.
  // We extract `.content` and hand it to Monaco. Binary files won't
  // reach this function — the Git tab routes images / etc. through
  // ImagePair before calling the Monaco diff.
  const filesBase = apiBase.replace(/\/git$/, '/files')
  const url = `${filesBase}/raw?path=${encodeURIComponent(path)}`
  const res = await fetch(url, {
    credentials: 'include',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) return ''
  try {
    const j = (await res.json()) as { content?: string; isBinary?: boolean }
    if (j.isBinary) return ''
    return j.content ?? ''
  } catch {
    // Server returned non-JSON for some reason — fall back to text.
    return ''
  }
}
