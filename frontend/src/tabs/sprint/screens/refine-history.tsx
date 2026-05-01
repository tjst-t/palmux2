// Refine History screen — shows every numbered entry from each sprint's
// refine.md, ordered by sprint ID then number.

import { useCallback } from 'react'

import { sprintApi } from '../api'
import styles from '../sprint-view.module.css'
import type { RefineResponse } from '../types'
import { useSprintData } from '../use-sprint-data'

import { ErrorBanner, ViewHeader } from './view-header'

interface RefineHistoryViewProps {
  repoId: string
  branchId: string
}

export function RefineHistoryView({ repoId, branchId }: RefineHistoryViewProps) {
  const fetcher = useCallback(
    (prev: string | null) => sprintApi.refine(repoId, branchId, prev),
    [repoId, branchId],
  )
  const { data, loading, error, offline, refresh } = useSprintData<RefineResponse>({
    repoId,
    branchId,
    scope: 'refine',
    fetcher,
  })

  return (
    <>
      <ViewHeader
        title="Refine History"
        offline={offline}
        loading={loading}
        onRefresh={refresh}
        testIdPrefix="sprint-refine"
      />
      <ErrorBanner message={error} />

      {!data && !error && <div className={styles.empty}>Loading…</div>}

      {data && data.entries.length === 0 && (
        <div className={styles.empty}>No refine.md files found yet.</div>
      )}

      {data && data.entries.length > 0 && (
        <div className={styles.refineList} data-testid="sprint-refine-list">
          {data.entries.map((e, i) => (
            <div key={`${e.sprintId}-${i}`} className={styles.refineItem}>
              <div>
                <span className={styles.refineNumber}>
                  {e.sprintId} · #{e.number}
                </span>
              </div>
              {e.title && <p className={styles.refineTitle}>{e.title}</p>}
              {e.body && <p className={styles.refineBody}>{truncateBody(e.body, 480)}</p>}
              {e.files && e.files.length > 0 && (
                <p className={styles.refineFiles}>files: {e.files.join(', ')}</p>
              )}
            </div>
          ))}
        </div>
      )}
    </>
  )
}

function truncateBody(s: string, n: number): string {
  if (s.length <= n) return s
  return s.slice(0, n - 1) + '…'
}
