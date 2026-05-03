// MarkdownView — preserves the pre-S010 Markdown rendering path and
// adds SPA-friendly link / image / anchor handling (S027).
//
// We deliberately keep ReactMarkdown + remark-gfm here (not Monaco)
// because users were already relying on the rendered look, and S010's
// charter says "preserve existing behaviour" for `.md`. The look-and-
// feel CSS is copied verbatim from the previous file-preview.module.css.
//
// S027: rehype-slug auto-assigns GitHub-compatible `id`s to headings,
// and a custom `components.a` / `components.img` override classifies
// links into 4 buckets so we never trigger a Palmux2-wide reload while
// the user is reading docs:
//
//   1. anchor (`#foo`)        → smooth scroll + history.replaceState
//   2. relative path          → React Router navigate to the same Files tab
//   3. same-origin absolute   → React Router navigate (Palmux2 route)
//   4. external (`http(s):`)  → open in new tab (target=_blank, noopener)
//
// `<img src>` is resolved similarly: relative paths are rewritten to
// `/api/repos/.../files/raw?path=...` so the existing Files-API + S010
// MIME map serves the bytes (no extra endpoint needed).

import { useCallback, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import ReactMarkdown from 'react-markdown'
import rehypeSlug from 'rehype-slug'
import remarkGfm from 'remark-gfm'

import styles from './markdown-view.module.css'
import type { ViewerProps } from './types'

/** Resolve `href` against the directory of the currently-open markdown
 *  file. Pure helper, exported for unit-style use if needed in the
 *  future. Returns `null` for empty hrefs. */
function dirnameOf(p: string): string {
  const i = p.lastIndexOf('/')
  return i < 0 ? '' : p.slice(0, i)
}

/** Posix-style `path.normalize` — collapses `./` and `../` segments
 *  without leaving the leading-up-dirs case in (which `URL`-based
 *  resolution would silently swallow into the origin). */
function normalizePath(p: string): string {
  const parts = p.split('/')
  const out: string[] = []
  for (const seg of parts) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      if (out.length === 0) {
        // Refuse to escape the worktree root — keep the segment so the
        // resulting URL still makes sense and the API can 404 cleanly.
        out.push('..')
        continue
      }
      // Don't pop a previous '..' (we already chose to keep them).
      if (out[out.length - 1] === '..') {
        out.push('..')
      } else {
        out.pop()
      }
      continue
    }
    out.push(seg)
  }
  return out.join('/')
}

type LinkKind =
  | { kind: 'anchor'; id: string }
  | { kind: 'relative'; resolved: string }
  | { kind: 'absolute-same-origin'; pathname: string; search: string; hash: string }
  | { kind: 'external'; href: string }
  | { kind: 'unknown' }

/** Classify an `<a href>` into one of the 4 SPA-handling buckets.
 *  `currentPath` is the worktree-relative path of the markdown file
 *  currently being rendered (used as the base for relative links).
 *  Module-private — kept here (not in a sibling `links.ts`) because it
 *  has no consumer outside this file and the logic is short enough that
 *  splitting it would add files-to-track without buying anything. */
function classifyLink(href: string | undefined, currentPath: string): LinkKind {
  if (!href) return { kind: 'unknown' }
  // 1. pure anchor
  if (href.startsWith('#')) {
    return { kind: 'anchor', id: decodeURIComponent(href.slice(1)) }
  }
  // 4. external schemes — http/https, plus mailto/tel which we also want
  //    to leave to the browser. We *don't* try to enumerate every scheme;
  //    anything matching `<scheme>:` (excluding our own relative paths)
  //    is treated as external.
  if (/^[a-z][a-z0-9+.-]*:/i.test(href)) {
    if (typeof window !== 'undefined') {
      try {
        const u = new URL(href, window.location.origin)
        if (u.origin === window.location.origin) {
          return {
            kind: 'absolute-same-origin',
            pathname: u.pathname,
            search: u.search,
            hash: u.hash,
          }
        }
      } catch {
        // fall through to external
      }
    }
    return { kind: 'external', href }
  }
  // 3. site-absolute (`/foo`) — same-origin SPA route
  if (href.startsWith('/')) {
    if (typeof window !== 'undefined') {
      try {
        const u = new URL(href, window.location.origin)
        return {
          kind: 'absolute-same-origin',
          pathname: u.pathname,
          search: u.search,
          hash: u.hash,
        }
      } catch {
        // fall through
      }
    }
    return { kind: 'absolute-same-origin', pathname: href, search: '', hash: '' }
  }
  // 2. relative path — resolve against the markdown file's directory.
  //    Split off any `?query#hash` so the path joins cleanly.
  let pathPart = href
  let suffix = ''
  const hashIdx = pathPart.indexOf('#')
  const queryIdx = pathPart.indexOf('?')
  // Whichever delimiter comes first wins — both are part of `suffix`.
  const cut =
    queryIdx === -1 ? hashIdx : hashIdx === -1 ? queryIdx : Math.min(queryIdx, hashIdx)
  if (cut !== -1) {
    suffix = pathPart.slice(cut)
    pathPart = pathPart.slice(0, cut)
  }
  const base = dirnameOf(currentPath)
  const joined = base ? `${base}/${pathPart}` : pathPart
  const resolved = normalizePath(joined) + suffix
  return { kind: 'relative', resolved }
}

/** Build the SPA URL for a worktree-relative file under the current
 *  Files tab. `pathPart` may include `?query#hash` — those pass through
 *  unchanged. */
function buildFilesUrl(
  repoId: string,
  branchId: string,
  tabId: string,
  pathPart: string,
): string {
  // Split path / query / hash so we encode each piece correctly.
  let p = pathPart
  let suffix = ''
  const hashIdx = p.indexOf('#')
  const queryIdx = p.indexOf('?')
  const cut = queryIdx === -1 ? hashIdx : hashIdx === -1 ? queryIdx : Math.min(queryIdx, hashIdx)
  if (cut !== -1) {
    suffix = p.slice(cut)
    p = p.slice(0, cut)
  }
  const base = `/${encodeURIComponent(repoId)}/${encodeURIComponent(branchId)}/${encodeURIComponent(tabId)}`
  const tail = p ? `/${p.split('/').map(encodeURIComponent).join('/')}` : ''
  return `${base}${tail}${suffix}`
}

export function MarkdownView({ body, path, apiBase, repoId, branchId, tabId }: ViewerProps) {
  const navigate = useNavigate()
  const containerRef = useRef<HTMLDivElement | null>(null)

  // S027 AC-5: when the page loads with a URL fragment (`#section`),
  // scroll to that heading after the markdown DOM has been rendered.
  // ReactMarkdown renders synchronously inside this component, but the
  // ids only become available after commit; one rAF is sufficient.
  // We re-run when `body.content` changes so a tab-swap that lands on a
  // freshly-loaded file with a hash still scrolls.
  useEffect(() => {
    if (!body) return
    if (typeof window === 'undefined') return
    const hash = window.location.hash
    if (!hash || hash.length < 2) return
    const id = decodeURIComponent(hash.slice(1))
    let raf1 = 0
    let raf2 = 0
    raf1 = requestAnimationFrame(() => {
      // double-rAF: the first frame schedules layout, the second
      // guarantees the headings are mounted with their slug ids.
      raf2 = requestAnimationFrame(() => {
        const root = containerRef.current
        if (!root) return
        const el = root.querySelector<HTMLElement>(`#${CSS.escape(id)}`)
        if (el) el.scrollIntoView({ behavior: 'auto', block: 'start' })
      })
    })
    return () => {
      if (raf1) cancelAnimationFrame(raf1)
      if (raf2) cancelAnimationFrame(raf2)
    }
  }, [body])

  // S027 AC-4: browser back/forward through anchor history. We listen
  // to popstate (fires for pushState entries) and hashchange (fires
  // when only the hash differs); both scroll the new hash into view.
  useEffect(() => {
    if (typeof window === 'undefined') return
    const onHashRestore = () => {
      const hash = window.location.hash
      if (!hash || hash.length < 2) return
      const id = decodeURIComponent(hash.slice(1))
      const root = containerRef.current
      if (!root) return
      const el = root.querySelector<HTMLElement>(`#${CSS.escape(id)}`)
      if (el) el.scrollIntoView({ behavior: 'auto', block: 'start' })
    }
    window.addEventListener('popstate', onHashRestore)
    window.addEventListener('hashchange', onHashRestore)
    return () => {
      window.removeEventListener('popstate', onHashRestore)
      window.removeEventListener('hashchange', onHashRestore)
    }
  }, [])

  const handleAnchorClick = useCallback(
    (e: React.MouseEvent<HTMLAnchorElement>, href: string) => {
      // Modifier keys / non-primary buttons → let the browser handle
      // (e.g. Cmd-click to open in new tab).
      if (e.defaultPrevented) return
      if (e.button !== 0) return
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
      const c = classifyLink(href, path)
      if (c.kind === 'anchor') {
        e.preventDefault()
        const root = containerRef.current
        const el = root?.querySelector<HTMLElement>(`#${CSS.escape(c.id)}`) ?? null
        if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
        if (typeof window !== 'undefined') {
          // pushState so the browser back button restores the previous
          // hash (AC-S027-1-4). We only push when the hash actually
          // changes — repeated clicks on the same heading don't deepen
          // the back-stack.
          const next = `#${c.id}`
          if (window.location.hash !== next) {
            window.history.pushState(window.history.state, '', next)
          }
        }
        return
      }
      if (c.kind === 'relative' && repoId && branchId && tabId) {
        e.preventDefault()
        navigate(buildFilesUrl(repoId, branchId, tabId, c.resolved))
        return
      }
      if (c.kind === 'absolute-same-origin') {
        // If it's a Palmux2 SPA route (anything not `/api/`, `/auth`,
        // or static asset paths) we route through React Router. Cross-
        // app jumps (e.g. /api/...) fall back to the browser default.
        if (
          c.pathname.startsWith('/api/') ||
          c.pathname.startsWith('/auth') ||
          c.pathname === '/favicon.ico'
        ) {
          return
        }
        e.preventDefault()
        navigate(`${c.pathname}${c.search}${c.hash}`)
        return
      }
      // external / unknown: let the renderer's `target="_blank"` /
      // browser default win.
    },
    [navigate, path, repoId, branchId, tabId],
  )

  if (!body) return <p className={styles.placeholder}>Loading…</p>
  return (
    <div className={styles.wrap} data-testid="markdown-view">
      <div className={styles.markdown} ref={containerRef}>
        <ReactMarkdown
          remarkPlugins={[remarkGfm]}
          rehypePlugins={[rehypeSlug]}
          components={{
            a({ href, children, ...rest }) {
              const c = classifyLink(href, path)
              if (c.kind === 'external') {
                return (
                  <a
                    {...rest}
                    href={href}
                    target="_blank"
                    rel="noopener noreferrer"
                    data-link-kind="external"
                  >
                    {children}
                  </a>
                )
              }
              const linkKind =
                c.kind === 'anchor'
                  ? 'anchor'
                  : c.kind === 'relative'
                    ? 'relative'
                    : c.kind === 'absolute-same-origin'
                      ? 'absolute'
                      : 'unknown'
              return (
                <a
                  {...rest}
                  href={href ?? '#'}
                  data-link-kind={linkKind}
                  onClick={(e) => href && handleAnchorClick(e, href)}
                >
                  {children}
                </a>
              )
            },
            img({ src, alt, ...rest }) {
              // Resolve relative image src to the Files raw API so the
              // existing MIME map / cache layer serves the bytes. Leave
              // absolute / data:/blob: / external untouched.
              let resolved = src ?? ''
              if (resolved && !/^[a-z][a-z0-9+.-]*:/i.test(resolved) && !resolved.startsWith('/')) {
                const base = dirnameOf(path)
                const joined = base ? `${base}/${resolved}` : resolved
                const norm = normalizePath(joined)
                resolved = `${apiBase}/raw?path=${encodeURIComponent(norm)}`
              }
              return <img {...rest} src={resolved} alt={alt ?? ''} />
            },
          }}
        >
          {body.content}
        </ReactMarkdown>
      </div>
    </div>
  )
}
