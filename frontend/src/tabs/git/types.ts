// Git tab types — minimal post-S029 surface.
//
// S029 (BREAKING) trimmed this file from ~320 LOC down to the handful
// of types the new minimal Git UI actually consumes:
//
//   * StatusReport / FileStatus  — `/git/status` response
//   * LogEntry                   — `/git/log` row
//   * BranchEntry                — `/git/branches` row
//   * LineRange                  — Monaco selection → `/git/stage-lines`
//
// API request / response types for write endpoints (commit / push /
// pull / fetch / branches / stage-lines) intentionally live as untyped
// literals at the call site — there is one caller per endpoint and
// adding wire-types here just creates two places to keep in sync.

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
  refs?: string[]
}

export interface BranchEntry {
  name: string
  isRemote: boolean
  isHead: boolean
  upstream?: string
  ahead?: number
  behind?: number
}

export interface LineRange {
  start: number
  end: number
}
