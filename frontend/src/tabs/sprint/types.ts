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
  // Se173ef: set when a pass AC was flipped back to fail on Sprint re-open.
  reopenedAt?: string
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

export type StoryStatusKind =
  | 'done'
  | 'in-progress'
  | 'pending'
  | 'blocked'
  | 'needs-human'
  | 'needs-user-review'

export interface Story {
  id: string
  title: string
  status: string
  statusKind: StoryStatusKind
  userStory?: string
  blockedReason?: string
  acceptanceCriteria: Acceptance[]
  tasks: Task[]
  // Se173ef: new story-level state (done vs needs_user_review distinct).
  reviewReason?: string
  userReviewRequired?: boolean
  addedInReview?: string
  reopenedAt?: string
  needsHuman?: string
  dependsOn?: string[]
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
  // Se173ef: rolling-wave / Review Mode metadata.
  detailLevel?: string
  phase?: string
  reviewReason?: string
  coarse?: boolean
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
  milestone?: boolean
  dependsOn?: string[]
  // Se173ef: rolling-wave metadata for badges + phase grouping.
  detailLevel?: string
  phase?: string
  coarse?: boolean
}

export interface Dependency {
  // S028: schema-derived. `from` is the dependent sprint; `refs` is
  // [from, prereq1, prereq2, ...] for backward compat with the Mermaid
  // edge derivation.
  from?: string
  refs?: string[]
  text: string
  // Se173ef: dependency reason surfaced separately (AC-2-3).
  reason?: string
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

// === Se173ef: BacklogEntry + current artifact set ==========================

export interface BacklogEntry {
  title?: string
  description?: string
  addedIn?: string
  reason?: string
  done: boolean
  text: string
  source?: string
  priority?: string
  status?: string
  promoted?: boolean
  promotedTo?: string
}

export interface OverviewRollup {
  needsUserReview: number
  blocked: number
  nextMilestone?: string
  backlogTotal: number
  backlogUnpromoted: number
}

export interface PhaseGroup {
  phase: string
  sprints: string[]
  done: number
  total: number
}

// --- verify-run.json ---
export interface VerifyRunEntry {
  name: string
  command?: string
  exitCode: number
  log?: string
  machineStatus: string
  junit?: {
    total: number
    passed: number
    failed: number
    errored: number
    skipped: number
  }
}

export interface VerifyRun {
  sprint?: string
  commandSource?: string
  runs: VerifyRunEntry[]
  overallMachineStatus: string
}

// --- verification-report.json ---
export interface VerificationAC {
  ac: string
  status: string
  evidence?: string
  overlookedByAutopilot?: boolean
  recommendedAction?: string
}

export interface VerificationFind {
  category?: string
  subtype?: string
  verdict: string
  detail?: string
  overlookedByAutopilot?: boolean
  recommendedAction?: string
}

export interface VerificationStory {
  story: string
  verdict: string
  acFindings?: VerificationAC[]
  forbiddenCategoryFindings?: VerificationFind[]
}

export interface VerificationFinding {
  category?: string
  story?: string
  ac?: string
  verdict: string
  detail?: string
  overlookedByAutopilot?: boolean
  recommendedAction?: string
}

export interface VerificationReport {
  sprint?: string
  overall: string
  verifiedAt?: string
  verifierModel?: string
  summary: {
    acFailures: number
    acWarnings: number
    forbiddenWarnings: number
    forbiddenFailures: number
    adrConflicts: number
    overlookedCount: number
  }
  stories: VerificationStory[]
  findings: VerificationFinding[]
  adrStatus?: string
  adrDetail?: string
}

// --- done-judgment.json ---
export interface Guard {
  num: number
  key: string
  label: string
  status: string
  detail: string
}

export interface DoneJudgmentStory {
  story: string
  guards: Guard[]
  overall: string
  note?: string
}

export interface DoneJudgment {
  sprint?: string
  precondition?: { detailLevel?: string; storiesNonempty: boolean; ok: boolean }
  stories: DoneJudgmentStory[]
}

// --- compromises.json ---
export interface Compromise {
  type?: string
  severity?: string
  story?: string
  file?: string
  rationale?: string
  diffSummary?: string
  recommendedAction?: string
  adrRef?: string
  overlookedByAutopilot?: boolean
}

export interface Blocker {
  type?: string
  severity?: string
  detail?: string
  resolution?: string
}

export interface Compromises {
  stoppedAt?: string
  compromises: Compromise[]
  blockers: Blocker[]
  scopeChanges: Blocker[]
}

// --- comprehension-report.md ---
export interface Comprehension {
  markdown: string
  headings: string[]
}

// --- prototype-review.json ---
export interface PrototypeScreen {
  file: string
  story?: string
  feedbackRounds: number
  approved: boolean
}

export interface PrototypeReview {
  sprintRange?: string[]
  screens: PrototypeScreen[]
  designDecisions?: string[]
  approvedByUser: boolean
  approvedAt?: string
}

// --- reopen.json ---
export interface Reopen {
  sprintId?: string
  reopenedAt?: string
  triggeredBy?: string
  milestone?: string
  reason?: string
  affectedAcceptanceCriteria?: string[]
  addedTasks?: Array<{ story?: string; taskId?: string; description?: string }>
}

// --- scenario-{Story}.json ---
export interface ScenarioDoc {
  story: string
  storyType?: string
  userRole?: string
  entryPoint?: string
  linkedAcs?: string[]
  count: number
}

// --- gui-spec-{Story}.json ---
export interface GUISpec {
  sprintId: string
  story?: string
  stateDiagram?: string
  scenarios?: Record<string, unknown>
  endpointContracts?: Array<{
    path: string
    method: string
    registered: boolean
    requestFields?: Record<string, unknown>
    responseFields?: Record<string, unknown>
  }>
  testFiles?: Record<string, string>
}

// --- AC findings (verification-report + ROADMAP; replaces acceptance-matrix) ---
export interface ACFindingRow {
  story: string
  ac: string
  description?: string
  status: 'pass' | 'fail' | 'warn' | 'pending' | 'no_test'
  roadmapStatus?: string
  verifierStatus?: string
  evidence?: string
  overlookedByAutopilot?: boolean
  recommendedAction?: string
  reopenedAt?: string
}

// --- generic additional smoke logs ---
export interface SmokeLog {
  file: string
  kind?: string
  overall: 'pass' | 'fail' | 'unknown'
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
  // Se173ef (Option A): dependencies + backlog + roll-up folded into Overview.
  dependencies: Dependency[]
  backlog: BacklogEntry[]
  rollup: OverviewRollup
  phases: PhaseGroup[]
  parseErrors?: ParseError[]
}

export interface SprintDetailResponse {
  sprint: Sprint
  verifyRun?: VerifyRun
  verification?: VerificationReport
  doneJudgment?: DoneJudgment
  compromises?: Compromises
  comprehension?: Comprehension
  prototypeReview?: PrototypeReview
  reopens: Reopen[]
  guiSpecs: GUISpec[]
  scenarios: ScenarioDoc[]
  acFindings: ACFindingRow[]
  additionalLogs: SmokeLog[]
  decisions: DecisionEntry[]
  // Legacy / back-compat sections (empty on current sprints).
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

// --- /review ---
export interface StoryRef {
  sprintId: string
  storyId: string
  title?: string
  status: string
  reviewReason?: string
  detail?: string
}

export interface CompromiseRef {
  sprintId: string
  kind: string
  severity: string
  type?: string
  story?: string
  detail?: string
}

export interface OverlookedRef {
  sprintId: string
  story?: string
  ac?: string
  category?: string
  verdict?: string
  detail?: string
}

export interface ReopenRef {
  sprintId: string
  reopenedAt?: string
  triggeredBy?: string
  reason?: string
}

export interface ReviewResponse {
  counts: {
    needsUserReview: number
    blocked: number
    highCompromise: number
    overlooked: number
    reopen: number
  }
  needsUserReview: StoryRef[]
  blocked: StoryRef[]
  compromises: CompromiseRef[]
  overlooked: OverlookedRef[]
  reopens: ReopenRef[]
  parseErrors?: ParseError[]
}

// --- /milestones ---
export interface MilestoneEntry {
  sprintId: string
  title: string
  phase?: string
  status: string
  statusKind: string
  comprehension?: Comprehension
  compromises?: Compromises
  verifyRunOverall?: string
  verifierOverall?: string
}

export interface MilestonesResponse {
  milestones: MilestoneEntry[]
  parseErrors?: ParseError[]
}

// --- /backlog ---
export interface BacklogResponse {
  items: BacklogEntry[]
  total: number
  unpromoted: number
  promoted: number
  parseErrors?: ParseError[]
}

// Scopes from `sprint.changed` WS event payload — used to drive partial
// refetches.
export type SprintChangedScope =
  | 'overview'
  | 'sprintDetail'
  | 'dependencies'
  | 'decisions'
  | 'refine'
  | 'review'
  | 'milestones'
  | 'backlog'
