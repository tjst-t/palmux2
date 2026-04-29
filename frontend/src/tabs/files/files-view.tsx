import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'

import { Divider } from '../../components/divider'
import { api } from '../../lib/api'
import type { TabViewProps } from '../../lib/tab-registry'
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
  const bodyRef = useRef<HTMLDivElement | null>(null)
  const listRatio = usePalmuxStore((s) => s.deviceSettings.filesListRatio)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)

  const apiBase = useMemo(
    () => `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/files`,
    [repoId, branchId],
  )

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
    [isUrlPanel, repoId, branchId, tabId, location.search, navigate, goToDir, path],
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
  }, [isUrlPanel, apiBase, splat])

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
  }, [isUrlPanel, apiBase, localPath])

  const onPick = (entry: Entry) => {
    if (entry.isDir) {
      goToDir(entry.path)
    } else {
      selectFile(entry.path)
    }
  }

  return (
    <div className={styles.wrap}>
      <header className={styles.header}>
        <Breadcrumb path={path} onNavigate={goToDir} />
        <button
          className={styles.searchToggle}
          onClick={() => setSearchOpen((v) => !v)}
        >
          {searchOpen ? '× Close search' : '🔍 Search'}
        </button>
      </header>
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
      <div className={styles.body} ref={bodyRef}>
        <div className={styles.listPane} style={{ flex: `0 0 ${listRatio}%` }}>
          {error && <p className={styles.error}>{error}</p>}
          <FileList entries={entries} selected={selected ?? undefined} onPick={onPick} />
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
            <FilePreview apiBase={apiBase} path={selected} lineNum={selectedLine} />
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
