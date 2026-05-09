/** Inline completion config for the composer.
 *
 *  Builds the `/` (slash command) and `@` (file mention) triggers
 *  for the inline-completion UI. The composer wires these into
 *  useInlineCompletion().
 */
import type {
  CompletionOption,
  CompletionTrigger,
} from '../../../components/inline-completion'
import type { InitInfo } from '../types'

export const INTERNAL_COMMANDS: { name: string; description: string }[] = [
  { name: 'clear', description: 'Start a fresh session (drop the active session_id)' },
  { name: 'model', description: 'Switch model: /model <name>' },
  // S018 — `/compact` is a CLI-handled slash command (it shows up in
  // the CLI's slash_commands list once init lands). Listing it here
  // too makes it discoverable before init completes and ensures the
  // confirm-on-submit interceptor (see submit) is reachable.
  { name: 'compact', description: 'Summarise past conversation to free context (destructive)' },
]

interface BuildTriggersArgs {
  repoId: string
  branchId: string
  initInfo?: InitInfo
}

export function buildCompletionTriggers({ repoId, branchId, initInfo }: BuildTriggersArgs): CompletionTrigger[] {
  const cliCommands = initInfo?.commands ?? []

  const slashTrigger: CompletionTrigger = {
    char: '/',
    name: 'Commands',
    fetchOptions: async (q) => {
      const all: CompletionOption[] = []
      // Internal commands first.
      for (const c of INTERNAL_COMMANDS) {
        all.push({
          id: 'internal:' + c.name,
          label: '/' + c.name,
          detail: c.description,
          insertText: '/' + c.name + ' ',
        })
      }
      for (const c of cliCommands) {
        const insertText = c.argumentHint ? '/' + c.name + ' ' : '/' + c.name + ' '
        all.push({
          id: 'cli:' + c.name,
          label: '/' + c.name + (c.argumentHint ? ' ' + c.argumentHint : ''),
          detail: c.description,
          insertText,
        })
        for (const a of c.aliases ?? []) {
          all.push({
            id: 'cli:' + c.name + ':' + a,
            label: '/' + a,
            detail: c.description,
            insertText: '/' + a + ' ',
          })
        }
      }
      const ql = q.toLowerCase()
      return all
        .filter((o) => !ql || o.label.toLowerCase().includes(ql))
        .slice(0, 30)
    },
  }

  const mentionTrigger: CompletionTrigger = {
    char: '@',
    name: 'Files',
    fetchOptions: async (q, signal) => {
      const query = q.trim()
      if (!query) return []
      try {
        const url =
          `/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}` +
          `/files/search?query=${encodeURIComponent(query)}`
        const res = await fetch(url, { credentials: 'include', signal })
        if (!res.ok) return []
        const data = (await res.json()) as { results?: { path: string; isDir?: boolean }[] }
        return (data.results ?? []).slice(0, 30).map((e) => ({
          id: e.path,
          label: '@' + e.path,
          detail: e.isDir ? 'directory' : '',
          insertText: '@' + e.path + ' ',
        }))
      } catch {
        return []
      }
    },
  }

  return [slashTrigger, mentionTrigger]
}
