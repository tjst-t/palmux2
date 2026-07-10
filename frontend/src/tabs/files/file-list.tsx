// S033: Major overhaul of FileList to support:
// - S033-1: inline create row at end of list + bottom CTA strip (📄+ / 📁+)
// - S033-2: inline rename row + right-click context menu (via onContextMenu prop)
// - S033-3: multi-select (tinted bg + accent left border, no checkboxes)
// - S033-3: touch long-press → select mode

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { useLongPress } from '../../hooks/use-long-press'
import styles from './file-list.module.css'
import type { Entry } from './types'

type CreateKind = 'file' | 'folder'

// S4323c8-1: sort control — key + direction, persisted device-locally.
type SortKey = 'name' | 'modTime' | 'size'
type SortDir = 'asc' | 'desc'

interface SortPref {
  key: SortKey
  dir: SortDir
}

const SORT_PREF_KEY = 'palmux:files:sort'
const DEFAULT_SORT_PREF: SortPref = { key: 'name', dir: 'asc' }

function readSortPref(): SortPref {
  try {
    const raw = window.localStorage.getItem(SORT_PREF_KEY)
    if (!raw) return DEFAULT_SORT_PREF
    const parsed = JSON.parse(raw) as Partial<SortPref>
    const key: SortKey = parsed.key === 'modTime' || parsed.key === 'size' ? parsed.key : 'name'
    const dir: SortDir = parsed.dir === 'desc' ? 'desc' : 'asc'
    return { key, dir }
  } catch {
    return DEFAULT_SORT_PREF
  }
}

function writeSortPref(pref: SortPref): void {
  try {
    window.localStorage.setItem(SORT_PREF_KEY, JSON.stringify(pref))
  } catch {
    // ignore — localStorage may be disabled in private mode
  }
}

function compareEntries(a: Entry, b: Entry, key: SortKey, dir: SortDir): number {
  let cmp: number
  switch (key) {
    case 'modTime': {
      const at = +new Date(a.modTime)
      const bt = +new Date(b.modTime)
      cmp = (Number.isNaN(at) ? 0 : at) - (Number.isNaN(bt) ? 0 : bt)
      break
    }
    case 'size':
      cmp = a.size - b.size
      break
    default:
      cmp = a.name.localeCompare(b.name)
      break
  }
  if (cmp === 0) cmp = a.name.localeCompare(b.name)
  return dir === 'desc' ? -cmp : cmp
}

/** Folders stay grouped above files; the chosen sort applies within each group. */
function sortEntries(entries: Entry[], key: SortKey, dir: SortDir): Entry[] {
  const dirs = entries.filter((e) => e.isDir)
  const files = entries.filter((e) => !e.isDir)
  dirs.sort((a, b) => compareEntries(a, b, key, dir))
  files.sort((a, b) => compareEntries(a, b, key, dir))
  return [...dirs, ...files]
}

interface Props {
  entries: Entry[]
  selected?: string
  onPick: (entry: Entry) => void
  /** S011-1-6: unsaved buffer paths */
  dirtyPaths?: string[]

  // S033-3: multi-select
  selectedPaths: Set<string>
  onSelectionChange: (paths: Set<string>) => void
  touchSelectMode: boolean
  onTouchSelectMode: (on: boolean) => void

  // S033-1: inline create
  createKind: CreateKind | null
  createValue: string
  createError: string | null
  createBusy: boolean
  onCreateValueChange: (v: string) => void
  onCreateSubmit: () => void
  onCreateCancel: () => void

  // S033-2: inline rename
  renameTarget: string | null   // path being renamed
  renameValue: string
  renameError: string | null
  renameBusy: boolean
  onRenameValueChange: (v: string) => void
  onRenameSubmit: () => void
  onRenameCancel: () => void

  // S033-2: context menu
  onContextMenu: (e: React.MouseEvent, entry: Entry) => void
  contextMenuTarget?: string    // path with context-open highlight

  // hotfix: upload — clicked → parent opens the upload modal which
  // accepts any mix of files and folders (drag-drop + click fallbacks
  // inside the modal).
  onOpenUpload: () => void
}

function fmtSize(n: number): string {
  if (n < 1024) return `${n}`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}K`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)}M`
  return `${(n / 1024 / 1024 / 1024).toFixed(1)}G`
}

function fmtDate(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(+d)) return ''
  return d.toLocaleDateString(undefined, { year: 'numeric', month: '2-digit', day: '2-digit' })
}

function iconFor(name: string): string {
  const ext = name.split('.').pop()?.toLowerCase() ?? ''
  switch (ext) {
    case 'md':
    case 'markdown':
      return '📝'
    case 'png':
    case 'jpg':
    case 'jpeg':
    case 'gif':
    case 'svg':
    case 'webp':
      return '🖼'
    case 'go':
      return '🐹'
    case 'ts':
    case 'tsx':
    case 'js':
    case 'jsx':
      return '🟨'
    case 'json':
      return '📋'
    case 'css':
      return '🎨'
    default:
      return '📄'
  }
}

interface RowProps {
  entry: Entry
  isSelected: boolean
  isMultiSelected: boolean
  isRenaming: boolean
  isContextOpen: boolean
  dirty: boolean
  touchSelectMode: boolean
  anchorPathRef: React.MutableRefObject<string | null>
  allEntries: Entry[]
  selectedPaths: Set<string>
  onPick: (e: Entry) => void
  onSelectionChange: (s: Set<string>) => void
  onTouchSelectMode: (on: boolean) => void
  onContextMenu: (e: React.MouseEvent, entry: Entry) => void
  // rename
  renameValue: string
  renameError: string | null
  renameBusy: boolean
  onRenameValueChange: (v: string) => void
  onRenameSubmit: () => void
  onRenameCancel: () => void
}

function FileRow({
  entry,
  isSelected,
  isMultiSelected,
  isRenaming,
  isContextOpen,
  dirty,
  touchSelectMode,
  anchorPathRef,
  allEntries,
  selectedPaths,
  onPick,
  onSelectionChange,
  onTouchSelectMode,
  onContextMenu,
  renameValue,
  renameError,
  renameBusy,
  onRenameValueChange,
  onRenameSubmit,
  onRenameCancel,
}: RowProps) {
  const renameInputRef = useRef<HTMLInputElement>(null)

  // Select-all-on-mount + pre-select the extension boundary.
  useEffect(() => {
    if (!isRenaming || !renameInputRef.current) return
    const input = renameInputRef.current
    input.focus()
    const dotIdx = entry.name.lastIndexOf('.')
    if (dotIdx > 0 && !entry.isDir) {
      input.setSelectionRange(0, dotIdx)
    } else {
      input.select()
    }
  }, [isRenaming, entry.name, entry.isDir])

  const longPress = useLongPress(
    useCallback(() => {
      // Long press on touch → enter select mode and select this item.
      if (!touchSelectMode) {
        onTouchSelectMode(true)
        anchorPathRef.current = entry.path
        const next = new Set(selectedPaths)
        next.add(entry.path)
        onSelectionChange(next)
      }
    }, [touchSelectMode, entry.path, selectedPaths, onSelectionChange, onTouchSelectMode, anchorPathRef]),
  )

  const handleClick = useCallback(
    (e: React.MouseEvent) => {
      if (touchSelectMode) {
        // In touch select mode, tap toggles.
        const next = new Set(selectedPaths)
        if (next.has(entry.path)) {
          next.delete(entry.path)
        } else {
          next.add(entry.path)
        }
        anchorPathRef.current = entry.path
        onSelectionChange(next)
        return
      }

      const isMac = navigator.platform.toLowerCase().includes('mac')
      const metaOrCtrl = isMac ? e.metaKey : e.ctrlKey

      if (metaOrCtrl) {
        // Toggle this item.
        const next = new Set(selectedPaths)
        if (next.has(entry.path)) {
          next.delete(entry.path)
        } else {
          next.add(entry.path)
          anchorPathRef.current = entry.path
        }
        onSelectionChange(next)
        return
      }

      if (e.shiftKey && anchorPathRef.current) {
        // Range select from anchor to this item.
        const anchorIdx = allEntries.findIndex((x) => x.path === anchorPathRef.current)
        const thisIdx = allEntries.findIndex((x) => x.path === entry.path)
        if (anchorIdx !== -1 && thisIdx !== -1) {
          const lo = Math.min(anchorIdx, thisIdx)
          const hi = Math.max(anchorIdx, thisIdx)
          const next = new Set(selectedPaths)
          for (let i = lo; i <= hi; i++) {
            next.add(allEntries[i].path)
          }
          onSelectionChange(next)
          return
        }
      }

      // hotfix: VS Code-style — plain click SELECTS the row (single,
      // replaces previous multi-selection) AND opens it. Anchor is
      // set so a subsequent Cmd-click extends the selection from THIS
      // item, not from an empty set. Without this, m1-click → m2-Cmd
      // -click → m3-Cmd-click ended up as {m2, m3} instead of
      // {m1, m2, m3}.
      onSelectionChange(new Set([entry.path]))
      anchorPathRef.current = entry.path
      onPick(entry)
    },
    [touchSelectMode, entry, selectedPaths, allEntries, anchorPathRef, onPick, onSelectionChange],
  )

  // Compute CSS classes for the row button.
  const rowClass = [
    styles.row,
    isSelected && !isMultiSelected ? styles.active : '',
    isMultiSelected ? styles.multiSelected : '',
    isContextOpen ? styles.contextOpen : '',
  ]
    .filter(Boolean)
    .join(' ')

  if (isRenaming) {
    return (
      <li>
        <div className={styles.inlineRow} data-testid="files-inline-rename">
          <span className={styles.icon}>{entry.isDir ? '📁' : iconFor(entry.name)}</span>
          <input
            ref={renameInputRef}
            className={styles.inlineInput}
            type="text"
            value={renameValue}
            onChange={(e) => onRenameValueChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                onRenameSubmit()
              } else if (e.key === 'Escape') {
                e.preventDefault()
                onRenameCancel()
              }
            }}
            disabled={renameBusy}
            data-testid="files-inline-rename-input"
          />
        </div>
        <div className={`${styles.inlineHint} ${renameError ? styles.inlineHintError : ''}`}>
          {renameError
            ? renameError
            : <>Renaming <code>{entry.name}</code> · <span className={styles.inlineHintKbd}>↵</span> save <span className={styles.inlineHintKbd}>Esc</span> cancel</>
          }
        </div>
      </li>
    )
  }

  return (
    <li>
      <button
        className={rowClass}
        onClick={handleClick}
        onContextMenu={(e) => {
          e.preventDefault()
          onContextMenu(e, entry)
        }}
        title={entry.path}
        data-dirty={dirty ? 'true' : undefined}
        {...longPress}
      >
        <span className={styles.icon}>{entry.isDir ? '📁' : iconFor(entry.name)}</span>
        <span className={styles.name}>
          {entry.name}
          {dirty && (
            <span className={styles.dirtyDot} data-testid="file-dirty-dot" title="Unsaved changes">
              {' '}●
            </span>
          )}
        </span>
        <span className={styles.meta}>{entry.isDir ? '' : fmtSize(entry.size)}</span>
        <span className={styles.meta}>{fmtDate(entry.modTime)}</span>
      </button>
    </li>
  )
}

export function FileList({
  entries,
  selected,
  onPick,
  dirtyPaths,
  selectedPaths,
  onSelectionChange,
  touchSelectMode,
  onTouchSelectMode,
  createKind,
  createValue,
  createError,
  createBusy,
  onCreateValueChange,
  onCreateSubmit,
  onCreateCancel,
  renameTarget,
  renameValue,
  renameError,
  renameBusy,
  onRenameValueChange,
  onRenameSubmit,
  onRenameCancel,
  onContextMenu,
  contextMenuTarget,
  onOpenUpload,
}: Props) {
  const dirtySet = useMemo(() => new Set(dirtyPaths ?? []), [dirtyPaths])
  const anchorPathRef = useRef<string | null>(null)
  const createInputRef = useRef<HTMLInputElement>(null)

  // S4323c8-1: sort control — key + direction, persisted to localStorage
  // (`palmux:` prefix, device-local like the git-view sidebar width pref)
  // and restored on mount so a reload keeps the user's chosen order.
  const [sortPref, setSortPref] = useState<SortPref>(() => readSortPref())
  const setSortKey = useCallback((key: SortKey) => {
    setSortPref((prev) => {
      const next = { ...prev, key }
      writeSortPref(next)
      return next
    })
  }, [])
  const toggleSortDir = useCallback(() => {
    setSortPref((prev) => {
      const next: SortPref = { ...prev, dir: prev.dir === 'asc' ? 'desc' : 'asc' }
      writeSortPref(next)
      return next
    })
  }, [])

  // [AC-S4323c8-1-2] folders stay grouped above files; the chosen sort
  // key/direction only reorders within each group.
  const sortedEntries = useMemo(
    () => sortEntries(entries, sortPref.key, sortPref.dir),
    [entries, sortPref.key, sortPref.dir],
  )

  // Auto-focus the create row input when it mounts.
  useEffect(() => {
    if (createKind && createInputRef.current) {
      createInputRef.current.focus()
    }
  }, [createKind])

  const isEmpty = sortedEntries.length === 0 && !createKind

  return (
    <div className={styles.container}>
      {/* [AC-S4323c8-1-1] sort control — key (name/modTime/size) + direction */}
      <div className={styles.sortBar} data-testid="files-sort-bar">
        <label className={styles.sortLabel} htmlFor="files-sort-key-select">
          Sort
        </label>
        <select
          id="files-sort-key-select"
          className={styles.sortSelect}
          value={sortPref.key}
          onChange={(e) => setSortKey(e.target.value as SortKey)}
          data-testid="files-sort-key"
        >
          <option value="name">Name</option>
          <option value="modTime">Modified</option>
          <option value="size">Size</option>
        </select>
        <button
          type="button"
          className={styles.sortDirBtn}
          onClick={toggleSortDir}
          aria-label={sortPref.dir === 'asc' ? 'Sort ascending' : 'Sort descending'}
          title={sortPref.dir === 'asc' ? 'Ascending' : 'Descending'}
          data-testid="files-sort-dir"
          data-dir={sortPref.dir}
        >
          {sortPref.dir === 'asc' ? '↑' : '↓'}
        </button>
      </div>

      {isEmpty && <p className={styles.empty}>(empty directory)</p>}

      {!isEmpty && (
        <ul className={styles.list} data-testid="files-list">
          {sortedEntries.map((e) => (
            <FileRow
              key={e.path}
              entry={e}
              isSelected={selected === e.path}
              isMultiSelected={selectedPaths.has(e.path)}
              isRenaming={renameTarget === e.path}
              isContextOpen={contextMenuTarget === e.path}
              dirty={!e.isDir && dirtySet.has(e.path)}
              touchSelectMode={touchSelectMode}
              anchorPathRef={anchorPathRef}
              allEntries={sortedEntries}
              selectedPaths={selectedPaths}
              onPick={onPick}
              onSelectionChange={onSelectionChange}
              onTouchSelectMode={onTouchSelectMode}
              onContextMenu={onContextMenu}
              renameValue={renameValue}
              renameError={renameError}
              renameBusy={renameBusy}
              onRenameValueChange={onRenameValueChange}
              onRenameSubmit={onRenameSubmit}
              onRenameCancel={onRenameCancel}
            />
          ))}

          {/* S033-1: inline create row at END of listing */}
          {createKind && (
            <li>
              <div className={styles.inlineRow} data-testid={createKind === 'file' ? 'files-inline-new-file' : 'files-inline-new-folder'}>
                <span className={styles.icon}>{createKind === 'folder' ? '📁' : '📄'}</span>
                <input
                  ref={createInputRef}
                  className={styles.inlineInput}
                  type="text"
                  value={createValue}
                  onChange={(e) => onCreateValueChange(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      onCreateSubmit()
                    } else if (e.key === 'Escape') {
                      e.preventDefault()
                      onCreateCancel()
                    }
                  }}
                  disabled={createBusy}
                  placeholder={createKind === 'folder' ? 'folder-name/' : 'filename.txt'}
                  data-testid={createKind === 'file' ? 'files-new-file-input' : 'files-new-folder-input'}
                />
              </div>
              <div className={`${styles.inlineHint} ${createError ? styles.inlineHintError : ''}`}>
                {createError
                  ? createError
                  : createKind === 'folder'
                    ? <><span className={styles.inlineHintKbd}>↵</span> create folder <span className={styles.inlineHintKbd}>Esc</span> cancel</>
                    : <><span className={styles.inlineHintKbd}>↵</span> create file <span className={styles.inlineHintKbd}>Esc</span> cancel</>
                }
              </div>
            </li>
          )}
        </ul>
      )}

      {/* S033-1: compact icon CTA strip at bottom of list pane */}
      <div className={styles.ctaStrip} data-testid="files-list-ctas">
        {/* hotfix: single Upload button → modal that accepts any mix of
            files & folders (drag-drop + click fallbacks). The browser's
            <input type=file> can do multi-file XOR single-folder, never
            both, so the modal owns this UX instead. */}
        <button
          className={styles.ctaBtn}
          data-tip="Upload files & folders"
          aria-label="Upload files and folders"
          disabled={!!createKind || !!renameTarget}
          onClick={onOpenUpload}
          data-testid="files-upload-btn"
        >
          <span className={styles.ctaGlyph}>📤</span>
        </button>
        <button
          className={styles.ctaBtn}
          data-tip="New file"
          aria-label="New file"
          disabled={!!createKind || !!renameTarget}
          onClick={() => onCreateValueChange('\x01new-file')} /* sentinel handled by parent */
          data-testid="files-new-file-btn"
        >
          <span className={styles.ctaGlyph}>📄</span>
          <span className={styles.ctaPlus}>+</span>
        </button>
        <button
          className={styles.ctaBtn}
          data-tip="New folder"
          aria-label="New folder"
          disabled={!!createKind || !!renameTarget}
          onClick={() => onCreateValueChange('\x02new-folder')} /* sentinel handled by parent */
          data-testid="files-new-folder-btn"
        >
          <span className={styles.ctaGlyph}>📁</span>
          <span className={styles.ctaPlus}>+</span>
        </button>
      </div>
    </div>
  )
}
