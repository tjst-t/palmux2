import { useEffect, useMemo, useState } from 'react'

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

export function FilesView({ repoId, branchId }: TabViewProps) {
  const [path, setPath] = useState<string>('')
  const [selected, setSelected] = useState<string | null>(null)
  const [entries, setEntries] = useState<Entry[]>([])
  const [error, setError] = useState<string | null>(null)
  const [searchOpen, setSearchOpen] = useState(false)

  const apiBase = useMemo(
    () => `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/files`,
    [repoId, branchId],
  )

  // Refresh listing whenever path changes.
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
      setPath(entry.path)
      setSelected(null)
    } else {
      setSelected(entry.path)
    }
  }

  return (
    <div className={styles.wrap}>
      <header className={styles.header}>
        <Breadcrumb path={path} onNavigate={(p) => { setPath(p); setSelected(null) }} />
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
            setSelected(p)
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
