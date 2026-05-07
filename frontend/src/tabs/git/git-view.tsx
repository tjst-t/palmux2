// Git tab — VS Code-style minimal layout (S029, BREAKING).
//
// The S012/S013/S014 implementations grew an extensive GUI for stash /
// cherry-pick / interactive rebase / 3-way merge / submodule / bisect
// etc., which made the *core* "review changes → commit → push" flow
// hard to find and crowded. This view replaces the whole surface with
// a 2-column layout inspired by the VS Code Source Control panel and
// the original palmux Git tab:
//
//   ┌──────────────────────────┬─────────────────────────────────────┐
//   │ Changes                  │                                     │
//   │   M src/foo.go           │                                     │
//   │   A docs/new.md          │   Monaco diff (working file or all  │
//   │ [commit message…]        │   files in a clicked commit)         │
//   │ [Commit] ↑ ↓ ⟳            │                                     │
//   ├──────────────────────────┤                                     │
//   │ History                  │                                     │
//   │   ● a1b2 main main↑    1h│                                     │
//   │   ● 7e8f          autopilot 3h                                  │
//   │   …                      │                                     │
//   ├──────────────────────────┴─────────────────────────────────────┤
//   │ ⎇ main ↑1 ↓0                                                    │
//   └────────────────────────────────────────────────────────────────┘
//
// Everything advanced (stash / cherry-pick / interactive rebase / 3-way
// merge / submodule / reflog / bisect / blame / file-history / tags) is
// done from the Bash or Claude tab — see CLAUDE.md / S029 description.
//
// Mobile (<600px): the 2-column layout collapses to a single column
// with three sub-tabs (Changes / History / Diff) at the top.

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
} from 'react'

import { DiffEditor } from '@monaco-editor/react'

import { api, ApiError } from '../../lib/api'
import { useGitStatusEvents } from '../../hooks/use-git-status-events'

import { GitMonacoDiff } from './git-monaco-diff'
import { ImagePair, isImageFile } from './git-image-diff'
import { monacoLanguageFor } from '../files/viewers/dispatcher'
import styles from './git-view.module.css'
import type {
  BranchEntry,
  FileStatus,
  LogEntry,
  StatusReport,
} from './types'
import type { TabViewProps } from '../../lib/tab-registry'

type Props = TabViewProps

// Selected revision describes what the top-left files pane and the
// right-hand Monaco diff are showing.
//
// `uncommitted` → the working tree (changes section). `path` selects
//                 which file's diff to show; undefined means "pane
//                 visible but no file picked yet".
// `commit`      → a historical commit. `path` is the file inside that
//                 commit currently shown in the diff (auto-selected to
//                 the first file once the commit's file list arrives).
//
// The Git tab boots with `{ kind: 'uncommitted' }` so users land on
// "what's about to ship" without an extra click.
type Selection =
  | { kind: 'uncommitted'; path?: string; staged?: boolean }
  | { kind: 'commit'; sha: string; refs?: string[]; path?: string }

// Mobile sub-tab.
type MobilePane = 'changes' | 'history' | 'diff'

interface CommitDiffFile {
  oldPath: string
  newPath: string
}

const HISTORY_PAGE = 50

// Resizable pane sizes — persisted per-device.
const SIDEBAR_WIDTH_KEY = 'palmux:git:sidebarWidth'
const CHANGES_HEIGHT_KEY = 'palmux:git:changesHeight'
const SIDEBAR_DEFAULT = 320
const SIDEBAR_MIN = 200
const SIDEBAR_MAX = 720
const CHANGES_DEFAULT = 280
const CHANGES_MIN = 120
const CHANGES_MAX_RESERVE = 120 // leave at least this much height for History

function readStoredNumber(key: string, fallback: number): number {
  if (typeof window === 'undefined') return fallback
  const raw = window.localStorage.getItem(key)
  if (raw == null) return fallback
  const n = Number(raw)
  return Number.isFinite(n) ? n : fallback
}

function clamp(n: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, n))
}

// Pointer-drag splitter. `axis` selects which delta to track: `x` for a
// vertical splitter (col-resize, drag left/right), `y` for a horizontal
// one (row-resize, drag up/down).
interface SplitterProps {
  axis: 'x' | 'y'
  onResize: (clientPos: number) => void
  testid?: string
}

function Splitter({ axis, onResize, testid }: SplitterProps) {
  const [active, setActive] = useState(false)

  useEffect(() => {
    if (!active) return
    const onMove = (e: PointerEvent) => onResize(axis === 'x' ? e.clientX : e.clientY)
    const onUp = () => setActive(false)
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    document.body.style.cursor = axis === 'x' ? 'col-resize' : 'row-resize'
    document.body.style.userSelect = 'none'
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
  }, [active, axis, onResize])

  return (
    <div
      className={
        axis === 'x'
          ? `${styles.vSplitter} ${active ? styles.splitterActive : ''}`
          : `${styles.hSplitter} ${active ? styles.splitterActive : ''}`
      }
      role="separator"
      aria-orientation={axis === 'x' ? 'vertical' : 'horizontal'}
      data-testid={testid}
      onPointerDown={(e) => {
        e.preventDefault()
        setActive(true)
      }}
    />
  )
}

export function GitView({ repoId, branchId }: Props) {
  const apiBase = `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/git`
  const [status, setStatus] = useState<StatusReport | null>(null)
  const [log, setLog] = useState<LogEntry[]>([])
  const [logExhausted, setLogExhausted] = useState(false)
  const [logLoading, setLogLoading] = useState(false)
  const [branches, setBranches] = useState<BranchEntry[]>([])
  const [error, setError] = useState<string | null>(null)
  const [selection, setSelection] = useState<Selection>({ kind: 'uncommitted' })
  const [reloadKey, setReloadKey] = useState(0)
  const [mobilePane, setMobilePane] = useState<MobilePane>('changes')

  // File list for the currently-selected commit (kind === 'commit').
  // Hoisted from the old CommitDiffPanel so the top-left pane can
  // render it instead of the right pane, and so the parent can
  // auto-pick the first file as the active diff target.
  const [commitFiles, setCommitFiles] = useState<CommitDiffFile[]>([])
  const [commitFilesLoading, setCommitFilesLoading] = useState(false)
  const [commitFilesError, setCommitFilesError] = useState<string | null>(null)
  const selectedSha = selection.kind === 'commit' ? selection.sha : null

  useEffect(() => {
    if (!selectedSha) {
      setCommitFiles([])
      setCommitFilesError(null)
      return
    }
    let cancelled = false
    setCommitFilesLoading(true)
    setCommitFilesError(null)
    api
      .get<{ files: { oldPath: string; newPath: string }[] | null }>(
        `${apiBase}/diff?sha=${encodeURIComponent(selectedSha)}`,
      )
      .then((res) => {
        if (cancelled) return
        const list = (res.files ?? []).map((f) => ({
          oldPath: f.oldPath,
          newPath: f.newPath,
        }))
        setCommitFiles(list)
      })
      .catch((e) => {
        if (!cancelled) setCommitFilesError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!cancelled) setCommitFilesLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [apiBase, selectedSha, reloadKey])

  // Auto-select the first file of a freshly-loaded commit so the diff
  // pane isn't empty after the user clicks a commit row.
  useEffect(() => {
    if (selection.kind !== 'commit' || selection.path) return
    if (commitFiles.length === 0) return
    const first = commitFiles[0]
    setSelection({ ...selection, path: first.newPath || first.oldPath })
  }, [commitFiles, selection])

  // Resizable layout state — pixel widths/heights persisted to localStorage.
  // The body element drives the col-resize splitter (we measure dragged X
  // relative to its left edge); the sidebar element drives the row-resize
  // splitter inside it.
  const bodyRef = useRef<HTMLDivElement>(null)
  const sidebarRef = useRef<HTMLDivElement>(null)
  const [sidebarWidth, setSidebarWidth] = useState(() =>
    readStoredNumber(SIDEBAR_WIDTH_KEY, SIDEBAR_DEFAULT),
  )
  const [changesHeight, setChangesHeight] = useState(() =>
    readStoredNumber(CHANGES_HEIGHT_KEY, CHANGES_DEFAULT),
  )
  const persistSidebarWidth = useCallback((w: number) => {
    setSidebarWidth(w)
    try { window.localStorage.setItem(SIDEBAR_WIDTH_KEY, String(w)) } catch {}
  }, [])
  const persistChangesHeight = useCallback((h: number) => {
    setChangesHeight(h)
    try { window.localStorage.setItem(CHANGES_HEIGHT_KEY, String(h)) } catch {}
  }, [])
  const onSidebarResize = useCallback((clientX: number) => {
    const el = bodyRef.current
    if (!el) return
    const rect = el.getBoundingClientRect()
    const next = clamp(
      clientX - rect.left,
      SIDEBAR_MIN,
      Math.min(SIDEBAR_MAX, rect.width - 240),
    )
    persistSidebarWidth(Math.round(next))
  }, [persistSidebarWidth])
  const onChangesResize = useCallback((clientY: number) => {
    const el = sidebarRef.current
    if (!el) return
    const rect = el.getBoundingClientRect()
    const next = clamp(
      clientY - rect.top,
      CHANGES_MIN,
      Math.max(CHANGES_MIN, rect.height - CHANGES_MAX_RESERVE),
    )
    persistChangesHeight(Math.round(next))
  }, [persistChangesHeight])

  // ---- Data fetchers ------------------------------------------------------

  const fetchStatus = useCallback(async () => {
    try {
      const res = await api.get<StatusReport>(`${apiBase}/status`)
      setStatus(res)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [apiBase])

  const fetchLog = useCallback(
    async (skip: number) => {
      setLogLoading(true)
      try {
        const url = `${apiBase}/log?limit=${HISTORY_PAGE}&skip=${skip}`
        const res = await api.get<LogEntry[] | null>(url)
        const entries = res ?? []
        setLog((prev) => (skip === 0 ? entries : [...prev, ...entries]))
        setLogExhausted(entries.length < HISTORY_PAGE)
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setLogLoading(false)
      }
    },
    [apiBase],
  )

  const fetchBranches = useCallback(async () => {
    try {
      const res = await api.get<BranchEntry[] | null>(`${apiBase}/branches`)
      setBranches(res ?? [])
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [apiBase])

  // Initial load.
  useEffect(() => {
    void fetchStatus()
    void fetchLog(0)
    void fetchBranches()
  }, [fetchStatus, fetchLog, fetchBranches])

  // Server-pushed git.statusChanged → refetch status + first page of log
  // + branches. The diff viewer keys off `reloadKey` so committed files
  // re-fetch as well.
  const onStatusChanged = useCallback(() => {
    void fetchStatus()
    void fetchLog(0)
    void fetchBranches()
    setReloadKey((k) => k + 1)
  }, [fetchStatus, fetchLog, fetchBranches])
  useGitStatusEvents(repoId, branchId, onStatusChanged)

  // ---- Derived: flat working-tree change list ---------------------------

  const conflicts = status?.conflicts ?? []
  const changes = useMemo(() => buildChangeList(status), [status])
  const branchName = status?.branch ?? '…'
  const headBranch = useMemo(
    () => branches.find((b) => b.isHead && !b.isRemote),
    [branches],
  )

  // ---- Stage / unstage / discard ----------------------------------------

  const onStage = useCallback(
    async (path: string) => {
      try {
        await api.post(`${apiBase}/stage`, { path })
        await fetchStatus()
        setReloadKey((k) => k + 1)
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      }
    },
    [apiBase, fetchStatus],
  )

  const onUnstage = useCallback(
    async (path: string) => {
      try {
        await api.post(`${apiBase}/unstage`, { path })
        await fetchStatus()
        setReloadKey((k) => k + 1)
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      }
    },
    [apiBase, fetchStatus],
  )

  // ---- Commit / push / pull / fetch -------------------------------------

  const [commitMessage, setCommitMessage] = useState('')
  const [committing, setCommitting] = useState(false)
  const onCommit = useCallback(
    async (e: FormEvent) => {
      e.preventDefault()
      if (!commitMessage.trim()) return
      setCommitting(true)
      setError(null)
      try {
        await api.post(`${apiBase}/commit`, { message: commitMessage })
        setCommitMessage('')
        await Promise.all([fetchStatus(), fetchLog(0), fetchBranches()])
        setReloadKey((k) => k + 1)
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setCommitting(false)
      }
    },
    [apiBase, commitMessage, fetchStatus, fetchLog, fetchBranches],
  )

  const [syncBusy, setSyncBusy] = useState<null | 'push' | 'pull' | 'fetch'>(null)
  const sync = useCallback(
    async (op: 'push' | 'pull' | 'fetch') => {
      setSyncBusy(op)
      setError(null)
      try {
        await api.post(`${apiBase}/${op}`, {})
        await Promise.all([fetchStatus(), fetchLog(0), fetchBranches()])
      } catch (e) {
        if (e instanceof ApiError && e.status === 422) {
          // Some op failed but the server returned a structured error.
          setError(e.message)
        } else {
          setError(e instanceof Error ? e.message : String(e))
        }
      } finally {
        setSyncBusy(null)
      }
    },
    [apiBase, fetchStatus, fetchLog, fetchBranches],
  )

  // ---- Render ------------------------------------------------------------

  const sidebar = (
    <aside
      className={styles.sidebar}
      data-testid="git-sidebar"
      ref={sidebarRef}
      style={{ width: sidebarWidth }}
    >
      {error && (
        <div className={styles.errorBanner} data-testid="git-error">
          {error}
          <button type="button" onClick={() => setError(null)} aria-label="dismiss">×</button>
        </div>
      )}

      {conflicts.length > 0 && (
        <ConflictBanner
          conflicts={conflicts}
          canContinue={conflicts.every((c) =>
            // Conflict resolution is "done" when the path is no longer
            // listed under conflicts AND is staged. We approximate
            // canContinue by checking the same path appears in
            // status.staged. (status fetches happen on every stage
            // event so this stays in sync.)
            (status?.staged ?? []).some((s) => s.path === c.path),
          )}
          onContinueMerge={async () => {
            // After resolving + staging conflicted files, the merge
            // commit is finalised by `git commit` with the prepared
            // MERGE_MSG (no extra message argument needed). Use an
            // empty message so the server uses MERGE_MSG verbatim.
            try {
              await api.post(`${apiBase}/commit`, { message: '' })
              await Promise.all([fetchStatus(), fetchLog(0), fetchBranches()])
              setReloadKey((k) => k + 1)
            } catch (e) {
              setError(e instanceof Error ? e.message : String(e))
            }
          }}
        />
      )}

      <div className={styles.changesPane} style={{ height: changesHeight }}>
        {selection.kind === 'uncommitted' ? (
          <ChangesSection
            changes={changes}
            commitMessage={commitMessage}
            committing={committing}
            canCommit={changes.some((c) => c.staged) && commitMessage.trim().length > 0}
            syncBusy={syncBusy}
            ahead={headBranch?.ahead ?? 0}
            behind={headBranch?.behind ?? 0}
            selection={selection}
            onSelect={(sel) => {
              setSelection(sel)
              if (sel.kind === 'uncommitted' && sel.path) setMobilePane('diff')
            }}
            onStage={onStage}
            onUnstage={onUnstage}
            onCommitMessageChange={setCommitMessage}
            onCommit={onCommit}
            onPush={() => sync('push')}
            onPull={() => sync('pull')}
            onFetch={() => sync('fetch')}
          />
        ) : (
          <CommitFilesPane
            sha={selection.sha}
            refs={selection.refs}
            files={commitFiles}
            loading={commitFilesLoading}
            error={commitFilesError}
            selectedPath={selection.path}
            onSelectFile={(path) => {
              setSelection({ ...selection, path })
              setMobilePane('diff')
            }}
          />
        )}
      </div>

      <Splitter axis="y" onResize={onChangesResize} testid="git-splitter-h" />

      <div className={styles.historyPane}>
        <HistorySection
          log={log}
          loading={logLoading}
          exhausted={logExhausted}
          selectedSha={selection.kind === 'commit' ? selection.sha : null}
          uncommittedSelected={selection.kind === 'uncommitted'}
          uncommittedCount={changes.length}
          onSelect={(entry) => {
            setSelection({ kind: 'commit', sha: entry.hash, refs: entry.refs })
            setMobilePane('changes')
          }}
          onSelectUncommitted={() => {
            setSelection({ kind: 'uncommitted' })
            setMobilePane('changes')
          }}
          onLoadMore={() => fetchLog(log.length)}
        />
      </div>
    </aside>
  )

  const main = (
    <main className={styles.main} data-testid="git-main">
      {selection.kind === 'uncommitted' && !selection.path && (
        <div className={styles.emptyMain}>
          {changes.length === 0
            ? 'No changes — working tree is clean'
            : 'Select a file from Changes to view its diff'}
        </div>
      )}
      {selection.kind === 'uncommitted' && selection.path && (
        <GitMonacoDiff
          apiBase={apiBase}
          path={selection.path}
          unified={false}
          reloadKey={reloadKey}
          onStaged={onStatusChanged}
        />
      )}
      {selection.kind === 'commit' && !selection.path && (
        <div className={styles.emptyMain}>
          {commitFilesLoading
            ? 'Loading…'
            : commitFiles.length === 0
              ? '(no changes — first commit or merge)'
              : 'Select a file to view its diff'}
        </div>
      )}
      {selection.kind === 'commit' && selection.path && (
        <CommitFileDiff
          apiBase={apiBase}
          sha={selection.sha}
          path={selection.path}
          reloadKey={reloadKey}
        />
      )}
    </main>
  )

  const statusBar = (
    <BranchStatusBar
      branchName={branchName}
      branches={branches}
      ahead={headBranch?.ahead ?? 0}
      behind={headBranch?.behind ?? 0}
      onSwitch={async (name) => {
        try {
          await api.post(`${apiBase}/switch`, { name })
          setSelection({ kind: 'uncommitted' })
          await Promise.all([fetchStatus(), fetchLog(0), fetchBranches()])
        } catch (e) {
          setError(e instanceof Error ? e.message : String(e))
        }
      }}
      onCreate={async (name) => {
        try {
          await api.post(`${apiBase}/branches`, { name, checkout: true })
          setSelection({ kind: 'uncommitted' })
          await Promise.all([fetchStatus(), fetchLog(0), fetchBranches()])
        } catch (e) {
          setError(e instanceof Error ? e.message : String(e))
        }
      }}
    />
  )

  return (
    <div className={styles.root} data-testid="git-tab">
      {/* Mobile sub-tab selector — hidden on desktop via CSS */}
      <nav className={styles.mobileTabs} data-testid="git-mobile-tabs">
        <button
          type="button"
          className={mobilePane === 'changes' ? styles.mobileTabActive : styles.mobileTab}
          onClick={() => setMobilePane('changes')}
        >
          Changes{changes.length > 0 ? ` (${changes.length})` : ''}
        </button>
        <button
          type="button"
          className={mobilePane === 'history' ? styles.mobileTabActive : styles.mobileTab}
          onClick={() => setMobilePane('history')}
        >
          History
        </button>
        <button
          type="button"
          className={mobilePane === 'diff' ? styles.mobileTabActive : styles.mobileTab}
          onClick={() => setMobilePane('diff')}
        >
          Diff
        </button>
      </nav>

      <div
        className={styles.body}
        data-mobile-pane={mobilePane}
        ref={bodyRef}
      >
        {sidebar}
        <Splitter axis="x" onResize={onSidebarResize} testid="git-splitter-v" />
        {main}
      </div>

      {statusBar}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Working tree (Changes) section
// ---------------------------------------------------------------------------

interface ChangeRow {
  path: string
  status: string // single-letter M/A/D/?/R/U
  staged: boolean
}

function buildChangeList(status: StatusReport | null): ChangeRow[] {
  if (!status) return []
  const out: ChangeRow[] = []
  // Conflicts win over both staged and unstaged: we surface them as
  // their own banner, but still list each path with U so the user can
  // click through to resolve.
  for (const f of status.conflicts ?? []) {
    out.push({ path: f.path, status: 'U', staged: false })
  }
  for (const f of status.staged ?? []) {
    out.push({ path: f.path, status: f.stagedCode || 'M', staged: true })
  }
  for (const f of status.unstaged ?? []) {
    out.push({ path: f.path, status: f.workingCode || 'M', staged: false })
  }
  for (const f of status.untracked ?? []) {
    out.push({ path: f.path, status: '?', staged: false })
  }
  // De-dup keeping the staged copy first when a path is both staged
  // and modified-after-staging.
  const seen = new Set<string>()
  return out.filter((r) => {
    const key = `${r.path}:${r.staged ? 's' : 'w'}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

interface ChangesSectionProps {
  changes: ChangeRow[]
  commitMessage: string
  committing: boolean
  canCommit: boolean
  syncBusy: null | 'push' | 'pull' | 'fetch'
  ahead: number
  behind: number
  selection: Selection
  onSelect: (sel: Selection) => void
  onStage: (path: string) => void
  onUnstage: (path: string) => void
  onCommitMessageChange: (s: string) => void
  onCommit: (e: FormEvent) => void
  onPush: () => void
  onPull: () => void
  onFetch: () => void
}

function ChangesSection({
  changes,
  commitMessage,
  committing,
  canCommit,
  syncBusy,
  ahead,
  behind,
  selection,
  onSelect,
  onStage,
  onUnstage,
  onCommitMessageChange,
  onCommit,
  onPush,
  onPull,
  onFetch,
}: ChangesSectionProps) {
  const [collapsed, setCollapsed] = useState(false)
  return (
    <section className={styles.section} data-testid="git-section-changes">
      <header className={styles.sectionHeader}>
        <button
          type="button"
          className={styles.sectionToggle}
          onClick={() => setCollapsed((v) => !v)}
          aria-expanded={!collapsed}
        >
          <span className={styles.chev}>{collapsed ? '▸' : '▾'}</span>
          Changes
          {changes.length > 0 && (
            <span className={styles.count}>{changes.length}</span>
          )}
        </button>
      </header>
      {!collapsed && (
        <>
          <ul className={styles.fileList} data-testid="git-changes-list">
            {changes.length === 0 && <li className={styles.empty}>No changes</li>}
            {changes.map((c) => {
              const selected =
                selection.kind === 'uncommitted' &&
                selection.path === c.path &&
                selection.staged === c.staged
              return (
                <li
                  key={`${c.path}:${c.staged ? 's' : 'w'}`}
                  className={selected ? styles.fileRowActive : styles.fileRow}
                  data-testid={`git-change-${c.path}`}
                  role="button"
                  tabIndex={0}
                  onClick={() =>
                    onSelect({ kind: 'uncommitted', path: c.path, staged: c.staged })
                  }
                  onKeyDown={(ev) => {
                    if (ev.key === 'Enter' || ev.key === ' ') {
                      ev.preventDefault()
                      onSelect({ kind: 'uncommitted', path: c.path, staged: c.staged })
                    }
                  }}
                >
                  <span className={`${styles.statusLetter} ${statusClass(c.status)}`}>
                    {c.status}
                  </span>
                  <span className={styles.filePath} title={c.path}>{c.path}</span>
                  <button
                    type="button"
                    className={styles.iconBtn}
                    title={c.staged ? 'Unstage' : 'Stage'}
                    onClick={(ev) => {
                      ev.stopPropagation()
                      c.staged ? onUnstage(c.path) : onStage(c.path)
                    }}
                    data-testid={`git-${c.staged ? 'unstage' : 'stage'}-${c.path}`}
                  >
                    {c.staged ? '−' : '+'}
                  </button>
                </li>
              )
            })}
          </ul>
          <form className={styles.commitForm} onSubmit={onCommit}>
            <textarea
              className={styles.commitMessage}
              placeholder="Commit message…"
              value={commitMessage}
              onChange={(e) => onCommitMessageChange(e.target.value)}
              rows={2}
              data-testid="git-commit-message"
            />
            <div className={styles.commitRow}>
              <button
                type="submit"
                className={styles.commitBtn}
                disabled={!canCommit || committing}
                data-testid="git-commit-btn"
              >
                {committing ? 'Committing…' : 'Commit'}
              </button>
              <button
                type="button"
                className={styles.iconBtn}
                title={`Pull${behind > 0 ? ` (↓${behind})` : ''}`}
                onClick={onPull}
                disabled={syncBusy !== null}
                data-testid="git-pull-btn"
              >
                ↓
                {behind > 0 && <span className={styles.badge}>{behind}</span>}
              </button>
              <button
                type="button"
                className={styles.iconBtn}
                title={`Push${ahead > 0 ? ` (↑${ahead})` : ''}`}
                onClick={onPush}
                disabled={syncBusy !== null}
                data-testid="git-push-btn"
              >
                ↑
                {ahead > 0 && <span className={styles.badge}>{ahead}</span>}
              </button>
              <button
                type="button"
                className={styles.iconBtn}
                title="Fetch"
                onClick={onFetch}
                disabled={syncBusy !== null}
                data-testid="git-fetch-btn"
              >
                ⟳
              </button>
            </div>
          </form>
        </>
      )}
    </section>
  )
}

function statusClass(s: string): string {
  switch (s) {
    case 'M':
      return styles.statusM
    case 'A':
      return styles.statusA
    case 'D':
      return styles.statusD
    case 'R':
      return styles.statusR
    case 'U':
      return styles.statusU
    case '?':
      return styles.statusQ
    default:
      return ''
  }
}

// ---------------------------------------------------------------------------
// History section
// ---------------------------------------------------------------------------

interface HistorySectionProps {
  log: LogEntry[]
  loading: boolean
  exhausted: boolean
  selectedSha: string | null
  uncommittedSelected: boolean
  uncommittedCount: number
  onSelect: (entry: LogEntry) => void
  onSelectUncommitted: () => void
  onLoadMore: () => void
}

function HistorySection({
  log,
  loading,
  exhausted,
  selectedSha,
  uncommittedSelected,
  uncommittedCount,
  onSelect,
  onSelectUncommitted,
  onLoadMore,
}: HistorySectionProps) {
  return (
    <section className={styles.section} data-testid="git-section-history">
      <header className={styles.sectionHeader}>
        <span className={styles.sectionLabel}>History</span>
      </header>
      <ul className={styles.historyList} data-testid="git-history-list">
        <li
          className={`${uncommittedSelected ? styles.historyRowActive : styles.historyRow} ${styles.uncommittedRow}`}
          data-testid="git-history-row-uncommitted"
          role="button"
          tabIndex={0}
          onClick={onSelectUncommitted}
          onKeyDown={(ev) => {
            if (ev.key === 'Enter' || ev.key === ' ') {
              ev.preventDefault()
              onSelectUncommitted()
            }
          }}
        >
          <span className={`${styles.dot} ${uncommittedCount > 0 ? styles.uncommittedDot : ''}`}>●</span>
          <span className={`${styles.hash} ${styles.uncommittedHash}`}>WIP</span>
          <span />
          <span className={`${styles.subject} ${styles.uncommittedSubject}`}>
            Uncommitted changes
          </span>
          <span className={styles.relTime}>
            {uncommittedCount > 0
              ? `${uncommittedCount} file${uncommittedCount === 1 ? '' : 's'}`
              : 'clean'}
          </span>
        </li>
        {log.map((e) => {
          const short = e.hash.slice(0, 7)
          const selected = e.hash === selectedSha
          return (
            <li
              key={e.hash}
              className={selected ? styles.historyRowActive : styles.historyRow}
              data-testid={`git-history-row-${short}`}
              role="button"
              tabIndex={0}
              onClick={() => onSelect(e)}
              onKeyDown={(ev) => {
                if (ev.key === 'Enter' || ev.key === ' ') {
                  ev.preventDefault()
                  onSelect(e)
                }
              }}
            >
              <span className={styles.dot}>●</span>
              <span className={styles.hash}>{short}</span>
              {e.refs && e.refs.length > 0 && (
                <span className={styles.refs}>
                  {e.refs.slice(0, 3).map((r) => (
                    <span key={r} className={styles.refChip} title={r}>{stripRef(r)}</span>
                  ))}
                </span>
              )}
              <span className={styles.subject} title={e.subject}>{e.subject}</span>
              <span className={styles.relTime}>{relTime(e.date)}</span>
            </li>
          )
        })}
        {log.length === 0 && !loading && <li className={styles.empty}>No commits</li>}
        {loading && <li className={styles.empty}>Loading…</li>}
      </ul>
      {!exhausted && !loading && log.length > 0 && (
        <button
          type="button"
          className={styles.loadMore}
          onClick={onLoadMore}
          data-testid="git-history-load-more"
        >
          Load more
        </button>
      )}
    </section>
  )
}

function stripRef(r: string): string {
  // refs come as "HEAD -> main", "origin/main", "tag: v1", etc — keep
  // them readable in narrow chips.
  return r.replace(/^HEAD ->\s*/, '').replace(/^tag:\s*/, 'tag: ')
}

function relTime(iso: string): string {
  const t = Date.parse(iso)
  if (!Number.isFinite(t)) return ''
  const d = (Date.now() - t) / 1000
  if (d < 60) return `${Math.floor(d)}s`
  if (d < 3600) return `${Math.floor(d / 60)}m`
  if (d < 86400) return `${Math.floor(d / 3600)}h`
  if (d < 86400 * 7) return `${Math.floor(d / 86400)}d`
  if (d < 86400 * 30) return `${Math.floor(d / (86400 * 7))}w`
  return `${Math.floor(d / (86400 * 30))}mo`
}

// ---------------------------------------------------------------------------
// Commit files pane — list of files inside a selected commit, rendered
// in the top-left sidebar pane (S029-fix). Replaces the old horizontal
// `commitDiffFiles` strip that lived above the diff. Selection is owned
// by the parent (selection.path), so this component is purely
// presentational.
// ---------------------------------------------------------------------------

interface CommitFilesPaneProps {
  sha: string
  refs?: string[]
  files: CommitDiffFile[]
  loading: boolean
  error: string | null
  selectedPath?: string
  onSelectFile: (path: string) => void
}

function CommitFilesPane({
  sha,
  refs,
  files,
  loading,
  error,
  selectedPath,
  onSelectFile,
}: CommitFilesPaneProps) {
  return (
    <section className={styles.section} data-testid="git-section-commit-files">
      <header className={styles.sectionHeader}>
        <span className={styles.sectionLabel}>
          <span className={styles.commitDiffSha} data-testid="git-commit-diff-sha">
            {sha.slice(0, 7)}
          </span>
          {refs && refs.length > 0 && (
            <span className={styles.commitDiffRefs}>
              {refs.slice(0, 3).map((r) => (
                <span key={r} className={styles.refChip} title={r}>{stripRef(r)}</span>
              ))}
            </span>
          )}
        </span>
        {files.length > 0 && <span className={styles.count}>{files.length}</span>}
      </header>
      {error && <p className={styles.errorBanner}>{error}</p>}
      <ul className={styles.fileList} data-testid="git-commit-files-list">
        {loading && <li className={styles.empty}>Loading…</li>}
        {!loading && !error && files.length === 0 && (
          <li className={styles.empty}>(no changes)</li>
        )}
        {files.map((f) => {
          const path = f.newPath || f.oldPath
          const status = commitFileStatus(f)
          const selected = selectedPath === path
          return (
            <li
              key={path}
              className={selected ? styles.fileRowActive : styles.fileRow}
              data-testid={`git-commit-file-${path}`}
              role="button"
              tabIndex={0}
              onClick={() => onSelectFile(path)}
              onKeyDown={(ev) => {
                if (ev.key === 'Enter' || ev.key === ' ') {
                  ev.preventDefault()
                  onSelectFile(path)
                }
              }}
            >
              <span className={`${styles.statusLetter} ${statusClass(status)}`}>
                {status}
              </span>
              <span className={styles.filePath} title={path}>{path}</span>
              <span />
            </li>
          )
        })}
      </ul>
    </section>
  )
}

function commitFileStatus(f: CommitDiffFile): string {
  if (!f.oldPath) return 'A'
  if (!f.newPath) return 'D'
  if (f.oldPath !== f.newPath) return 'R'
  return 'M'
}

interface CommitFileDiffProps {
  apiBase: string
  sha: string
  path: string
  reloadKey: number
}

// CommitFileDiff is a minimal Monaco diff viewer pinned to a specific
// commit. It avoids reusing GitMonacoDiff because that one is tied to
// "working vs HEAD" semantics — for committed history we want
// `<sha>^:path` vs `<sha>:path`. Image files are routed to ImagePair
// instead of Monaco since text DiffEditor can't render binary blobs.
function CommitFileDiff({ apiBase, sha, path, reloadKey }: CommitFileDiffProps) {
  const [orig, setOrig] = useState<string | null>(null)
  const [mod, setMod] = useState<string | null>(null)
  const [err, setErr] = useState<string | null>(null)

  const isImage = isImageFile(path)

  useEffect(() => {
    if (isImage) {
      // Skip the text fetch for images — ImagePair pulls the bytes
      // directly via /git/raw which preserves binary data.
      return
    }
    let cancelled = false
    setOrig(null)
    setMod(null)
    setErr(null)
    Promise.all([
      api
        .get<{ content: string }>(
          `${apiBase}/show?ref=${encodeURIComponent(sha + '^')}&path=${encodeURIComponent(path)}`,
        )
        .then((r) => r.content)
        .catch(() => ''),
      api
        .get<{ content: string }>(
          `${apiBase}/show?ref=${encodeURIComponent(sha)}&path=${encodeURIComponent(path)}`,
        )
        .then((r) => r.content)
        .catch((e) => {
          // Most likely a deleted file at sha — return ''.
          if (e instanceof ApiError && e.status === 404) return ''
          throw e
        }),
    ])
      .then(([o, m]) => {
        if (cancelled) return
        setOrig(o)
        setMod(m)
      })
      .catch((e) => {
        if (!cancelled) setErr(e instanceof Error ? e.message : String(e))
      })
    return () => {
      cancelled = true
    }
  }, [apiBase, sha, path, reloadKey, isImage])

  if (isImage) {
    const enc = encodeURIComponent(path)
    const shaShort = sha.slice(0, 7)
    return (
      <div className={styles.commitFileDiff}>
        <header className={styles.commitDiffSubheader}>{path}</header>
        <ImagePair
          leftSrc={`${apiBase}/raw?ref=${encodeURIComponent(sha + '^')}&path=${enc}`}
          rightSrc={`${apiBase}/raw?ref=${encodeURIComponent(sha)}&path=${enc}`}
          leftLabel={`${shaShort}^`}
          rightLabel={shaShort}
        />
      </div>
    )
  }

  return (
    <div className={styles.commitFileDiff}>
      <header className={styles.commitDiffSubheader}>{path}</header>
      {err && <p className={styles.errorBanner}>{err}</p>}
      {orig !== null && mod !== null ? (
        <LazyMonacoDiff path={path} original={orig} modified={mod} />
      ) : (
        <p className={styles.empty}>Loading…</p>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Lazy-mount Monaco DiffEditor for commit-pinned diffs. The package is
// the same one GitMonacoDiff uses; chunk-splitting keeps the initial
// bundle small.
// ---------------------------------------------------------------------------

interface LazyMonacoDiffProps {
  path: string
  original: string
  modified: string
}

function LazyMonacoDiff({ path, original, modified }: LazyMonacoDiffProps) {
  const language = useMemo(() => monacoLanguageFor(path), [path])
  // Force inline (unified) on narrow viewports so the diff stays
  // readable (DESIGN_PRINCIPLES priority 10 — mobile parity).
  const sideBySide = useViewportWide(900)
  return (
    <div className={styles.monacoFrame}>
      <DiffEditor
        height="100%"
        language={language}
        original={original}
        modified={modified}
        theme="vs-dark"
        options={{
          renderSideBySide: sideBySide,
          readOnly: true,
          automaticLayout: true,
          minimap: { enabled: false },
          quickSuggestions: false,
          codeLens: false,
          folding: true,
        }}
      />
    </div>
  )
}

function useViewportWide(min: number): boolean {
  const [wide, setWide] = useState(() =>
    typeof window === 'undefined' ? true : window.innerWidth >= min,
  )
  useEffect(() => {
    const onResize = () => setWide(window.innerWidth >= min)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [min])
  return wide
}

// ---------------------------------------------------------------------------
// Conflict banner
// ---------------------------------------------------------------------------

interface ConflictBannerProps {
  conflicts: FileStatus[]
  canContinue: boolean
  onContinueMerge: () => void
}

function ConflictBanner({ conflicts, canContinue, onContinueMerge }: ConflictBannerProps) {
  // S029 deliberately drops the in-tab 3-way merge editor. Conflicts are
  // surfaced and the user is sent to the Files tab (or Bash / Claude)
  // to edit the markers directly. After every conflicted path has been
  // staged, "Continue" finalises the merge with `git commit` (the
  // server uses the pre-prepared MERGE_MSG when the message is empty).
  return (
    <div className={styles.conflictBanner} data-testid="git-conflict-banner">
      <strong>Merge in progress</strong> — {conflicts.length} file
      {conflicts.length === 1 ? '' : 's'} conflicted
      <ul className={styles.conflictList}>
        {conflicts.map((c) => (
          <li key={c.path}>{c.path}</li>
        ))}
      </ul>
      <p className={styles.conflictHint}>
        Edit conflict markers in the Files tab (or in Bash / Claude),
        then stage the resolved files. Once everything is staged,
        click <strong>Continue</strong> to finalise the merge.
      </p>
      <button
        type="button"
        className={styles.continueBtn}
        onClick={onContinueMerge}
        disabled={!canContinue}
        title={canContinue ? 'Finalise the merge commit' : 'Stage all resolved files first'}
        data-testid="git-continue-merge"
      >
        Continue
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Branch status bar (bottom)
// ---------------------------------------------------------------------------

interface BranchStatusBarProps {
  branchName: string
  branches: BranchEntry[]
  ahead: number
  behind: number
  onSwitch: (name: string) => Promise<void>
  onCreate: (name: string) => Promise<void>
}

function BranchStatusBar({
  branchName,
  branches,
  ahead,
  behind,
  onSwitch,
  onCreate,
}: BranchStatusBarProps) {
  const [open, setOpen] = useState(false)
  const [filter, setFilter] = useState('')
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const ref = useRef<HTMLDivElement | null>(null)

  // Click-outside closes the dropdown.
  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => {
      if (!ref.current) return
      if (!ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])

  const filtered = useMemo(() => {
    const f = filter.trim().toLowerCase()
    return branches.filter(
      (b) => !b.isRemote && (f === '' || b.name.toLowerCase().includes(f)),
    )
  }, [branches, filter])

  return (
    <footer className={styles.statusBar} data-testid="git-status-bar" ref={ref}>
      <button
        type="button"
        className={styles.statusBarBtn}
        onClick={() => setOpen((v) => !v)}
        data-testid="git-branch-switcher-btn"
        aria-expanded={open}
      >
        <span className={styles.branchIcon}>⎇</span>
        <span className={styles.branchName}>{branchName}</span>
        {(ahead > 0 || behind > 0) && (
          <span className={styles.aheadBehind}>
            {ahead > 0 && `↑${ahead}`}
            {ahead > 0 && behind > 0 && ' '}
            {behind > 0 && `↓${behind}`}
          </span>
        )}
      </button>
      {open && (
        <div className={styles.branchDropdown} data-testid="git-branch-dropdown">
          {!creating ? (
            <>
              <input
                type="text"
                placeholder="Filter branches…"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                className={styles.branchFilter}
                autoFocus
              />
              <ul className={styles.branchList}>
                {filtered.length === 0 && <li className={styles.empty}>No matches</li>}
                {filtered.map((b) => (
                  <li
                    key={b.name}
                    className={b.isHead ? styles.branchRowActive : styles.branchRow}
                    role="button"
                    tabIndex={0}
                    onClick={() => {
                      setOpen(false)
                      void onSwitch(b.name)
                    }}
                    onKeyDown={(ev) => {
                      if (ev.key === 'Enter' || ev.key === ' ') {
                        ev.preventDefault()
                        setOpen(false)
                        void onSwitch(b.name)
                      }
                    }}
                    data-testid={`git-branch-row-${b.name}`}
                  >
                    <span className={styles.branchRowName}>{b.name}</span>
                    {b.isHead && <span className={styles.refChip}>current</span>}
                    {b.upstream && (
                      <span className={styles.branchRowUpstream}>{b.upstream}</span>
                    )}
                  </li>
                ))}
              </ul>
              <button
                type="button"
                className={styles.branchCreateBtn}
                onClick={() => setCreating(true)}
                data-testid="git-branch-create-btn"
              >
                + Create branch
              </button>
            </>
          ) : (
            <form
              className={styles.branchCreateForm}
              onSubmit={async (e) => {
                e.preventDefault()
                if (!newName.trim()) return
                await onCreate(newName.trim())
                setNewName('')
                setCreating(false)
                setOpen(false)
              }}
            >
              <input
                type="text"
                placeholder="new branch name"
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                className={styles.branchFilter}
                autoFocus
              />
              <div className={styles.branchCreateRow}>
                <button type="submit" className={styles.commitBtn}>Create</button>
                <button
                  type="button"
                  className={styles.iconBtn}
                  onClick={() => {
                    setCreating(false)
                    setNewName('')
                  }}
                >
                  ✕
                </button>
              </div>
            </form>
          )}
        </div>
      )}
    </footer>
  )
}

