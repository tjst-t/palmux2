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

// S012 ─ Commit / push / pull / branch types ─────────────────────────

export interface CommitOptions {
  message: string
  amend?: boolean
  signoff?: boolean
  noVerify?: boolean
  allowEmpty?: boolean
}

export interface CommitResult {
  hash: string
  subject: string
}

export interface PushOptions {
  remote?: string
  branch?: string
  setUpstream?: boolean
  force?: boolean
  forceWithLease?: boolean
  tags?: boolean
}

export interface PullOptions {
  remote?: string
  branch?: string
  rebase?: boolean
  ffOnly?: boolean
  noCommit?: boolean
}

export interface FetchOptions {
  remote?: string
  prune?: boolean
  all?: boolean
}

export interface CreateBranchOptions {
  name: string
  startFrom?: string
  checkout?: boolean
}

export interface DeleteBranchOptions {
  name: string
  force?: boolean
}

export interface SetUpstreamOptions {
  branch: string
  upstream: string
}

export interface LineRange {
  start: number
  end: number
}

export interface StageLinesRequest {
  path: string
  lineRanges: LineRange[]
}
