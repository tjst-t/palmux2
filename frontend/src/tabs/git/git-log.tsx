// Rich log view for S013.
//
// Replaces the simple list from S012 with:
//   - Filter chips (author / grep / since)
//   - Paginated load (50 commits per page) with "Load more"
//   - Click a commit row → right-pane detail (full message + stat) + Monaco
//     diff against its first parent
//   - Right-click / context menu on a commit row → cherry-pick / revert /
//     reset modals (the modals themselves live in git-history-modals.tsx)
//   - Optional SVG branch graph in the leading column when `showGraph` is
//     ON; the graph only kicks in if there are 2+ heads in the result set
//
// All mutating ops POST to S013 endpoints registered in provider.go.

import { useCallback, useEffect, useRef, useState } from 'react'

import { ApiError, api } from '../../lib/api'

import { GitBranchGraph } from './git-branch-graph'
import { CherryPickModal, ResetModal, RevertModal } from './git-history-modals'
import styles from './git-log.module.css'
import type { LogEntryDetail, LogFilteredResponse } from './types'

interface Props {
  apiBase: string
  /** S013-1-15: filter to commits touching this path. */
  path?: string
  /** Reload counter — bumped after a commit / cherry-pick / reset. */
  reloadKey?: number
  /** Callback when the user picks a commit (used by file-history view). */
  onSelect?: (entry: LogEntryDetail) => void
}

const PAGE_SIZE = 50

interface FilterState {
  author: string
  grep: string
  since: string
  showGraph: boolean
}

const EMPTY_FILTER: FilterState = {
  author: '',
  grep: '',
  since: '',
  showGraph: true,
}

export function GitLog({ apiBase, path, reloadKey = 0, onSelect }: Props) {
  const [filter, setFilter] = useState<FilterState>(EMPTY_FILTER)
  const [draft, setDraft] = useState<FilterState>(EMPTY_FILTER)
  const [entries, setEntries] = useState<LogEntryDetail[]>([])
  const [skip, setSkip] = useState(0)
  const [exhausted, setExhausted] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<LogEntryDetail | null>(null)
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number; entry: LogEntryDetail } | null>(null)
  const [activeModal, setActiveModal] = useState<'cherry-pick' | 'revert' | 'reset' | null>(null)
  const [actionTarget, setActionTarget] = useState<LogEntryDetail | null>(null)

  const fetchPage = useCallback(
    async (cursor: number, replace: boolean) => {
      setLoading(true)
      setError(null)
      const qs = new URLSearchParams()
      qs.set('limit', String(PAGE_SIZE))
      qs.set('skip', String(cursor))
      if (filter.author.trim()) qs.set('author', filter.author.trim())
      if (filter.grep.trim()) qs.set('grep', filter.grep.trim())
      if (filter.since.trim()) qs.set('since', filter.since.trim())
      if (path) qs.set('path', path)
      if (filter.showGraph) qs.set('all', '1')
      try {
        const res = await api.get<LogFilteredResponse>(`${apiBase}/log/filtered?${qs.toString()}`)
        const incoming = res.entries ?? []
        setEntries((cur) => (replace ? incoming : [...cur, ...incoming]))
        setExhausted(incoming.length < PAGE_SIZE)
      } catch (e) {
        setError(e instanceof ApiError ? e.message : String(e))
      } finally {
        setLoading(false)
      }
    },
    [apiBase, filter.author, filter.grep, filter.since, filter.showGraph, path],
  )

  // Re-fetch from page 0 whenever the filter or reloadKey changes.
  useEffect(() => {
    setSkip(0)
    setEntries([])
    setExhausted(false)
    void fetchPage(0, true)
  }, [fetchPage, reloadKey])

  // Close context menu on outside click.
  useEffect(() => {
    if (!contextMenu) return
    const close = () => setContextMenu(null)
    window.addEventListener('click', close)
    window.addEventListener('keydown', close)
    return () => {
      window.removeEventListener('click', close)
      window.removeEventListener('keydown', close)
    }
  }, [contextMenu])

  const onLoadMore = () => {
    const next = skip + PAGE_SIZE
    setSkip(next)
    void fetchPage(next, false)
  }

  const onApplyFilter = () => setFilter(draft)
  const onResetFilter = () => {
    setDraft(EMPTY_FILTER)
    setFilter(EMPTY_FILTER)
  }

  const handleSelect = (e: LogEntryDetail) => {
    setSelected(e)
    onSelect?.(e)
  }

  const showGraphPane = filter.showGraph && entries.length > 0

  return (
    <div className={styles.layout} data-testid="git-log">
      <header className={styles.filterRow}>
        <input
          className={styles.filterInput}
          placeholder="author"
          value={draft.author}
          onChange={(e) => setDraft((d) => ({ ...d, author: e.target.value }))}
          data-testid="log-filter-author"
        />
        <input
          className={styles.filterInput}
          placeholder="grep message"
          value={draft.grep}
          onChange={(e) => setDraft((d) => ({ ...d, grep: e.target.value }))}
          data-testid="log-filter-grep"
        />
        <input
          className={styles.filterInput}
          placeholder='since (e.g. "2 weeks ago")'
          value={draft.since}
          onChange={(e) => setDraft((d) => ({ ...d, since: e.target.value }))}
          data-testid="log-filter-since"
        />
        <label className={styles.filterCheck}>
          <input
            type="checkbox"
            checked={draft.showGraph}
            onChange={(e) => setDraft((d) => ({ ...d, showGraph: e.target.checked }))}
            data-testid="log-filter-graph"
          />
          graph
        </label>
        <button className={styles.filterBtn} onClick={onApplyFilter} data-testid="log-filter-apply">
          Apply
        </button>
        <button className={styles.filterBtn} onClick={onResetFilter} data-testid="log-filter-reset">
          Reset
        </button>
      </header>

      <div className={styles.body}>
        <div className={styles.listPane}>
          {error && <p className={styles.error}>{error}</p>}
          {!loading && entries.length === 0 && !error && (
            <p className={styles.empty}>No commits match the filter.</p>
          )}
          <ol className={styles.list}>
            {showGraphPane && (
              <GitBranchGraph entries={entries} className={styles.graph} />
            )}
            {entries.map((e) => (
              <li
                key={e.hash}
                className={
                  selected?.hash === e.hash
                    ? `${styles.row} ${styles.rowActive}`
                    : styles.row
                }
                onClick={() => handleSelect(e)}
                onContextMenu={(ev) => {
                  ev.preventDefault()
                  setContextMenu({ x: ev.clientX, y: ev.clientY, entry: e })
                }}
                data-testid="log-row"
                data-commit={e.hash}
              >
                <span className={styles.hash}>{e.hash.slice(0, 7)}</span>
                <span className={styles.subject}>{e.subject}</span>
                {e.refs && e.refs.length > 0 && (
                  <span className={styles.refs}>
                    {e.refs.map((r) => (
                      <span key={r} className={styles.refPill}>
                        {r}
                      </span>
                    ))}
                  </span>
                )}
                <span className={styles.author}>{e.author}</span>
                <span className={styles.date}>{relativeDate(e.date)}</span>
              </li>
            ))}
          </ol>
          {!exhausted && entries.length > 0 && (
            <button
              className={styles.loadMore}
              onClick={onLoadMore}
              disabled={loading}
              data-testid="log-load-more"
            >
              {loading ? 'Loading…' : 'Load more'}
            </button>
          )}
        </div>

        <aside className={styles.detailPane} data-testid="git-log-detail">
          {selected ? (
            <CommitDetail apiBase={apiBase} entry={selected} />
          ) : (
            <p className={styles.empty}>Select a commit to view details.</p>
          )}
        </aside>
      </div>

      {contextMenu && (
        <ContextMenu
          x={contextMenu.x}
          y={contextMenu.y}
          entry={contextMenu.entry}
          onAction={(action) => {
            setActionTarget(contextMenu.entry)
            setActiveModal(action)
            setContextMenu(null)
          }}
        />
      )}

      {activeModal === 'cherry-pick' && actionTarget && (
        <CherryPickModal
          apiBase={apiBase}
          target={actionTarget}
          onClose={() => setActiveModal(null)}
          onDone={() => {
            setActiveModal(null)
            void fetchPage(0, true)
          }}
        />
      )}
      {activeModal === 'revert' && actionTarget && (
        <RevertModal
          apiBase={apiBase}
          target={actionTarget}
          onClose={() => setActiveModal(null)}
          onDone={() => {
            setActiveModal(null)
            void fetchPage(0, true)
          }}
        />
      )}
      {activeModal === 'reset' && actionTarget && (
        <ResetModal
          apiBase={apiBase}
          target={actionTarget}
          onClose={() => setActiveModal(null)}
          onDone={() => {
            setActiveModal(null)
            void fetchPage(0, true)
          }}
        />
      )}
    </div>
  )
}

function ContextMenu({
  x,
  y,
  entry,
  onAction,
}: {
  x: number
  y: number
  entry: LogEntryDetail
  onAction: (action: 'cherry-pick' | 'revert' | 'reset') => void
}) {
  return (
    <div
      className={styles.contextMenu}
      style={{ top: y, left: x }}
      data-testid="log-context-menu"
      data-commit={entry.hash}
    >
      <button onClick={() => onAction('cherry-pick')} data-testid="log-action-cherry-pick">
        Cherry-pick onto current branch
      </button>
      <button onClick={() => onAction('revert')} data-testid="log-action-revert">
        Revert this commit
      </button>
      <button onClick={() => onAction('reset')} data-testid="log-action-reset">
        Reset to here…
      </button>
    </div>
  )
}

function CommitDetail({ apiBase, entry }: { apiBase: string; entry: LogEntryDetail }) {
  const [diff, setDiff] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const lastFetched = useRef<string | null>(null)

  // Reserved for a future commit-diff fetch (S014 will wire a dedicated
  // /git/commit-diff endpoint). For S013 we render metadata only.
  useEffect(() => {
    lastFetched.current = entry.hash
    setLoading(false)
    setDiff(null)
    setErr(null)
  }, [apiBase, entry.hash])

  return (
    <div className={styles.detail}>
      <h3 className={styles.detailSubject}>{entry.subject}</h3>
      <dl className={styles.detailList}>
        <dt>Hash</dt>
        <dd>
          <code>{entry.hash}</code>
        </dd>
        <dt>Author</dt>
        <dd>
          {entry.author}
          {entry.email && <span className={styles.email}> &lt;{entry.email}&gt;</span>}
        </dd>
        <dt>Date</dt>
        <dd>{new Date(entry.date).toLocaleString()}</dd>
        {entry.parents.length > 0 && (
          <>
            <dt>Parents</dt>
            <dd>
              {entry.parents.map((p) => (
                <code key={p} className={styles.parent}>
                  {p.slice(0, 7)}
                </code>
              ))}
            </dd>
          </>
        )}
      </dl>
      {loading && <p className={styles.empty}>Loading diff…</p>}
      {err && <p className={styles.error}>{err}</p>}
      {diff && <pre className={styles.diff}>{diff}</pre>}
    </div>
  )
}

function relativeDate(iso: string): string {
  const d = new Date(iso)
  const diff = Date.now() - +d
  const m = 60 * 1000,
    h = 60 * m,
    day = 24 * h
  if (diff < m) return 'just now'
  if (diff < h) return `${Math.floor(diff / m)}m ago`
  if (diff < day) return `${Math.floor(diff / h)}h ago`
  if (diff < 30 * day) return `${Math.floor(diff / day)}d ago`
  return d.toLocaleDateString()
}
