import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom'

import { useFocusedTerminal } from '../../hooks/use-focused-terminal'
import { api } from '../../lib/api'
import { terminalManager } from '../../lib/terminal-manager'
import { selectBranchById, selectRepoById, usePalmuxStore } from '../../stores/palmux-store'
import { ClaudeIcon } from '../icons/claude-icon'

import styles from './palette.module.css'
import { useCommandPaletteStore } from './store'

type Mode = 'all' | 'workspace' | 'tab' | 'slash' | 'command' | 'file'

interface PaletteItem {
  id: string
  kind: Mode
  icon: ReactNode
  label: string
  detail?: string
  /** Free-text used for client-side filtering. */
  searchable: string
  perform: () => void | Promise<void>
}

const SLASH_COMMANDS = [
  '/clear',
  '/compact',
  '/init',
  '/memory',
  '/help',
  '/model',
  '/cost',
  '/exit',
]

interface DetectedCommand {
  name: string
  source: string
  command: string
}

function detectMode(raw: string): { mode: Mode; needle: string } {
  if (raw.startsWith('@')) return { mode: 'workspace', needle: raw.slice(1) }
  if (raw.startsWith('#')) return { mode: 'tab', needle: raw.slice(1) }
  if (raw.startsWith('/')) return { mode: 'slash', needle: raw.slice(1) }
  if (raw.startsWith('>')) return { mode: 'command', needle: raw.slice(1) }
  if (raw.startsWith(':')) return { mode: 'file', needle: raw.slice(1) }
  return { mode: 'all', needle: raw }
}

function lastTabFor(repoId: string, branchId: string): string | null {
  try {
    return localStorage.getItem(`palmux:lastTab:${repoId}/${branchId}`)
  } catch {
    return null
  }
}

// parseRouteParams reads {repoId, branchId, tabId} from a pathname like
// "/repo/branch/tab/...". Empty when on "/" or any other route.
function parseRouteParams(pathname: string): { repoId?: string; branchId?: string; tabId?: string } {
  const [, repoId, branchId, tabId] = pathname.split('/').map((p) => (p ? decodeURIComponent(p) : p))
  return { repoId: repoId || undefined, branchId: branchId || undefined, tabId: tabId || undefined }
}

// Token-AND match: splits the needle on whitespace and requires every
// non-empty token to appear somewhere in the haystack. This lets users
// narrow across the " / " separators in workspace labels — e.g.
// "@gpu main" matches "tjst-t/ansible-bio-gpu / main / Claude".
function fuzzyContains(haystack: string, needle: string): boolean {
  if (!needle) return true
  const lh = haystack.toLowerCase()
  const tokens = needle.toLowerCase().split(/\s+/).filter(Boolean)
  if (tokens.length === 0) return true
  return tokens.every((t) => lh.includes(t))
}

export function CommandPalette() {
  const open = useCommandPaletteStore((s) => s.open)
  const initialQuery = useCommandPaletteStore((s) => s.initialQuery)
  const hide = useCommandPaletteStore((s) => s.hide)
  const toggle = useCommandPaletteStore((s) => s.toggle)

  // Global hotkey: Cmd+K / Ctrl+K toggles. Register once.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
        e.preventDefault()
        toggle()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [toggle])

  if (!open) return null
  return <PaletteInner key={initialQuery} initialQuery={initialQuery} onClose={hide} />
}

function PaletteInner({
  initialQuery,
  onClose,
}: {
  initialQuery: string
  onClose: () => void
}) {
  const [query, setQuery] = useState(initialQuery)
  const [active, setActive] = useState(0)
  const [commands, setCommands] = useState<DetectedCommand[]>([])
  const [files, setFiles] = useState<{ path: string; isDir: boolean }[]>([])
  const inputRef = useRef<HTMLInputElement | null>(null)
  const listRef = useRef<HTMLUListElement | null>(null)

  const navigate = useNavigate()
  const location = useLocation()
  const [searchParams] = useSearchParams()
  const repos = usePalmuxStore((s) => s.repos)
  const focused = useFocusedTerminal()
  // The palette is mounted at app root (outside <Routes>) so useParams()
  // always returns empty here. Parse the active repo/branch out of
  // location.pathname instead.
  const params = parseRouteParams(location.pathname)
  const activeRepo = usePalmuxStore((s) =>
    params.repoId ? selectRepoById(params.repoId)(s) : undefined,
  )
  const activeBranch = usePalmuxStore((s) =>
    params.repoId && params.branchId
      ? selectBranchById(params.repoId, params.branchId)(s)
      : undefined,
  )

  // Lazy-load commands + files when a branch is in scope.
  useEffect(() => {
    if (!activeRepo || !activeBranch) return
    let cancelled = false
    void api
      .get<DetectedCommand[]>(
        `/api/repos/${encodeURIComponent(activeRepo.id)}/branches/${encodeURIComponent(activeBranch.id)}/commands`,
      )
      .then((cs) => {
        if (!cancelled) setCommands(cs)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [activeRepo, activeBranch])

  // Files: lazy search using the file-search endpoint. Only fires when in
  // ":" mode and there's a query. The server takes a single substring, so
  // we send the first token and let the client-side fuzzyContains narrow
  // by any remaining tokens (so ":foo bar" works the same as ":foo bar"
  // in workspace mode).
  const { mode, needle } = detectMode(query)
  const firstToken = needle.split(/\s+/).filter(Boolean)[0] ?? ''
  useEffect(() => {
    if (!activeRepo || !activeBranch) return
    if (mode !== 'file' && mode !== 'all') return
    if (!firstToken) {
      setFiles([])
      return
    }
    let cancelled = false
    const t = window.setTimeout(() => {
      void api
        .get<{ results: { path: string; isDir: boolean }[] | null }>(
          `/api/repos/${encodeURIComponent(activeRepo.id)}/branches/${encodeURIComponent(activeBranch.id)}/files/search?path=&query=${encodeURIComponent(firstToken)}&case=0`,
        )
        .then((res) => {
          if (!cancelled) setFiles(res.results ?? [])
        })
        .catch(() => {})
    }, 80)
    return () => {
      cancelled = true
      window.clearTimeout(t)
    }
  }, [activeRepo, activeBranch, mode, firstToken])

  const items = useMemo<PaletteItem[]>(() => {
    const out: PaletteItem[] = []

    const includeWorkspace = mode === 'all' || mode === 'workspace'
    const includeTab = mode === 'tab'
    const includeSlash = mode === 'all' || mode === 'slash'
    const includeCommand = mode === 'all' || mode === 'command'
    const includeFile = mode === 'file'

    if (includeWorkspace) {
      // One row per (repo, branch). Selecting it navigates to the branch's
      // last-active tab — falling back to the Claude tab, then the first
      // tab — so the user lands where they were last working.
      for (const repo of repos) {
        for (const branch of repo.openBranches) {
          const label = `${repoDisplay(repo.ghqPath)} / ${branch.name}`
          if (!fuzzyContains(label, needle)) continue
          const remembered = lastTabFor(repo.id, branch.id)
          const target =
            (remembered && branch.tabSet.tabs.find((t) => t.id === remembered)) ||
            branch.tabSet.tabs.find((t) => t.type === 'claude') ||
            branch.tabSet.tabs[0]
          if (!target) continue
          const search = searchParams.toString() ? `?${searchParams.toString()}` : ''
          const url = `/${encodeURIComponent(repo.id)}/${encodeURIComponent(branch.id)}/${encodeURIComponent(target.id)}${search}`
          out.push({
            id: `ws:${repo.id}/${branch.id}`,
            kind: 'workspace',
            icon: '⌂',
            label,
            detail: `→ ${target.name}`,
            searchable: label,
            perform: () => navigate(url),
          })
        }
      }
    }

    if (includeTab && activeRepo && activeBranch) {
      // Tabs of the *currently focused* workspace. Useful as a quick switch
      // without leaving the palette.
      for (const tab of activeBranch.tabSet.tabs) {
        if (!fuzzyContains(`${tab.name} ${tab.type}`, needle)) continue
        const search = searchParams.toString() ? `?${searchParams.toString()}` : ''
        const url = `/${encodeURIComponent(activeRepo.id)}/${encodeURIComponent(activeBranch.id)}/${encodeURIComponent(tab.id)}${search}`
        out.push({
          id: `tab:${activeRepo.id}/${activeBranch.id}/${tab.id}`,
          kind: 'tab',
          icon: tabIcon(tab.type),
          label: tab.name,
          detail: tab.type,
          searchable: `${tab.name} ${tab.type}`,
          perform: () => navigate(url),
        })
      }
    }

    if (includeSlash) {
      for (const cmd of SLASH_COMMANDS) {
        if (!fuzzyContains(cmd, needle)) continue
        out.push({
          id: `slash:${cmd}`,
          kind: 'slash',
          icon: '/',
          label: cmd,
          detail: focused.tabType === 'claude' ? 'send to Claude' : 'no Claude tab',
          searchable: cmd,
          perform: () => {
            if (!focused.termKey) return
            terminalManager.sendInput(focused.termKey, cmd + '\r')
            terminalManager.focus(focused.termKey)
          },
        })
      }
    }

    if (includeCommand) {
      for (const c of commands) {
        if (!fuzzyContains(`${c.source} ${c.name} ${c.command}`, needle)) continue
        out.push({
          id: `cmd:${c.source}:${c.name}`,
          kind: 'command',
          icon: '>',
          label: c.command,
          detail: c.source,
          searchable: c.command,
          perform: () => {
            if (!focused.termKey) return
            terminalManager.sendInput(focused.termKey, c.command + '\r')
            terminalManager.focus(focused.termKey)
          },
        })
      }
    }

    if (includeFile && activeRepo && activeBranch) {
      for (const f of files) {
        if (!fuzzyContains(f.path, needle)) continue
        out.push({
          id: `file:${f.path}`,
          kind: 'file',
          icon: f.isDir ? '📁' : '📄',
          label: f.path,
          detail: 'files',
          searchable: f.path,
          perform: () => {
            const filesTabId = activeBranch.tabSet.tabs.find((t) => t.type === 'files')?.id
            if (!filesTabId) return
            const search = searchParams.toString() ? `?${searchParams.toString()}` : ''
            if (f.isDir) {
              const tail = f.path
                ? `/${f.path.split('/').map(encodeURIComponent).join('/')}`
                : ''
              navigate(
                `/${encodeURIComponent(activeRepo.id)}/${encodeURIComponent(activeBranch.id)}/${encodeURIComponent(filesTabId)}${tail}${search}`,
              )
            } else {
              const sp = new URLSearchParams(searchParams)
              sp.set('file', f.path)
              navigate(
                `/${encodeURIComponent(activeRepo.id)}/${encodeURIComponent(activeBranch.id)}/${encodeURIComponent(filesTabId)}?${sp.toString()}`,
              )
            }
          },
        })
      }
    }

    if (mode === 'all') {
      // Cap each kind so the list stays scannable.
      return capByKind(out, 6)
    }
    return out
  }, [repos, mode, needle, commands, files, focused, searchParams, navigate, activeRepo, activeBranch])

  // Reset highlight whenever the candidate set changes.
  useEffect(() => {
    setActive(0)
  }, [query, items.length])

  // Keyboard navigation.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
        return
      }
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setActive((i) => Math.min(items.length - 1, i + 1))
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        setActive((i) => Math.max(0, i - 1))
      } else if (e.key === 'Enter') {
        e.preventDefault()
        const item = items[active]
        if (item) {
          void item.perform()
          onClose()
        }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [items, active, onClose])

  // Scroll active item into view.
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-row="${active}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [active])

  // Auto-focus the input.
  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  const modeLabel =
    mode === 'workspace'
      ? 'workspaces'
      : mode === 'tab'
        ? 'tabs'
        : mode === 'slash'
          ? 'slash'
          : mode === 'command'
            ? 'commands'
            : mode === 'file'
              ? 'files'
              : ''

  return (
    <div className={styles.overlay} onClick={onClose}>
      <div className={styles.card} onClick={(e) => e.stopPropagation()}>
        <div className={styles.inputRow}>
          <input
            ref={inputRef}
            className={styles.input}
            value={query}
            placeholder="Type to search… use @workspace, /slash, >command, :file"
            onChange={(e) => setQuery(e.target.value)}
          />
          {modeLabel && <span className={styles.prefix}>filter: {modeLabel}</span>}
        </div>
        <div className={styles.hintRow}>
          <span><span className={styles.hint}>@</span> workspaces</span>
          <span><span className={styles.hint}>#</span> tabs</span>
          <span><span className={styles.hint}>/</span> slash</span>
          <span><span className={styles.hint}>&gt;</span> commands</span>
          <span><span className={styles.hint}>:</span> files</span>
          <span style={{ marginLeft: 'auto' }}><span className={styles.hint}>↵</span> select</span>
        </div>
        <ul className={styles.list} ref={listRef}>
          {items.length === 0 && (
            <li className={styles.empty}>
              {needle ? 'No matches.' : 'Start typing or press a prefix to filter.'}
            </li>
          )}
          {items.map((item, i) => (
            <li key={item.id}>
              <button
                type="button"
                data-row={i}
                className={i === active ? `${styles.row} ${styles.rowActive}` : styles.row}
                onMouseEnter={() => setActive(i)}
                onClick={() => {
                  void item.perform()
                  onClose()
                }}
              >
                <span className={styles.icon}>{item.icon}</span>
                <span className={styles.label}>{item.label}</span>
                {item.detail && <span className={styles.detail}>{item.detail}</span>}
              </button>
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}


function repoDisplay(ghqPath: string): string {
  const parts = ghqPath.split('/')
  return parts.slice(1).join('/') || ghqPath
}

function tabIcon(type: string): ReactNode {
  switch (type) {
    case 'claude':
      return <ClaudeIcon style={{ color: 'var(--color-accent-light)' }} />
    case 'bash':
      return '$'
    case 'files':
      return '📁'
    case 'git':
      return '⎇'
    default:
      return '•'
  }
}

function capByKind(items: PaletteItem[], n: number): PaletteItem[] {
  const buckets: Record<Mode, PaletteItem[]> = {
    all: [],
    workspace: [],
    tab: [],
    slash: [],
    command: [],
    file: [],
  }
  for (const it of items) buckets[it.kind].push(it)
  return [
    ...buckets.workspace.slice(0, n),
    ...buckets.tab.slice(0, n),
    ...buckets.slash.slice(0, n),
    ...buckets.command.slice(0, n),
    ...buckets.file.slice(0, n),
  ]
}
