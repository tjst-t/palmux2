// S017: shared default for readPreviewLineCount.
//
// The Claude tab clamps long tool_result outputs to a leading-N-lines
// preview so a single 5000-line `Read` or `grep` doesn't paint a wall
// of DOM. The cap is configurable via the global setting
// `readPreviewLineCount` (default 50). This module just centralises
// the default constant so both blocks.tsx (which renders the toggle)
// and any future server-driven defaults agree on the number.
//
// Mirrors `internal/config.DefaultReadPreviewLineCount` on the Go
// side. If you change one, change the other.
export const DEFAULT_READ_PREVIEW_LINE_COUNT = 50

/** sliceForPreview returns the first `n` newline-separated chunks of
 *  `s`, joined with `\n` (no trailing newline). Pure function so
 *  unit tests can pin the slicing rule. Empty input → empty string.
 *  When `n >= chunkCount`, returns the original string verbatim. */
export function sliceForPreview(s: string, n: number): string {
  if (!s || n <= 0) return ''
  const parts = s.split('\n')
  if (parts.length <= n) return s
  return parts.slice(0, n).join('\n')
}

/** countLines returns the number of newline-separated rows in `s`,
 *  ignoring a single trailing newline so "1\n2\n" counts as 2 rows
 *  rather than 3. Used by the "Show all (X lines)" label. */
export function countLines(s: string): number {
  if (!s) return 0
  const n = s.split('\n').length
  return s.endsWith('\n') ? n - 1 : n
}
