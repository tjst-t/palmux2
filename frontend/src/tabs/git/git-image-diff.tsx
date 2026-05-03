// Image diff helper — Monaco's DiffEditor is text-only and corrupts
// binary blobs. When the user clicks an image file we swap in a
// side-by-side `<img>` pair instead. Reused for both the working-tree
// view (HEAD vs working) and the commit-history view (sha^ vs sha).

import { useState } from 'react'

import styles from './git-image-diff.module.css'

const IMAGE_EXTS = new Set([
  'png',
  'jpg',
  'jpeg',
  'gif',
  'webp',
  'avif',
  'bmp',
  'ico',
  'svg',
])

export function isImageFile(path: string): boolean {
  const dot = path.lastIndexOf('.')
  if (dot < 0) return false
  return IMAGE_EXTS.has(path.slice(dot + 1).toLowerCase())
}

export interface ImagePairProps {
  leftSrc: string
  rightSrc: string
  leftLabel: string
  rightLabel: string
}

/**
 * Side-by-side image preview for git diffs. Each side renders as much
 * as it can — if a side 404s (e.g. the file didn't exist at HEAD), we
 * show a placeholder instead of a broken-image icon. Supports SVG via
 * the same <img> tag (browsers render SVG inline).
 */
export function ImagePair({ leftSrc, rightSrc, leftLabel, rightLabel }: ImagePairProps) {
  return (
    <div className={styles.wrap} data-testid="git-image-diff">
      <ImagePane src={leftSrc} label={leftLabel} side="left" />
      <ImagePane src={rightSrc} label={rightLabel} side="right" />
    </div>
  )
}

function ImagePane({
  src,
  label,
  side,
}: {
  src: string
  label: string
  side: 'left' | 'right'
}) {
  const [missing, setMissing] = useState(false)
  return (
    <figure className={styles.pane} data-side={side}>
      <figcaption className={styles.caption}>{label}</figcaption>
      <div className={styles.imageWrap}>
        {missing ? (
          <p className={styles.missing}>(not present at this revision)</p>
        ) : (
          // `crossOrigin` is omitted on purpose — same-origin requests
          // always carry the session cookie, no CORS preflight needed.
          // eslint-disable-next-line @next/next/no-img-element
          <img
            src={src}
            alt={label}
            className={styles.image}
            onError={() => setMissing(true)}
          />
        )}
      </div>
    </figure>
  )
}
