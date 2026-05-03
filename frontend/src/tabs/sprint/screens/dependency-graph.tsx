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
  // Pan/zoom transform — kept in a ref to avoid re-rendering on every
  // mousemove. The Mermaid <svg> inside the container is transformed
  // directly via inline style.
  const transformRef = useRef({ scale: 1, tx: 0, ty: 0 })
  // Timestamp until which click events on sprint nodes should be
  // ignored. Set briefly after a drag-pan so the synthesized click on
  // mouseup doesn't navigate to a sprint the user happens to release
  // over. Auto-expires (timestamp comparison) so a non-pan click that
  // arrives later still works.
  const suppressClickUntilRef = useRef(0)

  const applyTransform = useCallback(() => {
    const svg = containerRef.current?.querySelector<SVGSVGElement>('svg')
    if (!svg) return
    const { scale, tx, ty } = transformRef.current
    svg.style.transformOrigin = '0 0'
    svg.style.transform = `translate(${tx}px, ${ty}px) scale(${scale})`
  }, [])

  const resetTransform = useCallback(() => {
    transformRef.current = { scale: 1, tx: 0, ty: 0 }
    applyTransform()
  }, [applyTransform])

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
        // Reset transform every time we re-render the SVG.
        transformRef.current = { scale: 1, tx: 0, ty: 0 }
        applyTransform()

        // Wire click → onOpenSprint. Mermaid emits each node with id
        // `<containerId>-flowchart-<sprintId>-<n>`; pick the segment
        // between `-flowchart-` and the trailing `-N`.
        const nodes = containerRef.current.querySelectorAll<SVGGElement>('g.node')
        nodes.forEach((g) => {
          const raw = g.id || ''
          const m = raw.match(/-flowchart-([A-Za-z0-9_]+)-\d+$/)
          const sprintId = m ? m[1] : raw
          g.style.cursor = 'pointer'
          g.setAttribute('data-testid', `sprint-dep-node-${sprintId}`)
          g.addEventListener('click', () => {
            // If the click landed within the suppression window
            // following a drag, treat it as part of the pan and skip
            // navigation. A direct click without a recent drag
            // navigates as usual.
            if (performance.now() < suppressClickUntilRef.current) return
            onOpenSprint(sprintId)
          })
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
  }, [data?.mermaid, onOpenSprint, applyTransform])

  // Wheel zoom + drag pan. Pointer events are wired to `containerRef`;
  // we use a movement threshold to distinguish a drag from a click on
  // a node, and suppress the synthetic `click` that follows a drag so
  // node-clicks don't navigate after a pan.
  //
  // Re-runs when `data` arrives because the container <div> is gated
  // on `data && (...)` — until then `containerRef.current` is null.
  useEffect(() => {
    const wrap = containerRef.current
    if (!wrap) return

    let pointerStart: { x: number; y: number } | null = null
    let lastPos = { x: 0, y: 0 }
    let didDrag = false

    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const t = transformRef.current
      const factor = Math.exp(-e.deltaY * 0.0015)
      const newScale = Math.max(0.2, Math.min(8, t.scale * factor))
      const rect = wrap.getBoundingClientRect()
      const mx = e.clientX - rect.left
      const my = e.clientY - rect.top
      // Cursor-centered zoom: keep the point under the cursor fixed.
      t.tx = mx - (newScale / t.scale) * (mx - t.tx)
      t.ty = my - (newScale / t.scale) * (my - t.ty)
      t.scale = newScale
      applyTransform()
    }

    const onPointerDown = (e: PointerEvent) => {
      if (e.button !== 0) return
      pointerStart = { x: e.clientX, y: e.clientY }
      lastPos = { x: e.clientX, y: e.clientY }
      didDrag = false
      wrap.setPointerCapture(e.pointerId)
    }

    const onPointerMove = (e: PointerEvent) => {
      if (!pointerStart) return
      const dx = e.clientX - pointerStart.x
      const dy = e.clientY - pointerStart.y
      if (!didDrag && Math.hypot(dx, dy) > 4) {
        didDrag = true
        wrap.dataset.dragging = 'true'
      }
      if (didDrag) {
        const t = transformRef.current
        t.tx += e.clientX - lastPos.x
        t.ty += e.clientY - lastPos.y
        lastPos = { x: e.clientX, y: e.clientY }
        applyTransform()
      }
    }

    const onPointerUp = (e: PointerEvent) => {
      if (wrap.hasPointerCapture(e.pointerId)) {
        wrap.releasePointerCapture(e.pointerId)
      }
      if (didDrag) {
        // Mark a short window during which node clicks are ignored,
        // so the synthesized click that follows a drag doesn't
        // unintentionally navigate to whatever node lies under the
        // cursor at mouseup.
        suppressClickUntilRef.current = performance.now() + 250
      }
      pointerStart = null
      delete wrap.dataset.dragging
    }

    wrap.addEventListener('wheel', onWheel, { passive: false })
    wrap.addEventListener('pointerdown', onPointerDown)
    wrap.addEventListener('pointermove', onPointerMove)
    wrap.addEventListener('pointerup', onPointerUp)
    wrap.addEventListener('pointercancel', onPointerUp)
    return () => {
      wrap.removeEventListener('wheel', onWheel)
      wrap.removeEventListener('pointerdown', onPointerDown)
      wrap.removeEventListener('pointermove', onPointerMove)
      wrap.removeEventListener('pointerup', onPointerUp)
      wrap.removeEventListener('pointercancel', onPointerUp)
    }
    // `data` is a dep so the listeners re-attach once the container
    // <div> is in the DOM (it's gated on `data && (...)` in JSX).
  }, [applyTransform, data])

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
          <div
            className={styles.depGraphContainer}
            ref={containerRef}
            data-testid="sprint-dep-graph"
          />
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
            <span style={{ marginLeft: 'auto', color: 'var(--color-fg-muted)', fontSize: 11 }}>
              ホイールで拡大/縮小、 ドラッグで移動
            </span>
            <button
              type="button"
              className={styles.depGraphResetBtn}
              onClick={resetTransform}
              data-testid="sprint-dep-reset"
            >
              Reset view
            </button>
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
