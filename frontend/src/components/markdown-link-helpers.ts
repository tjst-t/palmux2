// Shared link-classification helpers for MarkdownBlock and MarkdownView.
// Kept in a separate file so markdown-block.tsx (which exports a React
// component) satisfies the react-refresh/only-export-components ESLint rule.

export type LinkKind =
  | { kind: 'anchor'; id: string }
  | { kind: 'same-origin'; pathname: string; search: string; hash: string }
  | { kind: 'external'; href: string }
  | { kind: 'unknown' }

/** Classify an `<a href>` into one of the SPA-handling buckets.
 *  Does NOT handle relative paths (no `currentPath` context) — that is
 *  Files-tab specific and lives in markdown-view.tsx. */
export function classifyLink(href: string | undefined): LinkKind {
  if (!href) return { kind: 'unknown' }
  if (href.startsWith('#')) {
    return { kind: 'anchor', id: decodeURIComponent(href.slice(1)) }
  }
  if (/^[a-z][a-z0-9+.-]*:/i.test(href)) {
    if (typeof window !== 'undefined') {
      try {
        const u = new URL(href, window.location.origin)
        if (u.origin === window.location.origin) {
          return { kind: 'same-origin', pathname: u.pathname, search: u.search, hash: u.hash }
        }
      } catch {
        // fall through to external
      }
    }
    return { kind: 'external', href }
  }
  if (href.startsWith('/')) {
    if (typeof window !== 'undefined') {
      try {
        const u = new URL(href, window.location.origin)
        return { kind: 'same-origin', pathname: u.pathname, search: u.search, hash: u.hash }
      } catch {
        // fall through
      }
    }
    return { kind: 'same-origin', pathname: href, search: '', hash: '' }
  }
  // Relative path — no base path available in this context.
  return { kind: 'unknown' }
}
