// SprintView — the top-level Sprint Dashboard tab. Se173ef reworks the IA
// to the approved Option A (5 tabs): Overview / Sprint Detail / Review /
// Milestones / Decisions. Dependencies + Backlog are folded into Overview
// and Refine is removed from the tab bar. The old `?view=dependencies` and
// `?view=refine` URLs still render their legacy screens (backward compat)
// but are no longer surfaced as tabs.
//
// Subtab selection is driven by React Router search params so the URL stays
// shareable and browser back/forward navigate (priority_rule 8 — navigation
// is pushState, transient UI state like filters is component state):
//
//   /<repo>/<branch>/sprint?view=detail&sprintId=Sd44947
//   /<repo>/<branch>/sprint?view=review
//   /<repo>/<branch>/sprint?view=milestones

import { useCallback, useEffect, useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'

import type { TabViewProps } from '../../lib/tab-registry'

import { DecisionTimelineView } from './screens/decision-timeline'
import { DependencyGraphView } from './screens/dependency-graph'
import { MilestonesView } from './screens/milestones'
import { OverviewView } from './screens/overview'
import { RefineHistoryView } from './screens/refine-history'
import { ReviewView } from './screens/review'
import { SprintDetailView } from './screens/sprint-detail'
import styles from './sprint-view.module.css'

type View =
  | 'overview'
  | 'detail'
  | 'review'
  | 'milestones'
  | 'decisions'
  | 'dependencies' // legacy — direct-URL only
  | 'refine' // legacy — direct-URL only

// Tabs shown in the bar (Option A). `new` marks the Se173ef additions.
const VIEW_LABELS: Array<{ id: View; label: string; isNew?: boolean }> = [
  { id: 'overview', label: 'Overview' },
  { id: 'detail', label: 'Sprint Detail' },
  { id: 'review', label: 'Review', isNew: true },
  { id: 'milestones', label: 'Milestones', isNew: true },
  { id: 'decisions', label: 'Decisions' },
]

const ALL_VIEWS: View[] = [
  'overview',
  'detail',
  'review',
  'milestones',
  'decisions',
  'dependencies',
  'refine',
]

function isView(v: string | null): v is View {
  return v !== null && (ALL_VIEWS as string[]).includes(v)
}

export function SprintView({ repoId, branchId }: TabViewProps) {
  const [searchParams, setSearchParams] = useSearchParams()

  const rawView = searchParams.get('view')
  const view: View = isView(rawView) ? rawView : 'overview'

  // Normalize an invalid (present-but-unrecognised) ?view= to overview via
  // replace (no extra history entry). Never call setSearchParams in render.
  useEffect(() => {
    if (rawView !== null && !isView(rawView)) {
      const sp = new URLSearchParams(searchParams)
      sp.set('view', 'overview')
      setSearchParams(sp, { replace: true })
    }
  }, [rawView, searchParams, setSearchParams])

  const sprintId = searchParams.get('sprintId') ?? ''
  const filter = searchParams.get('filter') ?? ''

  const setViewAndUrl = useCallback(
    (next: View, extra: Record<string, string | null> = {}, replace = false) => {
      const sp = new URLSearchParams(searchParams)
      sp.set('view', next)
      for (const [k, v] of Object.entries(extra)) {
        if (v === null || v === undefined || v === '') sp.delete(k)
        else sp.set(k, v)
      }
      if (next !== 'detail') sp.delete('sprintId')
      if (next !== 'decisions') sp.delete('filter')
      setSearchParams(sp, { replace })
    },
    [searchParams, setSearchParams],
  )

  const navigateToSprintDetail = useCallback(
    (id: string) => {
      setViewAndUrl('detail', { sprintId: id }, false)
    },
    [setViewAndUrl],
  )

  const resolveDefaultSprint = useCallback(
    (id: string) => {
      setViewAndUrl('detail', { sprintId: id }, true)
    },
    [setViewAndUrl],
  )

  const setDecisionFilter = useCallback(
    (f: string) => {
      setViewAndUrl('decisions', { filter: f || null }, false)
    },
    [setViewAndUrl],
  )

  const subtabs = useMemo(
    () =>
      VIEW_LABELS.map((v) => (
        <button
          key={v.id}
          type="button"
          className={`${styles.subtab} ${view === v.id ? styles.subtabActive : ''}`}
          data-testid={`sprint-subtab-${v.id}`}
          onClick={() => setViewAndUrl(v.id, {}, false)}
        >
          {v.label}
          {v.isNew && <span className={styles.subtabNew}>NEW</span>}
        </button>
      )),
    [view, setViewAndUrl],
  )

  return (
    <div className={styles.root} data-testid="sprint-view">
      <nav className={styles.subtabs} aria-label="Sprint dashboard sections">
        {subtabs}
      </nav>
      <div className={styles.body}>
        {view === 'overview' && (
          <OverviewView repoId={repoId} branchId={branchId} onOpenSprint={navigateToSprintDetail} />
        )}
        {view === 'detail' && (
          <SprintDetailView
            repoId={repoId}
            branchId={branchId}
            sprintId={sprintId}
            onOpenSprint={navigateToSprintDetail}
            onResolveDefaultSprint={resolveDefaultSprint}
          />
        )}
        {view === 'review' && (
          <ReviewView repoId={repoId} branchId={branchId} onOpenSprint={navigateToSprintDetail} />
        )}
        {view === 'milestones' && (
          <MilestonesView
            repoId={repoId}
            branchId={branchId}
            onOpenSprint={navigateToSprintDetail}
          />
        )}
        {view === 'decisions' && (
          <DecisionTimelineView
            repoId={repoId}
            branchId={branchId}
            filter={filter}
            onFilterChange={setDecisionFilter}
            onOpenSprint={navigateToSprintDetail}
          />
        )}
        {/* Legacy direct-URL screens (no longer in the tab bar). */}
        {view === 'dependencies' && (
          <DependencyGraphView
            repoId={repoId}
            branchId={branchId}
            onOpenSprint={navigateToSprintDetail}
          />
        )}
        {view === 'refine' && <RefineHistoryView repoId={repoId} branchId={branchId} />}
      </div>
    </div>
  )
}
