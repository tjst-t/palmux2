import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'

import { Divider } from '../../components/divider'
import { api } from '../../lib/api'
import type { TabViewProps } from '../../lib/tab-registry'
import { isDirty as isDirtyFn, makeEditorKey, useEditorStore } from '../../stores/editor-store'
import { usePalmuxStore } from '../../stores/palmux-store'

import { Breadcrumb } from './breadcrumb'
import { FileList } from './file-list'
import { FilePreview } from './file-preview'
import { FileSearch } from './file-search'
import styles from './files-view.module.css'
import type { Entry } from './types'

interface DirResponse {
  path: string
  entries: Entry[] | null
}

export function FilesView({ repoId, branchId, tabId }: TabViewProps) {
  // The URL is the source of truth for navigation when this Files view
  // matches the active panel. The path after `/files/` is treated as a
  // *resource path*: if it points at a directory we list it; if it points
  // at a file we list its parent directory and select the file in the
  // preview pane. `?line=N` jumps to a line inside the selected file.
  // Right-panel Files views (whose target lives in `?right`) can't share
  // that URL space, so they fall back to local state.
  const params = useParams()
  const location = useLocation()
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const isUrlPanel = params.repoId === repoId && params.branchId === branchId

  const splat = (params['*'] ?? '').replace(/^\/+|\/+$/g, '')
  const lineQuery = parseInt(searchParams.get('line') ?? '', 10)

  const [localPath, setLocalPath] = useState('')
  const [localSelected, setLocalSelected] = useState<string | null>(null)
  const [localLine, setLocalLine] = useState<number | undefined>(undefined)

  // Resolved state derived from the URL splat for the URL panel. After
  // the listDir probe we know whether splat was a directory (resolvedDir
  // = splat, resolvedSelected = null) or a file (resolvedDir = parent,
  // resolvedSelected = splat).
  const [resolvedDir, setResolvedDir] = useState('')
  const [resolvedSelected, setResolvedSelected] = useState<string | null>(null)

  const [entries, setEntries] = useState<Entry[]>([])
  const [error, setError] = useState<string | null>(null)
  const [searchOpen, setSearchOpen] = useState(false)

  // hotfix: VS Code-style "+ New file" affordance — small inline input
  // anchored to the header. Bumping refreshTick re-runs the dir-fetch
  // useEffects so the freshly-created file shows up in the listing.
  const [newFileOpen, setNewFileOpen] = useState(false)
  const [newFileName, setNewFileName] = useState('')
  const [newFileError, setNewFileError] = useState<string | null>(null)
  const [newFileBusy, setNewFileBusy] = useState(false)
  const [refreshTick, setRefreshTick] = useState(0)
  const bodyRef = useRef<HTMLDivElement | null>(null)
  const listRatio = usePalmuxStore((s) => s.deviceSettings.filesListRatio)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)

  const apiBase = useMemo(
    () => `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/files`,
    [repoId, branchId],
  )

  // S011-1-6: dirty paths inside this branch — used by the FileList
  // to decorate filenames with a `●` badge.
  // We subscribe to the entries map and derive the array via useMemo
  // so the selector returns a stable identity (Zustand's default
  // selector compares with Object.is — returning a fresh array each
  // tick would loop the renderer).
  const entriesMap = useEditorStore((s) => s.entries)
  const dirtyPaths = useMemo(() => {
    const prefix = `${repoId}/${branchId}/`
    const out: string[] = []
    for (const k of Object.keys(entriesMap)) {
      if (!k.startsWith(prefix)) continue
      const e = entriesMap[k]
      if (e.draft != null && e.pristine != null && e.draft !== e.pristine) {
        out.push(k.slice(prefix.length))
      }
    }
    return out
  }, [entriesMap, repoId, branchId])

  const path = isUrlPanel ? resolvedDir : localPath
  const selected = isUrlPanel ? resolvedSelected : localSelected
  const selectedLine = isUrlPanel
    ? Number.isFinite(lineQuery) && lineQuery > 0
      ? lineQuery
      : undefined
    : localLine

  const goToDir = useCallback(
    (next: string) => {
      const cleaned = next.replace(/^\/+|\/+$/g, '')
      if (!isUrlPanel) {
        setLocalPath(cleaned)
        setLocalSelected(null)
        return
      }
      const base = `/${encodeURIComponent(repoId)}/${encodeURIComponent(branchId)}/${encodeURIComponent(tabId)}`
      const tail = cleaned ? `/${cleaned.split('/').map(encodeURIComponent).join('/')}` : ''
      // Drop ?line — it's tied to the previous selection.
      const sp = new URLSearchParams(location.search)
      sp.delete('line')
      const search = sp.toString()
      navigate(`${base}${tail}${search ? '?' + search : ''}`)
    },
    [isUrlPanel, repoId, branchId, tabId, location.search, navigate],
  )

  const selectFile = useCallback(
    (filePath: string | null, lineNum?: number) => {
      // S011-1-8: if the currently-selected file is dirty, confirm
      // before swapping. Cancel keeps the user on the existing buffer.
      const cur = isUrlPanel ? resolvedSelected : localSelected
      if (cur && cur !== filePath) {
        const key = makeEditorKey(repoId, branchId, cur)
        if (isDirtyFn(useEditorStore.getState(), key)) {
          const ok = window.confirm(
            `You have unsaved changes in ${cur}. Discard them and switch files?`,
          )
          if (!ok) return
          // User accepted — drop the draft so the next visit shows
          // pristine content.
          useEditorStore.getState().clearDraft(key)
        }
      }
      if (!isUrlPanel) {
        setLocalSelected(filePath)
        setLocalLine(lineNum)
        return
      }
      if (!filePath) {
        // Clearing the selection navigates back to the listed directory.
        goToDir(path)
        return
      }
      const base = `/${encodeURIComponent(repoId)}/${encodeURIComponent(branchId)}/${encodeURIComponent(tabId)}`
      const tail = `/${filePath.split('/').map(encodeURIComponent).join('/')}`
      const sp = new URLSearchParams(location.search)
      if (lineNum && lineNum > 0) sp.set('line', String(lineNum))
      else sp.delete('line')
      const search = sp.toString()
      navigate(`${base}${tail}${search ? '?' + search : ''}`)
    },
    [isUrlPanel, repoId, branchId, tabId, location.search, navigate, goToDir, path, resolvedSelected, localSelected],
  )

  // Resolve splat → (listed dir, selected file) and fetch the dir listing.
  // We try splat as a directory first; the API answers "not a directory"
  // for files, in which case we re-fetch the parent and treat splat as the
  // selected file.
  useEffect(() => {
    if (!isUrlPanel) return
    let cancelled = false
    setError(null)
    ;(async () => {
      try {
        const dir = await api.get<DirResponse>(`${apiBase}?path=${encodeURIComponent(splat)}`)
        if (!cancelled) {
          setResolvedDir(splat)
          setResolvedSelected(null)
          setEntries(dir.entries ?? [])
        }
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err)
        // The listDir handler returns 500 with "not a directory" when the
        // path points at a file. Treat that as the "select this file" case.
        if (msg.includes('not a directory')) {
          const parent = dirnameOf(splat)
          try {
            const dir = await api.get<DirResponse>(`${apiBase}?path=${encodeURIComponent(parent)}`)
            if (!cancelled) {
              setResolvedDir(parent)
              setResolvedSelected(splat)
              setEntries(dir.entries ?? [])
            }
          } catch (err2) {
            if (!cancelled) {
              setEntries([])
              setError(err2 instanceof Error ? err2.message : String(err2))
            }
          }
          return
        }
        if (!cancelled) {
          setEntries([])
          setError(msg)
          setResolvedDir(splat)
          setResolvedSelected(null)
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [isUrlPanel, apiBase, splat, refreshTick])

  // Right-panel local state path: simpler, just refresh whenever localPath
  // changes (no URL/resource resolution involved).
  useEffect(() => {
    if (isUrlPanel) return
    let cancelled = false
    setError(null)
    ;(async () => {
      try {
        const dir = await api.get<DirResponse>(`${apiBase}?path=${encodeURIComponent(localPath)}`)
        if (!cancelled) setEntries(dir.entries ?? [])
      } catch (err) {
        if (!cancelled) {
          setEntries([])
          setError(err instanceof Error ? err.message : String(err))
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [isUrlPanel, apiBase, localPath, refreshTick])

  const onPick = (entry: Entry) => {
    if (entry.isDir) {
      goToDir(entry.path)
    } else {
      selectFile(entry.path)
    }
  }

  const submitNewFile = useCallback(async () => {
    const name = newFileName.trim()
    if (!name) return
    // Compose path: current dir + name. Reject leading "/" to keep the
    // create endpoint guarded — server also validates via resolveSafePath.
    if (name.startsWith('/')) {
      setNewFileError('path must be relative (no leading /)')
      return
    }
    const targetPath = path ? `${path}/${name}` : name
    setNewFileBusy(true)
    setNewFileError(null)
    try {
      await api.post<unknown>(`${apiBase}/create`, { path: targetPath, content: '' })
      setNewFileOpen(false)
      setNewFileName('')
      // Bump tick so dir listing re-fetches; then open the new file in
      // the editor pane so the user can start typing immediately.
      setRefreshTick((t) => t + 1)
      selectFile(targetPath)
    } catch (err) {
      setNewFileError(err instanceof Error ? err.message : String(err))
    } finally {
      setNewFileBusy(false)
    }
  }, [apiBase, newFileName, path, selectFile])

  const cancelNewFile = useCallback(() => {
    setNewFileOpen(false)
    setNewFileName('')
    setNewFileError(null)
  }, [])

  return (
    <div className={styles.wrap}>
      <header className={styles.header}>
        <Breadcrumb path={path} onNavigate={goToDir} />
        <div className={styles.headerActions}>
          <button
            className={styles.newFileBtn}
            onClick={() => {
              setNewFileOpen(true)
              setNewFileError(null)
            }}
            title="New file (UTF-8)"
            data-testid="files-new-file-btn"
          >
            ＋ New file
          </button>
          <button
            className={styles.searchToggle}
            onClick={() => setSearchOpen((v) => !v)}
          >
            {searchOpen ? '× Close search' : '🔍 Search'}
          </button>
        </div>
      </header>
      {newFileOpen && (
        <div className={styles.newFileRow}>
          <span style={{ color: 'var(--color-fg-muted)', fontFamily: 'var(--font-mono)', fontSize: 12 }}>
            {path ? path + '/' : ''}
          </span>
          <input
            autoFocus
            className={styles.newFileInput}
            type="text"
            placeholder="filename.txt (or relative/path/file.txt)"
            value={newFileName}
            onChange={(e) => {
              setNewFileName(e.target.value)
              setNewFileError(null)
            }}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                e.preventDefault()
                cancelNewFile()
              } else if (e.key === 'Enter') {
                e.preventDefault()
                void submitNewFile()
              }
            }}
            disabled={newFileBusy}
            data-testid="files-new-file-input"
          />
          <button
            className={styles.newFileSubmit}
            onClick={() => void submitNewFile()}
            disabled={!newFileName.trim() || newFileBusy}
            data-testid="files-new-file-submit"
          >
            {newFileBusy ? 'Creating…' : 'Create'}
          </button>
          <button
            className={styles.newFileCancel}
            onClick={cancelNewFile}
            disabled={newFileBusy}
          >
            Cancel
          </button>
        </div>
      )}
      {newFileError && (
        <div className={styles.newFileError} data-testid="files-new-file-error">{newFileError}</div>
      )}
      {searchOpen && (
        <FileSearch
          apiBase={apiBase}
          basePath={path}
          onPick={(target) => {
            if (target.isDir) {
              goToDir(target.path)
            } else {
              selectFile(target.path, target.lineNum)
            }
            setSearchOpen(false)
          }}
        />
      )}
      <div
        className={`${styles.body} ${selected ? styles.previewOpen : ''}`.trim()}
        ref={bodyRef}
      >
        <div className={styles.listPane} style={{ flex: `0 0 ${listRatio}%` }}>
          {error && <p className={styles.error}>{error}</p>}
          <FileList
            entries={entries}
            selected={selected ?? undefined}
            onPick={onPick}
            dirtyPaths={dirtyPaths}
          />
        </div>
        <div className={styles.dividerWrap}>
          <Divider
            ratio={listRatio}
            onChange={(r) => setDeviceSetting('filesListRatio', r)}
            containerRef={bodyRef}
            min={15}
            max={75}
          />
        </div>
        <div className={styles.previewPane}>
          {selected ? (
            <FilePreview
              apiBase={apiBase}
              repoId={repoId}
              branchId={branchId}
              tabId={tabId}
              path={selected}
              lineNum={selectedLine}
            />
          ) : (
            <p className={styles.empty}>Pick a file to preview.</p>
          )}
        </div>
      </div>
    </div>
  )
}

function dirnameOf(p: string): string {
  const cleaned = p.replace(/\/+$/, '')
  const idx = cleaned.lastIndexOf('/')
  return idx === -1 ? '' : cleaned.slice(0, idx)
}
