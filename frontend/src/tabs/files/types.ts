export interface Entry {
  name: string
  path: string
  isDir: boolean
  size: number
  modTime: string
  isLink?: boolean
}

export interface FileBody {
  path: string
  size: number
  mime: string
  isBinary: boolean
  content: string
  truncated?: boolean
}

export interface GrepHit {
  path: string
  lineNum: number
  line: string
}
