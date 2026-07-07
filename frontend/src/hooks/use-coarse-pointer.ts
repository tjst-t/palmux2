import { useEffect, useState } from 'react'

// Media query that is true when the PRIMARY pointing device is coarse (touch),
// i.e. a phone or tablet. Desktop browsers driven by a mouse/trackpad report a
// fine pointer, so this is the right signal for "is this a touch device" —
// independent of window width (a narrow desktop window is still a desktop).
const COARSE_POINTER_QUERY = '(pointer: coarse)'

// useCoarsePointer reports whether the primary input is touch. Used to show the
// bottom key-assist Toolbar (a touch affordance) only on touch devices and hide
// it on desktop browsers. Reactive: updates if the primary pointer changes (e.g.
// a tablet docked to a mouse). SSR-safe: defaults to false (desktop) when
// matchMedia is unavailable.
export function useCoarsePointer(): boolean {
  const [coarse, setCoarse] = useState<boolean>(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return false
    return window.matchMedia(COARSE_POINTER_QUERY).matches
  })

  useEffect(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return
    const mql = window.matchMedia(COARSE_POINTER_QUERY)
    const onChange = () => setCoarse(mql.matches)
    onChange()
    mql.addEventListener('change', onChange)
    return () => mql.removeEventListener('change', onChange)
  }, [])

  return coarse
}
