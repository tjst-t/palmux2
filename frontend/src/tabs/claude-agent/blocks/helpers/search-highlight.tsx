/** Helpers for Cmd+F search highlighting in conversation blocks (S018).
 *
 *  Each renderer that participates in search calls `searchMatchProps`
 *  to splat data-* attributes onto its outermost element so the search
 *  state machine can scroll to / style the matched block. Markdown
 *  bodies use `buildHighlightComponents` to wrap text content in
 *  <mark> elements via ReactMarkdown component overrides. Plain text
 *  bodies (ThinkingBlock, tool_result <pre>) use `highlightText`
 *  directly.
 */
import type * as React from 'react'

/** Returns the data-* attributes a renderer should splat onto its
 *  outermost element when the block participates in the active Cmd+F
 *  search. Empty object when the block doesn't match. */
export function searchMatchProps(
  blockId: string | undefined,
  query: string,
  openedBlocks: Set<string>,
  activeBlockId: string | undefined,
) {
  if (!query || !blockId) return {}
  if (!openedBlocks.has(blockId)) return {}
  return {
    'data-search-match': 'true',
    'data-search-active': blockId === activeBlockId ? 'true' : 'false',
  }
}

/** Recursively walk children and wrap raw string segments in <mark>
 *  via highlightText. Element children (e.g. <strong>, <em>) are
 *  preserved as-is — their own children pass through buildHighlight
 *  on the next render layer because we install the same component
 *  override on those tags too. */
export function highlightChildren(
  children: React.ReactNode,
  query: string,
  isActive: boolean,
): React.ReactNode {
  if (children == null || children === false) return children
  if (typeof children === 'string') return highlightText(children, query, isActive)
  if (Array.isArray(children)) {
    return children.map((c, i) =>
      typeof c === 'string'
        ? <span key={i}>{highlightText(c, query, isActive)}</span>
        : c,
    )
  }
  return children
}

/** Component overrides that wrap text content in <mark> for the
 *  active search query. Each entry preserves the original element's
 *  semantics — only the textual children are rewritten. */
export function buildHighlightComponents(query: string, isActive: boolean) {
  // Manually enumerate so each `tag` is a literal-typed JSX element name.
  // Building generically off `keyof JSX.IntrinsicElements` confuses the
  // strict-TS pipeline (Vite) about whether `tag` is constructible.
  const wrapP = (props: { children?: React.ReactNode }) => <p>{highlightChildren(props.children, query, isActive)}</p>
  const wrapLi = (props: { children?: React.ReactNode }) => <li>{highlightChildren(props.children, query, isActive)}</li>
  const wrapTd = (props: { children?: React.ReactNode }) => <td>{highlightChildren(props.children, query, isActive)}</td>
  const wrapTh = (props: { children?: React.ReactNode }) => <th>{highlightChildren(props.children, query, isActive)}</th>
  const wrapEm = (props: { children?: React.ReactNode }) => <em>{highlightChildren(props.children, query, isActive)}</em>
  const wrapStrong = (props: { children?: React.ReactNode }) => <strong>{highlightChildren(props.children, query, isActive)}</strong>
  const wrapH1 = (props: { children?: React.ReactNode }) => <h1>{highlightChildren(props.children, query, isActive)}</h1>
  const wrapH2 = (props: { children?: React.ReactNode }) => <h2>{highlightChildren(props.children, query, isActive)}</h2>
  const wrapH3 = (props: { children?: React.ReactNode }) => <h3>{highlightChildren(props.children, query, isActive)}</h3>
  const wrapH4 = (props: { children?: React.ReactNode }) => <h4>{highlightChildren(props.children, query, isActive)}</h4>
  const wrapH5 = (props: { children?: React.ReactNode }) => <h5>{highlightChildren(props.children, query, isActive)}</h5>
  const wrapH6 = (props: { children?: React.ReactNode }) => <h6>{highlightChildren(props.children, query, isActive)}</h6>
  const wrapCode = (props: { children?: React.ReactNode }) => <code>{highlightChildren(props.children, query, isActive)}</code>
  const wrapA = (props: { children?: React.ReactNode; href?: string }) => <a href={props.href}>{highlightChildren(props.children, query, isActive)}</a>
  const wrapBq = (props: { children?: React.ReactNode }) => <blockquote>{highlightChildren(props.children, query, isActive)}</blockquote>
  return {
    p: wrapP,
    li: wrapLi,
    td: wrapTd,
    th: wrapTh,
    em: wrapEm,
    strong: wrapStrong,
    h1: wrapH1,
    h2: wrapH2,
    h3: wrapH3,
    h4: wrapH4,
    h5: wrapH5,
    h6: wrapH6,
    code: wrapCode,
    a: wrapA,
    blockquote: wrapBq,
  }
}

/** highlightText splits a string into runs and wraps each match in a
 *  <mark> with `palmux-search-mark`. Returns the original string when
 *  the query is empty. Used by TextBlock / ThinkingBlock and the
 *  tool_result <pre> renderer.
 */
export function highlightText(text: string, query: string, isActive: boolean): React.ReactNode {
  if (!query || !text) return text
  const lower = text.toLowerCase()
  const needle = query.toLowerCase()
  const out: React.ReactNode[] = []
  let i = 0
  let key = 0
  while (i < text.length) {
    const next = lower.indexOf(needle, i)
    if (next < 0) {
      out.push(text.slice(i))
      break
    }
    if (next > i) out.push(text.slice(i, next))
    out.push(
      <mark
        key={`m${key++}`}
        className={`palmux-search-mark${isActive ? ' palmux-search-mark-active' : ''}`}
        data-testid="search-mark"
      >
        {text.slice(next, next + needle.length)}
      </mark>,
    )
    i = next + needle.length
  }
  return <>{out}</>
}
