import { useEffect, useState } from 'react'

// Breakpoints — keep in sync with CLAUDE.md.
export const MOBILE_MAX = 599 // < 600px
export const PC_COMPACT_MAX = 899 // < 900px

export type Viewport = 'mobile' | 'compact' | 'wide'

function classifyWidth(width: number): Viewport {
  if (width <= MOBILE_MAX) return 'mobile'
  if (width <= PC_COMPACT_MAX) return 'compact'
  return 'wide'
}

// useViewport returns the current viewport class. SSR-safe: falls back to
// 'wide' until hydration.
export function useViewport(): Viewport {
  const [v, setV] = useState<Viewport>(() =>
    typeof window === 'undefined' ? 'wide' : classifyWidth(window.innerWidth),
  )
  useEffect(() => {
    if (typeof window === 'undefined') return
    const onResize = () => setV(classifyWidth(window.innerWidth))
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])
  return v
}

export const isMobile = (v: Viewport) => v === 'mobile'
