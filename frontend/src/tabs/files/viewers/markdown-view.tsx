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
//
// S67cb0e: The shared link-classification + GFM rendering now lives in
// MarkdownBlock. MarkdownView injects custom `a` (adds relative-path
// navigation) and `img` (src rewriting) overrides so Files-specific
// behaviour is preserved without duplicating the core rendering stack.

import { useCallback, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'

import { MarkdownBlock } from '../../../components/markdown-block'
import { classifyLink } from '../../../components/markdown-link-helpers'
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

/** Classify a relative path href for Files-tab navigation. Returns null
 *  when the href is not a relative path (caller should fall back to the
 *  shared classifyLink).
 *
 *  Stays local (not in the shared markdown-link-helpers.ts) because it
 *  resolves against `currentPath` — context the shared MarkdownBlock has
 *  no notion of; only the Files viewer knows which file is open. */
function classifyRelative(href: string, currentPath: string): { resolved: string } | null {
  if (!href) return null
  if (href.startsWith('#') || href.startsWith('/') || /^[a-z][a-z0-9+.-]*:/i.test(href)) {
    return null
  }
  // Relative path — resolve against the markdown file's directory.
  let pathPart = href
  let suffix = ''
  const hashIdx = pathPart.indexOf('#')
  const queryIdx = pathPart.indexOf('?')
  const cut =
    queryIdx === -1 ? hashIdx : hashIdx === -1 ? queryIdx : Math.min(queryIdx, hashIdx)
  if (cut !== -1) {
    suffix = pathPart.slice(cut)
    pathPart = pathPart.slice(0, cut)
  }
  const base = dirnameOf(currentPath)
  const joined = base ? `${base}/${pathPart}` : pathPart
  const resolved = normalizePath(joined) + suffix
  return { resolved }
}

export function MarkdownView({ body, path, apiBase, repoId, branchId, tabId, onInternalNavigate }: ViewerProps) {
  const navigate = useNavigate()
  const containerRef = useRef<HTMLDivElement | null>(null)

  // S027 AC-5: when the page loads with a URL fragment (`#section`),
  // scroll to that heading after the markdown DOM has been rendered.
  useEffect(() => {
    if (!body) return
    if (typeof window === 'undefined') return
    const hash = window.location.hash
    if (!hash || hash.length < 2) return
    const id = decodeURIComponent(hash.slice(1))
    let raf1 = 0
    let raf2 = 0
    raf1 = requestAnimationFrame(() => {
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

  // S027 AC-4: browser back/forward through anchor history.
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

  // Files-specific link handler: adds relative-path navigation on top of
  // the shared classifyLink logic. MarkdownBlock's built-in `a` handler
  // is replaced by this one, which handles relative paths and delegates
  // all other cases to the same logic MarkdownBlock would use.
  const handleAnchorClick = useCallback(
    (e: React.MouseEvent<HTMLAnchorElement>, href: string) => {
      if (e.defaultPrevented) return
      if (e.button !== 0) return
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return

      // Check for Files-specific relative path first.
      const rel = classifyRelative(href, path)
      if (rel && repoId && branchId && tabId) {
        e.preventDefault()
        // Route through the panel-aware navigator when FilesView provides one, so
        // the right (local) split panel navigates itself instead of hijacking the
        // main route. Fall back to a plain route push when unavailable.
        if (onInternalNavigate) {
          onInternalNavigate(rel.resolved)
        } else {
          navigate(buildFilesUrl(repoId, branchId, tabId, rel.resolved))
        }
        return
      }

      const c = classifyLink(href)
      if (c.kind === 'anchor') {
        e.preventDefault()
        const root = containerRef.current
        const el = root?.querySelector<HTMLElement>(`#${CSS.escape(c.id)}`) ?? null
        if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
        if (typeof window !== 'undefined') {
          const next = `#${c.id}`
          if (window.location.hash !== next) {
            window.history.pushState(window.history.state, '', next)
          }
        }
        return
      }
      if (c.kind === 'same-origin') {
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
    [navigate, path, repoId, branchId, tabId, onInternalNavigate],
  )

  if (!body) return <p className={styles.placeholder}>Loading…</p>
  return (
    <div className={styles.wrap} data-testid="markdown-view" ref={containerRef}>
      <div className={styles.markdown}>
        <MarkdownBlock
          components={{
            a({ href, children, ...rest }) {
              const rel = classifyRelative(href ?? '', path)
              const c = classifyLink(href)
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
              const linkKind = rel
                ? 'relative'
                : c.kind === 'anchor'
                  ? 'anchor'
                  : c.kind === 'same-origin'
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
              // existing MIME map / cache layer serves the bytes.
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
        </MarkdownBlock>
      </div>
    </div>
  )
}
