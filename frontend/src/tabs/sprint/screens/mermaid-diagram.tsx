// MermaidDiagram — renders an arbitrary Mermaid source string into an SVG.
// Reused by the Overview folded dependency mini-graph and the Sprint Detail
// gui-spec state_diagram (AC-Se173ef-3-4), sharing the same lazy-loaded
// Mermaid module the Dependency graph screen already pulls in so we don't
// grow the initial bundle.

import { useEffect, useRef, useState } from 'react'

import styles from '../sprint-view.module.css'

// Module-scoped so repeated mounts reuse the initialized Mermaid instance.
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

interface MermaidDiagramProps {
  source: string
  testId?: string
  /** When the source is not a valid Mermaid diagram, render it as a
   *  monospace <pre> block instead of erroring (gui-spec state diagrams are
   *  sometimes free-form ASCII). */
  fallbackAsText?: boolean
}

let idCounter = 0

export function MermaidDiagram({ source, testId, fallbackAsText = true }: MermaidDiagramProps) {
  const ref = useRef<HTMLDivElement | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    let cancelled = false
    if (!source || !ref.current) return
    void (async () => {
      try {
        const m = await getMermaid()
        if (cancelled || !ref.current) return
        setFailed(false)
        idCounter += 1
        const { svg } = await m.render(`sprint-mmd-${idCounter}-${Date.now()}`, source)
        if (cancelled || !ref.current) return
        ref.current.innerHTML = svg
      } catch {
        if (!cancelled) setFailed(true)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [source])

  if (failed && fallbackAsText) {
    return (
      <pre className={styles.mermaidText} data-testid={testId}>
        {source}
      </pre>
    )
  }
  return <div className={styles.mermaidBox} ref={ref} data-testid={testId} />
}
