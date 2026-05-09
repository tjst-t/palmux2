/** Tone classification + summary line builder for HookBlock (S005). */
import type { Block } from '../../types'
import { firstLine } from './format'

// hookTone classifies the hook's outcome into a tone for the header
// badge: success ⇒ no tone (silent ok), warning ⇒ orange (modified
// payload / non-zero exit but treated as success), error ⇒ red.
export function hookTone(block: Block): '' | 'warning' | 'error' {
  if (!block.done) return ''
  const outcome = (block.hookOutcome || '').toLowerCase()
  const exit = block.hookExitCode ?? 0
  if (outcome === 'blocked' || (exit !== 0 && outcome !== 'success')) {
    return 'error'
  }
  if (outcome && outcome !== 'success') {
    return 'warning'
  }
  return ''
}

// hookSummaryLine builds the inline preview shown next to the header
// when the hook is collapsed. Prefers stdout's first line, falls back
// to stderr, then to the outcome string.
export function hookSummaryLine(block: Block): string {
  const stdout = (block.hookStdout ?? '').trim()
  if (stdout) return firstLine(stdout)
  const stderr = (block.hookStderr ?? '').trim()
  if (stderr) return firstLine(stderr)
  return block.hookOutcome || ''
}
