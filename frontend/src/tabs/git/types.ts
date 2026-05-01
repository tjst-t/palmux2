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

// S013 ─ Rich log / graph / stash / cherry-pick / revert / reset / tag types ───

export interface LogEntryDetail {
  hash: string
  parents: string[]
  subject: string
  author: string
  email: string
  date: string
  refs?: string[]
}

export interface LogFilter {
  author?: string
  grep?: string
  since?: string
  until?: string
  path?: string
  branch?: string
  skip?: number
  limit?: number
  all?: boolean
}

export interface LogFilteredResponse {
  entries: LogEntryDetail[] | null
  skip: number
  limit: number
}

export interface StashEntry {
  name: string
  index: number
  branch?: string
  subject: string
  date?: string
}

export interface StashPushOptions {
  message?: string
  includeUntracked?: boolean
  keepIndex?: boolean
}

export interface CherryPickOptions {
  commitSha: string
  noCommit?: boolean
}

export interface RevertOptions {
  commitSha: string
  noCommit?: boolean
}

export type ResetMode = 'soft' | 'mixed' | 'hard'

export interface ResetOptions {
  commitSha: string
  mode: ResetMode
}

export interface TagEntry {
  name: string
  annotated: boolean
  commitSha: string
  subject?: string
  tagger?: string
  date?: string
}

export interface CreateTagOptions {
  name: string
  commitSha?: string
  message?: string
  annotated?: boolean
  force?: boolean
}

export interface PushTagOptions {
  name: string
  remote?: string
  force?: boolean
}

export interface BlameLine {
  hash: string
  author: string
  email?: string
  authorTime?: string
  summary?: string
  origLine: number
  finalLine: number
  content: string
}

export interface BlameResponse {
  path: string
  revision?: string
  lines: BlameLine[] | null
}

// S014 ─ Conflict / rebase / merge / submodule / reflog / bisect ─────

export interface ConflictHunk {
  startLine: number
  endLine: number
  ours: string[]
  base?: string[]
  theirs: string[]
  oursLabel?: string
  theirsLabel?: string
}

export interface ConflictFile {
  path: string
  hunks: ConflictHunk[] | null
  hasBase: boolean
  binary: boolean
}

export interface ConflictBody {
  path: string
  ours: string
  base: string
  theirs: string
  merged: string
}

export type RebaseAction =
  | 'pick'
  | 'reword'
  | 'edit'
  | 'squash'
  | 'fixup'
  | 'drop'
  | 'exec'
  | string

export interface RebaseTodoEntry {
  action: RebaseAction
  sha?: string
  subject?: string
  raw: string
}

export interface RebaseStatus {
  active: boolean
  interactive: boolean
  onto?: string
  head?: string
  todo?: RebaseTodoEntry[] | null
  done?: RebaseTodoEntry[] | null
}

export interface RebaseStartOptions {
  onto: string
  interactive?: boolean
  todo?: RebaseTodoEntry[]
  autosquash?: boolean
  autostash?: boolean
}

export interface MergeOptions {
  branch: string
  noFf?: boolean
  squash?: boolean
  ffOnly?: boolean
  message?: string
}

export interface Submodule {
  path: string
  commit: string
  status: 'initialized' | 'not-initialized' | 'modified' | 'conflict'
  describe?: string
}

export interface ReflogEntry {
  hash: string
  ref: string
  action: string
  message: string
  date?: string
}

export interface BisectStatus {
  active: boolean
  currentSha?: string
  good?: string
  bad?: string
  remaining?: number
  log?: string
}

export interface BisectStartOptions {
  good: string
  bad: string
}
