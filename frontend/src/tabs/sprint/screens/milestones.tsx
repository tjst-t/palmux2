// Milestones screen (Se173ef, Option A NEW tab) — walks each milestone
// sprint's "何が変わったか (comprehension-report)" + "何を妥協したか
// (compromises)" + verification verdict. Data comes from GET
// /sprint/milestones. Older milestone sprints with no comprehension report
// degrade to a "過去 Sprint は記録対象外" note (§2.9 backward compat).

import { useCallback } from 'react'

import { MarkdownBlock } from '../../../components/markdown-block'
import { sprintApi } from '../api'
import styles from '../sprint-view.module.css'
import type { MilestoneEntry, MilestonesResponse } from '../types'
import { useSprintData } from '../use-sprint-data'

import { severityClass, verdictClass } from './view-helpers'
import { ErrorBanner, ParseErrorsBanner, ViewHeader } from './view-header'

interface MilestonesViewProps {
  repoId: string
  branchId: string
  onOpenSprint: (id: string) => void
}

export function MilestonesView({ repoId, branchId, onOpenSprint }: MilestonesViewProps) {
  const fetcher = useCallback(
    (prev: string | null) => sprintApi.milestones(repoId, branchId, prev),
    [repoId, branchId],
  )
  const { data, loading, error, offline, refresh } = useSprintData<MilestonesResponse>({
    repoId,
    branchId,
    scope: 'milestones',
    fetcher,
  })

  return (
    <div data-testid="sprint-milestones">
      <ViewHeader
        title="Milestones"
        offline={offline}
        loading={loading}
        onRefresh={refresh}
        testIdPrefix="sprint-milestones"
      />
      <ErrorBanner message={error} />
      <ParseErrorsBanner errors={data?.parseErrors} />

      {!data && !error && <div className={styles.empty}>Loading…</div>}

      {data && data.milestones.length === 0 && (
        <p className={styles.emptyNote}>マイルストーンスプリントはまだありません。</p>
      )}

      {data &&
        data.milestones.map((m) => (
          <MilestonePanel key={m.sprintId} m={m} onOpenSprint={onOpenSprint} />
        ))}
    </div>
  )
}

function MilestonePanel({
  m,
  onOpenSprint,
}: {
  m: MilestoneEntry
  onOpenSprint: (id: string) => void
}) {
  const hasComprehension = !!m.comprehension?.markdown
  const cm = m.compromises
  return (
    <section className={styles.section} data-testid={`sprint-milestone-entry-${m.sprintId}`}>
      <div className={styles.milestoneHead}>
        <h3 className={styles.sectionTitle} style={{ margin: 0 }}>
          ★{' '}
          <button type="button" className={styles.iconButton} onClick={() => onOpenSprint(m.sprintId)}>
            {m.sprintId}
          </button>{' '}
          {m.title}
        </h3>
        <div className={styles.milestoneMeta}>
          {m.phase && <span className={`${styles.badge} ${styles.badgePhase}`}>{m.phase}</span>}
          <span className={`${styles.badge}`}>{m.status}</span>
          {m.verifyRunOverall && (
            <span className={`${styles.runchip} ${styles.runchipInline}`}>
              <span className={verdictClass(m.verifyRunOverall)}>●</span> 機械判定 {m.verifyRunOverall}
            </span>
          )}
          {m.verifierOverall && (
            <span className={`${styles.runchip} ${styles.runchipInline}`}>
              <span className={verdictClass(m.verifierOverall)}>●</span> verifier {m.verifierOverall}
            </span>
          )}
        </div>
      </div>

      <div className={styles.milestoneGrid}>
        <div data-testid={`sprint-milestone-comprehension-${m.sprintId}`}>
          <div className={styles.subLabel}>Comprehension Report</div>
          {hasComprehension ? (
            <div className={styles.comprehension}>
              <MarkdownBlock>{m.comprehension!.markdown}</MarkdownBlock>
            </div>
          ) : (
            <p className={styles.emptyNote}>
              この時点の comprehension-report.md は未生成（旧スプリント）。§2.9 後方互換で「過去 Sprint は記録対象外」。
            </p>
          )}
        </div>
        <div data-testid={`sprint-milestone-compromises-${m.sprintId}`}>
          <div className={styles.subLabel}>Compromises</div>
          {!cm || (cm.compromises.length === 0 && cm.blockers.length === 0 && cm.scopeChanges.length === 0) ? (
            <div className={styles.crItem}>
              <span className={severityClass('low')}>妥協 0</span> surviving compromise なし
            </div>
          ) : (
            <div className={styles.crList}>
              {cm.compromises.map((c, i) => (
                <div key={`c${i}`} className={styles.crItem}>
                  <span className={severityClass(c.severity ?? 'low')}>{c.severity}</span>
                  {c.type} — {c.rationale}
                </div>
              ))}
              {cm.blockers.map((b, i) => (
                <div key={`b${i}`} className={styles.crItem}>
                  <span className={severityClass(b.severity ?? 'med')}>blocker</span>
                  {b.detail}
                </div>
              ))}
              {cm.scopeChanges.map((s, i) => (
                <div key={`s${i}`} className={styles.crItem}>
                  <span className={severityClass(s.severity ?? 'low')}>scope</span>
                  {s.detail}
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </section>
  )
}
