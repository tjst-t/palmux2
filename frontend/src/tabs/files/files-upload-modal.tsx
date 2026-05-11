// hotfix: single-button upload UX. The browser's `<input type=file>`
// element can do multi-file OR single-folder, but never both in one
// dialog — that's a hard constraint of the underlying file-picker
// API. To still let the user upload "multiple files + multiple
// folders, all at once", we wrap a drag-and-drop zone in a modal
// triggered by ONE Upload button. The webkitGetAsEntry API gives us
// FileSystemEntry objects for each dropped item; we recurse into
// directories ourselves.
//
// Click-based fallbacks (Choose files / Choose folder) stay in the
// modal for users on touch devices or who don't expect drag-and-drop.

import { useCallback, useEffect, useRef, useState } from 'react'

import { Modal } from '../../components/modal'

import styles from './files-upload-modal.module.css'

interface UploadItem {
  file: File
  /** Path relative to the upload root (e.g. `myproj/src/main.go`). */
  relativePath: string
}

interface Props {
  open: boolean
  /** Worktree-relative directory the items will be uploaded INTO.
   *  Just for display — the parent decides where the upload lands. */
  targetDir: string
  onClose: () => void
  onUpload: (items: UploadItem[]) => void
}

export function FilesUploadModal({ open, targetDir, onClose, onUpload }: Props) {
  const fileInputRef = useRef<HTMLInputElement>(null)
  const folderInputRef = useRef<HTMLInputElement>(null)
  const [dragActive, setDragActive] = useState(false)
  const [reading, setReading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const dragCounterRef = useRef(0)

  // Reset transient UI state when the modal closes.
  useEffect(() => {
    if (!open) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- prop-driven state sync (React 19 idiomatic exception)
      setDragActive(false)
      setReading(false)
      setError(null)
      dragCounterRef.current = 0
    }
  }, [open])

  const submit = useCallback(
    (items: UploadItem[]) => {
      if (items.length === 0) {
        setError('No files found in the selection.')
        return
      }
      onUpload(items)
      onClose()
    },
    [onClose, onUpload],
  )

  const onDragEnter = (e: React.DragEvent) => {
    e.preventDefault()
    dragCounterRef.current++
    if (dragCounterRef.current === 1) setDragActive(true)
  }
  const onDragLeave = (e: React.DragEvent) => {
    e.preventDefault()
    dragCounterRef.current = Math.max(0, dragCounterRef.current - 1)
    if (dragCounterRef.current === 0) setDragActive(false)
  }
  const onDragOver = (e: React.DragEvent) => {
    e.preventDefault()
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
  }
  const onDrop = async (e: React.DragEvent) => {
    e.preventDefault()
    setDragActive(false)
    dragCounterRef.current = 0
    setError(null)
    setReading(true)
    try {
      // Snapshot the items synchronously — the DataTransfer is gone
      // by the time the await chain resumes.
      const entries: FileSystemEntry[] = []
      for (const it of Array.from(e.dataTransfer.items)) {
        const entry = it.webkitGetAsEntry?.()
        if (entry) entries.push(entry)
      }
      const out: UploadItem[] = []
      await Promise.all(entries.map((en) => walkEntry(en, '', out)))
      submit(out)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setReading(false)
    }
  }

  const onFilePick = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? [])
    e.target.value = ''
    submit(files.map((f) => ({ file: f, relativePath: f.name })))
  }
  const onFolderPick = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? [])
    e.target.value = ''
    submit(
      files.map((f) => {
        const wf = f as File & { webkitRelativePath?: string }
        return { file: f, relativePath: wf.webkitRelativePath || f.name }
      }),
    )
  }

  return (
    <Modal open={open} onClose={onClose} title="Upload to Files" width={520}>
      <div className={styles.dest}>
        <span className={styles.destLabel}>Destination:</span>
        <code className={styles.destPath}>
          {targetDir === '' ? '/' : `/${targetDir}`}
        </code>
      </div>

      <div
        className={`${styles.dropZone} ${dragActive ? styles.dropZoneActive : ''}`}
        data-testid="files-upload-dropzone"
        onDragEnter={onDragEnter}
        onDragLeave={onDragLeave}
        onDragOver={onDragOver}
        onDrop={(e) => {
          void onDrop(e)
        }}
      >
        <div className={styles.dropTitle}>
          {reading ? 'Reading…' : 'Drop files & folders here'}
        </div>
        <div className={styles.dropHint}>
          Drag any mix of files and folders from your file manager. Folder
          structure is preserved.
        </div>
        <div className={styles.fallbackRow}>
          <button
            type="button"
            className={styles.fallbackBtn}
            onClick={() => fileInputRef.current?.click()}
            data-testid="files-upload-modal-files-btn"
            disabled={reading}
          >
            Choose files…
          </button>
          <button
            type="button"
            className={styles.fallbackBtn}
            onClick={() => folderInputRef.current?.click()}
            data-testid="files-upload-modal-folder-btn"
            disabled={reading}
          >
            Choose folder…
          </button>
        </div>
        {error && <div className={styles.error}>{error}</div>}
      </div>

      <div className={styles.foot}>
        <button
          type="button"
          className={styles.cancelBtn}
          onClick={onClose}
        >
          Cancel
        </button>
      </div>

      {/* Hidden inputs for the click-to-pick fallbacks. */}
      <input
        ref={fileInputRef}
        type="file"
        multiple
        style={{ display: 'none' }}
        data-testid="files-upload-files-input"
        onChange={onFilePick}
      />
      <input
        ref={folderInputRef}
        type="file"
        {...({ webkitdirectory: '', directory: '' } as Record<string, string>)}
        multiple
        style={{ display: 'none' }}
        data-testid="files-upload-folder-input"
        onChange={onFolderPick}
      />
    </Modal>
  )
}

// ──────────────────────────────────────────────────────────────────────
// FileSystemEntry recursion. The Drag-and-Drop API exposes entries for
// dropped items via webkitGetAsEntry(); for directories we read all
// children in batches via createReader().readEntries (which yields up
// to ~100 at a time and must be called in a loop until empty).

async function walkEntry(
  entry: FileSystemEntry,
  parentPath: string,
  out: UploadItem[],
): Promise<void> {
  if (entry.isFile) {
    const fe = entry as FileSystemFileEntry
    const file = await new Promise<File>((resolve, reject) => {
      fe.file(resolve, reject)
    })
    const rel = parentPath ? `${parentPath}/${entry.name}` : entry.name
    out.push({ file, relativePath: rel })
    return
  }
  if (entry.isDirectory) {
    const de = entry as FileSystemDirectoryEntry
    const children = await readAllEntries(de.createReader())
    const newParent = parentPath ? `${parentPath}/${entry.name}` : entry.name
    for (const c of children) {
      await walkEntry(c, newParent, out)
    }
  }
}

async function readAllEntries(
  reader: FileSystemDirectoryReader,
): Promise<FileSystemEntry[]> {
  const all: FileSystemEntry[] = []
  while (true) {
    const batch = await new Promise<FileSystemEntry[]>((resolve, reject) => {
      reader.readEntries(resolve, reject)
    })
    if (batch.length === 0) break
    all.push(...batch)
  }
  return all
}
