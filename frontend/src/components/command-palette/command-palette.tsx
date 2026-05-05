import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom'

import { api } from '../../lib/api'
import { resolveBashTarget } from '../../lib/bash-target'
import { pushRecent, listRecents } from '../../lib/recents'
import { terminalManager } from '../../lib/terminal-manager'
import { selectBranchById, selectRepoById, usePalmuxStore, type UserCommand } from '../../stores/palmux-store'
import { ClaudeIcon } from '../icons/claude-icon'
import { UserCommandsModal } from '../user-commands-modal'

import styles from './palette.module.css'
import { useCommandPaletteStore } from './store'

// Modes: 'slash' has been removed (S031-1). The '/' prefix is now treated as
// a plain 'all' search. '?' prefix is new for content grep (S031-5).
type Mode = 'all' | 'workspace' | 'tab' | 'command' | 'file' | 'grep'

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

interface DetectedCommand {
  name: string
  source: string
  command: string
}

interface GrepHit {
  path: string
  lineNum: number
  line: string
}

/** Stable fallback to avoid creating a new array reference on every render. */
const EMPTY_USER_COMMANDS: UserCommand[] = []

function detectMode(raw: string): { mode: Mode; needle: string } {
  if (raw.startsWith('@')) return { mode: 'workspace', needle: raw.slice(1) }
  if (raw.startsWith('#')) return { mode: 'tab', needle: raw.slice(1) }
  // S031-1: '/' no longer triggers slash mode — falls through to 'all'
  if (raw.startsWith('>')) return { mode: 'command', needle: raw.slice(1) }
  if (raw.startsWith(':')) return { mode: 'file', needle: raw.slice(1) }
  if (raw.startsWith('?')) return { mode: 'grep', needle: raw.slice(1) }
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

// Lower score = better match. Used to rank command-mode results so the
// closest match is at the top — `>make serve` should put `make serve`
// above `make serve-storage` and way above `make build-storage-servers`.
//
// Tier 1 (0–9999): the full needle appears as a contiguous substring
//   (e.g. "make serve" is in both "make serve" and "make serve-storage").
//   Score by the substring's position, with shorter haystacks winning
//   the tiebreak so the most-exact match floats to the top.
//
// Tier 2 (10000+): the tokens match individually but not contiguously
//   (e.g. "make build-storage-servers" matches "make" + "serve" via
//   "servers"). Score by the sum of each token's earliest position.
function matchScore(haystack: string, needle: string): number {
  const lh = haystack.toLowerCase()
  const ln = needle.toLowerCase().trim()
  if (!ln) return 0
  const exactIdx = lh.indexOf(ln)
  if (exactIdx >= 0) {
    return exactIdx * 100 + Math.min(99, lh.length)
  }
  const tokens = ln.split(/\s+/).filter(Boolean)
  let sum = 0
  for (const t of tokens) {
    const i = lh.indexOf(t)
    if (i < 0) return Number.POSITIVE_INFINITY
    sum += i
  }
  return 10000 + sum * 100 + Math.min(99, lh.length)
}

export function CommandPalette() {
  const open = useCommandPaletteStore((s) => s.open)
  const initialQuery = useCommandPaletteStore((s) => s.initialQuery)
  const hide = useCommandPaletteStore((s) => s.hide)
  const toggle = useCommandPaletteStore((s) => s.toggle)
  // S032: user commands modal — lifted to CommandPalette so it survives
  // after the palette hides (PaletteInner unmounts on hide).
  const [userCmdModalOpen, setUserCmdModalOpen] = useState(false)

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

  const openUserCmdModal = useCallback(() => {
    hide() // close the palette first
    setUserCmdModalOpen(true)
  }, [hide])

  return (
    <>
      {open && (
        <PaletteInner
          key={initialQuery}
          initialQuery={initialQuery}
          onClose={hide}
          onOpenUserCmdModal={openUserCmdModal}
        />
      )}
      <UserCommandsModal open={userCmdModalOpen} onClose={() => setUserCmdModalOpen(false)} />
    </>
  )
}

function PaletteInner({
  initialQuery,
  onClose,
  onOpenUserCmdModal,
}: {
  initialQuery: string
  onClose: () => void
  onOpenUserCmdModal: () => void
}) {
  const [query, setQuery] = useState(initialQuery)
  const [active, setActive] = useState(0)
  const [commands, setCommands] = useState<DetectedCommand[]>([])
  const [files, setFiles] = useState<{ path: string; isDir: boolean }[]>([])
  const [grepHits, setGrepHits] = useState<GrepHit[]>([])
  // S031-2: bash picker mode (Cmd+Enter)
  const [bashPickerCmd, setBashPickerCmd] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement | null>(null)
  const listRef = useRef<HTMLUListElement | null>(null)

  const navigate = useNavigate()
  const location = useLocation()
  const [searchParams] = useSearchParams()
  const repos = usePalmuxStore((s) => s.repos)
  const addTab = usePalmuxStore((s) => s.addTab)
  const removeTab = usePalmuxStore((s) => s.removeTab)
  const renameTab = usePalmuxStore((s) => s.renameTab)
  const setDeviceSetting = usePalmuxStore((s) => s.setDeviceSetting)
  const deviceSettings = usePalmuxStore((s) => s.deviceSettings)
  // S032: user-defined commands from global settings palette.userCommands
  const userCommands = usePalmuxStore((s) => s.globalSettings.palette?.userCommands ?? EMPTY_USER_COMMANDS)
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

  // Lazy-load commands when a branch is in scope.
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

  const { mode, needle } = detectMode(query)
  const firstToken = needle.split(/\s+/).filter(Boolean)[0] ?? ''

  // Files: lazy search using the file-search endpoint. Only fires when in
  // ":" mode and there's a query.
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

  // S031-5: Content grep mode — debounced fetch on '?' prefix
  useEffect(() => {
    if (!activeRepo || !activeBranch) return
    if (mode !== 'grep') {
      setGrepHits([])
      return
    }
    if (!needle) {
      setGrepHits([])
      return
    }
    let cancelled = false
    const t = window.setTimeout(() => {
      void api
        .get<{ hits: GrepHit[] | null }>(
          `/api/repos/${encodeURIComponent(activeRepo.id)}/branches/${encodeURIComponent(activeBranch.id)}/files/grep?path=&pattern=${encodeURIComponent(needle)}&case=0`,
        )
        .then((res) => {
          if (!cancelled) setGrepHits(res.hits ?? [])
        })
        .catch(() => {})
    }, 250)
    return () => {
      cancelled = true
      window.clearTimeout(t)
    }
  }, [activeRepo, activeBranch, mode, needle])

  // S031-2: resolveBashTarget helper — uses the store addTab so it can
  // auto-create a Bash tab when none exists.
  const runOnBash = useCallback(
    async (command: string, targetTabId?: string) => {
      if (!activeRepo || !activeBranch) return
      const termKey = await resolveBashTarget(
        activeRepo.id,
        activeBranch.id,
        activeBranch.tabSet.tabs,
        addTab,
        targetTabId,
      )
      if (!termKey) return
      // Navigate to that tab so the user can see output.
      // Use termKey.tabId directly — avoids the stale closure problem when
      // auto-create just created the tab and it's not yet in tabs[] snapshot.
      const search = searchParams.toString() ? `?${searchParams.toString()}` : ''
      if (activeRepo && activeBranch) {
        navigate(
          `/${encodeURIComponent(activeRepo.id)}/${encodeURIComponent(activeBranch.id)}/${encodeURIComponent(termKey.tabId)}${search}`,
        )
      }
      terminalManager.sendInput(termKey.termKey, command + '\r')
      terminalManager.focus(termKey.termKey)
    },
    [activeRepo, activeBranch, addTab, navigate, searchParams],
  )

  // S031-4: builtin commands
  const builtinCommands = useMemo<PaletteItem[]>(() => {
    if (!activeRepo || !activeBranch) return []
    const items: PaletteItem[] = []

    // Tab operations
    items.push({
      id: 'builtin:new-bash',
      kind: 'command',
      icon: '$',
      label: 'new bash',
      detail: 'builtin',
      searchable: 'new bash terminal',
      perform: async () => {
        const tab = await addTab(activeRepo.id, activeBranch.id, 'bash')
        const search = searchParams.toString() ? `?${searchParams.toString()}` : ''
        navigate(`/${encodeURIComponent(activeRepo.id)}/${encodeURIComponent(activeBranch.id)}/${encodeURIComponent(tab.id)}${search}`)
      },
    })
    items.push({
      id: 'builtin:new-claude',
      kind: 'command',
      icon: <ClaudeIcon style={{ color: 'var(--color-accent-light)' }} />,
      label: 'new claude',
      detail: 'builtin',
      searchable: 'new claude tab',
      perform: async () => {
        const tab = await addTab(activeRepo.id, activeBranch.id, 'claude')
        const search = searchParams.toString() ? `?${searchParams.toString()}` : ''
        navigate(`/${encodeURIComponent(activeRepo.id)}/${encodeURIComponent(activeBranch.id)}/${encodeURIComponent(tab.id)}${search}`)
      },
    })
    if (params.tabId) {
      items.push({
        id: 'builtin:close-current',
        kind: 'command',
        icon: '×',
        label: 'close current tab',
        detail: 'builtin',
        searchable: 'close current tab',
        perform: async () => {
          await removeTab(activeRepo.id, activeBranch.id, params.tabId!)
        },
      })
      items.push({
        id: 'builtin:rename-current',
        kind: 'command',
        icon: '✎',
        label: 'rename current tab',
        detail: 'builtin',
        searchable: 'rename current tab',
        perform: async () => {
          const currentTab = activeBranch.tabSet.tabs.find((t) => t.id === params.tabId)
          const newName = window.prompt('New tab name:', currentTab?.name ?? '')
          if (newName && newName.trim()) {
            await renameTab(activeRepo.id, activeBranch.id, params.tabId!, newName.trim())
          }
        },
      })
      items.push({
        id: 'builtin:close-others',
        kind: 'command',
        icon: '⊠',
        label: 'close other tabs',
        detail: 'builtin',
        searchable: 'close other tabs',
        perform: async () => {
          const others = activeBranch.tabSet.tabs.filter(
            (t) => t.id !== params.tabId && !t.protected,
          )
          for (const t of others) {
            await removeTab(activeRepo.id, activeBranch.id, t.id)
          }
        },
      })
    }

    // S031-5: theme / font / GitHub builtins
    items.push({
      id: 'builtin:toggle-theme',
      kind: 'command',
      icon: '◑',
      label: 'toggle theme',
      detail: 'builtin',
      searchable: 'toggle theme dark light',
      perform: () => {
        setDeviceSetting('theme', deviceSettings.theme === 'dark' ? 'light' : 'dark')
      },
    })
    items.push({
      id: 'builtin:increase-font',
      kind: 'command',
      icon: 'A+',
      label: 'increase font size',
      detail: 'builtin',
      searchable: 'increase font size bigger',
      perform: () => {
        setDeviceSetting('fontSize', Math.min(24, deviceSettings.fontSize + 1))
      },
    })
    items.push({
      id: 'builtin:decrease-font',
      kind: 'command',
      icon: 'A-',
      label: 'decrease font size',
      detail: 'builtin',
      searchable: 'decrease font size smaller',
      perform: () => {
        setDeviceSetting('fontSize', Math.max(8, deviceSettings.fontSize - 1))
      },
    })
    items.push({
      id: 'builtin:open-github',
      kind: 'command',
      icon: '⎇',
      label: 'open on GitHub',
      detail: 'builtin',
      searchable: 'open on github browser',
      perform: async () => {
        try {
          const res = await api.get<{ url: string }>(
            `/api/repos/${encodeURIComponent(activeRepo.id)}/branches/${encodeURIComponent(activeBranch.id)}/remote-url`,
          )
          if (res?.url) {
            window.open(res.url, '_blank', 'noopener,noreferrer')
          }
        } catch {
          // ignore
        }
      },
    })

    // S032: manage user commands — opens the UserCommandsModal
    items.push({
      id: 'builtin:manage-user-commands',
      kind: 'command',
      icon: '⚙',
      label: 'manage user commands',
      detail: 'builtin',
      searchable: 'manage user commands palette settings',
      perform: () => {
        onOpenUserCmdModal()
      },
    })

    return items
  }, [activeRepo, activeBranch, params.tabId, addTab, removeTab, renameTab, setDeviceSetting, deviceSettings, navigate, searchParams, onOpenUserCmdModal])

  const items = useMemo<PaletteItem[]>(() => {
    // S031-4: read recents fresh on every render so pushRecent() during the
    // same session is reflected immediately (fix 1 from self-review).
    const recents = listRecents()

    // S031-2: bash picker mode replaces normal items
    if (bashPickerCmd !== null && activeRepo && activeBranch) {
      const out: PaletteItem[] = []
      const bashTabs = activeBranch.tabSet.tabs.filter((t) => t.type === 'bash')
      for (const t of bashTabs) {
        out.push({
          id: `bash-picker:${t.id}`,
          kind: 'command',
          icon: '$',
          label: t.name,
          detail: `→ bash:${t.name}`,
          searchable: t.name,
          perform: () => runOnBash(bashPickerCmd, t.id),
        })
      }
      out.push({
        id: 'bash-picker:new',
        kind: 'command',
        icon: '+',
        label: 'Open new Bash tab',
        detail: '→ new bash',
        searchable: 'new bash',
        perform: () => runOnBash(bashPickerCmd, '__new__'),
      })
      return out
    }

    const out: PaletteItem[] = []

    const includeWorkspace = mode === 'all' || mode === 'workspace'
    const includeTab = mode === 'tab'
    const includeCommand = mode === 'all' || mode === 'command'
    const includeFile = mode === 'file'
    const includeGrep = mode === 'grep'

    // S031-4: empty query shows recents
    if (mode === 'all' && needle === '') {
      // Show up to 8 recent items
      const recentItems = recents.slice(0, 8)
      for (const r of recentItems) {
        out.push({
          id: `recent:${r.key}`,
          kind: 'workspace',
          icon: recentIcon(r.kind),
          label: r.label,
          detail: r.kind,
          searchable: r.label,
          perform: () => {
            if (r.url) navigate(r.url)
          },
        })
      }
      return out
    }

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
            perform: () => {
              pushRecent({ kind: 'workspace', key: `${repo.id}/${branch.id}`, label, url })
              navigate(url)
            },
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
          perform: () => {
            pushRecent({ kind: 'tab', key: `${activeRepo.id}/${activeBranch.id}/${tab.id}`, label: `${repoDisplay(activeRepo.ghqPath)} / ${activeBranch.name} / ${tab.name}`, url })
            navigate(url)
          },
        })
      }
    }

    // S031-2 / S032: commands mode — route to Bash tab
    if (includeCommand && activeRepo && activeBranch) {
      // Resolve mru bash tab name for detail display
      const mruBashTabName = resolveMruBashTabName(activeRepo.id, activeBranch.id, activeBranch.tabSet.tabs)

      // Builtin commands (filtered by needle)
      for (const b of builtinCommands) {
        if (!fuzzyContains(b.searchable, needle)) continue
        out.push(b)
      }

      // S032: user-defined commands — inserted between builtins and Make/npm.
      // target: bash → resolveBashTarget; url → window.open; files → navigate.
      const filesTabId = activeBranch.tabSet.tabs.find((t) => t.type === 'files')?.id
      for (const uc of userCommands) {
        const searchable = `${uc.name} ${uc.notes ?? ''} user`.toLowerCase()
        if (!fuzzyContains(searchable, needle)) continue
        const buildDetail = () => {
          if (uc.target === 'bash') return mruBashTabName ? `→ bash:${mruBashTabName}` : `→ bash`
          if (uc.target === 'url') return `→ url`
          if (uc.target === 'files') return `→ files`
          return 'user'
        }
        const ucCopy: UserCommand = { ...uc }
        out.push({
          id: `user:${ucCopy.name}`,
          kind: 'command',
          icon: '★',
          label: ucCopy.name,
          detail: buildDetail(),
          searchable,
          perform: async () => {
            if (ucCopy.target === 'bash' && ucCopy.command) {
              await runOnBash(ucCopy.command)
            } else if (ucCopy.target === 'url' && ucCopy.url) {
              window.open(ucCopy.url, '_blank', 'noopener,noreferrer')
            } else if (ucCopy.target === 'files' && ucCopy.path && filesTabId) {
              const tail = ucCopy.path
                ? `/${ucCopy.path.split('/').map(encodeURIComponent).join('/')}`
                : ''
              const search = searchParams.toString() ? `?${searchParams.toString()}` : ''
              navigate(
                `/${encodeURIComponent(activeRepo.id)}/${encodeURIComponent(activeBranch.id)}/${encodeURIComponent(filesTabId)}${tail}${search}`,
              )
            }
          },
        })
      }

      // Make/npm detected commands
      for (const c of commands) {
        if (!fuzzyContains(`${c.source} ${c.name} ${c.command}`, needle)) continue
        out.push({
          id: `cmd:${c.source}:${c.name}`,
          kind: 'command',
          icon: '>',
          label: c.command,
          detail: mruBashTabName ? `→ bash:${mruBashTabName}` : `→ bash`,
          searchable: c.command,
          perform: () => runOnBash(c.command),
        })
      }

      // S032: cap command mode at 40 items so a large Makefile + user cmds +
      // builtins doesn't flood the list and stays scrollable.
      // hotfix: rank cmdItems by matchScore so `> make serve` puts
      // `make serve` above `make serve-storage` / `make build-storage
      // -servers`. Builtins / user / Make / npm all flow through the
      // same scorer using their `searchable` field.
      if (mode === 'command') {
        const cmdItems = out.filter((it) => it.kind === 'command')
        const others = out.filter((it) => it.kind !== 'command')
        if (needle) {
          cmdItems.sort((a, b) => matchScore(a.searchable, needle) - matchScore(b.searchable, needle))
        }
        return [...others, ...cmdItems.slice(0, 40)]
      }
    }

    if (includeFile && activeRepo && activeBranch) {
      for (const f of files) {
        if (!fuzzyContains(f.path, needle)) continue
        const search = searchParams.toString() ? `?${searchParams.toString()}` : ''
        const filesTabId = activeBranch.tabSet.tabs.find((t) => t.type === 'files')?.id
        const fileUrl = filesTabId
          ? `/${encodeURIComponent(activeRepo.id)}/${encodeURIComponent(activeBranch.id)}/${encodeURIComponent(filesTabId)}/${f.path.split('/').map(encodeURIComponent).join('/')}${search}`
          : null
        out.push({
          id: `file:${f.path}`,
          kind: 'file',
          icon: f.isDir ? '📁' : '📄',
          label: f.path,
          detail: 'files',
          searchable: f.path,
          perform: () => {
            if (!filesTabId || !activeRepo || !activeBranch) return
            // Files routes treat the URL path as the resource — the tab
            // resolves file vs dir on load, no `?file=` query needed.
            const tail = f.path
              ? `/${f.path.split('/').map(encodeURIComponent).join('/')}`
              : ''
            if (fileUrl) pushRecent({ kind: 'file', key: f.path, label: f.path, url: fileUrl })
            navigate(
              `/${encodeURIComponent(activeRepo.id)}/${encodeURIComponent(activeBranch.id)}/${encodeURIComponent(filesTabId)}${tail}${search}`,
            )
          },
        })
      }
    }

    // S031-5: grep results
    if (includeGrep && activeRepo && activeBranch) {
      const filesTabId = activeBranch.tabSet.tabs.find((t) => t.type === 'files')?.id
      for (const h of grepHits) {
        out.push({
          id: `grep:${h.path}:${h.lineNum}`,
          kind: 'grep',
          icon: '¶',
          label: `${h.path}:${h.lineNum}`,
          detail: h.line.trim().slice(0, 60),
          searchable: `${h.path} ${h.line}`,
          perform: () => {
            if (!filesTabId || !activeRepo || !activeBranch) return
            const search = `?line=${h.lineNum}`
            const tail = `/${h.path.split('/').map(encodeURIComponent).join('/')}`
            navigate(
              `/${encodeURIComponent(activeRepo.id)}/${encodeURIComponent(activeBranch.id)}/${encodeURIComponent(filesTabId)}${tail}${search}`,
            )
          },
        })
      }
      if (grepHits.length === 0 && needle) {
        // Use a sentinel id so the render below can display a non-interactive
        // status row instead of a keyboard-selectable button (fix 4).
        out.push({
          id: 'grep:searching',
          kind: 'grep',
          icon: '¶',
          label: 'Searching…',
          detail: '',
          searchable: '',
          perform: () => {},
        })
      }
    }

    if (mode === 'all') {
      // Cap each kind so the list stays scannable.
      return capByKind(out, 6)
    }
    return out
  }, [repos, mode, needle, commands, files, grepHits, builtinCommands, userCommands, searchParams, navigate, activeRepo, activeBranch, runOnBash, bashPickerCmd])

  // Sentinel items (non-interactive, like grep:searching) don't participate
  // in keyboard navigation — compute the selectable count separately.
  const selectableCount = items.filter((it) => it.id !== 'grep:searching').length

  // Reset highlight whenever the candidate set changes.
  useEffect(() => {
    setActive(0)
  }, [query, items.length])

  // Keyboard navigation. S031-2: Cmd+Enter enters bash picker mode.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        // If in bash picker mode, go back to command mode
        if (bashPickerCmd !== null) {
          setBashPickerCmd(null)
          return
        }
        onClose()
        return
      }
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setActive((i) => Math.min(selectableCount - 1, i + 1))
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        setActive((i) => Math.max(0, i - 1))
      } else if (e.key === 'Enter') {
        e.preventDefault()
        // S031-2: Cmd+Enter (Mac) / Ctrl+Enter (Linux) opens bash picker
        if ((e.metaKey || e.ctrlKey) && mode === 'command' && bashPickerCmd === null) {
          const item = items[active]
          if (item && item.id.startsWith('cmd:')) {
            setBashPickerCmd(item.label)
            return
          }
        }
        const item = items[active]
        if (item) {
          void item.perform()
          if (!item.id.startsWith('bash-picker:new')) {
            onClose()
          } else {
            onClose()
          }
        }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [items, active, onClose, mode, bashPickerCmd, selectableCount])

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
    bashPickerCmd !== null
      ? 'select bash target'
      : mode === 'workspace'
        ? 'workspaces'
        : mode === 'tab'
          ? 'tabs'
          : mode === 'command'
            ? 'commands'
            : mode === 'file'
              ? 'files'
              : mode === 'grep'
                ? 'grep'
                : ''

  return (
    <div className={styles.overlay} onClick={onClose} data-testid="palette-overlay">
      <div className={styles.card} onClick={(e) => e.stopPropagation()} data-testid="palette-card">
        <div className={styles.inputRow}>
          <input
            ref={inputRef}
            className={styles.input}
            value={query}
            placeholder="Type to search… use @workspace, #tab, >command, :file, ?grep"
            onChange={(e) => {
              setQuery(e.target.value)
              setBashPickerCmd(null)
            }}
            data-testid="palette-input"
          />
          {modeLabel && <span className={styles.prefix} data-testid="palette-mode-label">filter: {modeLabel}</span>}
        </div>
        {/* S031-1: hint row no longer includes /slash */}
        <div className={styles.hintRow} data-testid="palette-hint-row">
          <span><span className={styles.hint}>@</span> workspaces</span>
          <span><span className={styles.hint}>#</span> tabs</span>
          <span><span className={styles.hint}>&gt;</span> commands</span>
          <span><span className={styles.hint}>:</span> files</span>
          <span><span className={styles.hint}>?</span> grep</span>
          <span style={{ marginLeft: 'auto' }}><span className={styles.hint}>↵</span> select</span>
        </div>
        {bashPickerCmd !== null && (
          <div className={styles.bashPickerBanner} data-testid="bash-picker-banner">
            <span>Send <code className={styles.bashPickerCmd}>{bashPickerCmd}</code> to:</span>
          </div>
        )}
        <ul className={styles.list} ref={listRef} data-testid="palette-list">
          {items.length === 0 && mode === 'all' && needle === '' && (
            <li className={styles.empty} data-testid="palette-empty-state">
              No recent items. Start typing to search.
            </li>
          )}
          {items.length === 0 && (mode !== 'all' || needle !== '') && (
            <li className={styles.empty}>
              {mode === 'grep' ? 'No matches.' : needle ? 'No matches.' : 'Start typing or press a prefix to filter.'}
            </li>
          )}
          {items.map((item, i) => {
            // Sentinel rows (e.g. grep:searching) are non-interactive status
            // indicators — render as plain <li> with the .empty style so they
            // are visible but not keyboard-selectable (fix 4).
            if (item.id === 'grep:searching') {
              return (
                <li key={item.id} className={styles.empty} data-testid="palette-grep-searching">
                  {item.label}
                </li>
              )
            }
            return (
              <li key={item.id}>
                <button
                  type="button"
                  data-row={i}
                  data-testid={`palette-item-${item.id.replace(/[:/]/g, '-')}`}
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
            )
          })}
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

function recentIcon(kind: string): string {
  switch (kind) {
    case 'workspace': return '⌂'
    case 'tab': return '⊡'
    case 'file': return '📄'
    default: return '•'
  }
}

function capByKind(items: PaletteItem[], n: number): PaletteItem[] {
  const buckets: Record<string, PaletteItem[]> = {
    workspace: [],
    tab: [],
    command: [],
    file: [],
    grep: [],
  }
  for (const it of items) {
    const b = buckets[it.kind]
    if (b) b.push(it)
  }
  return [
    ...buckets.workspace.slice(0, n),
    ...buckets.tab.slice(0, n),
    ...buckets.command.slice(0, n),
    ...buckets.file.slice(0, n),
    ...buckets.grep.slice(0, n),
  ]
}

// S031-2: return the mru bash tab name for detail display in command rows
function resolveMruBashTabName(
  repoId: string,
  branchId: string,
  tabs: { id: string; type: string; name: string }[],
): string | null {
  try {
    const stored = localStorage.getItem(`palmux:lastBashTab:${repoId}/${branchId}`)
    if (stored) {
      const tab = tabs.find((t) => t.id === stored)
      if (tab) return tab.name
    }
  } catch {
    // ignore
  }
  const firstBash = tabs.find((t) => t.type === 'bash')
  return firstBash?.name ?? null
}
