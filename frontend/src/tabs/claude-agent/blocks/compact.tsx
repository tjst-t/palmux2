/** CompactBlock — summarises a /compact boundary (S018).
 *
 *  The CLI emits a system/compact_boundary envelope between the pre-
 *  and post-compaction histories; the BE mints a synthetic
 *  role:"system" turn carrying this block. Its content is purely
 *  informational — the actual summary text the CLI generates lands as
 *  a synthetic user-role turn immediately after, which the user can
 *  scroll to and read in full.
 */
import type { Block } from '../types'
import styles from '../blocks.module.css'

export function CompactBlock({ block }: { block: Block }) {
  const turns = block.compactTurns ?? 0
  const pre = block.compactPreTokens ?? 0
  const post = block.compactPostTokens ?? 0
  const dur = block.compactDurationMs ?? 0
  const trigger = block.compactTrigger || 'manual'
  // Concise "Compacted: N turns into 1 summary" line + a dim subline
  // with token reduction + duration. Ratio is rounded to one decimal.
  const ratio = pre > 0 ? Math.max(0, ((pre - post) / pre) * 100) : 0
  const seconds = dur > 0 ? (dur / 1000).toFixed(dur < 10000 ? 1 : 0) : ''
  const tokenLine =
    pre > 0 || post > 0
      ? `${pre.toLocaleString()} → ${post.toLocaleString()} tokens (${ratio.toFixed(0)}% smaller)`
      : ''
  return (
    <div className={styles.compactBoundary} data-testid="compact-boundary">
      <span className={styles.compactRule} aria-hidden />
      <div className={styles.compactBody} data-compact-trigger={trigger}>
        <div className={styles.compactHeadline}>
          Compacted: {turns} {turns === 1 ? 'turn' : 'turns'} into 1 summary
          {trigger && trigger !== 'manual' ? ` (${trigger})` : ''}
        </div>
        {(tokenLine || seconds) && (
          <div className={styles.compactDetail}>
            {tokenLine}
            {tokenLine && seconds ? ' · ' : ''}
            {seconds ? `${seconds}s` : ''}
          </div>
        )}
      </div>
      <span className={styles.compactRule} aria-hidden />
    </div>
  )
}
