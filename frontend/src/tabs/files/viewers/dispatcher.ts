// Viewer dispatcher (S010).
//
// Routes a (path, mime) pair → viewer kind. The actual viewer components
// are lazy-loaded by the consumer (`file-preview.tsx`) so the Monaco /
// Drawio bundles only land in the browser the first time the user opens
// a code or diagram file.
//
// Order matters in `pickViewer` — `.drawio.svg` must be matched as
// drawio (not image), so the drawio check fires first.

export type ViewerKind = 'markdown' | 'drawio' | 'image' | 'monaco' | 'too-large'

const IMAGE_EXTS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg'])

/** True if `name` ends with a drawio variant we should hand to the
 *  embedded drawio viewer rather than to the image / Monaco fallbacks. */
export function isDrawioPath(name: string): boolean {
  const lower = name.toLowerCase()
  return (
    lower.endsWith('.drawio') ||
    lower.endsWith('.drawio.svg') ||
    lower.endsWith('.drawio.png') ||
    lower.endsWith('.drawio.xml')
  )
}

/** True if the file extension matches one of the inline-displayable
 *  image formats. SVG goes through DOMPurify in `image-view.tsx`. */
export function isImagePath(name: string): boolean {
  const idx = name.lastIndexOf('.')
  if (idx < 0) return false
  const ext = name.slice(idx + 1).toLowerCase()
  return IMAGE_EXTS.has(ext)
}

/** Markdown is the only special-cased text format because we want to
 *  keep the existing remark/gfm pipeline (DESIGN_PRINCIPLES: existing
 *  asset reuse). Everything else falls through to Monaco. */
export function isMarkdownPath(name: string): boolean {
  const lower = name.toLowerCase()
  return lower.endsWith('.md') || lower.endsWith('.markdown')
}

export interface ViewerInputs {
  /** Worktree-relative path. */
  path: string
  /** Server-reported size in bytes (from listDir or readFile metadata). */
  size: number
  /** Server-reported MIME (optional — used as a tiebreaker). */
  mime?: string
  /** Soft cap on bytes (S010 setting `previewMaxBytes`, default 10 MiB).
   *  When `size > maxBytes` we return `too-large` *without* requesting
   *  the body, sparing the bandwidth round-trip. */
  maxBytes: number
}

/** Pick the viewer kind for a given file. Pure function — no side
 *  effects, easy to unit test if we ever want to. */
export function pickViewer(input: ViewerInputs): ViewerKind {
  if (input.size > input.maxBytes) return 'too-large'
  if (isDrawioPath(input.path)) return 'drawio'
  if (isImagePath(input.path) || (input.mime ?? '').startsWith('image/')) return 'image'
  if (isMarkdownPath(input.path) || input.mime === 'text/markdown') return 'markdown'
  return 'monaco'
}

/** Map a path to a Monaco language id. Monaco's own
 *  `monaco.languages.getLanguages()` covers most of these but exposing
 *  the lookup here means the caller can render unknown extensions as
 *  `plaintext` (raw text fallback) without round-tripping through
 *  Monaco's full language registry. */
export function monacoLanguageFor(path: string): string {
  const lower = path.toLowerCase()
  // Compound extensions first.
  if (lower.endsWith('.d.ts')) return 'typescript'
  // Plain Dockerfile / Makefile / etc. — look at basename, not extension.
  const slash = lower.lastIndexOf('/')
  const base = slash >= 0 ? lower.slice(slash + 1) : lower
  if (base === 'dockerfile' || base.startsWith('dockerfile.')) return 'dockerfile'
  if (base === 'makefile' || base === 'gnumakefile') return 'shell'
  // Single-extension cases.
  const dot = base.lastIndexOf('.')
  if (dot < 0) return 'plaintext'
  const ext = base.slice(dot + 1)
  switch (ext) {
    case 'go':
      return 'go'
    case 'ts':
    case 'tsx':
    case 'mts':
    case 'cts':
      return 'typescript'
    case 'js':
    case 'jsx':
    case 'mjs':
    case 'cjs':
      return 'javascript'
    case 'py':
      return 'python'
    case 'rs':
      return 'rust'
    case 'java':
      return 'java'
    case 'c':
    case 'h':
      return 'c'
    case 'cc':
    case 'cpp':
    case 'cxx':
    case 'hpp':
    case 'hh':
      return 'cpp'
    case 'rb':
      return 'ruby'
    case 'php':
      return 'php'
    case 'swift':
      return 'swift'
    case 'kt':
    case 'kts':
      return 'kotlin'
    case 'sh':
    case 'bash':
    case 'zsh':
      return 'shell'
    case 'yaml':
    case 'yml':
      return 'yaml'
    case 'json':
    case 'jsonc':
      return 'json'
    case 'toml':
      return 'plaintext' // Monaco lacks a stock toml grammar
    case 'md':
    case 'markdown':
      return 'markdown'
    case 'sql':
      return 'sql'
    case 'html':
    case 'htm':
      return 'html'
    case 'css':
    case 'scss':
    case 'less':
      return 'css'
    case 'xml':
    case 'svg':
      return 'xml'
    case 'lua':
      return 'lua'
    case 'r':
      return 'r'
    case 'graphql':
    case 'gql':
      return 'graphql'
    default:
      return 'plaintext'
  }
}
