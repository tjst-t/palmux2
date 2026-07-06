// Review screen (Se173ef, Option A NEW tab) — the cross-sprint "要対応
// キュー". Aggregates everything that needs a human decision: stories in
// needs_user_review / blocked, high-severity compromises + blockers,
// verifier findings the implementer overlooked, and re-open history. This
// is the input to `autopilot review`. Data comes from GET /sprint/review.

import { useCallback } from 'react'

import { sprintApi } from '../api'
import styles from '../sprint-view.module.css'
import type { ReviewResponse } from '../types'
import { useSprintData } from '../use-sprint-data'

import { severityClass } from './view-helpers'
import { ErrorBanner, ParseErrorsBanner, ViewHeader } from './view-header'

interface ReviewViewProps {
  repoId: string
  branchId: string
  onOpenSprint: (id: string) => void
}

export function ReviewView({ repoId, branchId, onOpenSprint }: ReviewViewProps) {
  const fetcher = useCallback(
    (prev: string | null) => sprintApi.review(repoId, branchId, prev),
    [repoId, branchId],
  )
  const { data, loading, error, offline, refresh } = useSprintData<ReviewResponse>({
    repoId,
    branchId,
    scope: 'review',
    fetcher,
  })

  const counts = data?.counts

  return (
    <div data-testid="sprint-review">
      <ViewHeader
        title="Review — 要対応キュー"
        offline={offline}
        loading={loading}
        onRefresh={refresh}
        testIdPrefix="sprint-review"
      />
      <ErrorBanner message={error} />
      <ParseErrorsBanner errors={data?.parseErrors} />

      {!data && !error && <div className={styles.empty}>Loading…</div>}

      {data && (
        <>
          <p className={styles.help}>
            全スプリント横断で「人間の判断が要るもの」を集約 — <code>autopilot review</code> の入力。
          </p>

          <section className={styles.section} data-testid="sprint-review-counts">
            <div className={styles.countStrip}>
              {[
                { k: 'needs_user_review', v: counts?.needsUserReview ?? 0 },
                { k: 'blocked', v: counts?.blocked ?? 0 },
                { k: 'high compromise', v: counts?.highCompromise ?? 0 },
                { k: 'overlooked', v: counts?.overlooked ?? 0 },
                { k: 'reopen', v: counts?.reopen ?? 0 },
              ].map((c) => (
                <div key={c.k} className={styles.countCell}>
                  <div className={`${styles.countValue} ${c.v === 0 ? styles.countZero : styles.countWarn}`}>
                    {c.v}
                  </div>
                  <div className={styles.countLabel}>{c.k}</div>
                </div>
              ))}
            </div>
          </section>

          <section className={styles.section} data-testid="sprint-review-needsreview">
            <h3 className={styles.sectionTitle}>needs_user_review の Story</h3>
            {data.needsUserReview.length === 0 ? (
              <p className={styles.emptyNote}>現状は 0 件（全 Story done）。1 件でも出れば Overview のバッジが点灯し、ここに並ぶ。</p>
            ) : (
              <div className={styles.crList}>
                {data.needsUserReview.map((s) => (
                  <div key={`${s.sprintId}-${s.storyId}`} className={styles.crItem}>
                    <button
                      type="button"
                      className={styles.iconButton}
                      onClick={() => onOpenSprint(s.sprintId)}
                      data-testid={`sprint-review-story-${s.storyId}`}
                    >
                      {s.storyId}
                    </button>{' '}
                    <span className={`${styles.badge} ${styles.badgeNur}`}>needs_user_review</span>{' '}
                    {s.title}
                    {s.reviewReason && <div className={styles.acEvi}>review_reason: {s.reviewReason}</div>}
                    {s.detail && <div className={styles.acEvi}>{s.detail}</div>}
                  </div>
                ))}
              </div>
            )}
          </section>

          {data.blocked.length > 0 && (
            <section className={styles.section} data-testid="sprint-review-blocked">
              <h3 className={styles.sectionTitle}>blocked / needs_human</h3>
              <div className={styles.crList}>
                {data.blocked.map((s) => (
                  <div key={`${s.sprintId}-${s.storyId}`} className={styles.crItem}>
                    <button
                      type="button"
                      className={styles.iconButton}
                      onClick={() => onOpenSprint(s.sprintId)}
                    >
                      {s.storyId}
                    </button>{' '}
                    <span className={`${styles.badge} ${styles.badgeBlocked}`}>{s.status}</span> {s.title}
                    {s.detail && <div className={styles.acEvi}>{s.detail}</div>}
                  </div>
                ))}
              </div>
            </section>
          )}

          <section className={styles.section} data-testid="sprint-review-compromises">
            <h3 className={styles.sectionTitle}>high severity 妥協 / blocker</h3>
            {data.compromises.length === 0 ? (
              <p className={styles.emptyNote}>高深刻度の妥協はありません。</p>
            ) : (
              <div className={styles.crList}>
                {data.compromises.map((c, i) => (
                  <div key={i} className={styles.crItem}>
                    <span className={severityClass(c.severity)}>{c.severity}</span>
                    <button
                      type="button"
                      className={styles.iconButton}
                      onClick={() => onOpenSprint(c.sprintId)}
                    >
                      {c.sprintId}
                    </button>{' '}
                    <span className={styles.mono}>{c.kind}</span> {c.type} — {c.detail}
                  </div>
                ))}
              </div>
            )}
          </section>

          <section className={styles.section} data-testid="sprint-review-overlooked">
            <h3 className={styles.sectionTitle}>overlooked_by_autopilot</h3>
            {data.overlooked.length === 0 ? (
              <p className={styles.emptyNote}>verifier が見落としを拾った項目はありません（信頼の最終源はクリーン）。</p>
            ) : (
              <div className={styles.crList}>
                {data.overlooked.map((o, i) => (
                  <div key={i} className={styles.overlooked}>
                    <button
                      type="button"
                      className={styles.iconButton}
                      onClick={() => onOpenSprint(o.sprintId)}
                    >
                      {o.sprintId}
                    </button>{' '}
                    {o.ac && <span className={styles.mono}>{o.ac}</span>} <span className={styles.badge}>{o.category}</span> {o.detail}
                  </div>
                ))}
              </div>
            )}
          </section>

          <section className={styles.section} data-testid="sprint-review-reopen">
            <h3 className={styles.sectionTitle}>reopen 候補 / 履歴</h3>
            {data.reopens.length === 0 ? (
              <p className={styles.emptyNote}>
                再オープンされたスプリントはありません（reopen.json なし）。milestone review で AC 違反が見つかると、ここに triggered_by 付きで履歴が並ぶ。
              </p>
            ) : (
              <div className={styles.crList}>
                {data.reopens.map((r, i) => (
                  <div key={i} className={styles.crItem}>
                    <button
                      type="button"
                      className={styles.iconButton}
                      onClick={() => onOpenSprint(r.sprintId)}
                    >
                      {r.sprintId}
                    </button>{' '}
                    <span className={`${styles.badge} ${styles.badgeReopened}`}>{r.triggeredBy}</span>{' '}
                    {r.reopenedAt} — {r.reason}
                  </div>
                ))}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}
