// Sprint Detail screen — header (status / branch) + stories list +
// acceptance matrix + test results summary + recent decisions.
//
// S67cb0e-3: Prev/Next buttons + sprint select dropdown for keyboard /
// mouse navigation through all sprints in roadmap order.
// S67cb0e-4: description is rendered via MarkdownBlock.

import { useCallback, useEffect, useRef, useState } from 'react'

import { MarkdownBlock } from '../../../components/markdown-block'
import { sprintApi } from '../api'
import styles from '../sprint-view.module.css'
import type { OverviewResponse, SprintDetailResponse } from '../types'
import { useSprintData } from '../use-sprint-data'

import { ErrorBanner, ParseErrorsBanner, ViewHeader } from './view-header'
import { statusClass } from './view-helpers'

interface SprintDetailViewProps {
  repoId: string
  branchId: string
  sprintId: string
  onOpenSprint: (id: string) => void
}

export function SprintDetailView({
  repoId,
  branchId,
  sprintId,
  onOpenSprint,
}: SprintDetailViewProps) {
  const fetcher = useCallback(
    (prev: string | null) => {
      if (!sprintId) {
        return Promise.resolve({ status: 200, etag: null, body: null })
      }
      return sprintApi.sprintDetail(repoId, branchId, sprintId, prev)
    },
    [repoId, branchId, sprintId],
  )
  const { data, loading, error, offline, refresh } = useSprintData<SprintDetailResponse>({
    repoId,
    branchId,
    scope: 'sprintDetail',
    fetcher,
    key: sprintId,
  })

  // S67cb0e-3: fetch overview to get the ordered sprint list for prev/next.
  // Cached independently of sprintDetail — lightweight, rarely changes.
  const [overviewData, setOverviewData] = useState<OverviewResponse | null>(null)
  const overviewETagRef = useRef<string | null>(null)
  useEffect(() => {
    let cancelled = false
    // Reset the ETag on repoId/branchId change so a workspace switch does an
    // unconditional GET. Otherwise the stale If-None-Match could yield a 304,
    // leaving prev/next/dropdown showing the previous workspace's sprint list.
    overviewETagRef.current = null
    sprintApi.overview(repoId, branchId, overviewETagRef.current).then((res) => {
      if (cancelled) return
      if (res.status !== 304 && res.body !== null) {
        overviewETagRef.current = res.etag
        setOverviewData(res.body)
      }
    }).catch(() => {/* silently ignore — prev/next just won't render */})
    return () => { cancelled = true }
  }, [repoId, branchId])

  const timeline = overviewData?.timeline ?? []
  const currentIndex = timeline.findIndex((t) => t.id === sprintId)
  const prevId = currentIndex > 0 ? timeline[currentIndex - 1].id : null
  // When the current sprint isn't in the list (currentIndex === -1) keep Prev
  // disabled but let Next jump to the first real sprint, so navigation isn't a
  // dead end for a renamed/deleted sprint.
  const nextId =
    currentIndex >= 0
      ? currentIndex < timeline.length - 1
        ? timeline[currentIndex + 1].id
        : null
      : (timeline[0]?.id ?? null)

  if (!sprintId) {
    return (
      <>
        <ViewHeader
          title="Sprint Detail"
          offline={offline}
          loading={false}
          onRefresh={refresh}
          testIdPrefix="sprint-detail"
        />
        <div className={styles.empty}>
          Pick a sprint from the Overview timeline or the Dependency Graph.
        </div>
      </>
    )
  }

  return (
    <>
      <ViewHeader
        title={`Sprint Detail · ${sprintId}`}
        offline={offline}
        loading={loading}
        onRefresh={refresh}
        testIdPrefix="sprint-detail"
      >
        {/* S67cb0e-3: prev/next/dropdown navigation */}
        {timeline.length > 0 && (
          <>
            <button
              type="button"
              className={styles.navBtn}
              data-testid="sprint-detail-prev"
              disabled={prevId === null}
              onClick={() => prevId && onOpenSprint(prevId)}
              title="Previous sprint"
            >
              ◀ Prev
            </button>
            <select
              className={styles.sprintSelect}
              data-testid="sprint-detail-sprint-select"
              aria-label="Jump to sprint"
              // When the current sprintId isn't in the fetched timeline
              // (e.g. renamed/deleted sprint), select a placeholder option
              // so the dropdown always reflects what the body actually shows
              // rather than silently claiming sprint[0].
              value={sprintId}
              onChange={(e) => {
                if (e.target.value) onOpenSprint(e.target.value)
              }}
            >
              {currentIndex < 0 && (
                <option value={sprintId}>{sprintId} (not in roadmap)</option>
              )}
              {timeline.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.id} — {t.title}
                </option>
              ))}
            </select>
            <button
              type="button"
              className={styles.navBtn}
              data-testid="sprint-detail-next"
              disabled={nextId === null}
              onClick={() => nextId && onOpenSprint(nextId)}
              title="Next sprint"
            >
              Next ▶
            </button>
          </>
        )}
      </ViewHeader>
      <ErrorBanner message={error} />
      <ParseErrorsBanner errors={data?.parseErrors} />

      {!data && !error && <div className={styles.empty}>Loading…</div>}

      {data && (
        <>
          <section className={styles.section}>
            <h3 className={styles.sectionTitle}>
              {data.sprint.id}: {data.sprint.title}
              <span
                className={statusClass(data.sprint.statusKind)}
                style={{ marginLeft: 8, fontSize: 12 }}
                data-testid="sprint-detail-status"
              >
                [{data.sprint.statusKind}]
              </span>
            </h3>
            {data.sprint.description && (
              <div style={{ marginTop: 8 }} data-testid="sprint-detail-description">
                <MarkdownBlock>{data.sprint.description}</MarkdownBlock>
              </div>
            )}
          </section>

          <section className={styles.section}>
            <h3 className={styles.sectionTitle}>Stories ({(data.sprint.stories ?? []).length})</h3>
            <div className={styles.storiesList}>
              {(data.sprint.stories ?? []).map((s) => {
                const acs = s.acceptanceCriteria ?? []
                const tasks = s.tasks ?? []
                return (
                  <div key={s.id} className={styles.storyItem}>
                    <div className={styles.storyHeader}>
                      <h4>
                        {s.id}: {s.title}
                      </h4>
                      <span className={statusClass(s.statusKind)} style={{ fontSize: 12 }}>
                        [{s.statusKind}]
                      </span>
                    </div>
                    {s.userStory && (
                      <p className={styles.storyMeta} style={{ fontStyle: 'italic' }}>
                        {s.userStory}
                      </p>
                    )}
                    <div className={styles.storyMeta}>
                      {acs.length} acceptance criteria · {tasks.length} tasks
                    </div>
                    {acs.length > 0 && (
                      <ul className={styles.acList}>
                        {acs.map((ac, i) => (
                          <li key={i} className={ac.done ? styles.acDone : ''}>
                            {ac.done ? '✓' : '○'} {ac.text}
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                )
              })}
            </div>
          </section>

          <section className={styles.section}>
            <h3 className={styles.sectionTitle}>Acceptance matrix</h3>
            {(data.acceptanceMatrix ?? []).length === 0 ? (
              <p style={{ margin: 0, color: 'var(--color-fg-muted)', fontSize: 13 }}>
                No AC-tagged tests detected. Add lines like
                <code style={{ marginLeft: 4 }}>{'[AC-S016-1-1]'}</code> in your test files
                to populate this matrix.
              </p>
            ) : (
              <table className={styles.matrixTable}>
                <thead>
                  <tr>
                    <th>AC ID</th>
                    <th>Status</th>
                    <th>Notes</th>
                  </tr>
                </thead>
                <tbody>
                  {(data.acceptanceMatrix ?? []).map((row, i) => (
                    <tr key={i}>
                      <td>{row.acId}</td>
                      <td>
                        <span
                          className={`${styles.statusBadge} ${
                            row.status === 'pass'
                              ? styles.statusBadgePass
                              : row.status === 'fail'
                                ? styles.statusBadgeFail
                                : styles.statusBadgeNoTest
                          }`}
                        >
                          {row.status}
                        </span>
                      </td>
                      <td style={{ fontSize: 11, color: 'var(--color-fg-muted)' }}>{row.notes}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </section>

          <section className={styles.section}>
            <h3 className={styles.sectionTitle}>Test results</h3>
            <div className={styles.testSummaryRow}>
              {(['mock', 'e2e', 'acceptance'] as const).map((b) => {
                const v = data.e2eResults[b]
                return (
                  <div key={b} className={styles.testSummaryCell}>
                    <strong>
                      {v.passed} / {v.total}
                    </strong>
                    <span>{b}</span>
                  </div>
                )
              })}
            </div>
          </section>

          <section className={styles.section}>
            <h3 className={styles.sectionTitle}>Recent decisions</h3>
            {(data.decisions ?? []).length === 0 ? (
              <p style={{ margin: 0, color: 'var(--color-fg-muted)', fontSize: 13 }}>
                No decisions logged for this sprint yet.
              </p>
            ) : (
              <div className={styles.decisionList}>
                {(data.decisions ?? []).slice(0, 8).map((d, i) => (
                  <div key={i} className={styles.decisionItem}>
                    <div className={styles.decisionMeta}>
                      <span style={{ textTransform: 'uppercase', letterSpacing: '0.04em' }}>{d.category}</span>
                      {d.needsHuman && <span style={{ color: '#ef4444' }}>NEEDS HUMAN</span>}
                    </div>
                    {d.title && <p className={styles.decisionTitle}>{d.title}</p>}
                    <p className={styles.decisionBody}>{d.body}</p>
                  </div>
                ))}
                {(data.decisions ?? []).length > 8 && (
                  <button
                    type="button"
                    className={styles.iconButton}
                    onClick={() => onOpenSprint(data.sprint.id)}
                  >
                    See all {(data.decisions ?? []).length} decisions in the timeline
                  </button>
                )}
              </div>
            )}
          </section>
        </>
      )}
    </>
  )
}
