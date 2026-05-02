// Decision Timeline screen — cross-sprint decisions feed with category
// filter chips. ⚠️ NEEDS_HUMAN entries are surfaced via a dedicated
// filter chip (and styled inline).

import { useCallback } from 'react'

import { sprintApi } from '../api'
import styles from '../sprint-view.module.css'
import type { DecisionsResponse } from '../types'
import { useSprintData } from '../use-sprint-data'

import { ErrorBanner, ParseErrorsBanner, ViewHeader } from './view-header'

interface DecisionTimelineViewProps {
  repoId: string
  branchId: string
  filter: string
  onFilterChange: (filter: string) => void
  onOpenSprint: (id: string) => void
}

const FILTERS: Array<{ id: string; label: string }> = [
  { id: '', label: 'All' },
  { id: 'planning', label: 'Planning' },
  { id: 'implementation', label: 'Implementation' },
  { id: 'review', label: 'Review' },
  { id: 'backlog', label: 'Backlog' },
  { id: 'needs_human', label: '⚠ Needs human' },
]

export function DecisionTimelineView({
  repoId,
  branchId,
  filter,
  onFilterChange,
  onOpenSprint,
}: DecisionTimelineViewProps) {
  const fetcher = useCallback(
    (prev: string | null) => sprintApi.decisions(repoId, branchId, filter || null, prev),
    [repoId, branchId, filter],
  )
  const { data, loading, error, offline, refresh } = useSprintData<DecisionsResponse>({
    repoId,
    branchId,
    scope: 'decisions',
    fetcher,
    key: filter,
  })

  return (
    <>
      <ViewHeader
        title="Decision Timeline"
        offline={offline}
        loading={loading}
        onRefresh={refresh}
        testIdPrefix="sprint-decisions"
      />
      <ErrorBanner message={error} />
      <ParseErrorsBanner errors={data?.parseErrors} />

      <div className={styles.filterBar} data-testid="sprint-decisions-filters">
        {FILTERS.map((f) => (
          <button
            key={f.id || 'all'}
            type="button"
            className={`${styles.filterChip} ${filter === f.id ? styles.filterChipActive : ''}`}
            data-testid={`sprint-decisions-filter-${f.id || 'all'}`}
            onClick={() => onFilterChange(f.id)}
          >
            {f.label}
          </button>
        ))}
      </div>

      {!data && !error && <div className={styles.empty}>Loading…</div>}

      {data && (data.entries ?? []).length === 0 && (
        <div className={styles.empty}>No decisions match the current filter.</div>
      )}

      {data && (data.entries ?? []).length > 0 && (
        <div className={styles.decisionList} data-testid="sprint-decisions-list">
          {(data.entries ?? []).map((d, i) => (
            <div key={`${d.sprintId}-${i}`} className={styles.decisionItem}>
              <div className={styles.decisionMeta}>
                <button
                  type="button"
                  className={styles.iconButton}
                  style={{ padding: '0 6px', fontSize: 11 }}
                  onClick={() => onOpenSprint(d.sprintId)}
                >
                  {d.sprintId}
                </button>
                <span style={{ textTransform: 'uppercase', letterSpacing: '0.04em' }}>
                  {d.category}
                </span>
                {d.needsHuman && <span style={{ color: '#ef4444' }}>NEEDS HUMAN</span>}
              </div>
              {d.title && <p className={styles.decisionTitle}>{d.title}</p>}
              <p className={styles.decisionBody}>{d.body}</p>
            </div>
          ))}
        </div>
      )}
    </>
  )
}
