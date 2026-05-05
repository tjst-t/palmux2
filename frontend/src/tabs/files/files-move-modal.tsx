// S033-4-3: Move modal with directory incremental completion.
// - Single-item: input is full destination path (basename can change)
// - Batch: input is target directory only (basenames preserved)
// Uses debounced ?type=dir search for directory completion.

import { useCallback, useEffect, useRef, useState } from 'react'

import { Modal } from '../../components/modal'
import { api } from '../../lib/api'
import styles from './files-move-modal.module.css'
import type { Entry } from './types'

interface Props {
  items: Entry[]
  apiBase: string
  onClose: () => void
  onCompleted: () => void
}

interface DirEntry {
  name: string
  path: string
  isDir: boolean
}

function basename(p: string): string {
  return p.replace(/\/$/, '').split('/').pop() ?? p
}

function highlightMatch(text: string, query: string): React.ReactNode {
  if (!query) return text
  const idx = text.toLowerCase().indexOf(query.toLowerCase())
  if (idx === -1) return text
  return (
    <>
      {text.slice(0, idx)}
      <mark className={styles.match}>{text.slice(idx, idx + query.length)}</mark>
      {text.slice(idx + query.length)}
    </>
  )
}

export function FilesMoveModal({ items, apiBase, onClose, onCompleted }: Props) {
  const isBatch = items.length > 1
  const [inputVal, setInputVal] = useState(() => {
    if (isBatch) return ''
    // Single: pre-fill with the item's parent dir, so user types new basename
    const p = items[0].path
    const slash = p.lastIndexOf('/')
    return slash === -1 ? '' : p.slice(0, slash + 1)
  })
  const [completions, setCompletions] = useState<DirEntry[]>([])
  const [activeIdx, setActiveIdx] = useState(-1)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const debounceRef = useRef<number | null>(null)

  // Derived preview: what each item's destination path will be.
  // hotfix: a trailing `/` in single-item mode means "move into this
  // directory, keep the basename" — same behaviour as `mv foo bar/`
  // in shell. Without this, `to: "test/"` was silently stripped to
  // `to: "test"` and the backend tried to rename hogehoge → test,
  // colliding with the existing directory.
  const getDestPath = (item: Entry): string => {
    if (!isBatch) {
      const t = inputVal.trim()
      if (!t) return item.path
      if (t.endsWith('/')) return `${t}${basename(item.path)}`
      return t
    }
    // Batch: target dir + basename
    const dir = inputVal.trim().replace(/\/$/, '')
    return dir ? `${dir}/${basename(item.path)}` : item.path
  }

  // Debounced directory search for completions.
  useEffect(() => {
    if (debounceRef.current !== null) window.clearTimeout(debounceRef.current)
    const q = inputVal.trim()
    if (!q) {
      setCompletions([])
      return
    }
    // For batch, search dir part; for single, search the last segment of input
    // that could be a directory prefix.
    const searchQuery = isBatch ? q : (q.includes('/') ? q.split('/').pop() ?? q : q)
    debounceRef.current = window.setTimeout(async () => {
      try {
        const result = await api.get<{ results: DirEntry[] }>(
          `${apiBase}/search?type=dir&query=${encodeURIComponent(searchQuery)}&max=20`,
        )
        setCompletions(result.results ?? [])
        setActiveIdx(-1)
      } catch {
        setCompletions([])
      }
    }, 80)
    return () => {
      if (debounceRef.current !== null) window.clearTimeout(debounceRef.current)
    }
  }, [inputVal, apiBase, isBatch])

  const acceptCompletion = useCallback(
    (entry: DirEntry) => {
      if (isBatch) {
        setInputVal(entry.path + '/')
      } else {
        // Single: replace the directory part, keep whatever basename the user typed
        const cur = inputVal.trim()
        const slash = cur.lastIndexOf('/')
        const curBase = slash === -1 ? cur : cur.slice(slash + 1)
        setInputVal(entry.path + '/' + curBase)
      }
      setCompletions([])
      inputRef.current?.focus()
    },
    [inputVal, isBatch],
  )

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (completions.length === 0) return
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setActiveIdx((i) => Math.min(i + 1, completions.length - 1))
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        setActiveIdx((i) => Math.max(i - 1, -1))
      } else if (e.key === 'Tab') {
        e.preventDefault()
        const pick = activeIdx >= 0 ? completions[activeIdx] : completions[0]
        if (pick) acceptCompletion(pick)
      }
    },
    [completions, activeIdx, acceptCompletion],
  )

  const handleSubmit = useCallback(async () => {
    const target = inputVal.trim()
    if (!target) return
    setBusy(true)
    setError(null)
    try {
      if (isBatch) {
        await api.post<unknown>(`${apiBase}/move`, {
          paths: items.map((e) => e.path),
          target: target.replace(/\/$/, ''),
        })
      } else {
        // hotfix: trailing `/` → move into directory, preserve basename
        // (shell `mv foo bar/` semantics). Otherwise the backend would
        // rename foo → bar and collide with the existing directory.
        const toPath = target.endsWith('/')
          ? `${target}${basename(items[0].path)}`
          : target
        await api.post<unknown>(`${apiBase}/move`, {
          from: items[0].path,
          to: toPath,
        })
      }
      onCompleted()
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }, [inputVal, isBatch, items, apiBase, onCompleted, onClose])

  const handleFormKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Enter' && completions.length === 0) {
        e.preventDefault()
        void handleSubmit()
      } else if (e.key === 'Enter' && activeIdx >= 0 && completions[activeIdx]) {
        e.preventDefault()
        acceptCompletion(completions[activeIdx])
      }
    },
    [completions, activeIdx, acceptCompletion, handleSubmit],
  )

  const count = items.length
  const title = isBatch
    ? `Move ${count} items to a new directory`
    : `Move "${items[0].name}"`
  const sub = isBatch
    ? 'All selected items are moved into the chosen directory, keeping their basenames. Directory completions appear below as you type.'
    : 'Enter the full destination path (directory + new basename). Directory completions appear below as you type.'

  return (
    <Modal open onClose={onClose} width={640}>
      <div data-testid="files-move-modal">
        <div className={styles.modalHeader}>
          <h2 className={styles.title}>{title}</h2>
          <p className={styles.sub}>{sub}</p>
        </div>

        <div className={styles.field}>
          <label className={styles.label} htmlFor="files-move-input">
            {isBatch ? 'Target directory (repo-root-relative)' : 'Destination path (repo-root-relative)'}
          </label>
          <input
            id="files-move-input"
            ref={inputRef}
            autoFocus
            className={styles.input}
            type="text"
            value={inputVal}
            onChange={(e) => {
              setInputVal(e.target.value)
              setError(null)
            }}
            onKeyDown={(e) => {
              handleKeyDown(e)
              handleFormKeyDown(e)
            }}
            disabled={busy}
            data-testid="files-move-input"
            placeholder={isBatch ? 'e.g. src/widgets' : 'e.g. src/widgets/my-component.tsx'}
          />

          {completions.length > 0 && (
            <ul className={styles.completionList} data-testid="files-move-completion">
              {completions.map((c, i) => (
                <li key={c.path}>
                  <button
                    className={i === activeIdx ? `${styles.completionRow} ${styles.completionRowActive}` : styles.completionRow}
                    type="button"
                    onClick={() => acceptCompletion(c)}
                    tabIndex={-1}
                  >
                    <span className={styles.completionIcon}>📁</span>
                    <span>{highlightMatch(c.path + '/', inputVal.trim().split('/').pop() ?? '')}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
          {inputVal.trim() && completions.length === 0 && (
            <p className={styles.noMatches}>No directory matches — will create at this path if it exists</p>
          )}
        </div>

        {/* FROM → TO live preview */}
        {inputVal.trim() && (
          <div className={styles.movePreview} data-testid="files-move-preview">
            {items.map((item) => (
              <div key={item.path} className={styles.previewRow}>
                <div className={styles.previewFrom}>
                  <span className={styles.previewLabel}>FROM</span>
                  <span className={styles.previewPathFrom}>{item.path}</span>
                </div>
                <div className={styles.previewTo}>
                  <span className={styles.previewLabel}>TO</span>
                  <span className={styles.previewPathTo}>{getDestPath(item)}</span>
                </div>
              </div>
            ))}
          </div>
        )}

        {error && <div className={styles.error}>{error}</div>}

        <div className={styles.footer}>
          <span className={styles.kbdHint}>
            <span className={styles.kbd}>↑</span><span className={styles.kbd}>↓</span> navigate ·{' '}
            <span className={styles.kbd}>Tab</span> accept ·{' '}
            <span className={styles.kbd}>↵</span> move
          </span>
          <button className={styles.btnGhost} onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button
            className={styles.btnPrimary}
            onClick={() => void handleSubmit()}
            disabled={busy || !inputVal.trim()}
            data-testid="files-move-confirm"
          >
            {busy ? 'Moving…' : `Move ${count === 1 ? '1 item' : `${count} items`}`}
          </button>
        </div>
      </div>
    </Modal>
  )
}
