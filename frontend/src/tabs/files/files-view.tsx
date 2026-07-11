// S033: files-view.tsx — wires up all CRUD / multi-select / move / delete
// features introduced in S033 (create file/folder, rename, move, delete,
// multi-select, right-click context menu).
//
// State ownership:
//   selection (selectedPaths, touchSelectMode)  →  here (UI-transient)
//   inline create (createKind/Value/Error/Busy) →  here
//   inline rename (renameTarget/Value/Error/Busy) →  here
//   context menu (ctxMenu state)                →  here
//   move modal (moveModalItems)                 →  here
//   delete modal (deleteModalItems)             →  here

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'

import { Divider } from '../../components/divider'
import { api } from '../../lib/api'
import type { TabViewProps } from '../../lib/tab-registry'
import { isDirty as isDirtyFn, makeEditorKey, useEditorStore } from '../../stores/editor-store'
import { usePalmuxStore } from '../../stores/palmux-store'
import { useUploadsStore } from '../../stores/uploads-store'

import { Breadcrumb } from './breadcrumb'
import { FileList } from './file-list'
import { FilesContextMenu } from './files-context-menu'
import { FilesDeleteModal } from './files-delete-modal'
import { FilesMoveModal } from './files-move-modal'
import { FilesUploadModal } from './files-upload-modal'
import { FilePreview } from './file-preview'
import { FileSearch } from './file-search'
import { buildRestoreUrl, readFilesMemory, writeFilesMemory } from './files-memory'
import styles from './files-view.module.css'
import type { Entry } from './types'

interface DirResponse {
  path: string
  entries: Entry[] | null
}

type CreateKind = 'file' | 'folder'

interface CtxMenuState {
  x: number
  y: number
  entry: Entry
}

export function FilesView({ repoId, branchId, tabId }: TabViewProps) {
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

  // Latest 1-based cursor line tracked from the Monaco editor — written to
  // localStorage along with the URL so a tab/workspace switch round-trip
  // restores the user's exact scroll position, not just the file. Updated
  // (debounced inside MonacoView) on every cursor move.
  const cursorLineRef = useRef<number | undefined>(undefined)

  const [resolvedDir, setResolvedDir] = useState('')
  const [resolvedSelected, setResolvedSelected] = useState<string | null>(null)

  const [entries, setEntries] = useState<Entry[]>([])
  const [error, setError] = useState<string | null>(null)
  const [searchOpen, setSearchOpen] = useState(false)
  const [refreshTick, setRefreshTick] = useState(0)
  const bodyRef = useRef<HTMLDivElement | null>(null)
  const listRatio = usePalmuxStore((s) => s.deviceSettings.filesListRatio)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)

  // ── S033-1: inline create state ──────────────────────────────────────────
  const [createKind, setCreateKind] = useState<CreateKind | null>(null)
  const [createValue, setCreateValue] = useState('')
  const [createError, setCreateError] = useState<string | null>(null)
  const [createBusy, setCreateBusy] = useState(false)

  // ── S033-2: inline rename state ──────────────────────────────────────────
  const [renameTarget, setRenameTarget] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [renameError, setRenameError] = useState<string | null>(null)
  const [renameBusy, setRenameBusy] = useState(false)

  // ── S033-2: context menu state ───────────────────────────────────────────
  const [ctxMenu, setCtxMenu] = useState<CtxMenuState | null>(null)

  // ── S033-3: multi-select state ───────────────────────────────────────────
  const [selectedPaths, setSelectedPaths] = useState<Set<string>>(new Set())
  const [touchSelectMode, setTouchSelectMode] = useState(false)

  // ── S033-2/3: modal state ─────────────────────────────────────────────────
  const [deleteModalItems, setDeleteModalItems] = useState<Entry[] | null>(null)
  const [moveModalItems, setMoveModalItems] = useState<Entry[] | null>(null)

  const apiBase = useMemo(
    () => `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/files`,
    [repoId, branchId],
  )
  const remoteUrlBase = useMemo(
    () => `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/remote-url`,
    [repoId, branchId],
  )

  // ── Dirty path tracking (S011-1-6) ───────────────────────────────────────
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

  // ── Navigation helpers ────────────────────────────────────────────────────
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
      const sp = new URLSearchParams(location.search)
      sp.delete('line')
      const search = sp.toString()
      navigate(`${base}${tail}${search ? '?' + search : ''}`)
    },
    [isUrlPanel, repoId, branchId, tabId, location.search, navigate],
  )

  const selectFile = useCallback(
    (filePath: string | null, lineNum?: number) => {
      const cur = isUrlPanel ? resolvedSelected : localSelected
      if (cur && cur !== filePath) {
        const key = makeEditorKey(repoId, branchId, cur)
        if (isDirtyFn(useEditorStore.getState(), key)) {
          const ok = window.confirm(
            `You have unsaved changes in ${cur}. Discard them and switch files?`,
          )
          if (!ok) return
          useEditorStore.getState().clearDraft(key)
        }
      }
      if (!isUrlPanel) {
        setLocalSelected(filePath)
        setLocalLine(lineNum)
        return
      }
      if (!filePath) {
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

  // ── Dir listing effects ──────────────────────────────────────────────────
  // S13b16a-4: Reset `error` inline when (isUrlPanel, splat, refreshTick)
  // changes — React 19 "deriving state from props" idiom. The previous
  // implementation reset inside useEffect (`setError(null)` at the top
  // of the fetch), which `react-hooks/set-state-in-effect` flagged.
  const urlFetchKey = isUrlPanel ? `${splat}\x00${refreshTick}` : null
  const [trackedUrlFetchKey, setTrackedUrlFetchKey] = useState(urlFetchKey)
  if (trackedUrlFetchKey !== urlFetchKey) {
    setTrackedUrlFetchKey(urlFetchKey)
    if (urlFetchKey !== null) setError(null)
  }
  useEffect(() => {
    if (!isUrlPanel) return
    let cancelled = false
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
        // hotfix: splat doesn't exist (404 / not found). Most common
        // cause is the user moved or deleted the path they were
        // viewing — walk up the tree to the first ancestor that
        // still exists and navigate there so the URL stays in sync
        // with on-disk reality. Fall through to the error path only
        // if even the worktree root has gone (very unusual).
        let walked = dirnameOf(splat)
        let resolvedAncestor: { path: string; entries: Entry[] } | null = null
        while (walked) {
          try {
            const dir = await api.get<DirResponse>(`${apiBase}?path=${encodeURIComponent(walked)}`)
            resolvedAncestor = { path: walked, entries: dir.entries ?? [] }
            break
          } catch {
            walked = dirnameOf(walked)
          }
        }
        if (resolvedAncestor === null && walked === '') {
          // root probe — should always succeed unless the worktree itself is gone
          try {
            const dir = await api.get<DirResponse>(`${apiBase}?path=`)
            resolvedAncestor = { path: '', entries: dir.entries ?? [] }
          } catch {
            // give up, fall through
          }
        }
        if (!cancelled && resolvedAncestor !== null) {
          // Replace the URL so the next render is consistent with what
          // we actually loaded — the user shouldn't be left looking at
          // a dead splat in the address bar.
          goToDir(resolvedAncestor.path)
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
  }, [isUrlPanel, apiBase, splat, refreshTick, goToDir])

  // S13b16a-4: Same inline-reset pattern for the local-panel branch.
  const localFetchKey = !isUrlPanel ? `${localPath}\x00${refreshTick}` : null
  const [trackedLocalFetchKey, setTrackedLocalFetchKey] = useState(localFetchKey)
  if (trackedLocalFetchKey !== localFetchKey) {
    setTrackedLocalFetchKey(localFetchKey)
    if (localFetchKey !== null) setError(null)
  }
  useEffect(() => {
    if (isUrlPanel) return
    let cancelled = false
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

  // ── Per-(repo, branch) Files-tab location memory ────────────────────────
  // The TabBar's `goToTab` navigates to `/{repoId}/{branchId}/files` with no
  // splat, so switching to Bash/Claude and back lands on the worktree root.
  // We mirror the latest URL into localStorage on every change, then on
  // remount with an empty splat we replace-navigate to the saved URL — the
  // dir listing effect above then runs against the restored splat.
  //
  // Restoration is gated on `splat === ''` AND no `?line=` query, so an
  // explicit click on the breadcrumb root (which legitimately wants the
  // top dir) doesn't loop through the saved state. Once the user is sitting
  // at root we persist `{pathname:'.../files', search:''}` and the next
  // restore is a no-op.
  const hasRestoredRef = useRef('')
  useEffect(() => {
    if (!isUrlPanel) return
    // Reset the once-per-(repo, branch) latch when the workspace identity
    // changes — switching from workspace A → B → A should restore A's
    // saved state on the way back, not stay stuck because we already
    // restored once for A on initial mount.
    const key = `${repoId}/${branchId}`
    if (hasRestoredRef.current === key) return
    hasRestoredRef.current = key
    // Only restore when the user landed on a bare `/files` (no path).
    // We deliberately ignore `location.search` — the TabBar's `goToTab`
    // carries `?line=N` across tab clicks, so a Files-restore arriving
    // via a tab click would otherwise be skipped because of an inherited
    // line-query. A line-without-a-file is meaningless anyway, so we
    // overwrite.
    if (splat !== '') return
    const mem = readFilesMemory(repoId, branchId)
    if (!mem) return
    const target = buildRestoreUrl(mem)
    // Compare against the current URL so we don't churn the router on a
    // saved-but-trivial `/files` (no splat) state.
    if (target === location.pathname + location.search) return
    navigate(target, { replace: true })
  }, [isUrlPanel, splat, location.search, location.pathname, repoId, branchId, navigate])

  // Whenever the URL we're showing changes — initial mount, navigation
  // inside the tab, restore — write it to localStorage. We deliberately
  // pull `pathname` and `search` from `location` (not from `splat`) so
  // the restore round-trip writes the canonical URL the router produced.
  useEffect(() => {
    if (!isUrlPanel) return
    writeFilesMemory(repoId, branchId, {
      pathname: location.pathname,
      search: location.search,
      cursorLine: cursorLineRef.current,
    })
  }, [isUrlPanel, repoId, branchId, location.pathname, location.search])

  // Cursor-line tracking — Monaco fires `onCursorLineChange` (debounced
  // inside MonacoView) whenever the user moves their cursor or scrolls.
  // We store it in a ref + write to localStorage, but DON'T touch the URL
  // — pushing `?line=N` on every keystroke would fight Monaco's own
  // `revealLineInCenter` effect and reset the cursor column.
  const handleCursorLineChange = useCallback(
    (line: number) => {
      cursorLineRef.current = line
      if (!isUrlPanel) return
      writeFilesMemory(repoId, branchId, {
        pathname: location.pathname,
        search: location.search,
        cursorLine: line,
      })
    },
    [isUrlPanel, repoId, branchId, location.pathname, location.search],
  )

  // Reset the cached cursor line when the selected file changes — the line
  // number from a previous file shouldn't bleed into the next one. Monaco
  // will report a fresh value as soon as it mounts.
  useEffect(() => {
    cursorLineRef.current = undefined
  }, [selected])

  const onPick = (entry: Entry) => {
    if (entry.isDir) {
      goToDir(entry.path)
    } else {
      selectFile(entry.path)
    }
  }

  // ── S033-1: create file/folder ────────────────────────────────────────────
  // The FileList communicates CTA clicks via sentinel values in createValue.
  const handleCreateValueChange = useCallback((v: string) => {
    if (v === '\x01new-file') {
      setCreateKind('file')
      setCreateValue('')
      setCreateError(null)
      return
    }
    if (v === '\x02new-folder') {
      setCreateKind('folder')
      setCreateValue('')
      setCreateError(null)
      return
    }
    setCreateValue(v)
    setCreateError(null)
  }, [])

  const handleCreateSubmit = useCallback(async () => {
    const name = createValue.trim()
    if (!name) return
    if (name.startsWith('/')) {
      setCreateError('path must be relative (no leading /)')
      return
    }
    const targetPath = path ? `${path}/${name}` : name
    setCreateBusy(true)
    setCreateError(null)
    try {
      if (createKind === 'folder') {
        await api.post<unknown>(`${apiBase}/create-dir`, { path: targetPath })
        setCreateKind(null)
        setCreateValue('')
        setRefreshTick((t) => t + 1)
      } else {
        await api.post<unknown>(`${apiBase}/create`, { path: targetPath, content: '' })
        setCreateKind(null)
        setCreateValue('')
        setRefreshTick((t) => t + 1)
        selectFile(targetPath)
      }
    } catch (err) {
      setCreateError(err instanceof Error ? err.message : String(err))
    } finally {
      setCreateBusy(false)
    }
  }, [apiBase, createKind, createValue, path, selectFile])

  const handleCreateCancel = useCallback(() => {
    setCreateKind(null)
    setCreateValue('')
    setCreateError(null)
  }, [])

  // ── hotfix: upload — single modal handles files / folders / mix ──────────
  // The Zustand uploads store keeps the queue alive across tab/branch
  // switches; we just snapshot (repoId, branchId, dir) at submit time.
  const enqueueUploads = useUploadsStore((s) => s.enqueue)
  const [uploadModalOpen, setUploadModalOpen] = useState(false)
  const handleUploadSubmit = useCallback(
    (items: { file: File; relativePath: string }[]) => {
      enqueueUploads(repoId, branchId, path, items)
    },
    [enqueueUploads, repoId, branchId, path],
  )

  // Refresh the dir listing when an upload job for the current dir
  // makes progress (a new file lands). Tracking the count of "done"
  // items for the current dir means we refresh once per file-completed
  // event, never on each progress tick.
  const allJobs = useUploadsStore((s) => s.jobs)
  const lastDoneCountRef = useRef<number>(0)
  useEffect(() => {
    let doneCount = 0
    for (const j of allJobs) {
      if (j.repoId !== repoId || j.branchId !== branchId) continue
      if (j.baseDir !== path) continue
      for (const it of j.items) if (it.status === 'done') doneCount++
    }
    if (doneCount > lastDoneCountRef.current) {
      lastDoneCountRef.current = doneCount
      setRefreshTick((t) => t + 1)
    }
  }, [allJobs, repoId, branchId, path])

  // ── S033-2: inline rename ─────────────────────────────────────────────────
  const startRename = useCallback((entry: Entry) => {
    setRenameTarget(entry.path)
    setRenameValue(entry.name)
    setRenameError(null)
  }, [])

  const handleRenameSubmit = useCallback(async () => {
    if (!renameTarget) return
    const newName = renameValue.trim()
    if (!newName) return
    const parentDir = dirnameOf(renameTarget)
    const toPath = parentDir ? `${parentDir}/${newName}` : newName
    setRenameBusy(true)
    setRenameError(null)
    try {
      await api.post<unknown>(`${apiBase}/rename`, { from: renameTarget, to: toPath })
      // If the renamed file was selected, navigate to new path.
      if (selected === renameTarget) selectFile(toPath)
      setRenameTarget(null)
      setRenameValue('')
      setRefreshTick((t) => t + 1)
    } catch (err) {
      setRenameError(err instanceof Error ? err.message : String(err))
    } finally {
      setRenameBusy(false)
    }
  }, [renameTarget, renameValue, apiBase, selected, selectFile])

  const handleRenameCancel = useCallback(() => {
    setRenameTarget(null)
    setRenameValue('')
    setRenameError(null)
  }, [])

  // ── Sc7818e-3: download trigger — builds a multi-path query string and
  // clicks a native <a download> so the browser streams the file directly
  // from the server (no JS buffering). The same-origin cookie is carried
  // automatically. For folders or multi-select the server returns a zip.
  const triggerDownload = useCallback(
    (paths: string[]) => {
      if (paths.length === 0) return
      const qs = paths.map((p) => `path=${encodeURIComponent(p)}`).join('&')
      const a = document.createElement('a')
      a.href = `${apiBase}/download?${qs}`
      a.download = ''
      a.rel = 'noopener'
      document.body.appendChild(a)
      a.click()
      a.remove()
    },
    [apiBase],
  )

  // ── S033-2: context menu ──────────────────────────────────────────────────
  const handleContextMenu = useCallback((e: React.MouseEvent, entry: Entry) => {
    e.preventDefault()
    setCtxMenu({ x: e.clientX, y: e.clientY, entry })
  }, [])

  const handleContextAction = useCallback(
    async (action: { type: string }) => {
      if (!ctxMenu) return
      const entry = ctxMenu.entry
      switch (action.type) {
        case 'open':
          onPick(entry)
          break
        case 'rename':
          startRename(entry)
          break
        case 'move':
          setMoveModalItems([entry])
          break
        case 'copy-path':
          await navigator.clipboard.writeText(entry.path).catch(() => {
            // fallback: no-op if clipboard API unavailable
          })
          break
        case 'open-on-github': {
          try {
            const result = await api.get<{ url: string }>(
              `${remoteUrlBase}`,
            )
            if (result.url) {
              // Append the file path to the repo tree URL.
              const fileUrl = result.url + '/' + entry.path
              window.open(fileUrl, '_blank', 'noopener,noreferrer')
            }
          } catch {
            // Silently fail — GitHub URL is best-effort.
          }
          break
        }
        case 'download':
          triggerDownload([entry.path])
          break
        case 'delete':
          setDeleteModalItems([entry])
          break
        case 'batch-download':
          triggerDownload([...selectedPaths])
          break
        case 'batch-move':
          setMoveModalItems(
            entries.filter((e) => selectedPaths.has(e.path)),
          )
          break
        case 'batch-copy': {
          const paths = [...selectedPaths].join('\n')
          await navigator.clipboard.writeText(paths).catch(() => {})
          break
        }
        case 'batch-delete':
          setDeleteModalItems(entries.filter((e) => selectedPaths.has(e.path)))
          break
      }
    },
    [ctxMenu, entries, selectedPaths, remoteUrlBase, startRename, onPick, triggerDownload],
  )

  // ── S033-3: multi-select keyboard shortcuts ───────────────────────────────
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Only handle when inside the files view.
      if (selectedPaths.size === 0 && !touchSelectMode) return
      if (e.key === 'Escape') {
        // If context menu is open, let it handle Escape first; selection stays.
        if (ctxMenu) return
        setSelectedPaths(new Set())
        setTouchSelectMode(false)
      } else if ((e.key === 'Delete' || e.key === 'Backspace') && selectedPaths.size > 0) {
        // Don't intercept Backspace if user is typing in an input.
        const tag = (e.target as HTMLElement)?.tagName
        if (tag === 'INPUT' || tag === 'TEXTAREA') return
        const items = entries.filter((e) => selectedPaths.has(e.path))
        if (items.length > 0) setDeleteModalItems(items)
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [selectedPaths, touchSelectMode, entries, ctxMenu])

  // ── F2 key for inline rename ──────────────────────────────────────────────
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'F2') return
      const tag = (e.target as HTMLElement)?.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA') return
      // Find the currently-selected (single) file to rename.
      if (selected) {
        const entry = entries.find((x) => x.path === selected)
        if (entry) startRename(entry)
      } else if (selectedPaths.size === 1) {
        const [p] = [...selectedPaths]
        const entry = entries.find((x) => x.path === p)
        if (entry) startRename(entry)
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [selected, selectedPaths, entries, startRename])

  // ── Delete handler (from modal) ───────────────────────────────────────────
  const handleDeleteConfirm = useCallback(
    async (paths: string[]) => {
      await api.post<unknown>(`${apiBase}/batch-delete`, { paths })
      // Clear selection if deleted items were selected.
      const removed = new Set(paths)
      setSelectedPaths((prev) => {
        const next = new Set(prev)
        for (const p of removed) next.delete(p)
        return next
      })
      if (selected && removed.has(selected)) {
        selectFile(null)
      }
      setRefreshTick((t) => t + 1)
    },
    [apiBase, selected, selectFile],
  )

  return (
    <div className={styles.wrap}>
      <header className={styles.header}>
        <Breadcrumb path={path} onNavigate={goToDir} />
        <div className={styles.headerActions}>
          <button
            className={styles.searchToggle}
            onClick={() => setSearchOpen((v) => !v)}
          >
            {searchOpen ? '× Close search' : '🔍 Search'}
          </button>
        </div>
      </header>

      {/* S033-3: Multi-select action bar */}
      {(selectedPaths.size >= 2 || touchSelectMode) && (
        <div className={styles.multiSelectBar} data-testid="files-multi-select-bar">
          <span className={styles.multiSelectCount}>
            {selectedPaths.size} selected
          </span>
          <button
            className={styles.multiSelectBtn}
            disabled={selectedPaths.size === 0}
            onClick={() => triggerDownload([...selectedPaths])}
            data-testid="files-batch-download"
          >
            ⬇ Download
          </button>
          <button
            className={styles.multiSelectBtn}
            disabled={selectedPaths.size === 0}
            onClick={() => {
              const items = entries.filter((e) => selectedPaths.has(e.path))
              if (items.length > 0) setMoveModalItems(items)
            }}
            data-testid="files-batch-move"
          >
            → Move…
          </button>
          <button
            className={`${styles.multiSelectBtn} ${styles.multiSelectBtnDanger}`}
            disabled={selectedPaths.size === 0}
            onClick={() => {
              const items = entries.filter((e) => selectedPaths.has(e.path))
              if (items.length > 0) setDeleteModalItems(items)
            }}
            data-testid="files-batch-delete"
          >
            🗑 Delete
          </button>
          <button
            className={`${styles.multiSelectBtn} ${styles.multiSelectBtnGhost}`}
            onClick={() => {
              setSelectedPaths(new Set())
              setTouchSelectMode(false)
            }}
            data-testid="files-multi-clear"
          >
            ✕ Cancel
          </button>
        </div>
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
            selectedPaths={selectedPaths}
            onSelectionChange={setSelectedPaths}
            touchSelectMode={touchSelectMode}
            onTouchSelectMode={setTouchSelectMode}
            createKind={createKind}
            createValue={createValue}
            createError={createError}
            createBusy={createBusy}
            onCreateValueChange={handleCreateValueChange}
            onCreateSubmit={() => void handleCreateSubmit()}
            onCreateCancel={handleCreateCancel}
            renameTarget={renameTarget}
            renameValue={renameValue}
            renameError={renameError}
            renameBusy={renameBusy}
            onRenameValueChange={setRenameValue}
            onRenameSubmit={() => void handleRenameSubmit()}
            onRenameCancel={handleRenameCancel}
            onContextMenu={handleContextMenu}
            contextMenuTarget={ctxMenu?.entry.path}
            onOpenUpload={() => setUploadModalOpen(true)}
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
              onCursorLineChange={handleCursorLineChange}
              onInternalNavigate={selectFile}
            />
          ) : (
            <p className={styles.empty}>Pick a file to preview.</p>
          )}
        </div>
      </div>

      {/* Context menu portal */}
      {ctxMenu && (
        <FilesContextMenu
          x={ctxMenu.x}
          y={ctxMenu.y}
          target={ctxMenu.entry}
          selectedPaths={selectedPaths}
          onAction={handleContextAction}
          onClose={() => setCtxMenu(null)}
        />
      )}

      {/* Delete modal */}
      {deleteModalItems && (
        <FilesDeleteModal
          items={deleteModalItems}
          onClose={() => setDeleteModalItems(null)}
          onConfirm={handleDeleteConfirm}
        />
      )}

      {/* Move modal */}
      {moveModalItems && (
        <FilesMoveModal
          items={moveModalItems}
          apiBase={apiBase}
          onClose={() => setMoveModalItems(null)}
          onCompleted={() => {
            setMoveModalItems(null)
            setSelectedPaths(new Set())
            setRefreshTick((t) => t + 1)
          }}
        />
      )}

      {/* Upload modal — single entry point for files / folders / any mix */}
      <FilesUploadModal
        open={uploadModalOpen}
        targetDir={path}
        onClose={() => setUploadModalOpen(false)}
        onUpload={handleUploadSubmit}
      />
    </div>
  )
}

function dirnameOf(p: string): string {
  const cleaned = p.replace(/\/+$/, '')
  const idx = cleaned.lastIndexOf('/')
  return idx === -1 ? '' : cleaned.slice(0, idx)
}
