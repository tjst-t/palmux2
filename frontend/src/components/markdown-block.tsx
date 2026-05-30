// MarkdownBlock — reusable Markdown renderer shared across Sprint and any
// other view that needs plain-text→rendered-Markdown without Files-specific
// image src rewriting.
//
// Link policy:
//   1. anchor (#foo)              → smooth scroll + pushState
//   2. same-origin absolute (/…)  → React Router navigate
//   3. same-origin http(s): URL   → React Router navigate
//   4. external                   → target=_blank, rel=noopener noreferrer
//
// The component intentionally does NOT handle relative-path links
// (e.g. `./foo.md`) because it has no notion of a "current file path".
// Files-specific behaviour (relative path navigation, img src rewriting)
// lives in markdown-view.tsx.
//
// Props:
//   children  — the Markdown source string
//   className — optional extra class for the wrapper div
//   components — optional ReactMarkdown `components` overrides (e.g. for
//                custom img handling in a specialised consumer)

import { useCallback, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import ReactMarkdown from 'react-markdown'
import rehypeSlug from 'rehype-slug'
import remarkGfm from 'remark-gfm'
import type { Components } from 'react-markdown'

import { classifyLink } from './markdown-link-helpers'
import styles from './markdown-block.module.css'

interface MarkdownBlockProps {
  children: string
  className?: string
  /** Optional ReactMarkdown `components` overrides. Consumer-supplied
   *  overrides are merged on top of the built-in `a` override, so passing
   *  a custom `a` key replaces the link-policy logic entirely. */
  components?: Components
}

export function MarkdownBlock({ children, className, components: extraComponents }: MarkdownBlockProps) {
  const navigate = useNavigate()
  const containerRef = useRef<HTMLDivElement | null>(null)

  const handleAnchorClick = useCallback(
    (e: React.MouseEvent<HTMLAnchorElement>, href: string) => {
      if (e.defaultPrevented) return
      if (e.button !== 0) return
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return

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
      // external / unknown: let the renderer's target=_blank / browser default win.
    },
    [navigate],
  )

  const builtinComponents: Components = {
    a({ href, children: linkChildren, ...rest }) {
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
            {linkChildren}
          </a>
        )
      }
      const linkKind =
        c.kind === 'anchor' ? 'anchor' : c.kind === 'same-origin' ? 'absolute' : 'unknown'
      return (
        <a
          {...rest}
          href={href ?? '#'}
          data-link-kind={linkKind}
          onClick={(e) => href && handleAnchorClick(e, href)}
        >
          {linkChildren}
        </a>
      )
    },
  }

  const mergedComponents: Components = { ...builtinComponents, ...extraComponents }

  return (
    <div className={`${styles.markdownBlock}${className ? ` ${className}` : ''}`} ref={containerRef}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeSlug]}
        components={mergedComponents}
      >
        {children}
      </ReactMarkdown>
    </div>
  )
}
