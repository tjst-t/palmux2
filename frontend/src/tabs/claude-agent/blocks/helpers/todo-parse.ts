/** Parsers + iconography for TodoBlock (TodoWrite tool output). */
import styles from '../../blocks.module.css'

export interface TodoEntry {
  status?: string
  content?: string
  activeForm?: string
}

export function parseTodos(input: unknown): TodoEntry[] | null {
  if (!input) return null
  let value: unknown = input
  if (typeof input === 'string') {
    try { value = JSON.parse(input) } catch { return null }
  }
  if (Array.isArray(value)) return value as TodoEntry[]
  if (value && typeof value === 'object' && 'todos' in (value as Record<string, unknown>)) {
    const inner = (value as { todos: unknown }).todos
    if (Array.isArray(inner)) return inner as TodoEntry[]
  }
  return null
}

export function todoClass(status?: string): string {
  switch (status) {
    case 'completed':   return styles.todoCompleted
    case 'in_progress': return styles.todoInProgress
    default:            return styles.todoPending
  }
}

export function todoIcon(status?: string): string {
  switch (status) {
    case 'completed':   return '✔'
    case 'in_progress': return '◉'
    default:            return '○'
  }
}
