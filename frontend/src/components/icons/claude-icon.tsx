// Anthropic-style "Claude burst" — eight rounded rays emanating from
// a centre point. Used in tab icons / pickers where we want the Claude
// tab to feel branded rather than rely on a 🧠 emoji.
//
// Inherits its colour from `currentColor`, so callers control the tint
// via CSS (`color: var(--color-accent)` or similar). The svg fills its
// parent box; size with width/height or font-size.
import type { CSSProperties } from 'react'

interface Props {
  size?: number | string
  color?: string
  title?: string
  style?: CSSProperties
}

export function ClaudeIcon({ size = '1em', color, title, style }: Props) {
  // 8 rays at 45° intervals, slightly tapered. Drawn on a 24×24 grid
  // centred on (12,12). Each ray is a thick rounded stroke from the
  // centre out to one edge.
  const rays = [
    { x: 12, y: 2 },   // ↑
    { x: 19.07, y: 4.93 },
    { x: 22, y: 12 },  // →
    { x: 19.07, y: 19.07 },
    { x: 12, y: 22 },  // ↓
    { x: 4.93, y: 19.07 },
    { x: 2, y: 12 },   // ←
    { x: 4.93, y: 4.93 },
  ]
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="none"
      stroke={color ?? 'currentColor'}
      strokeWidth={2.6}
      strokeLinecap="round"
      style={style}
      aria-hidden={title ? undefined : true}
      role={title ? 'img' : undefined}
    >
      {title && <title>{title}</title>}
      {rays.map((r, i) => (
        <line key={i} x1={12} y1={12} x2={r.x} y2={r.y} />
      ))}
    </svg>
  )
}
