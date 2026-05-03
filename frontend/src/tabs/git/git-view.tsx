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

// Selected pane describes what the right-hand Monaco diff is showing.
// `working`  → working tree diff for one file (hot-edited by the user)
// `commit`   → diff of one historical commit (shown after History click)
// `none`     → no selection yet
type Selection =
  | { kind: 'none' }
  | { kind: 'working'; path: string; staged: boolean }
  | { kind: 'commit'; sha: string; refs?: string[] }

// Mobile sub-tab.
type MobilePane = 'changes' | 'history' | 'diff'

const HISTORY_PAGE = 50

export function GitView({ repoId, branchId }: Props) {
  const apiBase = `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/git`
  const [status, setStatus] = useState<StatusReport | null>(null)
  const [log, setLog] = useState<LogEntry[]>([])
  const [logExhausted, setLogExhausted] = useState(false)
  const [logLoading, setLogLoading] = useState(false)
  const [branches, setBranches] = useState<BranchEntry[]>([])
  const [error, setError] = useState<string | null>(null)
  const [selection, setSelection] = useState<Selection>({ kind: 'none' })
  const [reloadKey, setReloadKey] = useState(0)
  const [mobilePane, setMobilePane] = useState<MobilePane>('changes')

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
    <aside className={styles.sidebar} data-testid="git-sidebar">
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
          if (sel.kind !== 'none') setMobilePane('diff')
        }}
        onStage={onStage}
        onUnstage={onUnstage}
        onCommitMessageChange={setCommitMessage}
        onCommit={onCommit}
        onPush={() => sync('push')}
        onPull={() => sync('pull')}
        onFetch={() => sync('fetch')}
      />

      <HistorySection
        log={log}
        loading={logLoading}
        exhausted={logExhausted}
        selectedSha={selection.kind === 'commit' ? selection.sha : null}
        onSelect={(entry) => {
          setSelection({ kind: 'commit', sha: entry.hash, refs: entry.refs })
          setMobilePane('diff')
        }}
        onLoadMore={() => fetchLog(log.length)}
      />
    </aside>
  )

  const main = (
    <main className={styles.main} data-testid="git-main">
      {selection.kind === 'none' && (
        <div className={styles.emptyMain}>
          ファイルまたはコミットを選択してください
        </div>
      )}
      {selection.kind === 'working' && (
        <GitMonacoDiff
          apiBase={apiBase}
          path={selection.path}
          unified={false}
          reloadKey={reloadKey}
          onStaged={onStatusChanged}
        />
      )}
      {selection.kind === 'commit' && (
        <CommitDiffPanel
          apiBase={apiBase}
          sha={selection.sha}
          refs={selection.refs}
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
          setSelection({ kind: 'none' })
          await Promise.all([fetchStatus(), fetchLog(0), fetchBranches()])
        } catch (e) {
          setError(e instanceof Error ? e.message : String(e))
        }
      }}
      onCreate={async (name) => {
        try {
          await api.post(`${apiBase}/branches`, { name, checkout: true })
          setSelection({ kind: 'none' })
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

      <div className={styles.body} data-mobile-pane={mobilePane}>
        {sidebar}
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
                selection.kind === 'working' &&
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
                    onSelect({ kind: 'working', path: c.path, staged: c.staged })
                  }
                  onKeyDown={(ev) => {
                    if (ev.key === 'Enter' || ev.key === ' ') {
                      ev.preventDefault()
                      onSelect({ kind: 'working', path: c.path, staged: c.staged })
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
  onSelect: (entry: LogEntry) => void
  onLoadMore: () => void
}

function HistorySection({
  log,
  loading,
  exhausted,
  selectedSha,
  onSelect,
  onLoadMore,
}: HistorySectionProps) {
  return (
    <section className={styles.section} data-testid="git-section-history">
      <header className={styles.sectionHeader}>
        <span className={styles.sectionLabel}>History</span>
      </header>
      <ul className={styles.historyList} data-testid="git-history-list">
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
// Commit diff panel — list of files in the commit + Monaco diff per file.
// ---------------------------------------------------------------------------

interface CommitDiffPanelProps {
  apiBase: string
  sha: string
  refs?: string[]
  reloadKey: number
}

interface CommitDiffFile {
  oldPath: string
  newPath: string
}

function CommitDiffPanel({ apiBase, sha, refs, reloadKey }: CommitDiffPanelProps) {
  const [files, setFiles] = useState<CommitDiffFile[]>([])
  const [activeIdx, setActiveIdx] = useState(0)
  const [loadError, setLoadError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setFiles([])
    setActiveIdx(0)
    setLoadError(null)
    void api
      .get<{ files: { oldPath: string; newPath: string }[] | null }>(
        `${apiBase}/diff?sha=${encodeURIComponent(sha)}`,
      )
      .then((res) => {
        if (cancelled) return
        const list = (res.files ?? []).map((f) => ({
          oldPath: f.oldPath,
          newPath: f.newPath,
        }))
        setFiles(list)
      })
      .catch((e) => {
        if (!cancelled) {
          setLoadError(e instanceof Error ? e.message : String(e))
        }
      })
    return () => {
      cancelled = true
    }
  }, [apiBase, sha, reloadKey])

  const active = files[activeIdx]
  const path = active ? active.newPath || active.oldPath : ''

  return (
    <div className={styles.commitDiffPanel}>
      <header className={styles.commitDiffHeader}>
        <span className={styles.commitDiffSha} data-testid="git-commit-diff-sha">
          {sha.slice(0, 12)}
        </span>
        {refs && refs.length > 0 && (
          <span className={styles.commitDiffRefs}>
            {refs.map((r) => (
              <span key={r} className={styles.refChip}>{stripRef(r)}</span>
            ))}
          </span>
        )}
        <span className={styles.commitDiffCount}>
          {files.length} file{files.length === 1 ? '' : 's'}
        </span>
      </header>
      {loadError && <p className={styles.errorBanner}>{loadError}</p>}
      {files.length === 0 && !loadError && (
        <p className={styles.empty}>(no changes — first commit or merge with no diff)</p>
      )}
      {files.length > 1 && (
        <nav className={styles.commitDiffFiles} data-testid="git-commit-diff-files">
          {files.map((f, i) => (
            <button
              key={f.newPath || f.oldPath || i}
              type="button"
              className={i === activeIdx ? styles.commitDiffFileActive : styles.commitDiffFile}
              onClick={() => setActiveIdx(i)}
            >
              {f.newPath || f.oldPath}
            </button>
          ))}
        </nav>
      )}
      {active && (
        <CommitFileDiff apiBase={apiBase} sha={sha} path={path} reloadKey={reloadKey} />
      )}
    </div>
  )
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
// `<sha>^:path` vs `<sha>:path`.
function CommitFileDiff({ apiBase, sha, path, reloadKey }: CommitFileDiffProps) {
  const [orig, setOrig] = useState<string | null>(null)
  const [mod, setMod] = useState<string | null>(null)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
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
  }, [apiBase, sha, path, reloadKey])

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

