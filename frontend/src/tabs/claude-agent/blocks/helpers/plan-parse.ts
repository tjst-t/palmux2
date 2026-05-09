/** Parsers for ExitPlanMode plan content (S001).
 *
 *  The CLI's ExitPlanMode tool input has the shape `{"plan": "..."}`
 *  once finalised; while streaming, the partial JSON accumulates in
 *  `block.text`. We tolerate both shapes (and a couple of near-future
 *  schema variants) so a CLI version bump doesn't silently regress to
 *  an empty plan.
 */
import type { Block } from '../../types'
import { parseInputObject, shorten } from './format'

// extractPlanText pulls the human-readable plan markdown out of the
// block's payload.
export function extractPlanText(block: Block): string {
  const obj = parseInputObject(block)
  if (obj) {
    if (typeof obj.plan === 'string') return obj.plan
    if (typeof obj.markdown === 'string') return obj.markdown
    if (typeof obj.content === 'string') return obj.content
  }
  // Streaming partial. The CLI's ExitPlanMode input ships fields in
  // arbitrary order — `allowedPrompts` is sometimes serialised before
  // `plan`, so the in-flight `block.text` may not contain the plan key
  // yet. Try to extract just the plan field; if it's not there yet,
  // return empty rather than leaking the raw JSON to the user.
  const text = block.text ?? ''
  if (!text) return ''
  const trimmed = text.trim()
  if (trimmed.startsWith('{')) {
    try {
      const parsed = JSON.parse(trimmed) as Record<string, unknown>
      if (typeof parsed.plan === 'string') return parsed.plan
      if (typeof parsed.markdown === 'string') return parsed.markdown
      if (typeof parsed.content === 'string') return parsed.content
    } catch {
      // Streaming chunk: try to extract the literal plan string body
      // from the partial. Look for `"plan":"..."` and decode it.
      const m = trimmed.match(/"plan"\s*:\s*"((?:[^"\\]|\\.)*)/)
      if (m) {
        try {
          // Re-quote so JSON.parse handles escape sequences (\n, \t, …).
          return JSON.parse(`"${m[1]}"`) as string
        } catch {
          return m[1]
        }
      }
    }
    // We have a partial JSON object, but no plan field yet. Show
    // nothing — leaking allowedPrompts / planFilePath as raw JSON
    // confuses the user (see screenshots in S001-refine feedback).
    return ''
  }
  return text
}

export function firstNonBlankLine(s: string): string {
  if (!s) return ''
  for (const line of s.split('\n')) {
    const t = line.trim()
    if (t) return shorten(t.replace(/^#+\s*/, ''), 100)
  }
  return ''
}
