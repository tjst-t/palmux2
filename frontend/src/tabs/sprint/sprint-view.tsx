// SprintView — the top-level Sprint Dashboard tab. Controls subtab
// selection (Overview / Sprint Detail / Dependencies / Decisions / Refine)
// via React Router search params so the URL stays shareable across
// devices, e.g.
//
//   /<repo>/<branch>/sprint?view=detail&sprintId=S016
//   /<repo>/<branch>/sprint?view=dependencies
//   /<repo>/<branch>/sprint?view=decisions&filter=needs_human
//
// The body itself is dispatched to one of the five screen components
// below. Each screen owns its own data hook (use-sprint-data.ts) and a
// Refresh / offline indicator in its header.
//
// We don't use nested <Routes> here because the surrounding TabContent
// already renders SprintView for the `/sprint` URL; the Tab system
// doesn't currently support nested route segments per tab.

import { useCallback, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'

import type { TabViewProps } from '../../lib/tab-registry'

import { DecisionTimelineView } from './screens/decision-timeline'
import { DependencyGraphView } from './screens/dependency-graph'
import { OverviewView } from './screens/overview'
import { RefineHistoryView } from './screens/refine-history'
import { SprintDetailView } from './screens/sprint-detail'
import styles from './sprint-view.module.css'

type View = 'overview' | 'detail' | 'dependencies' | 'decisions' | 'refine'

const VIEW_LABELS: Array<{ id: View; label: string }> = [
  { id: 'overview', label: 'Overview' },
  { id: 'detail', label: 'Sprint Detail' },
  { id: 'dependencies', label: 'Dependencies' },
  { id: 'decisions', label: 'Decisions' },
  { id: 'refine', label: 'Refine' },
]

function isView(v: string | null): v is View {
  return v === 'overview' || v === 'detail' || v === 'dependencies' || v === 'decisions' || v === 'refine'
}

export function SprintView({ repoId, branchId }: TabViewProps) {
  const [searchParams, setSearchParams] = useSearchParams()
  const initialView: View = isView(searchParams.get('view')) ? (searchParams.get('view') as View) : 'overview'
  const [view, setView] = useState<View>(initialView)
  const sprintId = searchParams.get('sprintId') ?? ''
  const filter = searchParams.get('filter') ?? ''

  const setViewAndUrl = useCallback(
    (next: View, extra: Record<string, string | null> = {}) => {
      setView(next)
      const sp = new URLSearchParams(searchParams)
      sp.set('view', next)
      for (const [k, v] of Object.entries(extra)) {
        if (v === null || v === undefined || v === '') sp.delete(k)
        else sp.set(k, v)
      }
      // When leaving the detail view, drop the sprintId from the URL so
      // navigating back to detail without an explicit sprint shows the
      // current one again.
      if (next !== 'detail') sp.delete('sprintId')
      if (next !== 'decisions') sp.delete('filter')
      setSearchParams(sp, { replace: true })
    },
    [searchParams, setSearchParams],
  )

  const navigateToSprintDetail = useCallback(
    (id: string) => {
      setViewAndUrl('detail', { sprintId: id })
    },
    [setViewAndUrl],
  )

  const setDecisionFilter = useCallback(
    (f: string) => {
      setViewAndUrl('decisions', { filter: f || null })
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
          onClick={() => setViewAndUrl(v.id)}
        >
          {v.label}
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
          <OverviewView
            repoId={repoId}
            branchId={branchId}
            onOpenSprint={navigateToSprintDetail}
          />
        )}
        {view === 'detail' && (
          <SprintDetailView
            repoId={repoId}
            branchId={branchId}
            sprintId={sprintId}
            onOpenSprint={navigateToSprintDetail}
          />
        )}
        {view === 'dependencies' && (
          <DependencyGraphView
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
        {view === 'refine' && <RefineHistoryView repoId={repoId} branchId={branchId} />}
      </div>
    </div>
  )
}
