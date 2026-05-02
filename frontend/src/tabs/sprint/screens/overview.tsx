// Overview screen — project header + progress bar + current sprint
// summary + active autopilot list + sprint timeline.

import { useCallback } from 'react'

import { sprintApi } from '../api'
import styles from '../sprint-view.module.css'
import type { OverviewResponse } from '../types'
import { useSprintData } from '../use-sprint-data'

import { ErrorBanner, ParseErrorsBanner, ViewHeader } from './view-header'
import { statusClass } from './view-helpers'

interface OverviewViewProps {
  repoId: string
  branchId: string
  onOpenSprint: (sprintId: string) => void
}

export function OverviewView({ repoId, branchId, onOpenSprint }: OverviewViewProps) {
  const fetcher = useCallback(
    (prev: string | null) => sprintApi.overview(repoId, branchId, prev),
    [repoId, branchId],
  )
  const { data, loading, error, offline, refresh } = useSprintData<OverviewResponse>({
    repoId,
    branchId,
    scope: 'overview',
    fetcher,
  })

  return (
    <>
      <ViewHeader
        title="Overview"
        offline={offline}
        loading={loading}
        onRefresh={refresh}
        testIdPrefix="sprint-overview"
      />
      <ErrorBanner message={error} />
      <ParseErrorsBanner errors={data?.parseErrors} />

      {!data && !error && <div className={styles.empty}>Loading…</div>}

      {data && (
        <>
          <section className={styles.section}>
            <h3 className={styles.sectionTitle} data-testid="sprint-overview-project">
              {data.project || 'Untitled roadmap'}
            </h3>
            {data.vision && <p style={{ margin: 0, color: 'var(--color-fg-muted)', fontSize: 13 }}>{data.vision}</p>}
            <div style={{ marginTop: 12 }} data-testid="sprint-overview-progress">
              <div className={styles.progressTrack} aria-label="overall progress">
                <div
                  className={styles.progressFill}
                  style={{ width: `${Math.min(100, data.progress.percent)}%` }}
                />
              </div>
              <span className={styles.progressLabel}>
                {data.progress.done} / {data.progress.total} sprints ({data.progress.percent.toFixed(1)}%)
                {data.progress.inProgress > 0 ? ` · ${data.progress.inProgress} in progress` : ''}
              </span>
            </div>
          </section>

          {data.currentSprint && (
            <section className={styles.section}>
              <h3 className={styles.sectionTitle}>Current sprint</h3>
              <p style={{ margin: 0 }}>
                <button
                  type="button"
                  className={styles.iconButton}
                  onClick={() => onOpenSprint(data.currentSprint!.id)}
                  data-testid="sprint-overview-current"
                >
                  {data.currentSprint.id}: {data.currentSprint.title}
                </button>
                <span
                  className={statusClass(data.currentSprint.statusKind)}
                  style={{ marginLeft: 8, fontSize: 12 }}
                >
                  [{data.currentSprint.statusKind}]
                </span>
              </p>
              {data.currentSprint.description && (
                <p style={{ marginTop: 8, fontSize: 13, color: 'var(--color-fg-muted)' }}>
                  {truncate(data.currentSprint.description, 320)}
                </p>
              )}
            </section>
          )}

          <section className={styles.section} data-testid="sprint-overview-autopilot">
            <h3 className={styles.sectionTitle}>Active autopilot</h3>
            {(data.activeAutopilot ?? []).length === 0 ? (
              <p style={{ margin: 0, color: 'var(--color-fg-muted)', fontSize: 13 }}>
                No autopilot lock detected on this branch.
              </p>
            ) : (
              <div className={styles.autopilotList}>
                {(data.activeAutopilot ?? []).map((a) => (
                  <div key={a.lockPath} className={styles.autopilotItem}>
                    <span className={styles.autopilotPulse} aria-hidden />
                    <button
                      type="button"
                      className={styles.iconButton}
                      onClick={() => onOpenSprint(a.sprintId)}
                    >
                      {a.sprintId}
                    </button>
                    <span style={{ fontSize: 12, color: 'var(--color-fg-muted)' }}>
                      started {new Date(a.startedAt).toLocaleString()}
                      {a.pid ? ` · pid ${a.pid}` : ''}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </section>

          <section className={styles.section}>
            <h3 className={styles.sectionTitle}>Sprint timeline</h3>
            <div className={styles.timeline} data-testid="sprint-overview-timeline">
              {(data.timeline ?? []).map((t) => (
                <button
                  key={t.id}
                  type="button"
                  className={`${styles.timelineDot} ${statusClass(t.statusKind)}`}
                  onClick={() => onOpenSprint(t.id)}
                  data-testid={`sprint-timeline-${t.id}`}
                  data-statuskind={t.statusKind}
                >
                  <span>{t.id}</span>
                  <span style={{ color: 'var(--color-fg-muted)' }}>{truncate(t.title, 24)}</span>
                </button>
              ))}
            </div>
          </section>
        </>
      )}
    </>
  )
}

function truncate(s: string, n: number) {
  if (s.length <= n) return s
  return s.slice(0, n - 1) + '…'
}
