// TooLargeView — placeholder shown for files larger than the
// `previewMaxBytes` setting (S010, default 10 MiB).
//
// Rendered before any /raw fetch is even issued — the dispatcher picks
// this kind purely from the file's size metadata, so the bandwidth
// round trip is skipped entirely. The user gets a clear "your file is
// too big to preview here, open it locally" hint with the actual byte
// counts so the threshold doesn't feel arbitrary.

import styles from './too-large-view.module.css'

interface Props {
  path: string
  size: number
  maxBytes: number
}

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`
  return `${(n / 1024 / 1024).toFixed(1)} MiB`
}

export function TooLargeView({ path, size, maxBytes }: Props) {
  return (
    <div className={styles.wrap} data-testid="too-large-view">
      <p className={styles.title}>File too large to preview</p>
      <p className={styles.detail}>
        {path} — {fmtBytes(size)} (limit: {fmtBytes(maxBytes)})
      </p>
      <p className={styles.hint}>
        Adjust <code>previewMaxBytes</code> in your global settings if you want
        bigger files rendered, or open the file in your local editor.
      </p>
    </div>
  )
}
