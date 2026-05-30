// Overview screen — project header + progress bar + current sprint
// summary + active autopilot list + sprint timeline (JIRA-style table).
// S67cb0e-1: timeline is now a JIRA-style table (ID / Title / Status /
// Depends on / Milestone) with sticky thead and keyboard navigation.
// S67cb0e-4: current sprint description uses MarkdownBlock.

import { useCallback } from 'react'

import { MarkdownBlock } from '../../../components/markdown-block'
import { sprintApi } from '../api'
import styles from '../sprint-view.module.css'
import type { OverviewResponse } from '../types'
import { useSprintData } from '../use-sprint-data'

import { ErrorBanner, ParseErrorsBanner, ViewHeader } from './view-header'
import { statusClass, statusPillClass } from './view-helpers'

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
                <div style={{ marginTop: 8 }} data-testid="sprint-overview-current-description">
                  <MarkdownBlock>{data.currentSprint.description}</MarkdownBlock>
                </div>
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
            <div className={styles.timelineTableWrap} data-testid="sprint-overview-timeline">
              <table className={styles.timelineTable} data-testid="sprint-timeline-table">
                <thead>
                  <tr>
                    <th style={{ width: 96 }}>Sprint</th>
                    <th>Title</th>
                    <th style={{ width: 120 }}>Status</th>
                    <th style={{ width: 170 }}>Depends on</th>
                    <th style={{ width: 90, textAlign: 'center' }}>Milestone</th>
                  </tr>
                </thead>
                <tbody>
                  {(data.timeline ?? []).map((t) => (
                    <tr
                      key={t.id}
                      role="button"
                      tabIndex={0}
                      data-testid={`sprint-timeline-${t.id}`}
                      data-statuskind={t.statusKind}
                      onClick={() => onOpenSprint(t.id)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          onOpenSprint(t.id)
                        } else if (e.key === ' ') {
                          e.preventDefault()
                          onOpenSprint(t.id)
                        }
                      }}
                    >
                      <td className={styles.cellId}>{t.id}</td>
                      <td className={styles.cellTitle}>{t.title}</td>
                      <td>
                        <span
                          className={statusPillClass(t.statusKind)}
                          data-testid={`sprint-timeline-status-${t.id}`}
                        >
                          {t.statusKind}
                        </span>
                      </td>
                      <td>
                        {(t.dependsOn ?? []).length > 0 ? (
                          <div className={styles.depChips}>
                            {(t.dependsOn ?? []).map((dep) => (
                              <button
                                key={dep}
                                type="button"
                                className={styles.depChip}
                                data-testid={`sprint-timeline-dep-${t.id}-${dep}`}
                                onClick={(e) => {
                                  e.stopPropagation()
                                  onOpenSprint(dep)
                                }}
                              >
                                {dep}
                              </button>
                            ))}
                          </div>
                        ) : (
                          <span className={styles.depNone}>—</span>
                        )}
                      </td>
                      <td style={{ textAlign: 'center' }}>
                        {t.milestone ? (
                          <span className={styles.milestoneStar} title="milestone">★</span>
                        ) : (
                          <span className={styles.milestoneNone}>—</span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </>
      )}
    </>
  )
}
