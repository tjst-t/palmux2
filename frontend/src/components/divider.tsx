import { useEffect, useRef, useState } from 'react'

import styles from './main-area.module.css'

interface Props {
  ratio: number
  onChange: (ratio: number) => void
  containerRef: React.RefObject<HTMLDivElement | null>
  min?: number
  max?: number
}

export function Divider({ ratio, onChange, containerRef, min = 20, max = 80 }: Props) {
  const [active, setActive] = useState(false)
  const draftRatio = useRef(ratio)
  draftRatio.current = ratio

  useEffect(() => {
    if (!active) return
    const onMove = (e: PointerEvent) => {
      const el = containerRef.current
      if (!el) return
      const rect = el.getBoundingClientRect()
      const pct = ((e.clientX - rect.left) / rect.width) * 100
      const clamped = Math.min(max, Math.max(min, pct))
      if (Math.abs(clamped - draftRatio.current) >= 0.5) {
        draftRatio.current = clamped
        onChange(Math.round(clamped))
      }
    }
    const onUp = () => setActive(false)
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
  }, [active, containerRef, min, max, onChange])

  return (
    <div
      className={active ? `${styles.divider} ${styles.dividerActive}` : styles.divider}
      onPointerDown={(e) => {
        e.preventDefault()
        setActive(true)
      }}
      role="separator"
      aria-orientation="vertical"
      aria-valuenow={ratio}
      aria-valuemin={min}
      aria-valuemax={max}
    >
      <div className={styles.dividerHandle} />
    </div>
  )
}
