// Dependency Graph screen — renders the Mermaid graph delivered by the
// backend `/dependencies` endpoint and binds clicks on each sprint node
// back into Sprint Detail navigation.
//
// Mermaid is loaded *lazily* on first mount via dynamic import so the
// initial JS payload of every other tab stays small (S016 task -1-13).

import { useCallback, useEffect, useRef, useState } from 'react'

import { sprintApi } from '../api'
import styles from '../sprint-view.module.css'
import type { DependencyGraphResponse } from '../types'
import { useSprintData } from '../use-sprint-data'

import { ErrorBanner, ParseErrorsBanner, ViewHeader } from './view-header'

interface DependencyGraphViewProps {
  repoId: string
  branchId: string
  onOpenSprint: (id: string) => void
}

// We keep the Mermaid module reference at module scope so subsequent
// mounts of the Dependency Graph reuse it without re-importing.
let mermaidModulePromise: Promise<typeof import('mermaid')['default']> | null = null

async function getMermaid() {
  if (!mermaidModulePromise) {
    mermaidModulePromise = import('mermaid').then((mod) => {
      const m = mod.default
      m.initialize({
        startOnLoad: false,
        theme: 'dark',
        securityLevel: 'strict',
        themeVariables: {
          primaryColor: '#13151c',
          primaryTextColor: '#d4d4d8',
          primaryBorderColor: '#7c8aff',
          lineColor: '#6b6f7b',
          fontFamily: '"Geist", system-ui, sans-serif',
        },
      })
      return m
    })
  }
  return mermaidModulePromise
}

export function DependencyGraphView({ repoId, branchId, onOpenSprint }: DependencyGraphViewProps) {
  const fetcher = useCallback(
    (prev: string | null) => sprintApi.dependencies(repoId, branchId, prev),
    [repoId, branchId],
  )
  const { data, loading, error, offline, refresh } = useSprintData<DependencyGraphResponse>({
    repoId,
    branchId,
    scope: 'dependencies',
    fetcher,
  })

  const containerRef = useRef<HTMLDivElement | null>(null)
  const [renderError, setRenderError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    if (!data?.mermaid || !containerRef.current) return
    setRenderError(null)
    void (async () => {
      try {
        const m = await getMermaid()
        if (cancelled || !containerRef.current) return
        const { svg, bindFunctions } = await m.render(`sprint-dep-${Date.now()}`, data.mermaid)
        if (cancelled || !containerRef.current) return
        containerRef.current.innerHTML = svg
        if (bindFunctions) bindFunctions(containerRef.current)

        // Wire click → onOpenSprint. Mermaid emits each node with id
        // matching the sprint ID; we hook every <g class="node">.
        const nodes = containerRef.current.querySelectorAll<SVGGElement>('g.node')
        nodes.forEach((g) => {
          // Mermaid prefixes ids with `flowchart-` — strip it.
          const raw = g.id || ''
          const m = raw.match(/^flowchart-([A-Za-z0-9-]+)-\d+$/)
          const sprintId = m ? m[1] : raw
          g.style.cursor = 'pointer'
          g.setAttribute('data-testid', `sprint-dep-node-${sprintId}`)
          g.addEventListener('click', () => onOpenSprint(sprintId))
        })
      } catch (e) {
        if (!cancelled) {
          setRenderError(e instanceof Error ? e.message : String(e))
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [data?.mermaid, onOpenSprint])

  return (
    <>
      <ViewHeader
        title="Dependency Graph"
        offline={offline}
        loading={loading}
        onRefresh={refresh}
        testIdPrefix="sprint-dependencies"
      />
      <ErrorBanner message={error} />
      <ParseErrorsBanner errors={data?.parseErrors} />
      {renderError && (
        <div className={styles.errorBanner}>Failed to render Mermaid graph: {renderError}</div>
      )}

      {!data && !error && <div className={styles.empty}>Loading…</div>}

      {data && (
        <>
          <div className={styles.depGraphContainer} ref={containerRef} data-testid="sprint-dep-graph" />
          <div className={styles.depGraphLegend}>
            <span>
              <span className={styles.depLegendDot} style={{ background: '#64d2a0' }} />
              Done
            </span>
            <span>
              <span className={styles.depLegendDot} style={{ background: '#e8b45a' }} />
              In progress
            </span>
            <span>
              <span className={styles.depLegendDot} style={{ background: 'var(--color-border)' }} />
              Pending
            </span>
            <span>
              <span className={styles.depLegendDot} style={{ background: '#ef4444' }} />
              Blocked
            </span>
          </div>
          {(data.dependencies ?? []).length > 0 && (
            <details style={{ marginTop: 16 }}>
              <summary style={{ cursor: 'pointer', fontSize: 13, color: 'var(--color-fg-muted)' }}>
                Dependency notes ({(data.dependencies ?? []).length})
              </summary>
              <ul style={{ marginTop: 8, paddingLeft: 20, fontSize: 12, color: 'var(--color-fg)' }}>
                {(data.dependencies ?? []).map((d, i) => (
                  <li key={i}>{d.text}</li>
                ))}
              </ul>
            </details>
          )}
        </>
      )}
    </>
  )
}
