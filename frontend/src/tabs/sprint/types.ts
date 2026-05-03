// Type contracts mirroring internal/tab/sprint structs. Whenever the
// backend payload changes, update these in lockstep — there's no codegen.
//
// S028: backend was rewritten to consume JSON (ROADMAP.json + sprint-logs/
// **/*.json). The wire format the FE consumes is intentionally backwards
// compatible with the S016 markdown-era shape, with a few additive fields
// (description / blockedReason / failures / parseError line/column).

export interface Progress {
  total: number
  done: number
  inProgress: number
  remaining: number
  percent: number
}

export interface ParseError {
  section: string
  detail: string
  // S028: present when the parser pinpoints a JSON syntax / type error.
  line?: number
  column?: number
}

export interface Acceptance {
  // Stable legacy fields (Done / Text). The FE renders {ac.text} directly.
  done: boolean
  text: string
  // Schema-aligned fields, available when the source is ROADMAP.json.
  id?: string
  description?: string
  test?: string
  status?: 'pass' | 'fail' | 'pending' | 'no_test'
}

export interface Task {
  id?: string
  done: boolean
  text: string
  // Schema-aligned fields.
  title?: string
  description?: string
  status?: 'done' | 'pending'
}

export interface Story {
  id: string
  title: string
  status: string
  statusKind: 'done' | 'in-progress' | 'pending' | 'blocked' | 'needs-human'
  userStory?: string
  blockedReason?: string
  acceptanceCriteria: Acceptance[]
  tasks: Task[]
}

export interface Sprint {
  id: string
  title: string
  status: string
  statusKind: 'done' | 'in-progress' | 'pending' | 'blocked' | 'needs-human'
  description?: string
  milestone?: boolean
  stories: Story[]
  parseError?: string
  lineRange: [number, number]
}

export interface ActiveAutopilot {
  sprintId: string
  startedAt: string
  lockPath: string
  pid?: number
}

export interface TimelineEntry {
  id: string
  title: string
  statusKind: Sprint['statusKind']
}

export interface Dependency {
  // S028: schema-derived. `from` is the dependent sprint; `refs` is
  // [from, prereq1, prereq2, ...] for backward compat with the Mermaid
  // edge derivation.
  from?: string
  refs?: string[]
  text: string
}

export interface DecisionEntry {
  sprintId: string
  category: 'planning' | 'implementation' | 'review' | 'backlog' | 'needs_human' | 'other'
  title?: string
  body: string
  reference?: string
  timestamp?: string
  needsHuman?: boolean
}

export interface AcceptanceMatrixRow {
  acId: string
  story?: string
  testId?: string
  status: 'pass' | 'fail' | 'no-test'
  notes?: string
}

export interface TestSummary {
  total: number
  passed: number
  failed: number
}

export interface E2EResults {
  sprintId: string
  mock: TestSummary
  e2e: TestSummary
  acceptance: TestSummary
}

export interface RefineEntry {
  sprintId: string
  number: number
  title?: string
  body?: string
  files?: string[]
  testsRerun?: string[]
  testsPassed?: boolean
}

export interface FailureEntry {
  sprintId: string
  story?: string
  type?: string
  summary?: string
  attempts?: Array<{ approach?: string; result?: string }>
  resolution?: string
}

// === Endpoint response shapes ===============================================

export interface OverviewResponse {
  project: string
  vision?: string
  progress: Progress
  currentSprint?: Sprint
  nextMilestone?: string
  activeAutopilot: ActiveAutopilot[]
  timeline: TimelineEntry[]
  parseErrors?: ParseError[]
}

export interface SprintDetailResponse {
  sprint: Sprint
  decisions: DecisionEntry[]
  acceptanceMatrix: AcceptanceMatrixRow[]
  e2eResults: E2EResults
  failures?: FailureEntry[]
  parseErrors?: ParseError[]
}

export interface DependencyGraphResponse {
  sprints: TimelineEntry[]
  dependencies: Dependency[]
  mermaid: string
  parseErrors?: ParseError[]
}

export interface DecisionsResponse {
  entries: DecisionEntry[]
  parseErrors?: ParseError[]
}

export interface RefineResponse {
  entries: RefineEntry[]
}

// Scopes from `sprint.changed` WS event payload — used to drive partial
// refetches.
export type SprintChangedScope =
  | 'overview'
  | 'sprintDetail'
  | 'dependencies'
  | 'decisions'
  | 'refine'
