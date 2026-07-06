// Overview screen — Se173ef Option A. Keeps the JIRA-style timeline table
// (S67cb0e) but adds: a roll-up strip (needs_user_review / next milestone /
// backlog counts), a phase breakdown (new ROADMAP phase field), a folded
// dependency mini-graph, and a folded Backlog panel with drill-down. The
// timeline defaults to "未完了のみ" and the backlog to "未昇格のみ"; both
// filters are transient UI state (not pushState) per priority_rule 8.

import { useCallback, useMemo, useState } from 'react'

import { MarkdownBlock } from '../../../components/markdown-block'
import { sprintApi } from '../api'
import styles from '../sprint-view.module.css'
import type { BacklogEntry, OverviewResponse, TimelineEntry } from '../types'
import { useSprintData } from '../use-sprint-data'

import { MermaidDiagram } from './mermaid-diagram'
import { ErrorBanner, ParseErrorsBanner, ViewHeader } from './view-header'
import { statusClass, statusPillClass } from './view-helpers'

interface OverviewViewProps {
  repoId: string
  branchId: string
  onOpenSprint: (sprintId: string) => void
}

type TimelineFilter = 'incomplete' | 'all'
type BacklogFilter = 'unpromoted' | 'all' | 'promoted'

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

  // Transient UI state (NOT pushState).
  const [timelineFilter, setTimelineFilter] = useState<TimelineFilter>('incomplete')
  const [backlogFilter, setBacklogFilter] = useState<BacklogFilter>('unpromoted')

  const timeline = useMemo(() => data?.timeline ?? [], [data])
  const doneCount = timeline.filter((t) => t.statusKind === 'done').length
  const shownTimeline = useMemo(
    () =>
      timelineFilter === 'incomplete'
        ? timeline.filter((t) => t.statusKind !== 'done')
        : timeline,
    [timeline, timelineFilter],
  )

  const backlog = useMemo(() => data?.backlog ?? [], [data])
  const shownBacklog = useMemo(() => {
    switch (backlogFilter) {
      case 'unpromoted':
        return backlog.filter((b) => !b.promoted)
      case 'promoted':
        return backlog.filter((b) => b.promoted)
      default:
        return backlog
    }
  }, [backlog, backlogFilter])

  const depMermaid = useMemo(() => buildDepMermaid(data), [data])

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
            {data.vision && (
              <p style={{ margin: 0, color: 'var(--color-fg-muted)', fontSize: 13 }}>{data.vision}</p>
            )}
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

          {/* Roll-up strip (Option A) */}
          <section className={styles.section} data-testid="sprint-overview-rollup">
            <div className={styles.countStrip}>
              <button
                type="button"
                className={styles.rollupCell}
                data-testid="sprint-rollup-needsreview"
                onClick={() => onOpenSprint(data.rollup.nextMilestone || '')}
                disabled={data.rollup.needsUserReview === 0}
                title="Review タブへ"
              >
                <div className={`${styles.countValue} ${data.rollup.needsUserReview === 0 ? styles.countZero : styles.countWarn}`}>
                  {data.rollup.needsUserReview}
                </div>
                <div className={styles.countLabel}>要対応 (needs_user_review)</div>
              </button>
              <div className={styles.rollupCell} data-testid="sprint-rollup-milestone">
                <div className={styles.countValue} style={{ color: 'var(--color-accent-light, var(--color-accent))' }}>
                  {data.rollup.nextMilestone || '—'}
                </div>
                <div className={styles.countLabel}>次マイルストーン</div>
              </div>
              <div className={styles.rollupCell} data-testid="sprint-rollup-backlog">
                <div className={styles.countValue} style={{ color: 'var(--color-accent-light, var(--color-accent))' }}>
                  {data.rollup.backlogTotal}
                </div>
                <div className={styles.countLabel}>
                  backlog（未昇格 {data.rollup.backlogUnpromoted}）
                </div>
              </div>
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
                <span className={statusClass(data.currentSprint.statusKind)} style={{ marginLeft: 8, fontSize: 12 }}>
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
                    <button type="button" className={styles.iconButton} onClick={() => onOpenSprint(a.sprintId)}>
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

          {/* Phase breakdown (new phase field) */}
          {(data.phases ?? []).length > 0 && (
            <section className={styles.section} data-testid="sprint-overview-phases">
              <h3 className={styles.sectionTitle}>Phase 別進捗</h3>
              <div className={styles.phaseList}>
                {data.phases.map((p) => (
                  <div key={p.phase} className={styles.phaseRow}>
                    <span>{p.phase}</span>
                    <span className={styles.mono}>
                      {p.done}/{p.total} done
                    </span>
                  </div>
                ))}
              </div>
            </section>
          )}

          {/* Timeline with filter */}
          <section className={styles.section}>
            <h3 className={styles.sectionTitle}>Sprint timeline</h3>
            <div className={styles.filterBar}>
              <button
                type="button"
                className={`${styles.filterChip} ${timelineFilter === 'incomplete' ? styles.filterChipActive : ''}`}
                data-testid="sprint-timeline-filter-incomplete"
                onClick={() => setTimelineFilter('incomplete')}
              >
                未完了のみ
              </button>
              <button
                type="button"
                className={`${styles.filterChip} ${timelineFilter === 'all' ? styles.filterChipActive : ''}`}
                data-testid="sprint-timeline-filter-all"
                onClick={() => setTimelineFilter('all')}
              >
                すべて ({doneCount} done 含む)
              </button>
            </div>
            <div className={styles.timelineTableWrap} data-testid="sprint-overview-timeline">
              <table className={styles.timelineTable} data-testid="sprint-timeline-table">
                <thead>
                  <tr>
                    <th style={{ width: 96 }}>Sprint</th>
                    <th>Title</th>
                    <th style={{ width: 150 }}>Status</th>
                    <th style={{ width: 170 }}>Depends on</th>
                    <th style={{ width: 90, textAlign: 'center' }}>Milestone</th>
                  </tr>
                </thead>
                <tbody>
                  {shownTimeline.map((t) => (
                    <TimelineRow key={t.id} t={t} onOpenSprint={onOpenSprint} />
                  ))}
                </tbody>
              </table>
            </div>
          </section>

          {/* Folded dependency mini-graph (Option A internalizes Dependencies) */}
          <section className={styles.section} data-testid="sprint-overview-depgraph">
            <h3 className={styles.sectionTitle}>依存ミニグラフ</h3>
            {depMermaid ? (
              <MermaidDiagram source={depMermaid} testId="sprint-overview-depgraph-svg" />
            ) : (
              <p className={styles.emptyNote}>依存関係はありません。</p>
            )}
          </section>

          {/* Folded Backlog (Option A internalizes Backlog) */}
          <section className={styles.section} data-testid="sprint-backlog-panel">
            <h3 className={styles.sectionTitle}>
              Backlog <span className={styles.tag2}>· {data.rollup.backlogTotal} 件</span>
            </h3>
            <div className={styles.filterBar}>
              <button
                type="button"
                className={`${styles.filterChip} ${backlogFilter === 'all' ? styles.filterChipActive : ''}`}
                data-testid="sprint-backlog-filter-all"
                onClick={() => setBacklogFilter('all')}
              >
                All ({backlog.length})
              </button>
              <button
                type="button"
                className={`${styles.filterChip} ${backlogFilter === 'unpromoted' ? styles.filterChipActive : ''}`}
                data-testid="sprint-backlog-filter-unpromoted"
                onClick={() => setBacklogFilter('unpromoted')}
              >
                未昇格 ({data.rollup.backlogUnpromoted})
              </button>
              <button
                type="button"
                className={`${styles.filterChip} ${backlogFilter === 'promoted' ? styles.filterChipActive : ''}`}
                data-testid="sprint-backlog-filter-promoted"
                onClick={() => setBacklogFilter('promoted')}
              >
                Sprint 化済み ({backlog.length - data.rollup.backlogUnpromoted})
              </button>
            </div>
            {shownBacklog.length === 0 ? (
              <p className={styles.emptyNote}>該当する backlog 項目はありません。</p>
            ) : (
              <div className={styles.backlogList}>
                {shownBacklog.map((b, i) => (
                  <BacklogItem key={i} b={b} onOpenSprint={onOpenSprint} />
                ))}
              </div>
            )}
          </section>
        </>
      )}
    </>
  )
}

function TimelineRow({ t, onOpenSprint }: { t: TimelineEntry; onOpenSprint: (id: string) => void }) {
  return (
    <tr
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
        <span className={statusPillClass(t.statusKind)} data-testid={`sprint-timeline-status-${t.id}`}>
          {t.statusKind}
        </span>
        {t.coarse && (
          <span
            className={`${styles.badge} ${styles.badgeCoarse}`}
            data-testid={`sprint-timeline-coarse-${t.id}`}
            title="未詳細化 (placeholder)"
          >
            coarse
          </span>
        )}
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
          <span className={styles.milestoneStar} title="milestone">
            ★
          </span>
        ) : (
          <span className={styles.milestoneNone}>—</span>
        )}
      </td>
    </tr>
  )
}

function BacklogItem({ b, onOpenSprint }: { b: BacklogEntry; onOpenSprint: (id: string) => void }) {
  const [open, setOpen] = useState(false)
  return (
    <div className={`${styles.backlogItem} ${open ? styles.backlogItemOpen : ''}`} data-testid="sprint-backlog-item">
      <button
        type="button"
        className={styles.backlogHead}
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
      >
        <span className={styles.backlogCaret}>{open ? '▾' : '▸'}</span>
        <span className={styles.backlogTitle}>{b.title || b.text}</span>
        <span className={styles.backlogMeta}>
          {b.priority && <span className={styles.depChipStatic}>priority: {b.priority}</span>}
          {b.addedIn && <span className={styles.depChipStatic}>added_in: {b.addedIn}</span>}
          {b.promoted && b.promotedTo && (
            <button
              type="button"
              className={styles.depChip}
              onClick={(e) => {
                e.stopPropagation()
                onOpenSprint(b.promotedTo!)
              }}
              data-testid={`sprint-backlog-promoted-${b.promotedTo}`}
            >
              → {b.promotedTo}
            </button>
          )}
        </span>
      </button>
      {open && (
        <div className={styles.backlogBody}>
          {b.description && <p className={styles.acEvi} style={{ lineHeight: 1.6 }}>{b.description}</p>}
          {b.reason && (
            <p className={styles.acEvi}>
              <b>reason:</b> {b.reason}
            </p>
          )}
          {b.status && <p className={styles.acEvi}>status: {b.status}</p>}
        </div>
      )}
    </div>
  )
}

// buildDepMermaid renders a `graph LR` from the overview timeline +
// dependencies (Refs = [from, prereq...]). Returns "" when there are no
// edges so the caller can show an empty note.
function buildDepMermaid(data: OverviewResponse | null): string {
  if (!data) return ''
  const deps = data.dependencies ?? []
  const edges: Array<[string, string]> = []
  for (const d of deps) {
    const refs = d.refs ?? []
    if (refs.length < 2) continue
    const from = refs[0]
    for (const to of refs.slice(1)) {
      if (from !== to) edges.push([to, from])
    }
  }
  if (edges.length === 0) return ''
  const lines = ['graph LR']
  const nodes = new Set<string>()
  for (const [a, b] of edges) {
    nodes.add(a)
    nodes.add(b)
  }
  for (const t of data.timeline ?? []) {
    if (nodes.has(t.id)) {
      const label = t.milestone ? `${t.id} ★` : t.id
      lines.push(`  ${t.id}["${label}"]`)
    }
  }
  const seen = new Set<string>()
  for (const [a, b] of edges) {
    const key = `${a}->${b}`
    if (seen.has(key)) continue
    seen.add(key)
    lines.push(`  ${a} --> ${b}`)
  }
  return lines.join('\n')
}
