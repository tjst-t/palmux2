// Shared diff data shapes. Originally lived in tabs/git/types.ts; promoted
// to a top-level component so the Claude tab can render Edit/Write tool
// results with the same look as the Git diff view.

export interface DiffLine {
  kind: 'context' | 'add' | 'del' | 'meta'
  text: string
}

export interface DiffHunk {
  header: string
  lines: DiffLine[]
  oldStart: number
  oldCount: number
  newStart: number
  newCount: number
}

export interface DiffFile {
  oldPath: string
  newPath: string
  header: string
  hunks: DiffHunk[] | null
  isBinary?: boolean
}
