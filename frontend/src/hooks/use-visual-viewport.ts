// Track window.visualViewport so the page can shrink to the visible area
// when a mobile soft keyboard slides up. We expose the current height as a
// CSS variable on <html> (--app-vv-height) so any layout root can use it
// instead of 100vh / 100% — those don't react to virtual keyboards.

import { useEffect } from 'react'

export function useVisualViewport() {
  useEffect(() => {
    if (typeof window === 'undefined') return
    const vv = window.visualViewport
    const apply = () => {
      const h = vv ? Math.round(vv.height) : window.innerHeight
      document.documentElement.style.setProperty('--app-vv-height', `${h}px`)
    }
    apply()
    if (vv) {
      vv.addEventListener('resize', apply)
      vv.addEventListener('scroll', apply)
    } else {
      window.addEventListener('resize', apply)
    }
    return () => {
      if (vv) {
        vv.removeEventListener('resize', apply)
        vv.removeEventListener('scroll', apply)
      } else {
        window.removeEventListener('resize', apply)
      }
    }
  }, [])
}
