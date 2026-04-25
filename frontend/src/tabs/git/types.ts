export interface FileStatus {
  path: string
  oldPath?: string
  stagedCode: string
  workingCode: string
}

export interface StatusReport {
  branch: string
  staged: FileStatus[] | null
  unstaged: FileStatus[] | null
  untracked: FileStatus[] | null
  conflicts?: FileStatus[] | null
}

export interface LogEntry {
  hash: string
  subject: string
  author: string
  email: string
  date: string
}

export interface BranchEntry {
  name: string
  isRemote: boolean
  isHead: boolean
}

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

export interface DiffResponse {
  mode: 'working' | 'staged'
  raw: string
  files: DiffFile[] | null
}
