// S033: Major overhaul of FileList to support:
// - S033-1: inline create row at end of list + bottom CTA strip (📄+ / 📁+)
// - S033-2: inline rename row + right-click context menu (via onContextMenu prop)
// - S033-3: multi-select (tinted bg + accent left border, no checkboxes)
// - S033-3: touch long-press → select mode

import { useCallback, useEffect, useMemo, useRef } from 'react'

import { useLongPress } from '../../hooks/use-long-press'
import styles from './file-list.module.css'
import type { Entry } from './types'

type CreateKind = 'file' | 'folder'

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
  anchorPath: React.MutableRefObject<string | null>
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
  anchorPath,
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
        anchorPath.current = entry.path
        const next = new Set(selectedPaths)
        next.add(entry.path)
        onSelectionChange(next)
      }
    }, [touchSelectMode, entry.path, selectedPaths, onSelectionChange, onTouchSelectMode, anchorPath]),
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
        anchorPath.current = entry.path
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
          anchorPath.current = entry.path
        }
        onSelectionChange(next)
        return
      }

      if (e.shiftKey && anchorPath.current) {
        // Range select from anchor to this item.
        const anchorIdx = allEntries.findIndex((x) => x.path === anchorPath.current)
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

      // Normal click: open + clear multi-select.
      if (selectedPaths.size > 0) {
        onSelectionChange(new Set())
      }
      anchorPath.current = entry.path
      onPick(entry)
    },
    [touchSelectMode, entry, selectedPaths, allEntries, anchorPath, onPick, onSelectionChange],
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
}: Props) {
  const dirtySet = useMemo(() => new Set(dirtyPaths ?? []), [dirtyPaths])
  const anchorPath = useRef<string | null>(null)
  const createInputRef = useRef<HTMLInputElement>(null)

  // Auto-focus the create row input when it mounts.
  useEffect(() => {
    if (createKind && createInputRef.current) {
      createInputRef.current.focus()
    }
  }, [createKind])

  const isEmpty = entries.length === 0 && !createKind

  return (
    <div className={styles.container}>
      {isEmpty && <p className={styles.empty}>(empty directory)</p>}

      {!isEmpty && (
        <ul className={styles.list} data-testid="files-list">
          {entries.map((e) => (
            <FileRow
              key={e.path}
              entry={e}
              isSelected={selected === e.path}
              isMultiSelected={selectedPaths.has(e.path)}
              isRenaming={renameTarget === e.path}
              isContextOpen={contextMenuTarget === e.path}
              dirty={!e.isDir && dirtySet.has(e.path)}
              touchSelectMode={touchSelectMode}
              anchorPath={anchorPath}
              allEntries={entries}
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
