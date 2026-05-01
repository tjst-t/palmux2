// Type contracts mirroring internal/tab/sprint structs. Whenever the
// backend payload changes, update these in lockstep — there's no codegen.

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
}

export interface Acceptance {
  done: boolean
  text: string
}

export interface Task {
  id?: string
  done: boolean
  text: string
}

export interface Story {
  id: string
  title: string
  status: string
  statusKind: 'done' | 'in-progress' | 'pending' | 'blocked' | 'needs-human'
  userStory?: string
  acceptanceCriteria: Acceptance[]
  tasks: Task[]
}

export interface Sprint {
  id: string
  title: string
  status: string
  statusKind: 'done' | 'in-progress' | 'pending' | 'blocked' | 'needs-human'
  description?: string
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
  text: string
  refs?: string[]
}

export interface DecisionEntry {
  sprintId: string
  category: 'planning' | 'implementation' | 'review' | 'backlog' | 'needs_human' | 'other'
  title?: string
  body: string
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
