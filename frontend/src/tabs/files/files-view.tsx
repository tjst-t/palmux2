import { useCallback, useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'

import { api } from '../../lib/api'
import type { TabViewProps } from '../../lib/tab-registry'

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
  // matches the active panel — `…/files/<dir>` is the directory, `?file=`
  // selects a file. Right-panel Files views (whose target lives in `?right`)
  // can't share that URL space, so they fall back to local state.
  const params = useParams()
  const location = useLocation()
  const [searchParams, setSearchParams] = useSearchParams()
  const navigate = useNavigate()
  const isUrlPanel = params.repoId === repoId && params.branchId === branchId

  const splat = (params['*'] ?? '').replace(/^\/+|\/+$/g, '')
  const fileQuery = searchParams.get('file') ?? ''

  const [localPath, setLocalPath] = useState('')
  const [localSelected, setLocalSelected] = useState<string | null>(null)

  const path = isUrlPanel ? splat : localPath
  const selected = isUrlPanel ? (fileQuery ? fileQuery : null) : localSelected

  const [entries, setEntries] = useState<Entry[]>([])
  const [error, setError] = useState<string | null>(null)
  const [searchOpen, setSearchOpen] = useState(false)

  const apiBase = useMemo(
    () => `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/files`,
    [repoId, branchId],
  )

  const goToDir = useCallback(
    (next: string) => {
      const cleaned = next.replace(/^\/+|\/+$/g, '')
      if (!isUrlPanel) {
        setLocalPath(cleaned)
        setLocalSelected(null)
        return
      }
      // Build /:repo/:branch/:tab/<dir> while preserving other search params,
      // but drop ?file because the user moved out of the previous selection.
      const base = `/${encodeURIComponent(repoId)}/${encodeURIComponent(branchId)}/${encodeURIComponent(tabId)}`
      const tail = cleaned ? `/${cleaned.split('/').map(encodeURIComponent).join('/')}` : ''
      const sp = new URLSearchParams(location.search)
      sp.delete('file')
      const search = sp.toString()
      navigate(`${base}${tail}${search ? '?' + search : ''}`)
    },
    [isUrlPanel, repoId, branchId, tabId, location.search, navigate],
  )

  const selectFile = useCallback(
    (filePath: string | null) => {
      if (!isUrlPanel) {
        setLocalSelected(filePath)
        return
      }
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          if (filePath) next.set('file', filePath)
          else next.delete('file')
          return next
        },
        { replace: false },
      )
    },
    [isUrlPanel, setSearchParams],
  )

  // Refresh listing whenever the directory changes.
  useEffect(() => {
    let cancelled = false
    setError(null)
    ;(async () => {
      try {
        const dir = await api.get<DirResponse>(`${apiBase}?path=${encodeURIComponent(path)}`)
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
  }, [apiBase, path])

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
          onPick={(p) => {
            selectFile(p)
            setSearchOpen(false)
          }}
        />
      )}
      <div className={styles.body}>
        <div className={styles.listPane}>
          {error && <p className={styles.error}>{error}</p>}
          <FileList entries={entries} selected={selected ?? undefined} onPick={onPick} />
        </div>
        <div className={styles.previewPane}>
          {selected ? (
            <FilePreview apiBase={apiBase} path={selected} />
          ) : (
            <p className={styles.empty}>Pick a file to preview.</p>
          )}
        </div>
      </div>
    </div>
  )
}
